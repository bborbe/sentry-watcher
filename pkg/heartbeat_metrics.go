// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

//counterfeiter:generate -o ../mocks/heartbeat_metrics.go --fake-name HeartbeatMetrics . HeartbeatMetrics

// HeartbeatMetrics publishes the check's reading where Prometheus can see it.
//
// The check is a one-shot CronJob that lives ~100 ms and exits, so there is
// nothing to scrape; the reading has to be pushed. This interface is the seam
// that keeps the binary's transport out of the checker.
type HeartbeatMetrics interface {
	// PublishLastSeen reports the newest heartbeat's lastSeen.
	//
	// It returns no error on purpose. A metrics push that fails must not fail
	// the check: the check's job is to establish whether ingestion is retaining
	// events, and losing the report of that finding is a strictly smaller
	// failure than reporting the check itself as broken. Failures are logged
	// (go-prometheus/push-gateway-non-fatal).
	//
	// A zero newest is not published — see pushGatewayHeartbeatMetrics.
	PublishLastSeen(ctx context.Context, newest time.Time)
}

// heartbeatPushJob is the push-gateway group the reading lands under.
//
// It is not the binary name: the metric's namespace already carries that, and
// this is the string an operator greps for on the gateway itself.
const heartbeatPushJob = "sentry_heartbeat"

// heartbeatMetricSubsystem and heartbeatMetricName complete the metric name to
// sentry_watcher_heartbeat_last_seen_timestamp_seconds, per
// go-prometheus/naming ({namespace}_{subsystem}_{name}_{unit}).
const (
	heartbeatMetricSubsystem = "heartbeat"
	heartbeatMetricName      = "last_seen_timestamp_seconds"
)

// NewPushGatewayHeartbeatMetrics returns a HeartbeatMetrics that pushes to the
// push gateway at url, under the heartbeatPushJob group.
func NewPushGatewayHeartbeatMetrics(url string) HeartbeatMetrics {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Subsystem: heartbeatMetricSubsystem,
		Name:      heartbeatMetricName,
		Help: "Unix timestamp of the newest Sentry heartbeat event the check could see. " +
			"An alert on time() - this value reports ingestion that has stopped retaining " +
			"events; it is a timestamp rather than an age so that a dead pusher makes the " +
			"computed age grow instead of freezing at its last healthy value.",
	})
	return &pushGatewayHeartbeatMetrics{
		gauge:  gauge,
		pusher: push.New(url, heartbeatPushJob).Collector(gauge),
	}
}

type pushGatewayHeartbeatMetrics struct {
	gauge  prometheus.Gauge
	pusher *push.Pusher
}

// PublishLastSeen sets the gauge and pushes it.
//
// A zero newest is deliberately *not* pushed. Zero means the check could not
// establish a timestamp — no heartbeat event was visible, its lastSeen would
// not parse, or the query failed. Leaving the gateway holding the previous
// timestamp is the correct signal in all three cases: the computed age keeps
// growing, so the alert fires on "this check can no longer verify ingestion"
// rather than going quiet. Overwriting the series with a zero would instead
// read as a heartbeat from 1970 and pin the age at ~56 years.
func (m *pushGatewayHeartbeatMetrics) PublishLastSeen(ctx context.Context, newest time.Time) {
	if newest.IsZero() {
		glog.V(2).Infof("heartbeat lastSeen unknown — leaving gateway value in place")
		return
	}
	m.gauge.Set(float64(newest.Unix()))
	if err := m.pusher.PushContext(ctx); err != nil {
		glog.Warningf("push heartbeat last-seen metric: %v", err)
	}
}
