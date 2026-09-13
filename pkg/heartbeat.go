// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

//counterfeiter:generate -o ../mocks/heartbeat_checker.go --fake-name HeartbeatChecker . HeartbeatChecker

// HeartbeatChecker verifies that Sentry is actually retaining events.
//
// A quiet fleet and dead ingestion both produce "no new issues", so counting
// issues cannot tell them apart. The heartbeat resolves that ambiguity: a
// known workload emits a deliberately-tagged error event on a schedule, and
// this checker asserts one is visible. Absence is unambiguous — there is no
// "legitimately quiet" reading of a missing heartbeat.
//
// The client is expected to be configured with the heartbeat query
// (SentryConfig.Query, e.g. "sentry-heartbeat"), so UnresolvedIssues returns
// heartbeat events rather than the fleet's real alerts.
type HeartbeatChecker interface {
	// Verify returns nil when a heartbeat event is visible in Sentry. A
	// non-nil error means none was found — ingestion is not retaining events,
	// or the heartbeat emitter has stopped.
	Verify(ctx context.Context) error
}

// NewHeartbeatChecker returns a HeartbeatChecker backed by the given client.
func NewHeartbeatChecker(client SentryClient) HeartbeatChecker {
	return &heartbeatChecker{client: client}
}

type heartbeatChecker struct {
	client SentryClient
}

func (c *heartbeatChecker) Verify(ctx context.Context) error {
	issues, err := c.client.UnresolvedIssues(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "query heartbeat")
	}
	if len(issues) == 0 {
		return errors.Errorf(
			ctx,
			"no heartbeat event visible in Sentry — ingestion is not retaining events",
		)
	}
	return nil
}
