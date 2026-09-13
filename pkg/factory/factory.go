// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package factory wires concrete dependencies for the sentry-watcher binary.
package factory

import (
	"net/http"
	"time"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	libkafka "github.com/bborbe/kafka"
	"github.com/bborbe/log"

	"github.com/bborbe/sentry-watcher/pkg"
)

// CreateKafkaSender constructs a typed create-task command sender backed by a
// Kafka sync producer. defaultVault is the Obsidian vault slug substituted into
// each command's TargetVault when unset (empty = controller default, openclaw).
func CreateKafkaSender(
	syncProducer libkafka.SyncProducer,
	topicPrefix base.TopicPrefix,
	defaultVault string,
) task.CreateCommandSender {
	sender := cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory)
	return task.NewCreateCommandSender(sender, defaultVault)
}

// CreateWatcher wires all dependencies and returns a ready-to-use Watcher.
//
// Pure composition — no I/O. The Kafka sync producer and the task sender are
// constructed by the caller (so it controls connection lifecycle + cleanup).
// heartbeatQuery, when non-empty, enables the zero-events heartbeat check: the
// watcher asserts a heartbeat event matching that expression arrived within
// heartbeatMaxAge. Empty disables the check, which is the correct configuration
// for a deployment with no heartbeat emitter — an enabled check with no emitter
// would report "ingestion dead" on every cycle.
//
// heartbeatMaxAge is how old the newest heartbeat may be and still count as
// present. It is required whenever heartbeatQuery is set: the org-issues
// endpoint applies no time window of its own, so without a bound the check
// passes forever once a single heartbeat has been seen.
func CreateWatcher(
	httpClient *http.Client,
	sender task.CreateCommandSender,
	cursorPath string,
	metrics pkg.Metrics,
	cfg pkg.TaskConfig,
	projectAllowlist []string,
	seedDate string,
	sentryCfg pkg.SentryConfig,
	heartbeatQuery string,
	heartbeatMaxAge time.Duration,
) pkg.Watcher {
	client := pkg.NewSentryClient(httpClient, sentryCfg)
	publisher := pkg.NewTaskPublisher(sender, metrics)

	// The heartbeat check searches a different expression than the watcher's
	// own scan, so it needs its own client rather than sharing the one above.
	var heartbeat pkg.HeartbeatChecker
	if heartbeatQuery != "" {
		heartbeatCfg := sentryCfg
		heartbeatCfg.Query = heartbeatQuery
		heartbeat = pkg.NewHeartbeatChecker(pkg.HeartbeatCheckerConfig{
			Client: pkg.NewSentryClient(httpClient, heartbeatCfg),
			MaxAge: heartbeatMaxAge,
		})
	}

	return pkg.NewWatcher(
		client,
		publisher,
		metrics,
		pkg.NewFileCursorStore(cursorPath),
		cfg,
		projectAllowlist,
		seedDate,
		heartbeat,
	)
}
