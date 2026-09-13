// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	heartbeat "github.com/bborbe/sentry-watcher/cmd/heartbeat"
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
})

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	RunSpecs(t, "Heartbeat Suite", suiteConfig, reporterConfig)
}
