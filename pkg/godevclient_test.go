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

var _ = Describe("pkg.GoDevClient", func() {
	var (
		ctx     context.Context
		server  *httptest.Server
		payload string
		status  int
	)

	BeforeEach(func() {
		ctx = context.Background()
		status = http.StatusOK
	})

	JustBeforeEach(func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(payload))
		}))
	})

	AfterEach(func() {
		server.Close()
	})

	client := func() pkg.GoDevClient {
		return pkg.NewGoDevClient(server.Client(), server.URL)
	}

	Context("with multiple stable and unstable entries", func() {
		BeforeEach(func() {
			payload = `[
				{"version":"go1.27rc1","stable":false},
				{"version":"go1.26.5","stable":true},
				{"version":"go1.26.10","stable":true},
				{"version":"go1.25.11","stable":true}
			]`
		})

		It("returns the max stable version by integer compare", func() {
			v, err := client().LatestStable(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(v.String()).To(Equal("go1.26.10"))
		})
	})

	Context("with no stable entries", func() {
		BeforeEach(func() {
			payload = `[{"version":"go1.27rc1","stable":false}]`
		})

		It("returns an error", func() {
			_, err := client().LatestStable(ctx)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("with unparseable stable versions mixed in", func() {
		BeforeEach(func() {
			payload = `[
				{"version":"weird","stable":true},
				{"version":"go1.26.4","stable":true}
			]`
		})

		It("skips unparseable entries and returns the parseable max", func() {
			v, err := client().LatestStable(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(v.String()).To(Equal("go1.26.4"))
		})
	})

	Context("with a non-200 status", func() {
		BeforeEach(func() {
			status = http.StatusServiceUnavailable
			payload = ``
		})

		It("returns an error", func() {
			_, err := client().LatestStable(ctx)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("with malformed JSON", func() {
		BeforeEach(func() {
			payload = `not json`
		})

		It("returns an error", func() {
			_, err := client().LatestStable(ctx)
			Expect(err).To(HaveOccurred())
		})
	})
})
