// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command sentry-check-heartbeat verifies that Sentry is still retaining events,
// and publishes what it saw so the alert can be raised from outside it.
//
// It is the detector half of the zero-events check, deliberately split from the
// poller. The poller's emit path is superseded by the Kafka-native consumer, and
// running it would file a second copy of every Analyze-Sentry-issue task — a
// different dedup key and a different assignee, so the controller's
// `(issue-key, date)` dedup could not collapse them. This binary only *reads*
// Sentry; it has no analysis path, so there is nothing for it to double-file.
//
// It no longer files a vault task of its own. The heartbeat alert is raised by a
// Prometheus rule over the age metric this binary pushes (see publishReading) and
// declared as an `Alert` CR in bborbe/nuke — so the alert's timing is no longer
// welded to this CronJob's own schedule, which is what produced the 2026-10-10
// false alarm. The run still fails on a missing heartbeat: that failure is the
// cluster-level signal, no longer the human one.
//
// One-shot. Run it on a schedule shorter than HeartbeatMaxAge, so a missing
// heartbeat is caught within N + one interval.
package main

import (
	"context"
	stderrors "errors"
	"net/http"
	"os"
	"time"

	"github.com/bborbe/errors"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	"github.com/golang/glog"

	"github.com/bborbe/sentry-watcher/pkg"
	"github.com/bborbe/sentry-watcher/pkg/factory"
)

// httpClientTimeout bounds the single Sentry request.
const httpClientTimeout = 30 * time.Second

func main() {
	app := NewApplication()
	os.Exit(service.Main(
		context.Background(),
		app,
		&app.SentryDSN,
		&app.SentryProxy,
		ExcludeHeartbeatMissing,
	))
}

// ExcludeHeartbeatMissing keeps the expected alarm out of Sentry.
//
// The heartbeat-missing error is the detector *working*: it is delivered to a
// human by the vault task and to the cluster by the failed run. Reporting it a
// third time spends the org's ingest quota on a condition that is already known
// — through the same proxy this check exists to watch — and files an error event
// the analyzer pipeline can pick up as noise. Unexpected failures ("the check
// could not run") are deliberately still reported: nobody else carries those.
//
// The DSN stays required. Dropping it from the deployment instead would fail
// argument validation and the watchdog would stop watching silently — the exact
// failure this binary exists to catch.
func ExcludeHeartbeatMissing(options *service.Options) {
	options.ExcludeErrors = append(options.ExcludeErrors, func(err error) bool {
		return stderrors.Is(err, pkg.ErrHeartbeatMissing)
	})
}

// NewApplication creates an Application with its real dependencies.
//
// Returns the concrete *Application (not an interface): it is a service.Main
// config/DTO struct whose fields are reflected for arg/env binding, and the
// call site addresses &app.SentryDSN. The sibling cmd/run-once uses the same
// shape.
func NewApplication() *Application {
	return &Application{
		CreateChecker:          factory.CreateHeartbeatChecker,
		CreateHeartbeatMetrics: pkg.NewPushGatewayHeartbeatMetrics,
	}
}

// Application is the check-heartbeat CLI wiring, with injectable factories for tests.
type Application struct {
	SentryDSN   string `required:"true"  arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"Project DSN service.Main reports its own errors with" display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"sentry-proxy base URL"`

	Stage string `required:"true" arg:"stage" env:"STAGE" usage:"Deployment stage (dev|prod)"`

	SentryURL       string `required:"false" arg:"sentry-url"        env:"SENTRY_URL"        usage:"Base URL of the Sentry org"                                                            default:"https://bborbe.sentry.io"`
	SentryOrg       string `required:"false" arg:"sentry-org"        env:"SENTRY_ORG"        usage:"Sentry org slug"                                                                       default:"bborbe"`
	SentryAPIToken  string `required:"true"  arg:"sentry-api-token"  env:"SENTRY_API_TOKEN"  usage:"Sentry REST API Bearer token"                                                                                             display:"length"`
	HeartbeatQuery  string `required:"true"  arg:"heartbeat-query"   env:"HEARTBEAT_QUERY"   usage:"Search expression matching a heartbeat event (e.g. source:sentry-heartbeat)"`
	HeartbeatMaxAge string `required:"false" arg:"heartbeat-max-age" env:"HEARTBEAT_MAX_AGE" usage:"How old the newest heartbeat may be and still count as present (Go duration, e.g. 6h)" default:"6h"`

	// PushGatewayURL is required rather than defaulted: a check that silently
	// stops publishing its reading is precisely the failure this metric exists
	// to make visible, so a missing value must fail startup loudly instead of
	// quietly disabling the alert.
	PushGatewayURL string `required:"true" arg:"pushgateway-url" env:"PUSHGATEWAY_URL" usage:"Push gateway the heartbeat reading is published to (e.g. http://pushgateway.monitoring.svc.cluster.local:9090)"`

	CreateChecker          CheckerFunc
	CreateHeartbeatMetrics HeartbeatMetricsFunc
}

// CheckerFunc creates a HeartbeatChecker. Matches
// factory.CreateHeartbeatChecker exactly so tests can substitute a
// stub-returning closure. Named per go-functional-composition/func-type-name.
type CheckerFunc func(
	httpClient *http.Client,
	sentryCfg pkg.SentryConfig,
	query string,
	maxAge time.Duration,
) pkg.HeartbeatChecker

// HeartbeatMetricsFunc creates the publisher for the check's reading. Matches
// pkg.NewPushGatewayHeartbeatMetrics so tests can substitute a recorder that
// captures what would have been pushed. Production passes a nil client — the
// publisher supplies its own bounded one, so a stuck gateway cannot block the
// run before the alert is filed.
type HeartbeatMetricsFunc func(url string, client *http.Client) pkg.HeartbeatMetrics

// Run performs one heartbeat check and returns.
//
// Exit contract: nil when the heartbeat is present, and a non-nil error when it
// is missing, so the scheduled run fails. That failure is now the *cluster*
// signal only — the human-facing alert is the Prometheus rule over the metric
// publishReading pushes, which is deliberately not keyed on this exit code.
//
// A query failure also returns an error, and ErrHeartbeatMissing is what
// separates the two: the check could not run, which is not evidence that
// ingestion is dead.
func (a *Application) Run(ctx context.Context, _ libsentry.Client) error {
	maxAge := pkg.DefaultHeartbeatMaxAge
	if a.HeartbeatMaxAge != "" {
		parsed, err := time.ParseDuration(a.HeartbeatMaxAge)
		if err != nil {
			return errors.Wrapf(ctx, err, "parse heartbeat max age %q", a.HeartbeatMaxAge)
		}
		maxAge = parsed
	}

	httpClient := &http.Client{Timeout: httpClientTimeout}
	checker := a.CreateChecker(
		httpClient,
		pkg.SentryConfig{URL: a.SentryURL, Org: a.SentryOrg, APIToken: a.SentryAPIToken},
		a.HeartbeatQuery,
		maxAge,
	)

	observation, verifyErr := checker.Verify(ctx)
	a.publishReading(ctx, observation)

	if verifyErr == nil {
		glog.V(2).Infof("heartbeat present maxAge=%s", maxAge)
		return nil
	}
	if !stderrors.Is(verifyErr, pkg.ErrHeartbeatMissing) {
		return errors.Wrap(ctx, verifyErr, "heartbeat check could not run")
	}

	return errors.Wrap(ctx, verifyErr, "heartbeat missing")
}

// publishReading pushes what the check saw to the gateway.
//
// It runs before the verdict is acted on, and on every path, because the alert
// keys on the *age* of the newest heartbeat: a reading the verdict calls stale
// is exactly the value Prometheus needs, and publishing only on success would
// withhold the metric at the one moment it matters.
//
// A zero reading is passed through rather than filtered here — the publisher
// decides what "unknown" means for the gateway, and keeping that rule in one
// place is what stops the two callers drifting apart.
//
// Publishing never fails the check. A gateway that is down costs a metric, not
// a verdict, and turning that into a failed run would report the detector as
// broken at the moment it is working.
func (a *Application) publishReading(ctx context.Context, observation pkg.HeartbeatObservation) {
	if a.CreateHeartbeatMetrics == nil {
		return
	}
	a.CreateHeartbeatMetrics(a.PushGatewayURL, nil).PublishLastSeen(ctx, observation.Newest)
}

// The alert path that lived here — building a day-keyed vault task, publishing
// it over Kafka and counting the publish — was removed on 2026-10-15. The
// heartbeat alert is raised by a Prometheus rule over the age metric this binary
// pushes, declared as an `Alert` CR in bborbe/nuke's `sentry-heartbeat/alerts/`.
//
// The consequence to keep in mind: this binary's remaining human-visible output
// is that metric and its exit code, and the exit code is deliberately *not* the
// alert channel — a run that fails is a cluster signal, while the alert fires on
// the age crossing the bound regardless of whether this CronJob ran at all.
// See [[The Heartbeat Alert Hand-Rolls What Prometheus and Alertmanager Provide]].
