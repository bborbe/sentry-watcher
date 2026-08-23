// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	libhttp "github.com/bborbe/http"
	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
	"github.com/bborbe/sentry-watcher/pkg/handler"
)

// newRouter wraps a libhttp.WithError handler on path in a mux router (via
// libhttp.NewErrorHandler) so {shortId}/{date} path variables are populated
// exactly as they are in main.go's server.
func newRouter(path string, h libhttp.WithError) http.Handler {
	router := mux.NewRouter()
	router.Path(path).Handler(libhttp.NewErrorHandler(h))
	return router
}

var _ = Describe("pkg.handler cursor handlers", func() {
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

	serve := func(router http.Handler, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	Describe("NewSetCursorHandler", func() {
		It("writes the short-id date to the cursor (round-trip via LoadCursor)", func() {
			router := newRouter("/setcursor/{shortId}/{date}",
				handler.NewSetCursorHandler(cursorPath))
			rec := serve(router, "/setcursor/SENTRY-TEST-1/2026-08-01")

			Expect(rec.Code).To(Equal(http.StatusOK))
			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.Seen["SENTRY-TEST-1"]).To(Equal("2026-08-01"))
		})

		It("preserves existing cursor entries on set", func() {
			Expect(pkg.SaveCursor(ctx, cursorPath, &pkg.Cursor{Seen: map[string]string{
				"SENTRY-TEST-2": "2026-08-22",
			}})).To(Succeed())

			router := newRouter("/setcursor/{shortId}/{date}",
				handler.NewSetCursorHandler(cursorPath))
			rec := serve(router, "/setcursor/SENTRY-TEST-1/2026-08-01")

			Expect(rec.Code).To(Equal(http.StatusOK))
			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.Seen).To(Equal(map[string]string{
				"SENTRY-TEST-1": "2026-08-01",
				"SENTRY-TEST-2": "2026-08-22",
			}))
		})

		It("rejects an invalid date with 400 and does not write", func() {
			router := newRouter("/setcursor/{shortId}/{date}",
				handler.NewSetCursorHandler(cursorPath))
			rec := serve(router, "/setcursor/SENTRY-TEST-1/not-a-date")

			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			_, err := os.Stat(cursorPath)
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("rejects a missing short-id with 400", func() {
			// An empty path segment would be redirected by the mux router (301),
			// so inject the path vars directly to exercise the empty-short-id guard.
			req := httptest.NewRequest(http.MethodPost, "/setcursor/2026-08-01", nil)
			req = mux.SetURLVars(req, map[string]string{"shortId": "", "date": "2026-08-01"})
			rec := httptest.NewRecorder()
			libhttp.NewErrorHandler(handler.NewSetCursorHandler(cursorPath)).ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("NewResetCursorHandler", func() {
		It("removes the cursor file (next LoadCursor cold-starts empty)", func() {
			Expect(
				pkg.SaveCursor(ctx, cursorPath, &pkg.Cursor{Seen: map[string]string{
					"SENTRY-TEST-1": "2026-08-23",
				}}),
			).To(Succeed())

			router := newRouter("/resetcursor", handler.NewResetCursorHandler(cursorPath))
			rec := serve(router, "/resetcursor")

			Expect(rec.Code).To(Equal(http.StatusOK))
			_, statErr := os.Stat(cursorPath)
			Expect(os.IsNotExist(statErr)).To(BeTrue())

			loaded, err := pkg.LoadCursor(ctx, cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.Seen).To(BeEmpty())
		})

		It("treats a missing cursor file as success", func() {
			router := newRouter("/resetcursor", handler.NewResetCursorHandler(cursorPath))
			rec := serve(router, "/resetcursor")
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})

	Describe("NewTriggerHandler", func() {
		It("invokes Poll once and returns 200", func() {
			// Plain http.Handler wiring mirrors main.go's /trigger registration.
			req := httptest.NewRequest(http.MethodPost, "/trigger", nil)
			rec := httptest.NewRecorder()

			watcher := &fakeWatcher{pollErr: nil}
			libhttp.NewErrorHandler(handler.NewTriggerHandler(watcher)).ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(watcher.pollCalls).To(Equal(1))
		})

		It("propagates a Poll error", func() {
			req := httptest.NewRequest(http.MethodPost, "/trigger", nil)
			rec := httptest.NewRecorder()

			watcher := &fakeWatcher{pollErr: context.Canceled}
			libhttp.NewErrorHandler(handler.NewTriggerHandler(watcher)).ServeHTTP(rec, req)

			Expect(rec.Code).NotTo(Equal(http.StatusOK))
		})
	})
})

// fakeWatcher is a minimal Watcher stub for the trigger-handler tests.
type fakeWatcher struct {
	pollErr   error
	pollCalls int
}

func (f *fakeWatcher) Poll(ctx context.Context) error {
	f.pollCalls++
	return f.pollErr
}
