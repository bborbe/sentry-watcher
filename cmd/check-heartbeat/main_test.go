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

	"github.com/IBM/sarama"
	saramamocks "github.com/IBM/sarama/mocks"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	check "github.com/bborbe/sentry-watcher/cmd/check-heartbeat"
	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

// newFakeProducer returns a sarama mock SyncProducer that records sends in
// memory — no network connection.
//
// One send is expected: the alert path publishes exactly one CreateTaskCommand.
// The mock fails the spec on an unexpected send, which is what catches a caller
// that publishes more than the one-per-day task this binary promises.
func newFakeProducer(t GinkgoTInterface) libkafka.SyncProducer {
	producer := saramamocks.NewSyncProducer(t, sarama.NewConfig())
	// Returns the producer for chaining, not an error — nothing to assert here.
	_ = producer.ExpectSendMessageAndSucceed()
	return libkafka.NewSyncProducerFromSaramaSyncProducer(producer)
}

var _ = Describe("Run", func() {
	var (
		ctx     context.Context
		checker *mocks.HeartbeatChecker
		app     *check.Application
		// alerts counts producer creations, which happen only on the alert
		// path — so it is a direct proxy for "a task was attempted".
		alerts int
		// heartbeatMetrics records what would have been pushed to the gateway.
		heartbeatMetrics *mocks.HeartbeatMetrics
	)

	BeforeEach(func() {
		ctx = context.Background()
		checker = &mocks.HeartbeatChecker{}
		heartbeatMetrics = &mocks.HeartbeatMetrics{}
		alerts = 0
		app = &check.Application{
			Stage:          "dev",
			KafkaBrokers:   libkafka.Brokers{"localhost:9092"},
			SentryURL:      "https://bborbe.sentry.io",
			SentryOrg:      "bborbe",
			SentryAPIToken: "token",
			HeartbeatQuery: "source:sentry-heartbeat",
			TaskAssignee:   "bborbe",
			TaskStatus:     "in_progress",
			TaskPhase:      "planning",
			TargetVault:    "personal",
			CreateProducer: func(_ context.Context, _ libkafka.Brokers) (libkafka.SyncProducer, error) {
				alerts++
				return newFakeProducer(GinkgoT()), nil
			},
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
			CurrentDateTime: libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
			}),
		}
	})

	// The healthy path: ingestion is retaining events, so the run succeeds and
	// files nothing. A task here would be a false positive on every good cycle.
	It("returns nil and files no task when the heartbeat is present", func() {
		checker.VerifyReturns(pkg.HeartbeatObservation{}, nil)

		Expect(app.Run(ctx, nil)).To(Succeed())
		Expect(alerts).To(Equal(0))
	})

	// The finding, and the reason this binary exists: the alert has to be
	// *delivered*, not logged. The run also fails so the scheduled job shows it.
	It("files an alert task and fails the run when the heartbeat is missing", func() {
		checker.VerifyReturns(
			pkg.HeartbeatObservation{},
			stderrors.Join(pkg.ErrHeartbeatMissing, stderrors.New("stale")),
		)

		err := app.Run(ctx, nil)

		Expect(err).To(HaveOccurred())
		Expect(alerts).To(Equal(1))
	})

	// A check that could not run is not evidence that ingestion is dead.
	// Filing here would cry wolf exactly when the Sentry API is unreachable —
	// the failure mode the sentinel exists to separate.
	It("files no task when the check itself failed", func() {
		checker.VerifyReturns(pkg.HeartbeatObservation{}, stderrors.New("sentry unreachable"))

		err := app.Run(ctx, nil)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("heartbeat check could not run"))
		Expect(alerts).To(Equal(0))
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

// The expected alarm must not reach Sentry. It is already delivered twice — the
// vault task for a human, the failed run for the cluster — and reporting it a
// third time spends the org's ingest quota on a known condition, through the
// same proxy this check exists to watch.
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
