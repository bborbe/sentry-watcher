// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"net/http"
	"os"
	"time"

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

// NewSetCursorHandler returns an HTTP handler that validates the {shortId} and
// {date} URL variables (date in 2006-01-02 form) and writes the short-id's
// cursor entry to that date. Setting it to a date before the issue's current
// activity date makes the next poll emit a task; setting it to the current date
// suppresses emit.
//
// Wrap with libhttp.NewDangerousHandlerWrapper at the call site to require a
// passphrase — the bare handler does not enforce auth.
//
// Route: /setcursor/{shortId}/{date}. Invalid date → 400.
func NewSetCursorHandler(cursorPath string) http.Handler {
	return libhttp.NewErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				shortID := mux.Vars(req)["shortId"]
				date := mux.Vars(req)["date"]
				if shortID == "" || len(shortID) > 64 {
					return libhttp.WrapWithStatusCode(
						errors.Errorf(ctx, "invalid {shortId} path variable"),
						http.StatusBadRequest,
					)
				}
				if _, err := time.Parse("2006-01-02", date); err != nil {
					return libhttp.WrapWithStatusCode(
						errors.Wrapf(ctx, err, "invalid date %q", date),
						http.StatusBadRequest,
					)
				}
				cursor, err := LoadCursor(ctx, cursorPath)
				if err != nil {
					return errors.Wrapf(ctx, err, "load cursor before set")
				}
				cursor.Seen[shortID] = date
				if err := SaveCursor(ctx, cursorPath, cursor); err != nil {
					return errors.Wrapf(ctx, err, "save cursor after set")
				}
				glog.Warningf("cursor set shortID=%s date=%s", shortID, date)
				_, _ = libhttp.WriteAndGlog(resp, "cursor set %s to %s", shortID, date)
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
