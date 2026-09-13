// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"

	task "github.com/bborbe/agent/command/task"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.Watcher.Poll", func() {
	var (
		ctx        context.Context
		client     *mocks.SentryClient
		publisher  *mocks.TaskPublisher
		metrics    *mocks.Metrics
		cursorPath string
		tmpDir     string
		w          pkg.Watcher
		allowlist  []string
	)

	issue := func(shortID, project, lastSeen string) pkg.Issue {
		return pkg.Issue{
			ShortID:   shortID,
			Title:     "boom",
			LastSeen:  lastSeen,
			Permalink: "https://bborbe.sentry.io/issues/1/",
			Project:   pkg.Project{Slug: project, Name: project},
		}
	}

	writeCursor := func(seen map[string]string) {
		Expect(pkg.SaveCursor(ctx, cursorPath, &pkg.Cursor{Seen: seen})).To(Succeed())
	}

	loadedCursor := func() *pkg.Cursor {
		c, err := pkg.LoadCursor(ctx, cursorPath)
		Expect(err).NotTo(HaveOccurred())
		return c
	}

	filterSkipReasons := func() []string {
		reasons := make([]string, 0, metrics.IncFilterSkippedCallCount())
		for i := 0; i < metrics.IncFilterSkippedCallCount(); i++ {
			reasons = append(reasons, metrics.IncFilterSkippedArgsForCall(i))
		}
		return reasons
	}

	newWatcher := func(seedDate string) pkg.Watcher {
		return pkg.NewWatcher(
			client,
			publisher,
			metrics,
			pkg.NewFileCursorStore(cursorPath),
			pkg.TaskConfig{Stage: "prod", Assignee: "sentry-issue-analyzer"},
			allowlist,
			seedDate,
			nil, // heartbeat check disabled — covered by heartbeat_test.go
		)
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "watcher-poll-*")
		Expect(err).NotTo(HaveOccurred())
		cursorPath = filepath.Join(tmpDir, "cursor.json")
		allowlist = []string{"hell", "nuke-dev", "nuke-prod", "quant"}

		client = &mocks.SentryClient{}
		publisher = &mocks.TaskPublisher{}
		metrics = &mocks.Metrics{}
		w = newWatcher("")
	})

	AfterEach(func() {
		_ = os.RemoveAll(tmpDir) // #nosec G104 -- best-effort temp dir cleanup
	})

	Context("cold start (no cursor, no seed date)", func() {
		BeforeEach(func() {
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"),
				issue("SENTRY-TEST-2", "hell", "2026-08-22T10:00:00Z"),
			}, nil)
		})

		It("seeds the cursor with current dates and emits no task", func() {
			Expect(w.Poll(ctx)).To(Succeed())

			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))

			Expect(loadedCursor().Seen).To(Equal(map[string]string{
				"SENTRY-TEST-1": "2026-08-23",
				"SENTRY-TEST-2": "2026-08-22",
			}))
		})
	})

	Context("cold start with configured seed date", func() {
		BeforeEach(func() {
			w = newWatcher("2026-08-01")
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"),
			}, nil)
			publisher.PublishCreateReturns(true)
		})

		It("emits one task for each in-scope active issue dated after the seed", func() {
			Expect(w.Poll(ctx)).To(Succeed())

			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			_, cmd := publisher.PublishCreateArgsForCall(0)
			Expect(cmd.Frontmatter["short_id"]).To(Equal("SENTRY-TEST-1"))
			Expect(cmd.Frontmatter["date"]).To(Equal("2026-08-23"))
			Expect(cmd.Frontmatter["task_type"]).To(Equal("sentry-issue-analyzer"))

			Expect(loadedCursor().Seen["SENTRY-TEST-1"]).To(Equal("2026-08-23"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("seed-date cold start does not leak pre-seed issues on later polls", func() {
		BeforeEach(func() {
			w = newWatcher("2026-08-01")
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"), // after seed → emit
				issue("SENTRY-TEST-2", "hell", "2026-08-01T04:00:00Z"),      // at seed → suppressed
			}, nil)
			publisher.PublishCreateReturns(true)
		})

		It("emits only the after-seed issue on the first poll and nothing on the second", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			_, cmd := publisher.PublishCreateArgsForCall(0)
			Expect(cmd.Frontmatter["short_id"]).To(Equal("SENTRY-TEST-1"))
			Expect(filterSkipReasons()).To(ContainElement("already_seen"))

			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(1)) // no leak on second poll
			Expect(loadedCursor().Seen["SENTRY-TEST-2"]).To(Equal("2026-08-01"))
		})
	})

	Context("publish fails for one of several new issues", func() {
		BeforeEach(func() {
			w = newWatcher("2026-08-01")
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"),
				issue("SENTRY-TEST-2", "hell", "2026-08-22T04:00:00Z"),
			}, nil)
			publisher.PublishCreateStub = func(_ context.Context, cmd task.CreateCommand) bool {
				return cmd.Frontmatter["short_id"] == "SENTRY-TEST-1"
			}
		})

		It("advances only the successfully published issue", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(2))

			seen := loadedCursor().Seen
			Expect(seen["SENTRY-TEST-1"]).To(Equal("2026-08-23")) // published → advanced
			Expect(
				seen["SENTRY-TEST-2"],
			).To(Equal("2026-08-01"))
			// baseline held; re-emits next cycle
		})
	})

	Context("cold start with seed date older than issue activity", func() {
		BeforeEach(func() {
			w = newWatcher("2026-08-23")
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"),
			}, nil)
		})

		It("emits nothing — issue date not after the seed", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("already_seen"))
		})
	})

	Context("new activity date for a known issue", func() {
		BeforeEach(func() {
			writeCursor(map[string]string{"SENTRY-TEST-1": "2026-08-22"})
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"),
			}, nil)
			publisher.PublishCreateReturns(true)
		})

		It("emits one task for the new date and advances the cursor", func() {
			Expect(w.Poll(ctx)).To(Succeed())

			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			_, cmd := publisher.PublishCreateArgsForCall(0)
			Expect(cmd.Frontmatter["short_id"]).To(Equal("SENTRY-TEST-1"))
			Expect(cmd.Frontmatter["date"]).To(Equal("2026-08-23"))

			Expect(loadedCursor().Seen["SENTRY-TEST-1"]).To(Equal("2026-08-23"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("already-seen issue (same date as cursor)", func() {
		BeforeEach(func() {
			writeCursor(map[string]string{"SENTRY-TEST-1": "2026-08-23"})
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"),
			}, nil)
		})

		It("emits no task and records already_seen", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("already_seen"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("project outside the allowlist", func() {
		BeforeEach(func() {
			writeCursor(map[string]string{"OTHER-1": "2026-08-23"})
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("OTHER-1", "some-other-project", "2026-08-23T04:00:00Z"),
			}, nil)
		})

		It("skips the issue and records project_not_in_scope", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("project_not_in_scope"))
		})
	})

	Context("missing short-id", func() {
		BeforeEach(func() {
			// A non-empty cursor avoids the cold-start seed path so the filter
			// chain actually runs for this cycle.
			writeCursor(map[string]string{"SENTRY-TEST-9": "2026-08-22"})
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("", "nuke-prod", "2026-08-23T04:00:00Z"),
			}, nil)
		})

		It("skips the issue and records missing_short_id", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("missing_short_id"))
		})
	})

	Context("invalid last-seen timestamp", func() {
		BeforeEach(func() {
			writeCursor(map[string]string{"SENTRY-TEST-9": "2026-08-22"})
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "not-a-time"),
			}, nil)
		})

		It("skips the issue and records invalid_date", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("invalid_date"))
		})
	})

	Context("sentry query error", func() {
		BeforeEach(func() {
			writeCursor(map[string]string{"SENTRY-TEST-1": "2026-08-22"})
			client.UnresolvedIssuesReturns(nil, stderrors.New("network down"))
		})

		It("holds the cursor and records sentry_error", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("sentry_error"))

			Expect(loadedCursor().Seen["SENTRY-TEST-1"]).To(Equal("2026-08-22"))
		})
	})

	Context("publish fails on a new date", func() {
		BeforeEach(func() {
			writeCursor(map[string]string{"SENTRY-TEST-1": "2026-08-22"})
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"),
			}, nil)
			publisher.PublishCreateReturns(false)
		})

		It("does not advance the cursor entry", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(loadedCursor().Seen["SENTRY-TEST-1"]).To(Equal("2026-08-22"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("mixed in-scope and out-of-scope issues", func() {
		BeforeEach(func() {
			writeCursor(map[string]string{
				"SENTRY-TEST-1": "2026-08-22",
				"SENTRY-TEST-3": "2026-08-22",
			})
			client.UnresolvedIssuesReturns([]pkg.Issue{
				issue("SENTRY-TEST-1", "nuke-prod", "2026-08-23T04:00:00Z"), // new → emit
				issue("SENTRY-TEST-2", "unrelated", "2026-08-23T04:00:00Z"), // out of scope
				issue("SENTRY-TEST-3", "quant", "2026-08-22T04:00:00Z"),     // already seen
			}, nil)
			publisher.PublishCreateReturns(true)
		})

		It("emits only for the new in-scope issue", func() {
			Expect(w.Poll(ctx)).To(Succeed())

			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			_, cmd := publisher.PublishCreateArgsForCall(0)
			Expect(cmd.Frontmatter["short_id"]).To(Equal("SENTRY-TEST-1"))

			Expect(filterSkipReasons()).To(ContainElement("project_not_in_scope"))
			Expect(filterSkipReasons()).To(ContainElement("already_seen"))
			Expect(metrics.IncIssuesScannedArgsForCall(0)).To(Equal(3))
		})
	})
})
