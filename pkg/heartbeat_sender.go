// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// HeartbeatTag is the tag the emitter sets on its synthetic event and the
// checker searches for. Both sides read it from here so they cannot drift —
// a mismatch would make the detector report "ingestion dead" forever, which
// is indistinguishable from the real failure it exists to detect.
const HeartbeatTag = "sentry-heartbeat"

// HeartbeatSearchExpression is the Sentry org-issues search matching a
// heartbeat event. Set this as HEARTBEAT_QUERY on the watcher.
const HeartbeatSearchExpression = "source:" + HeartbeatTag

//counterfeiter:generate -o ../mocks/heartbeat_sender.go --fake-name HeartbeatSender . HeartbeatSender

// HeartbeatSender emits the synthetic event the checker looks for.
//
// The event is deliberately an *error* event, not a plain message: the checker
// reads the org-issues endpoint, which lists error groups. A message-level
// event would never appear there and the check would report ingestion dead
// forever.
type HeartbeatSender interface {
	// Send emits one heartbeat event. A nil return means Sentry accepted the
	// envelope — which, while the org is over quota, does NOT mean it was
	// retained. Retention is the checker's question, not the sender's.
	Send(ctx context.Context) error
}

// HeartbeatSenderConfig configures the heartbeat emitter.
type HeartbeatSenderConfig struct {
	// DSN is the project DSN, e.g. https://<key>@<host>/<projectID>. Required.
	DSN string
	// ProxyURL, when set, is the sentry-proxy base URL (e.g.
	// http://sentry-proxy.prod.svc.cluster.local:9090). Empty posts straight
	// to the DSN host. Set it so the heartbeat traverses the same path as real
	// fleet events — a heartbeat that bypasses the proxy proves nothing about
	// the path that actually broke.
	ProxyURL string
	// HTTPClient is the client used for the POST; nil → a 30s-timeout client.
	HTTPClient *http.Client
	// CurrentDateTimeGetter supplies the event timestamp and the elapsed-time
	// reading on the audit line (go-time/no-time-now-direct);
	// nil → libtime.NewCurrentDateTime().
	CurrentDateTimeGetter libtime.CurrentDateTimeGetter
	// Metrics records the POST's outcome so it outlives the pod, which the log
	// line cannot: the failed emitter pods of 2026-10-10/11 were garbage-
	// collected within a day and their logs with them. nil → the outcome is
	// logged but not published, so the sender stays constructible without a
	// gateway.
	Metrics HeartbeatEmitterMetrics
}

// NewHeartbeatSender returns a sender for the given config.
func NewHeartbeatSender(cfg HeartbeatSenderConfig) HeartbeatSender {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.CurrentDateTimeGetter == nil {
		cfg.CurrentDateTimeGetter = libtime.NewCurrentDateTime()
	}
	return &heartbeatSender{cfg: cfg}
}

type heartbeatSender struct {
	cfg HeartbeatSenderConfig
}

func (s *heartbeatSender) Send(ctx context.Context) error {
	endpoint, err := ingestEndpoint(ctx, s.cfg)
	if err != nil {
		return err
	}
	now := s.cfg.CurrentDateTimeGetter.Now().Time().UTC().Format(time.RFC3339)
	body, eventID, err := heartbeatEnvelope(ctx, s.cfg.DSN, now)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(body),
	)
	if err != nil {
		return errors.Wrap(ctx, err, "build heartbeat request")
	}
	req.Header.Set("Content-Type", "application/x-sentry-envelope")

	// The POST is the one call that crosses the process boundary, so it gets
	// exactly one audit line — the success branch or one of the two failure
	// branches, never two for the same call (go-logging/external-call-logs-
	// response, whose carve-out from no-log-and-return-error this is). The line
	// is at V0, not V(2): the V(2) line it replaces was suppressed at the
	// default verbosity, so a successful cycle left no trace at all in
	// `kubectl logs`.
	start := s.cfg.CurrentDateTimeGetter.Now().Time()
	resp, err := s.cfg.HTTPClient.Do(req)
	elapsedMS := s.cfg.CurrentDateTimeGetter.Now().Time().Sub(start).Milliseconds()
	if err != nil {
		glog.Warningf(
			"heartbeat not reached proxy event_id=%s elapsed_ms=%d err=%v",
			eventID,
			elapsedMS,
			err,
		)
		s.recordNotReachedProxy(ctx)
		return errors.Wrapf(ctx, err, "post heartbeat %s", eventID)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusErr := errors.Errorf(ctx, "post heartbeat %s: status %d", eventID, resp.StatusCode)
		glog.Warningf(
			"heartbeat posted event_id=%s status=%d elapsed_ms=%d err=%v",
			eventID,
			resp.StatusCode,
			elapsedMS,
			statusErr,
		)
		s.recordPosted(ctx)
		return statusErr
	}
	glog.Infof(
		"heartbeat posted event_id=%s status=%d elapsed_ms=%d",
		eventID,
		resp.StatusCode,
		elapsedMS,
	)
	s.recordPosted(ctx)
	return nil
}

// recordPosted advances the "reached the proxy" counter. A nil recorder is
// tolerated so the sender stays constructible without a gateway.
func (s *heartbeatSender) recordPosted(ctx context.Context) {
	if s.cfg.Metrics == nil {
		return
	}
	s.cfg.Metrics.PublishPosted(ctx)
}

// recordNotReachedProxy advances the "never reached the proxy" counter, the
// outcome a rejected envelope must not be confused with.
func (s *heartbeatSender) recordNotReachedProxy(ctx context.Context) {
	if s.cfg.Metrics == nil {
		return
	}
	s.cfg.Metrics.PublishNotReachedProxy(ctx)
}

// ingestEndpoint resolves the envelope URL, routing through the proxy when one
// is configured.
func ingestEndpoint(ctx context.Context, cfg HeartbeatSenderConfig) (string, error) {
	parsed, err := url.Parse(cfg.DSN)
	if err != nil {
		return "", errors.Wrap(ctx, err, "parse dsn")
	}
	projectID := strings.TrimPrefix(parsed.Path, "/")
	if parsed.User == nil || projectID == "" || parsed.Host == "" {
		return "", errors.Errorf(ctx, "dsn is missing key, host, or project id")
	}
	if cfg.ProxyURL != "" {
		return fmt.Sprintf(
			"%s/api/%s/envelope/",
			strings.TrimSuffix(cfg.ProxyURL, "/"),
			projectID,
		), nil
	}
	return fmt.Sprintf("%s://%s/api/%s/envelope/", parsed.Scheme, parsed.Host, projectID), nil
}

// heartbeatEnvelope builds the three-line envelope the ingest endpoint expects.
//
// The grammar is `Item = Headers "\n" Payload` — an item is its own header line
// followed by the payload. This builder previously emitted the envelope header
// and then the event object as a *second* line, so Sentry parsed the event as
// the item header and found no payload: the envelope was accepted with a 200 and
// nothing was ever retained. Every emitter run still reported success, because
// Send inspects only the HTTP status, and the heartbeat stayed invisible for the
// whole outage it was built to detect. Keep the three lines distinct.
func heartbeatEnvelope(ctx context.Context, dsn string, now string) ([]byte, string, error) {
	eventID, err := newEventID(ctx)
	if err != nil {
		return nil, "", errors.Wrap(ctx, err, "generate event id")
	}
	header := fmt.Sprintf(`{"event_id":%q,"sent_at":%q,"dsn":%q}`, eventID, now, dsn)
	// level=error so the event surfaces as an issue — the checker reads the
	// org-issues endpoint, which lists error groups only.
	payload := fmt.Sprintf(
		`{"event_id":%q,"timestamp":%q,"level":"error","logger":%q,`+
			`"message":%q,"tags":{"source":%q}}`,
		eventID, now, HeartbeatTag, HeartbeatTag, HeartbeatTag,
	)
	// length is the payload's byte count, which is what lets the parser find the
	// payload's end without relying on EOF to terminate it.
	itemHeader := fmt.Sprintf(`{"type":"event","length":%d}`, len(payload))
	return []byte(header + "\n" + itemHeader + "\n" + payload + "\n"), eventID, nil
}

// newEventID returns a fresh hex event id for one heartbeat envelope.
//
// It takes the caller's context so a failure to read randomness is wrapped with
// it rather than returned bare — the repo's error convention, and the one this
// helper was the last exception to.
func newEventID(ctx context.Context) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(ctx, err, "read random bytes")
	}
	return hex.EncodeToString(buf), nil
}
