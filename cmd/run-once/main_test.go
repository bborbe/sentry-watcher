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
	task "github.com/bborbe/agent/command/task"
	libkafka "github.com/bborbe/kafka"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/gexec"

	runonce "github.com/bborbe/sentry-watcher/cmd/run-once"
	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

// fakeProducerFactory returns a sarama mock SyncProducer that records calls in
// memory — no network connection.
func fakeProducerFactory(
	t GinkgoTInterface,
) func(context.Context, libkafka.Brokers, string) (libkafka.SyncProducer, error) {
	return func(_ context.Context, _ libkafka.Brokers, _ string) (libkafka.SyncProducer, error) {
		return libkafka.NewSyncProducerFromSaramaSyncProducer(
			saramamocks.NewSyncProducer(t, sarama.NewConfig()),
		), nil
	}
}

var _ = Describe("Run", func() {
	var (
		ctx         context.Context
		watcherMock *mocks.Watcher
		app         *runonce.Application
	)

	BeforeEach(func() {
		ctx = context.Background()
		watcherMock = &mocks.Watcher{}
		app = &runonce.Application{
			Stage:          "dev",
			CursorPath:     "/tmp/cursor.json",
			KafkaBrokers:   libkafka.Brokers{"localhost:9092"},
			CreateProducer: fakeProducerFactory(GinkgoT()),
		}
	})

	watcherMockFactory := func() runonce.WatcherFactory {
		return func(
			_ *http.Client,
			_ task.CreateCommandSender,
			_ string,
			_ pkg.Metrics,
			_ pkg.TaskConfig,
			_ []string,
			_ string,
			_ pkg.SentryConfig,
		) pkg.Watcher {
			return watcherMock
		}
	}

	It("Poll succeeds returns nil", func() {
		watcherMock.PollReturns(nil)
		app.CreateWatcher = watcherMockFactory()

		err := app.Run(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(watcherMock.PollCallCount()).To(Equal(1))
	})

	It("Poll fails returns wrapped error", func() {
		watcherMock.PollReturns(stderrors.New("kafka unavailable"))
		app.CreateWatcher = watcherMockFactory()

		err := app.Run(ctx, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("poll failed"))
	})
})

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
	RunSpecs(t, "Run-Once Suite", suiteConfig, reporterConfig)
}
