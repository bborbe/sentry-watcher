// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.Watcher.Poll", func() {
	var (
		ctx          context.Context
		client       *mocks.GoDevClient
		imageChecker *mocks.ImageChecker
		publisher    *mocks.TaskPublisher
		metrics      *mocks.Metrics
		cursorPath   string
		tmpDir       string
		w            pkg.Watcher
	)

	mustVersion := func(s string) pkg.Version {
		v, err := pkg.ParseVersion(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		return v
	}

	writeCursor := func(version string) {
		Expect(pkg.SaveCursor(ctx, cursorPath, &pkg.Cursor{LastSeenVersion: version})).To(Succeed())
	}

	filterSkipReasons := func() []string {
		reasons := make([]string, 0, metrics.IncFilterSkippedCallCount())
		for i := 0; i < metrics.IncFilterSkippedCallCount(); i++ {
			reasons = append(reasons, metrics.IncFilterSkippedArgsForCall(i))
		}
		return reasons
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "watcher-poll-*")
		Expect(err).NotTo(HaveOccurred())
		cursorPath = filepath.Join(tmpDir, "cursor.json")

		client = &mocks.GoDevClient{}
		imageChecker = &mocks.ImageChecker{}
		publisher = &mocks.TaskPublisher{}
		metrics = &mocks.Metrics{}
		w = pkg.NewWatcher(
			client,
			imageChecker,
			publisher,
			metrics,
			cursorPath,
			pkg.TaskConfig{Stage: "prod"},
			"",
		)
	})

	AfterEach(func() {
		_ = os.RemoveAll(tmpDir) // #nosec G104 -- best-effort temp dir cleanup
	})

	Context("cold start (no cursor)", func() {
		BeforeEach(func() {
			client.LatestStableReturns(mustVersion("go1.26.5"), nil)
		})

		It("seeds the cursor and emits no task", func() {
			Expect(w.Poll(ctx)).To(Succeed())

			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.26.5"))
		})
	})

	Context("cold start with configured seed version", func() {
		BeforeEach(func() {
			// No cursor on disk; seed to a lower version so the first poll emits.
			w = pkg.NewWatcher(
				client,
				imageChecker,
				publisher,
				metrics,
				cursorPath,
				pkg.TaskConfig{Stage: "prod"},
				"go1.26.4",
			)
			client.LatestStableReturns(mustVersion("go1.26.5"), nil)
			imageChecker.ImageExistsReturns(true, nil)
			publisher.PublishCreateReturns(true)
		})

		It("emits one task for latest, classifies patch, advances the cursor", func() {
			Expect(w.Poll(ctx)).To(Succeed())

			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			_, cmd := publisher.PublishCreateArgsForCall(0)
			Expect(cmd.Frontmatter["new_version"]).To(Equal("go1.26.5"))
			Expect(cmd.Frontmatter["previous_version"]).To(Equal("go1.26.4"))
			Expect(cmd.Frontmatter["release_kind"]).To(Equal("patch"))

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.26.5"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("new patch version", func() {
		BeforeEach(func() {
			writeCursor("go1.26.4")
			client.LatestStableReturns(mustVersion("go1.26.5"), nil)
			imageChecker.ImageExistsReturns(true, nil)
			publisher.PublishCreateReturns(true)
		})

		It("emits one task, classifies patch, advances the cursor", func() {
			Expect(w.Poll(ctx)).To(Succeed())

			Expect(publisher.PublishCreateCallCount()).To(Equal(1))
			_, cmd := publisher.PublishCreateArgsForCall(0)
			Expect(cmd.Frontmatter["new_version"]).To(Equal("go1.26.5"))
			Expect(cmd.Frontmatter["previous_version"]).To(Equal("go1.26.4"))
			Expect(cmd.Frontmatter["release_kind"]).To(Equal("patch"))

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.26.5"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("new minor version", func() {
		BeforeEach(func() {
			writeCursor("go1.26.5")
			client.LatestStableReturns(mustVersion("go1.27.0"), nil)
			imageChecker.ImageExistsReturns(true, nil)
			publisher.PublishCreateReturns(true)
		})

		It("classifies the bump as minor", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			_, cmd := publisher.PublishCreateArgsForCall(0)
			Expect(cmd.Frontmatter["release_kind"]).To(Equal("minor"))
		})
	})

	Context("unchanged version", func() {
		BeforeEach(func() {
			writeCursor("go1.26.5")
			client.LatestStableReturns(mustVersion("go1.26.5"), nil)
		})

		It("emits no task and records version_unchanged", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("version_unchanged"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("older upstream line than cursor (lower version)", func() {
		BeforeEach(func() {
			writeCursor("go1.26.5")
			// Upstream max somehow reports a lower version than the cursor.
			client.LatestStableReturns(mustVersion("go1.25.11"), nil)
		})

		It("emits no task and records version_unchanged", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("version_unchanged"))
		})
	})

	Context("go.dev query error", func() {
		BeforeEach(func() {
			writeCursor("go1.26.5")
			client.LatestStableReturns(pkg.Version{}, stderrors.New("network down"))
		})

		It("holds the cursor and records go_dev_error", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("go_dev_error"))

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.26.5"))
		})
	})

	Context("publish fails on a new version", func() {
		BeforeEach(func() {
			writeCursor("go1.26.4")
			client.LatestStableReturns(mustVersion("go1.26.5"), nil)
			imageChecker.ImageExistsReturns(true, nil)
			publisher.PublishCreateReturns(false)
		})

		It("does not advance the cursor", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.26.4"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("new version but docker image not yet published", func() {
		BeforeEach(func() {
			writeCursor("go1.26.5")
			client.LatestStableReturns(mustVersion("go1.27.0"), nil)
			imageChecker.ImageExistsReturns(false, nil)
		})

		It("holds the cursor and records image_not_ready, emitting nothing", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(filterSkipReasons()).To(ContainElement("image_not_ready"))

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.26.5"))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		})
	})

	Context("docker image check errors", func() {
		BeforeEach(func() {
			writeCursor("go1.26.5")
			client.LatestStableReturns(mustVersion("go1.27.0"), nil)
			imageChecker.ImageExistsReturns(false, stderrors.New("docker hub down"))
		})

		It("holds the cursor and records image_check_error", func() {
			Expect(w.Poll(ctx)).To(Succeed())
			Expect(publisher.PublishCreateCallCount()).To(Equal(0))
			Expect(metrics.IncPollCycleArgsForCall(0)).To(Equal("image_check_error"))

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.26.5"))
		})
	})
})
