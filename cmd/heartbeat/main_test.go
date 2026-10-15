// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	heartbeat "github.com/bborbe/sentry-watcher/cmd/heartbeat"
	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("Main", func() {
	// Build failures in this binary are otherwise invisible until a CronJob
	// runs it — the service never starts, so nothing logs.
	It("Compiles", func() {
		_, err := gexec.Build(".", "-mod=mod", "-buildvcs=false")
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("Run", func() {
	// A malformed DSN fails before any request leaves the pod, so this
	// exercises the real Run path without touching the network.
	It("rejects a malformed DSN before attempting a request", func() {
		app := &heartbeat.Application{
			SentryDSN: "https://o12345.ingest.sentry.io/6789",
		}

		err := app.Run(context.Background(), nil)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("dsn is missing"))
	})

	// Wiring, not behaviour: the publisher's own push is covered in pkg. What
	// this catches is the failure mode where the publisher is built but never
	// handed to the sender — the cycle would then be instrumented on paper and
	// silent in the cluster, which is exactly how the 2026-10-10/11 evidence was
	// lost.
	It("publishes the cycle outcome through the injected publisher", func() {
		sentry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer sentry.Close()

		recorder := &mocks.HeartbeatEmitterMetrics{}
		app := &heartbeat.Application{
			SentryDSN:   "https://abc123@o12345.ingest.sentry.io/6789",
			SentryProxy: sentry.URL,
			CreateHeartbeatEmitterMetrics: func(
				string,
				*http.Client,
			) pkg.HeartbeatEmitterMetrics {
				return recorder
			},
		}

		Expect(app.Run(context.Background(), nil)).To(Succeed())

		Expect(recorder.PublishPostedCallCount()).To(Equal(1))
		Expect(recorder.PublishNotReachedProxyCallCount()).To(Equal(0))
	})
})

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	// Without a suite timeout a hung test holds the runner until the CI-level
	// timeout — the only backstop, and a slow one. Matches pkg/suite_test.go.
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Heartbeat Suite", suiteConfig, reporterConfig)
}
