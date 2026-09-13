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

// CreateHeartbeatChecker builds the zero-events checker: a Sentry client
// searching query, paired with the maxAge staleness bound.
//
// The client is a second one rather than the watcher's own — the two search
// different expressions. Whether the check is enabled at all is a boot-time
// decision and so lives in the caller, not here.
func CreateHeartbeatChecker(
	httpClient *http.Client,
	sentryCfg pkg.SentryConfig,
	query string,
	maxAge time.Duration,
) pkg.HeartbeatChecker {
	heartbeatCfg := sentryCfg
	heartbeatCfg.Query = query
	return pkg.NewHeartbeatChecker(pkg.HeartbeatCheckerConfig{
		Client: pkg.NewSentryClient(httpClient, heartbeatCfg),
		MaxAge: maxAge,
	})
}

// CreateWatcher wires all dependencies and returns a ready-to-use Watcher.
//
// Pure composition — no I/O and no conditionals. The Kafka sync producer, the
// task sender, and the heartbeat checker are all constructed by the caller (so
// it owns connection lifecycle, cleanup, and the boot-time enable/disable
// decisions).
//
// heartbeat is the zero-events check, or nil to disable it — which is the
// correct configuration for a deployment with no heartbeat emitter, since an
// enabled check with no emitter reports "ingestion dead" on every cycle.
func CreateWatcher(
	httpClient *http.Client,
	sender task.CreateCommandSender,
	cursorPath string,
	metrics pkg.Metrics,
	cfg pkg.TaskConfig,
	projectAllowlist []string,
	seedDate string,
	sentryCfg pkg.SentryConfig,
	heartbeat pkg.HeartbeatChecker,
) pkg.Watcher {
	client := pkg.NewSentryClient(httpClient, sentryCfg)
	publisher := pkg.NewTaskPublisher(sender, metrics)

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
