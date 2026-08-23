// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.Version", func() {
	ctx := context.Background()

	Describe("ParseVersion", func() {
		It("parses major.minor.patch", func() {
			v, err := pkg.ParseVersion(ctx, "go1.26.5")
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Major).To(Equal(1))
			Expect(v.Minor).To(Equal(26))
			Expect(v.Patch).To(Equal(5))
			Expect(v.Raw).To(Equal("go1.26.5"))
		})

		It("parses major.minor with patch defaulting to 0", func() {
			v, err := pkg.ParseVersion(ctx, "go1.27")
			Expect(err).NotTo(HaveOccurred())
			Expect(v.Major).To(Equal(1))
			Expect(v.Minor).To(Equal(27))
			Expect(v.Patch).To(Equal(0))
			Expect(v.String()).To(Equal("go1.27.0"))
		})

		DescribeTable("rejects invalid version strings",
			func(s string) {
				_, err := pkg.ParseVersion(ctx, s)
				Expect(err).To(HaveOccurred())
			},
			Entry("empty", ""),
			Entry("no go prefix", "1.26.5"),
			Entry("rc suffix", "go1.27rc1"),
			Entry("beta suffix", "go1.27beta1"),
			Entry("trailing dot", "go1.26."),
			Entry("four components", "go1.26.5.1"),
			Entry("non-numeric minor", "go1.x.5"),
		)
	})

	Describe("Compare / Less", func() {
		It("orders go1.26.10 above go1.26.9 (integer, not string, compare)", func() {
			a, err := pkg.ParseVersion(ctx, "go1.26.9")
			Expect(err).NotTo(HaveOccurred())
			b, err := pkg.ParseVersion(ctx, "go1.26.10")
			Expect(err).NotTo(HaveOccurred())
			Expect(a.Less(b)).To(BeTrue())
			Expect(b.Less(a)).To(BeFalse())
			Expect(a.Compare(b)).To(BeNumerically("<", 0))
		})

		It("orders by minor before patch", func() {
			a, _ := pkg.ParseVersion(ctx, "go1.26.5")
			b, _ := pkg.ParseVersion(ctx, "go1.27.0")
			Expect(a.Less(b)).To(BeTrue())
		})

		It("orders by major before minor", func() {
			a, _ := pkg.ParseVersion(ctx, "go1.99.0")
			b, _ := pkg.ParseVersion(ctx, "go2.0.0")
			Expect(a.Less(b)).To(BeTrue())
		})

		It("reports equal versions as not-less and Compare 0", func() {
			a, _ := pkg.ParseVersion(ctx, "go1.26.5")
			b, _ := pkg.ParseVersion(ctx, "go1.26.5")
			Expect(a.Less(b)).To(BeFalse())
			Expect(a.Compare(b)).To(Equal(0))
		})
	})

	Describe("String / Number", func() {
		It("String reconstructs canonical go-prefixed form", func() {
			v, _ := pkg.ParseVersion(ctx, "go1.27")
			Expect(v.String()).To(Equal("go1.27.0"))
		})

		It("Number strips the go prefix", func() {
			v, _ := pkg.ParseVersion(ctx, "go1.27.0")
			Expect(v.Number()).To(Equal("1.27.0"))
		})
	})
})
