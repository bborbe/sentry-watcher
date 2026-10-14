// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"time"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/sentry-watcher/pkg/tasktemplate"
)

//counterfeiter:generate -o ../mocks/heartbeat_checker.go --fake-name HeartbeatChecker . HeartbeatChecker

// ErrHeartbeatMissing marks a Verify failure that *is* the finding: no
// heartbeat newer than MaxAge is visible.
//
// Callers that act on the absence — filing an alert, paging someone — must
// match on this rather than on "Verify returned an error". A query failure also
// returns an error, and it means the check could not run, which is not evidence
// that ingestion is dead; alerting on it would cry wolf exactly when the Sentry
// API is unreachable. The two are deliberately separable.
var ErrHeartbeatMissing = stderrors.New("heartbeat missing")

// HeartbeatChecker verifies that the heartbeat is still arriving.
//
// A quiet fleet and dead ingestion both produce "no new issues", so counting
// issues cannot tell them apart. The heartbeat resolves that ambiguity: a
// known workload emits a deliberately-tagged error event on a schedule, and
// this checker asserts a *recent* one is visible. Absence is unambiguous —
// there is no "legitimately quiet" reading of a missing heartbeat.
//
// The age bound is not optional. The org-issues endpoint applies no time
// window of its own — fetchPage sends only query/limit/cursor — so a bare tag
// query matches a heartbeat from any point in the past: the first event ever
// sent would satisfy it forever, and the detector would stay silent through
// exactly the ingestion blackout it exists to catch. The bound is therefore
// enforced here, against the issue's own lastSeen, rather than in the search
// expression — no Sentry search-syntax assumption is load-bearing. (The
// sibling statsPeriod approach was already disproven on this endpoint: it is
// an event-recency filter, not a stats window.)
//
// The client is expected to be configured with the heartbeat query
// (SentryConfig.Query, e.g. "source:sentry-heartbeat"), so UnresolvedIssues
// returns heartbeat events rather than the fleet's real alerts.
type HeartbeatChecker interface {
	// Verify returns the observation and nil when a heartbeat event newer than
	// MaxAge is visible. A non-nil error means none is; which candidate cause
	// applies is not established here.
	//
	// The observation is returned on the missing path too, not only on success.
	// A stale heartbeat still has a lastSeen, and that timestamp is the whole
	// content of the published metric: the alert keys on its age, so the value
	// that has to reach Prometheus is precisely the one the verdict calls bad.
	// Only an outcome that establishes no timestamp at all — no event visible,
	// an unparseable lastSeen, or a failed query — leaves Newest zero.
	Verify(ctx context.Context) (HeartbeatObservation, error)
}

// HeartbeatObservation is what one check saw, independent of its verdict.
//
// It is separate from the error rather than folded into it because the two
// answer different questions: the error says whether ingestion is healthy, the
// observation says what the newest heartbeat's lastSeen was. A caller
// publishing the metric needs the second even when the first is bad.
type HeartbeatObservation struct {
	// Newest is the most recent heartbeat's lastSeen. Zero when the check
	// could not establish one — see Verify.
	Newest time.Time
}

// DefaultHeartbeatMaxAge is how old the newest heartbeat may be when
// HEARTBEAT_MAX_AGE is unset: N = 6h, the window this detector is specified at.
// Applied in the binaries rather than only via the arg's `default:` tag, so a
// caller that constructs the config directly (a test, a future entrypoint)
// gets the same window instead of a zero bound that reports ingestion dead on
// every cycle.
const DefaultHeartbeatMaxAge = 6 * time.Hour

// HeartbeatCheckerConfig groups the checker's connection and staleness settings.
type HeartbeatCheckerConfig struct {
	// Client is the org-issues client, configured with the heartbeat query
	// (SentryConfig.Query). Required.
	Client SentryClient

	// MaxAge is how old the newest heartbeat may be and still count as present.
	// Expected > 0: a zero bound reports ingestion dead on every cycle, so callers
	// must supply a real window (the deployment default is 6h, matching the N the
	// detector is specified at).
	MaxAge time.Duration

	// CurrentDateTimeGetter supplies "now" (go-time/no-time-now-direct);
	// nil → libtime.NewCurrentDateTime().
	CurrentDateTimeGetter libtime.CurrentDateTimeGetter
}

// NewHeartbeatChecker returns a HeartbeatChecker for the given config.
func NewHeartbeatChecker(cfg HeartbeatCheckerConfig) HeartbeatChecker {
	if cfg.CurrentDateTimeGetter == nil {
		cfg.CurrentDateTimeGetter = libtime.NewCurrentDateTime()
	}
	return &heartbeatChecker{cfg: cfg}
}

type heartbeatChecker struct {
	cfg HeartbeatCheckerConfig
}

func (c *heartbeatChecker) Verify(ctx context.Context) (HeartbeatObservation, error) {
	issues, err := c.cfg.Client.UnresolvedIssues(ctx)
	if err != nil {
		return HeartbeatObservation{}, errors.Wrap(ctx, err, "query heartbeat")
	}
	if len(issues) == 0 {
		return HeartbeatObservation{}, errors.Wrap(
			ctx,
			ErrHeartbeatMissing,
			"no heartbeat event visible in Sentry",
		)
	}

	newest, err := newestHeartbeat(ctx, issues)
	if err != nil {
		return HeartbeatObservation{}, err
	}

	// The observation is populated before the bound is judged, so a stale
	// heartbeat still reports its lastSeen. That is deliberate: the published
	// metric exists to carry the age that makes the alert fire, and withholding
	// it exactly when the heartbeat is stale would blind the alert.
	observation := HeartbeatObservation{Newest: newest}

	age := c.cfg.CurrentDateTimeGetter.Now().Time().Sub(newest)
	if age <= c.cfg.MaxAge {
		return observation, nil
	}
	return observation, errors.Wrapf(
		ctx,
		ErrHeartbeatMissing,
		"newest heartbeat is %s old, past the %s limit",
		age.Truncate(time.Second),
		c.cfg.MaxAge,
	)
}

// newestHeartbeat returns the most recent lastSeen across the returned issues.
//
// An issue whose lastSeen cannot be parsed is skipped rather than counted as
// fresh — a malformed timestamp is not evidence that anything arrived. If none
// parse, the check cannot be evaluated at all, and says so: that outcome is
// deliberately reported as neither "heartbeat present" nor "heartbeat absent",
// because the query succeeded but its answer is unusable.
func newestHeartbeat(ctx context.Context, issues []Issue) (time.Time, error) {
	var newest time.Time
	unparseable := 0
	for _, issue := range issues {
		// A cancelled context stops the scan rather than running it out: the
		// caller's deadline is authoritative even over a bounded in-memory loop.
		select {
		case <-ctx.Done():
			return time.Time{}, errors.Wrap(ctx, ctx.Err(), "iterate heartbeat issues")
		default:
		}
		t, err := time.Parse(time.RFC3339, issue.LastSeen)
		if err != nil {
			unparseable++
			continue
		}
		if t.After(newest) {
			newest = t
		}
	}
	if newest.IsZero() {
		return time.Time{}, errors.Errorf(
			ctx,
			"%d heartbeat event(s) visible but none carry a parseable lastSeen — cannot evaluate staleness",
			unparseable,
		)
	}
	return newest, nil
}

// HeartbeatMissingShortID is the synthetic short-id the alert task carries.
// Deliberately not a Sentry short-id: the alert reports the *absence* of
// events, so there is no issue for it to link to.
const HeartbeatMissingShortID = "SENTRY-HEARTBEAT-MISSING"

// BuildHeartbeatAlertCommand assembles the CreateTaskCommand emitted when no
// heartbeat is visible.
//
// date is the UTC calendar date, and it is what makes this safe to run often:
// the derived task id collapses every failure in a day to one task. A detector
// stuck on a dead pipeline fails on *every* check, so a per-check id would bury
// the signal under its own repetition. The title/body templates are the
// heartbeat pair rather than the analyze pair — this task must not read as a
// Sentry issue.
func BuildHeartbeatAlertCommand(
	ctx context.Context,
	date string,
	maxAge time.Duration,
	cfg TaskConfig,
) (task.CreateCommand, error) {
	cfg.TitleTemplate = tasktemplate.HeartbeatTitle
	cfg.BodyTemplate = tasktemplate.HeartbeatBody
	return BuildCreateCommand(
		ctx,
		Issue{
			ShortID: HeartbeatMissingShortID,
			Title:   "no heartbeat within " + maxAge.String(),
		},
		date,
		cfg,
	)
}
