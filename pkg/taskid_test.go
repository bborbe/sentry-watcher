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
		first := pkg.DeriveTaskID("go1.27.0")
		for i := 0; i < 10000; i++ {
			Expect(pkg.DeriveTaskID("go1.27.0")).To(Equal(first))
		}
	})

	It("differs when version differs", func() {
		Expect(pkg.DeriveTaskID("go1.27.0")).NotTo(Equal(pkg.DeriveTaskID("go1.26.5")))
	})

	It("distinguishes patch-level versions", func() {
		Expect(pkg.DeriveTaskID("go1.26.9")).NotTo(Equal(pkg.DeriveTaskID("go1.26.10")))
	})

	It("pins the namespace contract", func() {
		ns := uuid.MustParse("800ae6f3-d5ac-47e8-a330-958561a4e1ad")
		expected := uuid.NewSHA1(ns, []byte("go-version:go1.27.0"))
		Expect(pkg.DeriveTaskID("go1.27.0")).To(Equal(expected))
	})
})
