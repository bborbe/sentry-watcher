// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/bborbe/sentry-watcher/pkg"
)

// These tests drive the real push client against a stub gateway rather than a
// mock of HeartbeatEmitterMetrics. The behaviour that matters is the wire
// format — which series are written, and whether a push happens at all — so a
// mock would assert the thing under test against itself.
var _ = Describe("pkg.NewPushGatewayHeartbeatEmitterMetrics", func() {
	var (
		ctx      context.Context
		server   *httptest.Server
		mu       sync.Mutex
		requests []pushedRequest
	)

	BeforeEach(func() {
		ctx = context.Background()
		mu = sync.Mutex{}
		requests = nil
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			mu.Lock()
			requests = append(requests, pushedRequest{
				path:        r.URL.Path,
				contentType: r.Header.Get("Content-Type"),
				body:        string(body),
			})
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
	})

	AfterEach(func() {
		server.Close()
	})

	// The whole reason the outcome is pushed rather than only logged: the failed
	// emitter pods of 2026-10-10/11 were garbage-collected within about a day,
	// so a log line dies with its pod and the next occurrence cannot be
	// reconstructed from one. This series outlives it.
	//
	// Both series are written on every push, the zero one included. A
	// pushgateway series that has never been pushed does not exist, so an alert
	// reading it would see no data rather than a zero — silence on the very
	// case it exists for.
	It("pushes both outcome gauges under its own group, zero-initialised", func() {
		pkg.NewPushGatewayHeartbeatEmitterMetrics(server.URL, nil).PublishPosted(ctx)

		Expect(requests).To(HaveLen(1))
		// Exactly the emitter's group: the check's `sentry_heartbeat` is a
		// prefix of it, so a substring assertion would pass on a shared group —
		// which would let either binary's push overwrite the other's series.
		Expect(requests[0].path).To(Equal("/metrics/job/sentry_heartbeat_emitter"))
		Expect(pushedValues(requests[0])).To(Equal(map[string]float64{
			"sentry_watcher_heartbeat_emitter_posted":            1,
			"sentry_watcher_heartbeat_emitter_not_reached_proxy": 0,
		}))
	})

	// The distinction the pair exists for. A cycle that starts and never reaches
	// the proxy is a different fault from one the proxy answers and refuses, and
	// a single series cannot tell them apart.
	It("records a cycle that never reached the proxy separately", func() {
		pkg.NewPushGatewayHeartbeatEmitterMetrics(server.URL, nil).PublishNotReachedProxy(ctx)

		Expect(requests).To(HaveLen(1))
		Expect(pushedValues(requests[0])).To(Equal(map[string]float64{
			"sentry_watcher_heartbeat_emitter_posted":            0,
			"sentry_watcher_heartbeat_emitter_not_reached_proxy": 1,
		}))
	})

	// Both series are gauges, and the type is the assertion here. A counter is
	// monotonically non-decreasing by definition, but this value goes 1 → 0 on
	// the next cycle of the other outcome — so typed as a counter it would
	// violate the contract, making rate() and increase() nonsense over it and
	// misleading anyone who read the `_total` name as a running total. The
	// family's own type in the pushed body is what pins that.
	It("exports both series as gauges, not counters", func() {
		pkg.NewPushGatewayHeartbeatEmitterMetrics(server.URL, nil).PublishPosted(ctx)

		Expect(requests).To(HaveLen(1))
		Expect(pushedTypes(requests[0])).To(Equal(map[string]dto.MetricType{
			"sentry_watcher_heartbeat_emitter_posted":            dto.MetricType_GAUGE,
			"sentry_watcher_heartbeat_emitter_not_reached_proxy": dto.MetricType_GAUGE,
		}))
	})

	// The push must stay on the gateway's replacing PUT default. Switching it to
	// POST ("push_add") is the obvious-looking fix for "these counters never
	// accumulate", and it is wrong: the pushgateway merges by metric *name* on
	// POST, it does not add values — the project states the non-goal itself,
	// "explicitly not an aggregator or distributed counter but rather a metrics
	// cache". A one-shot emitter pushes 0 or 1 under either method, so POST buys
	// no accumulation and costs the self-healing replace that clears a stale
	// family instead of letting it 400 the next push. Pinned here so the change
	// cannot be made again on the assumption that it would help.
	It("pushes with the replacing method, not an adding one", func() {
		var methodMu sync.Mutex
		var method string
		recorder := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methodMu.Lock()
				method = r.Method
				methodMu.Unlock()
				w.WriteHeader(http.StatusOK)
			}),
		)
		defer recorder.Close()

		pkg.NewPushGatewayHeartbeatEmitterMetrics(recorder.URL, nil).PublishPosted(ctx)

		methodMu.Lock()
		defer methodMu.Unlock()
		Expect(method).To(Equal(http.MethodPut))
	})

	// A gateway that is down costs a metric, not a heartbeat: the methods have
	// no error to return, and the emitter must not be reported as broken at the
	// moment it is doing its job.
	It("stays silent when the gateway rejects the push", func() {
		failing := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}),
		)
		defer failing.Close()

		Expect(func() {
			pkg.NewPushGatewayHeartbeatEmitterMetrics(failing.URL, nil).PublishPosted(ctx)
		}).NotTo(Panic())
	})

	// The reason the publisher supplies its own client rather than taking the
	// push package's default. push.New installs a bare &http.Client{} with no
	// timeout, and the push runs before the emitter returns, so a gateway that
	// accepts the connection and never answers would hold the CronJob open until
	// activeDeadlineSeconds — turning a metrics outage into a missed heartbeat
	// cycle, which is the failure this whole detector exists to catch.
	It("gives up instead of hanging when the gateway never answers", func() {
		stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(2 * time.Second)
			w.WriteHeader(http.StatusOK)
		}))
		defer stuck.Close()

		metrics := pkg.NewPushGatewayHeartbeatEmitterMetrics(
			stuck.URL,
			&http.Client{Timeout: 100 * time.Millisecond},
		)

		done := make(chan struct{})
		go func() {
			defer close(done)
			metrics.PublishNotReachedProxy(ctx)
		}()

		Eventually(done).WithTimeout(time.Second).Should(BeClosed())
	})
})

// pushedFamilies decodes every metric family in a pushed body.
//
// It decodes to EOF rather than reading one family, because a push carries the
// whole collector set — both outcomes of the cycle just run, not only the one
// that moved. The body is read through the exposition decoder and the format
// taken from the request's own Content-Type, for the reason heartbeatValue
// gives: the push client sends protobuf-delimited by default, so a line-oriented
// parser reads binary framing as a malformed metric, and pinning either the
// encoding or the float rendering would make these specs fail on a client_golang
// change rather than on a behaviour change.
func pushedFamilies(request pushedRequest) map[string]*dto.MetricFamily {
	format := expfmt.ResponseFormat(http.Header{"Content-Type": []string{request.contentType}})
	decoder := expfmt.NewDecoder(strings.NewReader(request.body), format)

	families := map[string]*dto.MetricFamily{}
	for {
		// Allocated fresh each iteration rather than reused: Decode fills the
		// message in place, and a dto.MetricFamily embeds a protobuf message
		// state containing a mutex, so copying one to keep it (or reusing the
		// receiver across decodes) is a lock copy — which govet's copylocks
		// rejects, and rightly, since the copy and the original would then
		// share a mutex they each believe they own.
		family := &dto.MetricFamily{}
		if err := decoder.Decode(family); err == io.EOF {
			return families
		} else if err != nil {
			Expect(err).NotTo(HaveOccurred())
			return families
		}
		families[family.GetName()] = family
	}
}

// pushedValues returns each family's gauge value by family name.
//
// The gauge accessor is deliberate: these series are gauges because their value
// goes 1 → 0 between cycles, so reading GetCounter() here would be reading a
// type the publisher must never emit. The sibling spec on pushedTypes asserts
// the type itself.
func pushedValues(request pushedRequest) map[string]float64 {
	values := map[string]float64{}
	for name, family := range pushedFamilies(request) {
		for _, metric := range family.GetMetric() {
			values[name] = metric.GetGauge().GetValue()
		}
	}
	return values
}

// pushedTypes returns each family's declared type by family name.
func pushedTypes(request pushedRequest) map[string]dto.MetricType {
	types := map[string]dto.MetricType{}
	for name, family := range pushedFamilies(request) {
		types[name] = family.GetType()
	}
	return types
}
