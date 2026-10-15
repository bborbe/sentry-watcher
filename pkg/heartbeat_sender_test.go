// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.HeartbeatSender.Send", func() {
	const dsn = "https://abc123@o12345.ingest.sentry.io/6789"

	var (
		ctx    context.Context
		server *httptest.Server
		got    struct {
			path        string
			contentType string
			body        string
		}
		status int
	)

	BeforeEach(func() {
		ctx = context.Background()
		got.path, got.contentType, got.body = "", "", ""
		status = http.StatusOK
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got.path = r.URL.Path
			got.contentType = r.Header.Get("Content-Type")
			got.body = string(b)
			w.WriteHeader(status)
		}))
		DeferCleanup(server.Close)
	})

	// The heartbeat must traverse the same path as real fleet events: a
	// heartbeat that bypasses the proxy proves nothing about the path that
	// actually broke in the 2026-09-06 blackout.
	It("routes through the proxy when one is configured", func() {
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        dsn,
			ProxyURL:   server.URL,
			HTTPClient: server.Client(),
		})

		Expect(sender.Send(ctx)).To(Succeed())
		Expect(got.path).To(Equal("/api/6789/envelope/"))
		Expect(got.contentType).To(Equal("application/x-sentry-envelope"))
	})

	// The tag is what the checker searches for. If the emitter stopped setting
	// it, the detector would report "ingestion dead" forever — indistinguishable
	// from the real failure.
	It("sets the tag the checker searches for", func() {
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        dsn,
			ProxyURL:   server.URL,
			HTTPClient: server.Client(),
		})

		Expect(sender.Send(ctx)).To(Succeed())
		Expect(got.body).To(ContainSubstring(`"source":"` + pkg.HeartbeatTag + `"`))
		// level=error is load-bearing: the checker reads the org-issues
		// endpoint, which lists error groups only. A message-level event would
		// never appear there.
		Expect(got.body).To(ContainSubstring(`"level":"error"`))
		Expect(got.body).To(ContainSubstring(dsn))
	})

	// The grammar is `Item = Headers "\n" Payload`: an item is its own header
	// line, then the payload. The builder used to emit the event object as the
	// second line, so Sentry parsed it as the item header and found no payload —
	// the envelope was accepted with a 200 and nothing was ever retained, which
	// is how the heartbeat stayed invisible for the whole outage it exists to
	// detect while every emitter run reported success. Asserting the *shape* is
	// what catches that: the ContainSubstring checks above pass on both the
	// two-line and the three-line body.
	It("emits the item header and the payload as separate lines", func() {
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        dsn,
			ProxyURL:   server.URL,
			HTTPClient: server.Client(),
		})

		Expect(sender.Send(ctx)).To(Succeed())

		lines := strings.Split(strings.TrimSuffix(got.body, "\n"), "\n")
		Expect(lines).To(HaveLen(3))
		Expect(lines[0]).To(ContainSubstring(`"dsn"`))
		Expect(lines[1]).To(MatchRegexp(`^\{"type":"event","length":\d+\}$`))
		// length must equal the payload's byte count, not merely be a number. A
		// correct-looking but numerically wrong length — an off-by-one, or the
		// header's own length — is rejected by Sentry exactly as the two-line body
		// was, so asserting only `\d+` would leave the one field this fix exists to
		// pin unprotected.
		Expect(lines[1]).To(Equal(fmt.Sprintf(`{"type":"event","length":%d}`, len(lines[2]))))
		Expect(lines[2]).To(ContainSubstring(`"level":"error"`))
		Expect(lines[2]).To(ContainSubstring(`"source":"` + pkg.HeartbeatTag + `"`))
	})

	It("returns an error on a non-2xx response", func() {
		status = http.StatusTooManyRequests
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        dsn,
			ProxyURL:   server.URL,
			HTTPClient: server.Client(),
		})

		err := sender.Send(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("429"))
	})

	// The outcome has to outlive the pod, so it is recorded at the boundary the
	// POST crosses rather than reconstructed from a log line later.
	It("records the posted outcome when the proxy answers", func() {
		recorder := &mocks.HeartbeatEmitterMetrics{}
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        dsn,
			ProxyURL:   server.URL,
			HTTPClient: server.Client(),
			Metrics:    recorder,
		})

		Expect(sender.Send(ctx)).To(Succeed())

		Expect(recorder.PublishPostedCallCount()).To(Equal(1))
		Expect(recorder.PublishNotReachedProxyCallCount()).To(Equal(0))
	})

	// A rejected envelope *did* reach the proxy, so it is not the same fault as
	// a cycle that never got there. Recording it as never-reached would point
	// the next reader at the emitter's transport instead of at whatever the
	// proxy refused — the distinction this pair exists to preserve.
	It("records a rejected envelope as posted, not as never-reached", func() {
		status = http.StatusTooManyRequests
		recorder := &mocks.HeartbeatEmitterMetrics{}
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        dsn,
			ProxyURL:   server.URL,
			HTTPClient: server.Client(),
			Metrics:    recorder,
		})

		Expect(sender.Send(ctx)).NotTo(Succeed())

		Expect(recorder.PublishPostedCallCount()).To(Equal(1))
		Expect(recorder.PublishNotReachedProxyCallCount()).To(Equal(0))
	})

	// The 2026-10-10 case: the proxy log showed no envelope POST at either
	// failed hour, so the cycle started and never arrived. That is this branch.
	It("records the never-reached outcome when the POST produces no response", func() {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		recorder := &mocks.HeartbeatEmitterMetrics{}
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        dsn,
			ProxyURL:   deadURL,
			HTTPClient: server.Client(),
			Metrics:    recorder,
		})

		Expect(sender.Send(ctx)).NotTo(Succeed())

		Expect(recorder.PublishNotReachedProxyCallCount()).To(Equal(1))
		Expect(recorder.PublishPostedCallCount()).To(Equal(0))
	})

	It("rejects a DSN missing its key, host, or project id", func() {
		sender := pkg.NewHeartbeatSender(pkg.HeartbeatSenderConfig{
			DSN:        "https://o12345.ingest.sentry.io/6789",
			HTTPClient: server.Client(),
		})

		err := sender.Send(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("dsn is missing"))
	})
})
