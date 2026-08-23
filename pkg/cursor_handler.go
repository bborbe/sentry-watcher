// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"net/http"
	"os"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
	"github.com/gorilla/mux"
)

// NewResetCursorHandler returns an HTTP handler that deletes the cursor file at
// cursorPath, so the next poll cold-starts (re-seeds). A missing file is treated
// as success (already reset).
//
// Wrap with libhttp.NewDangerousHandlerWrapper at the call site to require a
// passphrase — the bare handler does not enforce auth.
func NewResetCursorHandler(cursorPath string) http.Handler {
	return libhttp.NewErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, _ *http.Request) error {
				if err := os.Remove(cursorPath); err != nil && !os.IsNotExist(err) {
					return errors.Wrapf(ctx, err, "remove cursor file path=%s", cursorPath)
				}
				glog.Warningf("cursor reset (file removed) path=%s", cursorPath)
				_, _ = libhttp.WriteAndGlog(resp, "cursor reset path=%s", cursorPath)
				return nil
			},
		),
	)
}

// NewSetCursorHandler returns an HTTP handler that validates the {version} URL
// variable as a Go version (e.g. go1.26.5) and writes it as the cursor's
// LastSeenVersion. Setting it to a version lower than the current latest makes
// the next poll emit a task; setting it to the current latest suppresses emit.
//
// Wrap with libhttp.NewDangerousHandlerWrapper at the call site to require a
// passphrase — the bare handler does not enforce auth.
//
// Route: /setcursor/{version}. Invalid version → 400.
func NewSetCursorHandler(cursorPath string) http.Handler {
	return libhttp.NewErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				version := mux.Vars(req)["version"]
				if version == "" {
					return libhttp.WrapWithStatusCode(
						errors.Errorf(ctx, "missing {version} path variable"),
						http.StatusBadRequest,
					)
				}
				if _, err := ParseVersion(ctx, version); err != nil {
					return libhttp.WrapWithStatusCode(
						errors.Wrapf(ctx, err, "invalid go version %q", version),
						http.StatusBadRequest,
					)
				}
				if err := SaveCursor(ctx, cursorPath, &Cursor{LastSeenVersion: version}); err != nil {
					return errors.Wrapf(ctx, err, "save cursor after set")
				}
				glog.Warningf("cursor set version=%s", version)
				_, _ = libhttp.WriteAndGlog(resp, "cursor set to %s", version)
				return nil
			},
		),
	)
}

// NewTriggerHandler returns an HTTP handler that invokes the watcher's Poll once
// immediately, so an operator can force a poll cycle without waiting for the
// interval tick (e.g. for live end-to-end testing after a cursor reset/set).
func NewTriggerHandler(watcher Watcher) http.Handler {
	return libhttp.NewErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, _ *http.Request) error {
				if err := watcher.Poll(ctx); err != nil {
					return errors.Wrapf(ctx, err, "trigger poll")
				}
				glog.Warningf("poll triggered via /trigger")
				_, _ = libhttp.WriteAndGlog(resp, "poll triggered")
				return nil
			},
		),
	)
}
