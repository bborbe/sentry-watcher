// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.HeartbeatChecker.Verify", func() {
	var (
		ctx    context.Context
		client *mocks.SentryClient
		now    time.Time
		cfg    pkg.HeartbeatCheckerConfig
	)

	// lastSeenOf renders a heartbeat that last arrived the given age before `now`.
	lastSeenOf := func(age time.Duration) pkg.Issue {
		return pkg.Issue{
			ShortID:  "SENTRY-HEARTBEAT",
			LastSeen: now.Add(-age).Format(time.RFC3339),
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		client = &mocks.SentryClient{}
		now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
		cfg = pkg.HeartbeatCheckerConfig{
			Client: client,
			MaxAge: 6 * time.Hour,
			CurrentDateTimeGetter: libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
		}
	})

	// The heartbeat is recent: ingestion is retaining events, so the check must
	// stay silent. A false positive here would alert on every healthy poll.
	It("returns nil when a recent heartbeat event is visible", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{lastSeenOf(30 * time.Minute)}, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)
		Expect(err).To(Succeed())
	})

	// The bound is inclusive: a heartbeat exactly at MaxAge is still within it.
	It("returns nil when the heartbeat is exactly at the MaxAge bound", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{lastSeenOf(6 * time.Hour)}, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)
		Expect(err).To(Succeed())
	})

	// The load-bearing case for this commit: the endpoint applies no time window,
	// so a heartbeat that is present but old must NOT satisfy the check. Before
	// the age bound, this passed — meaning the detector would have stayed silent
	// through a repeat of the 2026-09-06 blackout.
	It("returns an error when the only visible heartbeat is older than MaxAge", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{lastSeenOf(7 * time.Hour)}, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("past the 6h0m0s limit"))
		Expect(err.Error()).To(ContainSubstring("ingestion is not retaining events"))
	})

	// Several heartbeats accumulate over time; only the newest decides. An old
	// one left in the result set must not mask a fresh one, and vice versa.
	It("judges staleness by the newest heartbeat, not the oldest", func() {
		client.UnresolvedIssuesReturns(
			[]pkg.Issue{lastSeenOf(72 * time.Hour), lastSeenOf(10 * time.Minute)},
			nil,
		)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)
		Expect(err).To(Succeed())
	})

	// The core signal: no heartbeat visible. This is what a quiet fleet and
	// dead ingestion have in common, and only the heartbeat distinguishes them
	// — an empty result here means events are not being retained.
	It("returns an error naming the absence when no heartbeat is visible", func() {
		client.UnresolvedIssuesReturns(nil, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no heartbeat event visible"))
	})

	// A query failure is not the same finding as an absent heartbeat — the
	// check could not run, so it must not be reported as "ingestion is dead".
	It("wraps a query error rather than reporting an absent heartbeat", func() {
		client.UnresolvedIssuesReturns(nil, stderrors.New("boom"))

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("query heartbeat"))
		Expect(err.Error()).NotTo(ContainSubstring("no heartbeat event visible"))
	})

	// The scan honours the caller's deadline instead of running to completion:
	// an unreachable or slow Sentry makes the issue list large, and the caller's
	// context is authoritative over a bounded in-memory loop.
	It("stops on a cancelled context rather than completing the scan", func() {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		client.UnresolvedIssuesReturns([]pkg.Issue{lastSeenOf(10 * time.Minute)}, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(cancelled)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("context canceled"))
		Expect(err.Error()).NotTo(ContainSubstring("no heartbeat event visible"))
	})

	// A present-but-unreadable timestamp is its own outcome: the query worked,
	// but the answer cannot be judged. Reporting it as either "present" or
	// "absent" would state something the data does not support.
	It("reports an unparseable lastSeen as neither present nor absent", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{{ShortID: "SENTRY-HEARTBEAT"}}, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("cannot evaluate staleness"))
		Expect(err.Error()).NotTo(ContainSubstring("no heartbeat event visible"))
	})

	// The sentinel contract callers branch on. pkg.ErrHeartbeatMissing has to
	// survive the wrap, or a caller that files an alert only on the *finding*
	// would silently stop filing anything.
	It("marks an absent heartbeat with ErrHeartbeatMissing", func() {
		client.UnresolvedIssuesReturns(nil, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(stderrors.Is(err, pkg.ErrHeartbeatMissing)).To(BeTrue())
	})

	It("marks a stale heartbeat with ErrHeartbeatMissing", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{lastSeenOf(7 * time.Hour)}, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(stderrors.Is(err, pkg.ErrHeartbeatMissing)).To(BeTrue())
	})

	// The counter-case, and the whole reason the sentinel exists: a query
	// failure must NOT be mistaken for the finding, or the detector cries wolf
	// whenever the Sentry API is unreachable.
	It("does not mark a query failure with ErrHeartbeatMissing", func() {
		client.UnresolvedIssuesReturns(nil, stderrors.New("boom"))

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(stderrors.Is(err, pkg.ErrHeartbeatMissing)).To(BeFalse())
	})

	It("does not mark an unparseable lastSeen with ErrHeartbeatMissing", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{{ShortID: "SENTRY-HEARTBEAT"}}, nil)

		_, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(stderrors.Is(err, pkg.ErrHeartbeatMissing)).To(BeFalse())
	})
})

// The observation is the half of Verify's answer that the published metric
// carries, and it is deliberately independent of the verdict. The load-bearing
// case is the stale heartbeat: the verdict is "missing", but that timestamp is
// exactly what the alert needs, because the alert fires on its age. Returning
// it only on success would blind the alert at the one moment it must fire.
var _ = Describe("pkg.HeartbeatChecker.Verify observation", func() {
	var (
		ctx    context.Context
		client *mocks.SentryClient
		now    time.Time
		cfg    pkg.HeartbeatCheckerConfig
	)

	lastSeenOf := func(age time.Duration) pkg.Issue {
		return pkg.Issue{
			ShortID:  "SENTRY-HEARTBEAT",
			LastSeen: now.Add(-age).Format(time.RFC3339),
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		client = &mocks.SentryClient{}
		now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
		cfg = pkg.HeartbeatCheckerConfig{
			Client: client,
			MaxAge: 6 * time.Hour,
			CurrentDateTimeGetter: libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
		}
	})

	// The reason Verify returns an observation at all.
	It("returns the newest lastSeen even when the verdict is stale", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{lastSeenOf(7 * time.Hour)}, nil)

		observation, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(err).To(HaveOccurred())
		Expect(stderrors.Is(err, pkg.ErrHeartbeatMissing)).To(BeTrue())
		Expect(observation.Newest).To(Equal(now.Add(-7 * time.Hour)))
	})

	It("returns the newest lastSeen when the heartbeat is present", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{lastSeenOf(30 * time.Minute)}, nil)

		observation, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(err).To(Succeed())
		Expect(observation.Newest).To(Equal(now.Add(-30 * time.Minute)))
	})

	It("reports the newest lastSeen, not the oldest", func() {
		client.UnresolvedIssuesReturns(
			[]pkg.Issue{lastSeenOf(72 * time.Hour), lastSeenOf(10 * time.Minute)},
			nil,
		)

		observation, err := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(err).To(Succeed())
		Expect(observation.Newest).To(Equal(now.Add(-10 * time.Minute)))
	})

	// Zero is the publisher's "unknown" signal, and it has to stay
	// distinguishable from a real timestamp: a zero pushed to the gateway would
	// read as a heartbeat from 1970 and pin the computed age at ~56 years,
	// which is a permanent alert rather than a silent one.
	It("leaves the observation zero when no heartbeat is visible", func() {
		client.UnresolvedIssuesReturns(nil, nil)

		observation, _ := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(observation.Newest.IsZero()).To(BeTrue())
	})

	It("leaves the observation zero when the query fails", func() {
		client.UnresolvedIssuesReturns(nil, stderrors.New("boom"))

		observation, _ := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(observation.Newest.IsZero()).To(BeTrue())
	})

	It("leaves the observation zero when no lastSeen parses", func() {
		client.UnresolvedIssuesReturns([]pkg.Issue{{ShortID: "SENTRY-HEARTBEAT"}}, nil)

		observation, _ := pkg.NewHeartbeatChecker(cfg).Verify(ctx)

		Expect(observation.Newest.IsZero()).To(BeTrue())
	})
})
