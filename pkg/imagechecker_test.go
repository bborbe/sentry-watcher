// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.ImageChecker", func() {
	var (
		ctx          context.Context
		tokenServer  *httptest.Server
		regServer    *httptest.Server
		manifestCall *http.Request
		tokenPayload string
		manifestCode int
	)

	mustVersion := func(s string) pkg.Version {
		v, err := pkg.ParseVersion(ctx, s)
		Expect(err).NotTo(HaveOccurred())
		return v
	}

	client := func() pkg.ImageChecker {
		return pkg.NewImageChecker(regServer.Client(), tokenServer.URL, regServer.URL)
	}

	BeforeEach(func() {
		ctx = context.Background()
		tokenPayload = `{"token":"test-token"}` // #nosec G101 -- test fixture, not a real credential
		manifestCode = http.StatusOK
		manifestCall = nil
	})

	JustBeforeEach(func() {
		tokenServer = httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tokenPayload))
			}),
		)
		regServer = httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				manifestCall = r
				w.WriteHeader(manifestCode)
			}),
		)
	})

	AfterEach(func() {
		tokenServer.Close()
		regServer.Close()
	})

	Context("image exists", func() {
		It("returns true", func() {
			exists, err := client().ImageExists(ctx, mustVersion("go1.27.0"))
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())
			Expect(manifestCall.URL.Path).To(Equal("/manifests/1.27.0"))
			Expect(manifestCall.Header.Get("Authorization")).To(Equal("Bearer test-token"))
		})
	})

	Context("image not yet published (404)", func() {
		BeforeEach(func() {
			manifestCode = http.StatusNotFound
		})

		It("returns false with no error", func() {
			exists, err := client().ImageExists(ctx, mustVersion("go1.99.0"))
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeFalse())
		})
	})

	Context("token endpoint fails", func() {
		BeforeEach(func() {
			tokenPayload = `{"error":"unauthorized"}` // #nosec G101 -- test fixture, not a real credential
		})

		It("returns an error", func() {
			_, err := client().ImageExists(ctx, mustVersion("go1.27.0"))
			Expect(err).To(HaveOccurred())
		})
	})

	Context("unexpected manifest status", func() {
		BeforeEach(func() {
			manifestCode = http.StatusServiceUnavailable
		})

		It("returns an error (fail-closed)", func() {
			_, err := client().ImageExists(ctx, mustVersion("go1.27.0"))
			Expect(err).To(HaveOccurred())
		})
	})
})
