// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command sentry-watcher-run-once runs a single Sentry poll cycle then exits.
// Intended for local smoke-testing. No HTTP server, no poll loop.
package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/sentry-watcher/pkg"
	"github.com/bborbe/sentry-watcher/pkg/factory"
)

// httpClientTimeout bounds the single Sentry request.
const httpClientTimeout = 30 * time.Second

func main() {
	app := NewApplication()
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

// NewApplication creates an Application with default dependencies.
//
// Returns the concrete *Application (not an interface): Application is a
// service.Main config/DTO struct whose fields are reflected for arg/env binding,
// and the call site addresses its fields (&app.SentryDSN) for the SentryDSN/
// SentryProxy wiring. The go-architecture/constructor-returns-interface rule
// exempts DTO/config structs without a same-package interface; the sibling
// watchers (e.g. github-release-watcher cmd/run-once) use the same shape.
func NewApplication() *Application {
	return &Application{
		CreateWatcher: factory.CreateWatcher,
		CreateProducer: func(ctx context.Context, brokers libkafka.Brokers, name string) (libkafka.SyncProducer, error) {
			return libkafka.NewSyncProducerWithName(ctx, brokers, name)
		},
	}
}

// Application is the run-once CLI wiring, with injectable factories for tests.
type Application struct {
	SentryDSN   string `required:"false" arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"SentryDSN"    display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"Sentry Proxy"`

	Stage        string           `required:"true"  arg:"stage"         env:"STAGE"         usage:"Deployment stage (dev|prod)"`
	CursorPath   string           `required:"false" arg:"cursor-path"   env:"CURSOR_PATH"   usage:"Cursor persistence path"                                                                                                                                                                default:"/data/cursor.json"`
	SeedDate     string           `required:"false" arg:"seed-date"     env:"SEED_DATE"     usage:"Optional UTC date (2006-01-02) to seed the cursor with on cold-start; makes the first poll emit for every in-scope active issue dated after it. Empty = seed to current, emit nothing."`
	KafkaBrokers libkafka.Brokers `required:"true"  arg:"kafka-brokers" env:"KAFKA_BROKERS" usage:"Comma-separated Kafka broker list"`

	SentryURL      string `required:"false" arg:"sentry-url"       env:"SENTRY_URL"       usage:"Base URL of the Sentry org"                                                                               default:"https://bborbe.sentry.io"`
	SentryOrg      string `required:"false" arg:"sentry-org"       env:"SENTRY_ORG"       usage:"Sentry org slug"                                                                                          default:"bborbe"`
	SentryAPIToken string `required:"true"  arg:"sentry-api-token" env:"SENTRY_API_TOKEN" usage:"Sentry REST API Bearer token"                                                                                                                     display:"length"`
	SentryProjects string `required:"true"  arg:"sentry-projects"  env:"SENTRY_PROJECTS"  usage:"Comma-separated per-cluster project slug allowlist"                                                       default:"hell,nuke-dev,nuke-prod,quant"`
	SentryQuery    string `required:"false" arg:"sentry-query"     env:"SENTRY_QUERY"     usage:"Sentry org-issues search expression; empty = is:unresolved."`
	HeartbeatQuery string `required:"false" arg:"heartbeat-query"  env:"HEARTBEAT_QUERY"  usage:"Search expression for the zero-events heartbeat check (e.g. sentry-heartbeat). Empty disables the check."`

	TargetVault string `required:"false" arg:"target-vault" env:"TARGET_VAULT" usage:"Obsidian vault slug for the emitted task (e.g. 'personal'); empty = controller default (openclaw)."`

	// TopicPrefix selects the Kafka topic prefix used for CQRS topic construction
	// (e.g. "develop" / "master"); independent of Stage. Empty means unprefixed topics.
	TopicPrefix    base.TopicPrefix `required:"false" arg:"topic-prefix" env:"TOPIC_PREFIX" usage:"Kafka topic prefix for CQRS topic construction"`
	CreateWatcher  WatcherFunc
	CreateProducer SyncProducerFunc
}

// WatcherFunc creates a Watcher. Matches factory.CreateWatcher's signature
// exactly so tests can substitute a mock-returning closure. Named per
// go-functional-composition/func-type-name (interface + Func).
type WatcherFunc func(
	httpClient *http.Client,
	sender task.CreateCommandSender,
	cursorPath string,
	metrics pkg.Metrics,
	cfg pkg.TaskConfig,
	projectAllowlist []string,
	seedDate string,
	sentryCfg pkg.SentryConfig,
	heartbeatQuery string,
) pkg.Watcher

// SyncProducerFunc creates a Kafka sync producer. Matches
// libkafka.NewSyncProducerWithName so tests can stub with a fake producer.
// Named per go-functional-composition/func-type-name (interface + Func).
type SyncProducerFunc func(
	ctx context.Context,
	brokers libkafka.Brokers,
	name string,
) (libkafka.SyncProducer, error)

// Run executes a single poll cycle and returns.
func (a *Application) Run(ctx context.Context, _ libsentry.Client) error {
	if a.SeedDate != "" {
		if _, err := time.Parse("2006-01-02", a.SeedDate); err != nil {
			return errors.Wrapf(ctx, err, "parse seed date %q", a.SeedDate)
		}
	}

	projects := splitProjects(a.SentryProjects)
	if len(projects) == 0 {
		return errors.Errorf(ctx, "SENTRY_PROJECTS must list at least one project slug")
	}

	syncProducer, err := a.CreateProducer(ctx, a.KafkaBrokers, "sentry-watcher-run-once")
	if err != nil {
		return errors.Wrap(ctx, err, "create sync producer")
	}
	defer func() {
		if cerr := syncProducer.Close(); cerr != nil {
			glog.Warningf("close kafka sync producer: %v", cerr)
		}
	}()

	httpClient := &http.Client{Timeout: httpClientTimeout}
	metrics := pkg.NewMetrics(prometheus.NewRegistry())
	sender := factory.CreateKafkaSender(syncProducer, a.TopicPrefix, a.TargetVault)
	w := a.CreateWatcher(
		httpClient,
		sender,
		a.CursorPath,
		metrics,
		pkg.TaskConfig{
			Stage:    a.Stage,
			Assignee: "sentry-issue-analyzer",
			Status:   "in_progress",
			Phase:    "planning",
		},
		projects,
		a.SeedDate,
		pkg.SentryConfig{
			URL:      a.SentryURL,
			Org:      a.SentryOrg,
			APIToken: a.SentryAPIToken,
			Query:    a.SentryQuery,
		},
		a.HeartbeatQuery,
	)

	if err := w.Poll(ctx); err != nil {
		return errors.Wrap(ctx, err, "poll failed")
	}
	return nil
}

// splitProjects splits and trims the comma-separated project allowlist env.
func splitProjects(s string) []string {
	var projects []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	return projects
}
