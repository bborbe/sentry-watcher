// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.DeriveTaskID", func() {
	It("is deterministic for identical inputs", func() {
		first := pkg.DeriveTaskID("SENTRY-TEST-1", "2026-08-23")
		for i := 0; i < 10000; i++ {
			Expect(pkg.DeriveTaskID("SENTRY-TEST-1", "2026-08-23")).To(Equal(first))
		}
	})

	It("differs when the short-id differs", func() {
		Expect(pkg.DeriveTaskID("SENTRY-TEST-1", "2026-08-23")).
			NotTo(Equal(pkg.DeriveTaskID("SENTRY-TEST-2", "2026-08-23")))
	})

	It("differs when the date differs (fresh daily task)", func() {
		Expect(pkg.DeriveTaskID("SENTRY-TEST-1", "2026-08-23")).
			NotTo(Equal(pkg.DeriveTaskID("SENTRY-TEST-1", "2026-08-24")))
	})

	It("pins the namespace contract", func() {
		ns := uuid.MustParse("1b06ab0c-eede-481f-b6ee-e91b0708fba9")
		expected := uuid.NewSHA1(ns, []byte("sentry:SENTRY-TEST-1:2026-08-23"))
		Expect(pkg.DeriveTaskID("SENTRY-TEST-1", "2026-08-23")).To(Equal(expected))
	})
})
