// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"testing"
	"time"

	"github.com/bborbe/cron"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"
)

// Main compiles the root binary via gexec.Build as the go-testing/
// main-test-with-compiles guard — make test fails if the sentry-watcher binary
// fails to link. This suite exists at the repo root alongside main.go.
var _ = Describe("Main", func() {
	It("Compiles", func() {
		_, err := gexec.Build(".", "-mod=mod", "-buildvcs=false")
		Expect(err).NotTo(HaveOccurred())
	})
})

// CronExpression guards the scheduler config. bborbe/cron's CreateDefaultParser
// is strict 6-field (seconds-first); a 5-field "0 0 * * *" is rejected, and the
// canonical daily-at-midnight default fires at the next 00:00Z.
var _ = Describe("CronExpression", func() {
	It("parses the canonical daily-at-midnight default", func() {
		schedule, err := cron.CreateDefaultParser().Parse("0 0 0 * * ?")
		Expect(err).NotTo(HaveOccurred())
		now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
		Expect(schedule.Next(now)).To(Equal(time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)))
	})

	It("rejects a 5-field expression instead of silently running hourly", func() {
		_, err := cron.CreateDefaultParser().Parse("0 0 * * *")
		Expect(err).To(HaveOccurred())
	})
})

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 120 * time.Second
	RunSpecs(t, "Main Suite", suiteConfig, reporterConfig)
}
