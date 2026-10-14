// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command sentry-check-heartbeat verifies that Sentry is still retaining events,
// and files one vault task per UTC day when it is not.
//
// It is the detector half of the zero-events check, deliberately split from the
// poller. The poller's emit path is superseded by the Kafka-native consumer, and
// running it would file a second copy of every Analyze-Sentry-issue task — a
// different dedup key and a different assignee, so the controller's
// `(issue-key, date)` dedup could not collapse them. This binary only *reads*
// Sentry and, on failure, writes a single alert task; it has no analysis path,
// so there is nothing for it to double-file.
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

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"

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
		CreateChecker: factory.CreateHeartbeatChecker,
		CreateProducer: func(ctx context.Context, brokers libkafka.Brokers) (libkafka.SyncProducer, error) {
			return libkafka.NewSyncProducerWithName(ctx, brokers, "sentry-check-heartbeat")
		},
		CreateHeartbeatMetrics: pkg.NewPushGatewayHeartbeatMetrics,
		CurrentDateTime:        libtime.NewCurrentDateTime(),
	}
}

// Application is the check-heartbeat CLI wiring, with injectable factories for tests.
type Application struct {
	SentryDSN   string `required:"true"  arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"Project DSN service.Main reports its own errors with" display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"sentry-proxy base URL"`

	Stage        string           `required:"true" arg:"stage"         env:"STAGE"         usage:"Deployment stage (dev|prod)"`
	KafkaBrokers libkafka.Brokers `required:"true" arg:"kafka-brokers" env:"KAFKA_BROKERS" usage:"Comma-separated Kafka broker list"`

	SentryURL       string `required:"false" arg:"sentry-url"        env:"SENTRY_URL"        usage:"Base URL of the Sentry org"                                                            default:"https://bborbe.sentry.io"`
	SentryOrg       string `required:"false" arg:"sentry-org"        env:"SENTRY_ORG"        usage:"Sentry org slug"                                                                       default:"bborbe"`
	SentryAPIToken  string `required:"true"  arg:"sentry-api-token"  env:"SENTRY_API_TOKEN"  usage:"Sentry REST API Bearer token"                                                                                             display:"length"`
	HeartbeatQuery  string `required:"true"  arg:"heartbeat-query"   env:"HEARTBEAT_QUERY"   usage:"Search expression matching a heartbeat event (e.g. source:sentry-heartbeat)"`
	HeartbeatMaxAge string `required:"false" arg:"heartbeat-max-age" env:"HEARTBEAT_MAX_AGE" usage:"How old the newest heartbeat may be and still count as present (Go duration, e.g. 6h)" default:"6h"`

	// PushGatewayURL is required rather than defaulted, for the same reason
	// TaskAssignee is: a check that silently stops publishing its reading is
	// precisely the failure this metric exists to make visible, so a missing
	// value must fail startup loudly instead of quietly disabling the alert.
	PushGatewayURL string `required:"true" arg:"pushgateway-url" env:"PUSHGATEWAY_URL" usage:"Push gateway the heartbeat reading is published to (e.g. http://pushgateway.monitoring.svc.cluster.local:9090)"`

	// TaskAssignee names whoever owns Sentry ingestion. It has no default: the
	// poller's analyzer assignee would be wrong here — there is no issue to
	// analyze, and a silently-wrong owner is worse than a startup failure.
	TaskAssignee string `required:"true" arg:"task-assignee" env:"TASK_ASSIGNEE" usage:"Frontmatter assignee for the alert task — whoever owns Sentry ingestion"`
	TaskStatus   string `required:"true" arg:"task-status"   env:"TASK_STATUS"   usage:"Frontmatter status for the alert task"                                   default:"in_progress"`
	TaskPhase    string `required:"true" arg:"task-phase"    env:"TASK_PHASE"    usage:"Frontmatter phase for the alert task"                                    default:"planning"`

	TargetVault string `required:"false" arg:"target-vault" env:"TARGET_VAULT" usage:"Obsidian vault slug the alert task is materialized into; empty = controller default (openclaw)"`

	// TopicPrefix selects the Kafka topic prefix used for CQRS topic construction
	// (e.g. "develop" / "master"); independent of Stage.
	TopicPrefix base.TopicPrefix `required:"false" arg:"topic-prefix" env:"TOPIC_PREFIX" usage:"Kafka topic prefix for CQRS topic construction"`

	CreateChecker          CheckerFunc
	CreateProducer         ProducerFunc
	CreateHeartbeatMetrics HeartbeatMetricsFunc
	CurrentDateTime        libtime.CurrentDateTimeGetter
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

// ProducerFunc creates a Kafka sync producer. Matches
// libkafka.NewSyncProducerWithName so tests can stub with a fake producer.
type ProducerFunc func(
	ctx context.Context,
	brokers libkafka.Brokers,
) (libkafka.SyncProducer, error)

// HeartbeatMetricsFunc creates the publisher for the check's reading. Matches
// pkg.NewPushGatewayHeartbeatMetrics so tests can substitute a recorder that
// captures what would have been pushed. Production passes a nil client — the
// publisher supplies its own bounded one, so a stuck gateway cannot block the
// run before the alert is filed.
type HeartbeatMetricsFunc func(url string, client *http.Client) pkg.HeartbeatMetrics

// Run performs one heartbeat check and returns.
//
// Exit contract: nil when the heartbeat is present. When it is missing, the
// alert task is filed *and* a non-nil error is returned, so the scheduled run
// fails too — the task is for a human, the failed run is for the cluster.
//
// A query failure returns an error but files **no** task: the check could not
// run, which is not evidence that ingestion is dead. Alerting on it would cry
// wolf exactly when the Sentry API is unreachable.
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

	if err := a.alert(ctx, maxAge); err != nil {
		return err
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

// alert files the single alert task for the current UTC day.
func (a *Application) alert(ctx context.Context, maxAge time.Duration) error {
	syncProducer, err := a.CreateProducer(ctx, a.KafkaBrokers)
	if err != nil {
		return errors.Wrap(ctx, err, "create sync producer")
	}
	defer func() {
		if cerr := syncProducer.Close(); cerr != nil {
			glog.Warningf("close kafka sync producer: %v", cerr)
		}
	}()

	cmd, err := pkg.BuildHeartbeatAlertCommand(
		ctx,
		a.CurrentDateTime.Now().Time().UTC().Format("2006-01-02"),
		maxAge,
		pkg.TaskConfig{
			Stage:    a.Stage,
			Assignee: a.TaskAssignee,
			Status:   a.TaskStatus,
			Phase:    a.TaskPhase,
		},
	)
	if err != nil {
		return errors.Wrap(ctx, err, "build heartbeat alert command")
	}

	sender := factory.CreateKafkaSender(syncProducer, a.TopicPrefix, a.TargetVault)
	// A fresh registry rather than nil, matching cmd/run-once. Passing nil
	// registers on the *default* registry, which panics on a second
	// registration of the same series — unreachable for a one-shot binary, but
	// it makes the alert path single-use per process, which is a trap for any
	// caller that drives Run twice. This binary serves no /metrics endpoint, so
	// nothing is lost by not using the default registry.
	if !pkg.NewTaskPublisher(sender, pkg.NewMetrics(prometheus.NewRegistry())).
		PublishCreate(ctx, cmd) {
		return errors.Errorf(ctx, "publish heartbeat alert task failed")
	}
	glog.V(2).Infof("heartbeat alert published taskID=%s", string(cmd.TaskIdentifier))
	return nil
}
