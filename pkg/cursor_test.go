// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.Cursor", func() {
	var (
		ctx    context.Context
		tmpDir string
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "cursor-goversion-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(tmpDir) // #nosec G104 -- best-effort temp dir cleanup
	})

	Describe("LoadCursor", func() {
		It("returns cold-start empty cursor when file is missing", func() {
			path := filepath.Join(tmpDir, "nonexistent.json")
			cursor, err := pkg.LoadCursor(ctx, path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cursor).NotTo(BeNil())
			Expect(cursor.LastSeenVersion).To(BeEmpty())
		})

		It("returns error on corrupt JSON", func() {
			path := filepath.Join(tmpDir, "corrupt.json")
			Expect(os.WriteFile(path, []byte("not json"), 0600)).To(Succeed())
			cursor, err := pkg.LoadCursor(ctx, path)
			Expect(err).To(HaveOccurred())
			Expect(cursor).To(BeNil())
		})
	})

	Describe("SaveCursor", func() {
		It("returns error when target directory does not exist", func() {
			path := filepath.Join(tmpDir, "missing-dir", "cursor.json")
			err := pkg.SaveCursor(ctx, path, &pkg.Cursor{LastSeenVersion: "go1.26.5"})
			Expect(err).To(HaveOccurred())
		})

		It("does atomic write — no .tmp file remains after success", func() {
			path := filepath.Join(tmpDir, "atomic.json")
			Expect(
				pkg.SaveCursor(ctx, path, &pkg.Cursor{LastSeenVersion: "go1.26.5"}),
			).To(Succeed())
			_, err := os.Stat(path + ".tmp")
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Describe("SaveCursor + LoadCursor round-trip", func() {
		It("preserves LastSeenVersion", func() {
			path := filepath.Join(tmpDir, "roundtrip.json")
			Expect(
				pkg.SaveCursor(ctx, path, &pkg.Cursor{LastSeenVersion: "go1.27.0"}),
			).To(Succeed())
			loaded, err := pkg.LoadCursor(ctx, path)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.27.0"))
		})
	})
})
