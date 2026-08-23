// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command sentry-watcher-run-once runs a single go.dev poll cycle then
// exits. Intended for local smoke-testing. No HTTP server, no poll loop.
package main

import (
	"context"
	"net/http"
	"os"
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

// httpClientTimeout bounds the single go.dev request.
const httpClientTimeout = 30 * time.Second

func main() {
	app := NewApplication()
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

// NewApplication creates an Application with default dependencies.
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
	CursorPath   string           `required:"false" arg:"cursor-path"   env:"CURSOR_PATH"   usage:"Cursor persistence path"                                                                                                                                                                           default:"/data/cursor.json"`
	SeedVersion  string           `required:"false" arg:"seed-version"  env:"SEED_VERSION"  usage:"Optional go version (e.g. go1.26.4) to seed the cursor with on cold-start instead of the current latest; makes the poll emit a task for the current latest. Empty = seed to latest, emit nothing."`
	KafkaBrokers libkafka.Brokers `required:"true"  arg:"kafka-brokers" env:"KAFKA_BROKERS" usage:"Comma-separated Kafka broker list"`
	// TopicPrefix selects the Kafka topic prefix used for CQRS topic construction
	// (e.g. "develop" / "master"); independent of Stage. Empty means unprefixed topics.
	TopicPrefix    base.TopicPrefix `required:"false" arg:"topic-prefix"  env:"TOPIC_PREFIX"  usage:"Kafka topic prefix for CQRS topic construction"`
	TargetVault    string           `required:"false" arg:"target-vault"  env:"TARGET_VAULT"  usage:"Obsidian vault slug for the emitted task (e.g. 'personal'); empty = controller default (openclaw)."`
	CreateWatcher  WatcherFactory
	CreateProducer ProducerFactory
}

// WatcherFactory creates a Watcher. Matches factory.CreateWatcher's signature
// exactly so tests can substitute a mock-returning closure.
type WatcherFactory func(
	httpClient *http.Client,
	sender task.CreateCommandSender,
	cursorPath string,
	metrics pkg.Metrics,
	cfg pkg.TaskConfig,
	seedVersion string,
) pkg.Watcher

// ProducerFactory creates a Kafka sync producer. Matches
// libkafka.NewSyncProducerWithName so tests can stub with a fake producer.
type ProducerFactory func(
	ctx context.Context,
	brokers libkafka.Brokers,
	name string,
) (libkafka.SyncProducer, error)

// Run executes a single poll cycle and returns.
func (a *Application) Run(ctx context.Context, _ libsentry.Client) error {
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
	w := a.CreateWatcher(httpClient, sender, a.CursorPath, metrics, pkg.TaskConfig{
		Stage:    a.Stage,
		Assignee: "human",
		Status:   "in_progress",
		Phase:    "todo",
	}, a.SeedVersion)

	if err := w.Poll(ctx); err != nil {
		return errors.Wrap(ctx, err, "poll failed")
	}
	return nil
}
