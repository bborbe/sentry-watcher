// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command sentry-heartbeat emits one synthetic Sentry event then exits.
//
// It is the emitter half of the zero-events detector: sentry-watcher asserts
// that a heartbeat event is visible, which distinguishes "the fleet is quiet"
// from "ingestion has stopped retaining events" — the ambiguity that let the
// 2026-09-06 blackout run three days undetected. Intended to run on a
// schedule (a CronJob) at an interval shorter than the watcher's staleness
// threshold.
package main

import (
	"context"
	"os"

	"github.com/bborbe/errors"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	"github.com/golang/glog"

	"github.com/bborbe/sentry-watcher/pkg"
)

func main() {
	app := &Application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

// Application is the heartbeat CLI config, with fields reflected for arg/env
// binding by service.Main.
//
// Exported so the external test package can construct it, matching the sibling
// cmd/run-once Application.
type Application struct {
	// SentryDSN carries display:"length" so a startup log prints its length
	// rather than the key.
	SentryDSN   string `required:"true"  arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"Project DSN the heartbeat is sent with"                                                      display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"sentry-proxy base URL; set it so the heartbeat traverses the same path as real fleet events"`
}

// Run sends one heartbeat and returns.
func (a *Application) Run(ctx context.Context, _ libsentry.Client) error {
	sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
		DSN:      a.SentryDSN,
		ProxyURL: a.SentryProxy,
	})
	if err := sender.Send(ctx); err != nil {
		return errors.Wrap(ctx, err, "send heartbeat")
	}
	glog.V(2).Infof("heartbeat sent tag=%s", pkg.HeartbeatTag)
	return nil
}
