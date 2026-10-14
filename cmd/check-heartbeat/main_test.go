// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"context"
	stderrors "errors"
	"net/http"
	"testing"
	"time"

	"github.com/bborbe/errors"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	check "github.com/bborbe/sentry-watcher/cmd/check-heartbeat"
	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("Run", func() {
	var (
		ctx     context.Context
		checker *mocks.HeartbeatChecker
		app     *check.Application
		// heartbeatMetrics records what would have been pushed to the gateway.
		heartbeatMetrics *mocks.HeartbeatMetrics
	)

	BeforeEach(func() {
		ctx = context.Background()
		checker = &mocks.HeartbeatChecker{}
		heartbeatMetrics = &mocks.HeartbeatMetrics{}
		app = &check.Application{
			Stage:          "dev",
			SentryURL:      "https://bborbe.sentry.io",
			SentryOrg:      "bborbe",
			SentryAPIToken: "token",
			HeartbeatQuery: "source:sentry-heartbeat",
			CreateChecker: func(
				_ *http.Client,
				_ pkg.SentryConfig,
				_ string,
				_ time.Duration,
			) pkg.HeartbeatChecker {
				return checker
			},
			PushGatewayURL: "http://pushgateway.monitoring.svc.cluster.local:9090",
			CreateHeartbeatMetrics: func(_ string, _ *http.Client) pkg.HeartbeatMetrics {
				return heartbeatMetrics
			},
		}
	})

	// The healthy path: ingestion is retaining events, so the run succeeds.
	It("returns nil when the heartbeat is present", func() {
		checker.VerifyReturns(pkg.HeartbeatObservation{}, nil)

		Expect(app.Run(ctx, nil)).To(Succeed())
	})

	// The finding still fails the run, so the scheduled job shows it — but that
	// failure is now the *cluster* signal only. The human-facing alert is the
	// Prometheus rule over the published metric, deliberately not keyed on this
	// exit code, so this spec asserts the sentinel rather than a filed task.
	It("fails the run and keeps the sentinel when the heartbeat is missing", func() {
		checker.VerifyReturns(
			pkg.HeartbeatObservation{},
			stderrors.Join(pkg.ErrHeartbeatMissing, stderrors.New("stale")),
		)

		err := app.Run(ctx, nil)

		Expect(err).To(HaveOccurred())
		Expect(stderrors.Is(err, pkg.ErrHeartbeatMissing)).To(BeTrue())
	})

	// A check that could not run is not evidence that ingestion is dead — the
	// sentinel is what lets a caller tell the two apart, and the wrapping here
	// must not blur them into one "the check failed" reading.
	It("separates a check that could not run from the finding", func() {
		checker.VerifyReturns(pkg.HeartbeatObservation{}, stderrors.New("sentry unreachable"))

		err := app.Run(ctx, nil)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("heartbeat check could not run"))
		Expect(stderrors.Is(err, pkg.ErrHeartbeatMissing)).To(BeFalse())
	})

	It("publishes the reading when the heartbeat is present", func() {
		fresh := time.Date(2026, 9, 13, 11, 30, 0, 0, time.UTC)
		checker.VerifyReturns(pkg.HeartbeatObservation{Newest: fresh}, nil)

		Expect(app.Run(ctx, nil)).To(Succeed())

		Expect(heartbeatMetrics.PublishLastSeenCallCount()).To(Equal(1))
		_, published := heartbeatMetrics.PublishLastSeenArgsForCall(0)
		Expect(published).To(Equal(fresh))
	})

	// The case that justifies returning an observation at all. The verdict here
	// is bad, but the alert fires on the *age* of this timestamp — so a run that
	// published only on success would withhold the metric at the one moment the
	// alert has to work.
	It("publishes the reading even when the heartbeat is stale", func() {
		stale := time.Date(2026, 9, 13, 5, 0, 0, 0, time.UTC)
		checker.VerifyReturns(
			pkg.HeartbeatObservation{Newest: stale},
			stderrors.Join(pkg.ErrHeartbeatMissing, stderrors.New("stale")),
		)

		Expect(app.Run(ctx, nil)).To(HaveOccurred())

		Expect(heartbeatMetrics.PublishLastSeenCallCount()).To(Equal(1))
		_, published := heartbeatMetrics.PublishLastSeenArgsForCall(0)
		Expect(published).To(Equal(stale))
	})

	// A check that could not establish a timestamp must say exactly that, rather
	// than have a plausible-looking value invented for it. The publisher is the
	// one place that decides what "unknown" means for the gateway.
	It("passes an unknown reading through when the check established no timestamp", func() {
		checker.VerifyReturns(pkg.HeartbeatObservation{}, stderrors.New("sentry unreachable"))

		Expect(app.Run(ctx, nil)).To(HaveOccurred())

		Expect(heartbeatMetrics.PublishLastSeenCallCount()).To(Equal(1))
		_, published := heartbeatMetrics.PublishLastSeenArgsForCall(0)
		Expect(published.IsZero()).To(BeTrue())
	})
})

// The expected alarm must not reach Sentry. It is already carried twice — by the
// age metric the Prometheus alert fires on, and by the failed run — and
// reporting it a third time spends the org's ingest quota on a known condition,
// through the same proxy this check exists to watch.
var _ = Describe("ExcludeHeartbeatMissing", func() {
	It("excludes the heartbeat-missing error the run returns", func() {
		options := service.NewOptions(check.ExcludeHeartbeatMissing)
		err := errors.Wrap(context.Background(), pkg.ErrHeartbeatMissing, "heartbeat missing")

		Expect(excluded(options.ExcludeErrors, err)).To(BeTrue())
	})

	// A check that could not run is the one failure nobody else carries, so it
	// has to stay reportable.
	It("keeps unrelated errors reportable", func() {
		options := service.NewOptions(check.ExcludeHeartbeatMissing)

		Expect(excluded(options.ExcludeErrors, stderrors.New("sentry unreachable"))).To(BeFalse())
	})
})

// excluded reports whether any exclusion predicate claims err.
func excluded(exclusions libsentry.ExcludeErrors, err error) bool {
	for _, exclude := range exclusions {
		if exclude(err) {
			return true
		}
	}
	return false
}

var _ = Describe("Main", func() {
	It("Compiles", func() {
		_, err := gexec.Build(".", "-mod=mod", "-buildvcs=false")
		Expect(err).NotTo(HaveOccurred())
	})
})

func TestSuite(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 120 * time.Second
	RunSpecs(t, "Check-Heartbeat Suite", suiteConfig, reporterConfig)
}
