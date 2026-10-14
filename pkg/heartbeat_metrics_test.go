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

// pushedRequest is one request the stub gateway received.
type pushedRequest struct {
	path        string
	contentType string
	body        string
}

// These tests drive the real push client against a stub gateway rather than a
// mock of HeartbeatMetrics. The behaviour that matters is the wire format —
// which series name is written, and whether a push happens at all — so a mock
// would assert the thing under test against itself.
var _ = Describe("pkg.NewPushGatewayHeartbeatMetrics", func() {
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

	It("pushes the lastSeen as an absolute Unix timestamp under the heartbeat group", func() {
		newest := time.Unix(1792002166, 0)

		pkg.NewPushGatewayHeartbeatMetrics(server.URL, nil).PublishLastSeen(ctx, newest)

		Expect(requests).To(HaveLen(1))
		Expect(requests[0].path).To(ContainSubstring("/metrics/job/sentry_heartbeat"))
		Expect(heartbeatValue(requests[0])).To(Equal(float64(newest.Unix())))
	})

	// The guard that decides whether a dead check alerts or goes silent. A zero
	// is "the check could not establish a timestamp", and pushing it would write
	// a heartbeat from 1970 — pinning the computed age at ~56 years and turning
	// a transient query failure into a permanent alert. Leaving the gateway
	// holding the previous value instead lets the age keep growing, which is the
	// honest signal for "this check can no longer verify ingestion".
	It("pushes nothing when the reading is unknown", func() {
		pkg.NewPushGatewayHeartbeatMetrics(server.URL, nil).PublishLastSeen(ctx, time.Time{})

		Expect(requests).To(BeEmpty())
	})

	// A gateway that is down costs a metric, not a verdict: PublishLastSeen has
	// no error to return, and the check must not be reported as broken at the
	// moment it is working.
	It("stays silent when the gateway rejects the push", func() {
		failing := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}),
		)
		defer failing.Close()

		Expect(func() {
			pkg.NewPushGatewayHeartbeatMetrics(failing.URL, nil).
				PublishLastSeen(ctx, time.Unix(1792002166, 0))
		}).NotTo(Panic())
	})

	// The reason the publisher supplies its own client rather than taking the
	// push package's default. The push runs *before* the verdict is acted on, so
	// a gateway that accepts the connection and never answers would block the
	// run past the point where the alert task is filed — turning a metrics
	// outage into a silent detector outage, which is the failure this whole
	// detector exists to catch. push.New installs a bare &http.Client{} with no
	// timeout, so unbounded is the default unless the publisher bounds it.
	It("gives up instead of hanging when the gateway never answers", func() {
		stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(2 * time.Second)
			w.WriteHeader(http.StatusOK)
		}))
		defer stuck.Close()

		metrics := pkg.NewPushGatewayHeartbeatMetrics(
			stuck.URL,
			&http.Client{Timeout: 100 * time.Millisecond},
		)

		done := make(chan struct{})
		go func() {
			defer close(done)
			metrics.PublishLastSeen(ctx, time.Unix(1792002166, 0))
		}()

		Eventually(done).WithTimeout(time.Second).Should(BeClosed())
	})

	// Successive pushes must replace the value rather than accumulate groups:
	// the series is the newest heartbeat, not a history of readings.
	It("replaces the value on a later push to the same group", func() {
		metrics := pkg.NewPushGatewayHeartbeatMetrics(server.URL, nil)

		metrics.PublishLastSeen(ctx, time.Unix(1792000000, 0))
		metrics.PublishLastSeen(ctx, time.Unix(1792002166, 0))

		Expect(requests).To(HaveLen(2))
		Expect(requests[0].path).To(Equal(requests[1].path))
		Expect(heartbeatValue(requests[1])).To(Equal(float64(1792002166)))
	})
})

// heartbeatValue returns the value of the heartbeat series in a pushed body.
//
// The body is decoded with the exposition decoder rather than string-matched,
// and the format is taken from the request's own Content-Type. The push client
// sends protobuf-delimited by default, so a line-oriented parser reads binary
// framing as a malformed metric — and pinning either the encoding or the float
// rendering would make this spec fail on a client_golang change rather than on
// a behaviour change.
func heartbeatValue(request pushedRequest) float64 {
	format := expfmt.ResponseFormat(http.Header{"Content-Type": []string{request.contentType}})

	var family dto.MetricFamily
	err := expfmt.NewDecoder(strings.NewReader(request.body), format).Decode(&family)
	Expect(err).NotTo(HaveOccurred())
	Expect(family.GetName()).To(Equal("sentry_watcher_heartbeat_last_seen_timestamp_seconds"))
	Expect(family.GetMetric()).To(HaveLen(1))

	return family.GetMetric()[0].GetGauge().GetValue()
}
