// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
)

// newRouter wraps handler on path in a mux router so {version} path variables
// are populated exactly as they are in main.go's server.
func newRouter(path string, handler http.Handler) http.Handler {
	router := mux.NewRouter()
	router.Path(path).Handler(handler)
	return router
}

var _ = Describe("pkg.CursorHandler", func() {
	var (
		ctx        context.Context
		tmpDir     string
		cursorPath string
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tmpDir, err = os.MkdirTemp("", "cursor-handler-*")
		Expect(err).NotTo(HaveOccurred())
		cursorPath = filepath.Join(tmpDir, "cursor.json")
	})

	AfterEach(func() {
		_ = os.RemoveAll(tmpDir) // #nosec G104 -- best-effort temp dir cleanup
	})

	serve := func(handler http.Handler, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	Describe("NewSetCursorHandler", func() {
		It("writes the version to the cursor (round-trip via LoadCursor)", func() {
			router := newRouter("/setcursor/{version}", pkg.NewSetCursorHandler(cursorPath))
			rec := serve(router, "/setcursor/go1.27.0")

			Expect(rec.Code).To(Equal(http.StatusOK))
			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(Equal("go1.27.0"))
		})

		It("rejects an invalid version with 400 and does not write", func() {
			router := newRouter("/setcursor/{version}", pkg.NewSetCursorHandler(cursorPath))
			rec := serve(router, "/setcursor/not-a-version")

			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			_, err := os.Stat(cursorPath)
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Describe("NewResetCursorHandler", func() {
		It("removes the cursor file (next LoadCursor cold-starts empty)", func() {
			Expect(
				pkg.SaveCursor(ctx, cursorPath, &pkg.Cursor{LastSeenVersion: "go1.26.5"}),
			).To(Succeed())

			router := newRouter("/resetcursor", pkg.NewResetCursorHandler(cursorPath))
			rec := serve(router, "/resetcursor")

			Expect(rec.Code).To(Equal(http.StatusOK))
			_, statErr := os.Stat(cursorPath)
			Expect(os.IsNotExist(statErr)).To(BeTrue())

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastSeenVersion).To(BeEmpty())
		})

		It("treats a missing cursor file as success", func() {
			router := newRouter("/resetcursor", pkg.NewResetCursorHandler(cursorPath))
			rec := serve(router, "/resetcursor")
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})
})
