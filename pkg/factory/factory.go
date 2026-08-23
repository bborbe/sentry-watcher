// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package factory wires concrete dependencies for the sentry-watcher binary.
package factory

import (
	"net/http"

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
func CreateWatcher(
	httpClient *http.Client,
	sender task.CreateCommandSender,
	cursorPath string,
	metrics pkg.Metrics,
	cfg pkg.TaskConfig,
	projectAllowlist []string,
	seedDate string,
	sentryCfg pkg.SentryConfig,
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
	)
}
