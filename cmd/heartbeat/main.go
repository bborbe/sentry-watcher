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
	"net/http"
	"os"

	"github.com/bborbe/errors"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"

	"github.com/bborbe/sentry-watcher/pkg"
)

func main() {
	app := NewApplication()
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

// NewApplication creates an Application with its real dependencies.
//
// Returns the concrete *Application (not an interface): it is a service.Main
// config/DTO struct whose fields are reflected for arg/env binding, and the
// call site addresses &app.SentryDSN. The sibling cmd/check-heartbeat uses the
// same shape.
func NewApplication() *Application {
	return &Application{
		CreateHeartbeatEmitterMetrics: pkg.NewPushGatewayHeartbeatEmitterMetrics,
	}
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

	// PushGatewayURL is required rather than defaulted, for the reason the
	// sibling cmd/check-heartbeat gives: the emitter's outcome has to be
	// readable from the cluster after the pod is gone, and a deployment that
	// silently stops publishing it is exactly the blindness this metric exists
	// to end. A missing value must therefore fail startup loudly.
	PushGatewayURL string `required:"true" arg:"pushgateway-url" env:"PUSHGATEWAY_URL" usage:"Push gateway the emitter's per-cycle outcome is published to (e.g. http://pushgateway.monitoring.svc.cluster.local:9090)"`

	// CreateHeartbeatEmitterMetrics creates the publisher for the cycle's
	// outcome. Matches pkg.NewPushGatewayHeartbeatEmitterMetrics so tests can
	// substitute a recorder that captures what would have been pushed.
	// Production passes a nil client — the publisher supplies its own bounded
	// one, so a stuck gateway cannot hold the CronJob past its deadline.
	CreateHeartbeatEmitterMetrics HeartbeatEmitterMetricsFunc
}

// HeartbeatEmitterMetricsFunc creates the publisher for the emitter's per-cycle
// outcome. Matches pkg.NewPushGatewayHeartbeatEmitterMetrics so tests can
// substitute a recorder that captures what would have been pushed.
type HeartbeatEmitterMetricsFunc func(url string, client *http.Client) pkg.HeartbeatEmitterMetrics

// Run sends one heartbeat and returns.
//
// The success signal is the sender's own boundary audit line, emitted at V0
// with the status code and elapsed ms — the previous `glog.V(2).Infof` was
// suppressed at the default verbosity, so a successful cycle left no trace in
// `kubectl logs` at all.
func (a *Application) Run(ctx context.Context, _ libsentry.Client) error {
	sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
		DSN:      a.SentryDSN,
		ProxyURL: a.SentryProxy,
		Metrics:  a.createMetrics(),
	})
	if err := sender.Send(ctx); err != nil {
		return errors.Wrap(ctx, err, "send heartbeat")
	}
	return nil
}

// createMetrics builds the publisher, or returns nil when none is wired.
//
// The nil case is what the sibling tests construct: they exercise Run without a
// gateway, and the sender tolerates a nil recorder. A gateway that is down
// costs a metric, not a heartbeat.
func (a *Application) createMetrics() pkg.HeartbeatEmitterMetrics {
	if a.CreateHeartbeatEmitterMetrics == nil {
		return nil
	}
	return a.CreateHeartbeatEmitterMetrics(a.PushGatewayURL, nil)
}
