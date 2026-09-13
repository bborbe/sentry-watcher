// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.HeartbeatChecker.Verify", func() {
	var (
		ctx    context.Context
		client *mocks.SentryClient
	)

	BeforeEach(func() {
		ctx = context.Background()
		client = &mocks.SentryClient{}
	})

	// The heartbeat is present: ingestion is retaining events, so the check
	// must stay silent. A false positive here would alert on every healthy poll.
	It("returns nil when a heartbeat event is visible", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{{ShortID: "SENTRY-HEARTBEAT"}}, nil)

		Expect(pkg.NewHeartbeatChecker(client).Verify(ctx)).To(Succeed())
	})

	// The core signal: no heartbeat visible. This is what a quiet fleet and
	// dead ingestion have in common, and only the heartbeat distinguishes them
	// — an empty result here means events are not being retained.
	It("returns an error naming the absence when no heartbeat is visible", func() {
		client.UnresolvedIssuesReturns(nil, nil)

		err := pkg.NewHeartbeatChecker(client).Verify(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no heartbeat event visible"))
	})

	// A query failure is not the same finding as an absent heartbeat — the
	// check could not run, so it must not be reported as "ingestion is dead".
	It("wraps a query error rather than reporting an absent heartbeat", func() {
		client.UnresolvedIssuesReturns(nil, stderrors.New("boom"))

		err := pkg.NewHeartbeatChecker(client).Verify(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("query heartbeat"))
		Expect(err.Error()).NotTo(ContainSubstring("no heartbeat event visible"))
	})
})
