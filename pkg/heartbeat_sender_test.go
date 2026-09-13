// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
