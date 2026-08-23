// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	task "github.com/bborbe/agent/command/task"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../mocks/task_publisher.go --fake-name TaskPublisher . TaskPublisher

// TaskPublisher sends a pre-built CreateTaskCommand via the supplied
// CreateCommandSender. Returns true on successful send, false on error.
type TaskPublisher interface {
	PublishCreate(ctx context.Context, cmd task.CreateCommand) bool
}

// NewTaskPublisher returns a TaskPublisher that wraps the given sender + metrics.
func NewTaskPublisher(sender task.CreateCommandSender, metrics Metrics) TaskPublisher {
	return &taskPublisher{sender: sender, metrics: metrics}
}

type taskPublisher struct {
	sender  task.CreateCommandSender
	metrics Metrics
}

func (p *taskPublisher) PublishCreate(ctx context.Context, cmd task.CreateCommand) bool {
	if err := p.sender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf(
			"publish create-task failed taskID=%s title=%q err=%v",
			string(cmd.TaskIdentifier),
			cmd.Title,
			err,
		)
		p.metrics.IncPublished("error")
		return false
	}
	glog.V(2).Infof(
		"published CreateTaskCommand taskID=%s title=%q",
		string(cmd.TaskIdentifier),
		cmd.Title,
	)
	p.metrics.IncPublished("create")
	return true
}
