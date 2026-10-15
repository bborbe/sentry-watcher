// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"net/http"
	"time"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

//counterfeiter:generate -o ../mocks/heartbeat_emitter_metrics.go --fake-name HeartbeatEmitterMetrics . HeartbeatEmitterMetrics

// HeartbeatEmitterMetrics publishes the emitter's per-cycle outcome where
// Prometheus can see it.
//
// The emitter is a one-shot CronJob that lives ~100 ms and exits, so there is
// nothing to scrape, and its only other trace is a log line that dies with the
// pod. The 2026-10-10/11 failures had to be reconstructed from a job reason and
// the proxy's log because the pods had been garbage-collected within a day;
// these series outlive the pod, which is the whole point of publishing them.
//
// The two methods are deliberately two *outcomes*, not one success/failure
// pair: "the emitter never reached the proxy" and "the proxy answered and
// refused the envelope" are different faults with different owners, and a
// single series cannot separate them.
type HeartbeatEmitterMetrics interface {
	// PublishPosted reports that the POST reached the proxy and returned an
	// HTTP response, whatever its status. A rejected envelope lands here, not
	// in PublishNotReachedProxy.
	//
	// It returns no error on purpose, mirroring HeartbeatMetrics.PublishLastSeen:
	// a metrics push that fails must not fail the emitter. A gateway that is
	// down costs a metric, not a heartbeat.
	PublishPosted(ctx context.Context)

	// PublishNotReachedProxy reports that the POST never produced a response —
	// a transport failure, or a request that could not be built — so nothing
	// reached Sentry at all.
	PublishNotReachedProxy(ctx context.Context)
}

// heartbeatEmitterPushJob is the push-gateway group the emitter's outcome
// lands under.
//
// It is not the binary name: the metric's namespace already carries that, and
// this is the string an operator greps for on the gateway itself. It is a
// separate group from heartbeatPushJob so a push from either binary cannot
// overwrite the other's series.
const heartbeatEmitterPushJob = "sentry_heartbeat_emitter"

// heartbeatEmitterMetricSubsystem and the two names below complete the metric
// names to sentry_watcher_heartbeat_emitter_posted and
// sentry_watcher_heartbeat_emitter_not_reached_proxy, per
// go-prometheus/naming ({namespace}_{subsystem}_{name}).
//
// Neither carries the `_total` suffix, because neither is a counter. The value
// is a per-cycle state that goes 1 → 0 on the next cycle of the other outcome,
// and a Prometheus counter is monotonically non-decreasing by definition — see
// the constructor for why that makes a gauge the only honest type here.
const (
	heartbeatEmitterMetricSubsystem     = "heartbeat_emitter"
	heartbeatEmitterPostedName          = "posted"
	heartbeatEmitterNotReachedProxyName = "not_reached_proxy"
)

// heartbeatEmitterPushTimeout bounds one push to the gateway when no client is
// given.
//
// It exists for the same reason heartbeatPushTimeout does: push.New installs a
// bare &http.Client{} with no timeout at all, and the push runs before the
// emitter returns, so a gateway that accepts the connection and never answers
// would hold the CronJob open until activeDeadlineSeconds. Ten seconds stays
// well inside that deadline while still absorbing a slow gateway.
const heartbeatEmitterPushTimeout = 10 * time.Second

// NewPushGatewayHeartbeatEmitterMetrics returns a HeartbeatEmitterMetrics that
// pushes to the push gateway at url, under the heartbeatEmitterPushJob group.
//
// A nil client gets one bounded by heartbeatEmitterPushTimeout. Pass a client
// only to override that bound, which the tests do so the hang case stays fast.
//
// Both series are gauges, and that is a contract point rather than a style
// choice. The value is the *last cycle's* outcome: the emitter starts from zero
// every run, so it publishes 0 or 1, and each cycle overwrites the previous
// one — 1 → 0 whenever the next cycle takes the other branch. A Prometheus
// counter is monotonically non-decreasing by definition, so typing this as one
// would violate the contract: `rate()` and `increase()` compute nonsense over a
// value that resets every cycle, and any consumer reading a `_total` name is
// entitled to assume a running total that this series will never hold.
//
// A running total is also not obtainable here. The gateway stores what it is
// given — its POST ("push_add") merges by metric name, it does not add values,
// and the project states the non-goal itself: "explicitly not an aggregator or
// distributed counter but rather a metrics cache". No HTTP method changes that.
// The push is therefore left at the gateway's self-healing default (PUT,
// replacing the group), so a stale family left behind by an earlier push is
// cleared rather than surviving to 400 the next one.
//
// The three questions this instrumentation serves have three different
// producers, and this pair answers only the third:
//
//   - *how many* cycles were missed — kube-state-metrics, not these series:
//     `count_over_time((kube_job_status_failed{namespace="prod",
//     job_name=~"sentry-heartbeat-emitter-.+"} == 1)[48h:1m]) > 0`.
//   - *is anything completing at all* — the gateway's own `push_time_seconds`
//     age, which grows when the CronJob stops pushing.
//   - *which way the last cycle failed* — this pair.
//
// The per-cycle record an observation window needs comes from that first
// producer too, not from these gauges: a non-2xx response or a transport
// failure returns an error from Send, so the Job exits non-zero and
// kube-state-metrics records `kube_job_status_failed` for that cycle — which
// carries each cycle's send outcome for as long as Prometheus retains it,
// where a last-value gauge carries only the most recent one.
func NewPushGatewayHeartbeatEmitterMetrics(
	url string,
	client *http.Client,
) HeartbeatEmitterMetrics {
	if client == nil {
		client = &http.Client{Timeout: heartbeatEmitterPushTimeout}
	}
	posted := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Subsystem: heartbeatEmitterMetricSubsystem,
		Name:      heartbeatEmitterPostedName,
		Help: "Whether the most recent heartbeat-emitter cycle's POST reached the proxy: 1 when " +
			"it returned an HTTP response, accepted or rejected, and 0 when it did not. The " +
			"complementary value is carried by " +
			"sentry_watcher_heartbeat_emitter_not_reached_proxy, so exactly one of the pair " +
			"reads 1 after every cycle.",
	})
	notReachedProxy := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Subsystem: heartbeatEmitterMetricSubsystem,
		Name:      heartbeatEmitterNotReachedProxyName,
		Help: "Whether the most recent heartbeat-emitter cycle's POST failed before the proxy " +
			"answered: 1 when it produced no HTTP response at all, so nothing reached Sentry, " +
			"and 0 when the proxy did answer. A 1 here means the emitter's transport or the " +
			"proxy's reachability is broken — never that Sentry rejected the envelope, which is " +
			"what sentry_watcher_heartbeat_emitter_posted records.",
	})
	// Both gauges are attached to the pusher below, so both are gathered and
	// pushed on every run — including a run where neither branch set a value.
	// That is the mechanism that keeps a reader from seeing *no data* rather
	// than a zero on the very case the pair exists for, and it is worth stating
	// precisely: a pushed series exists because its collector was gathered, not
	// because a value was written to it. An .Add(0) here would be a no-op on a
	// fresh gauge and would credit the wrong line with the behaviour.
	return &pushGatewayHeartbeatEmitterMetrics{
		posted:          posted,
		notReachedProxy: notReachedProxy,
		pusher: push.New(url, heartbeatEmitterPushJob).
			Client(client).
			Collector(posted).
			Collector(notReachedProxy),
	}
}

type pushGatewayHeartbeatEmitterMetrics struct {
	posted          prometheus.Gauge
	notReachedProxy prometheus.Gauge
	pusher          *push.Pusher
}

// PublishPosted records this cycle's outcome as posted. It sets rather than
// increments: the series is the last cycle's state, and a fresh process starts
// from the pre-initialised zero, so both gauges describe this cycle by the time
// the push runs.
func (m *pushGatewayHeartbeatEmitterMetrics) PublishPosted(ctx context.Context) {
	m.posted.Set(1)
	m.push(ctx)
}

// PublishNotReachedProxy records this cycle's outcome as never having reached
// the proxy — the branch a rejected envelope must not land in.
func (m *pushGatewayHeartbeatEmitterMetrics) PublishNotReachedProxy(ctx context.Context) {
	m.notReachedProxy.Set(1)
	m.push(ctx)
}

// push publishes the current value of both gauges.
//
// A failure is logged and swallowed. The emitter's job is to put a heartbeat
// into Sentry; losing the report of how that went is a strictly smaller
// failure than reporting the emitter itself as broken, so publishing never
// fails the run. The push carries the whole collector set rather than only the
// series that moved, so the gateway holds both outcomes of the cycle just run
// — a one-sided update would leave the other series reading a previous cycle.
func (m *pushGatewayHeartbeatEmitterMetrics) push(ctx context.Context) {
	if err := m.pusher.PushContext(ctx); err != nil {
		glog.Warningf("push heartbeat emitter metric: %v", err)
	}
}
