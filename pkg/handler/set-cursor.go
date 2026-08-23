// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
	"github.com/gorilla/mux"

	"github.com/bborbe/sentry-watcher/pkg"
)

// SetCursorHandler handles POST /setcursor/{shortId}/{date}.
type SetCursorHandler = libhttp.WithError

// NewSetCursorHandler returns a handler that validates the {shortId} and {date}
// URL variables (date in 2006-01-02 form) and writes the short-id's cursor entry
// to that date. Setting it to a date before the issue's current activity date
// makes the next poll emit a task; setting it to the current date suppresses
// emit. The CursorStore is injected (no package-level LoadCursor/SaveCursor) per
// go-composition/no-package-function-calls-in-business-logic.
//
// Wrap with libhttp.NewDangerousHandlerWrapper at the call site to require a
// passphrase — the bare handler does not enforce auth.
func NewSetCursorHandler(store pkg.CursorStore) SetCursorHandler {
	return &setCursorHandler{store: store}
}

type setCursorHandler struct {
	store pkg.CursorStore
}

func (h *setCursorHandler) ServeHTTP(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
) error {
	// mux.Vars reads the URL-vars map from the request context; read it once into
	// a local so both fields come from the same map (no repeated context lookups).
	vars := mux.Vars(req)
	shortID := vars["shortId"]
	date := vars["date"]
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
	cursor, err := h.store.Load(ctx)
	if err != nil {
		return errors.Wrapf(ctx, err, "load cursor before set")
	}
	cursor.Seen[shortID] = date
	if err := h.store.Save(ctx, cursor); err != nil {
		return errors.Wrapf(ctx, err, "save cursor after set")
	}
	glog.Warningf("cursor set shortID=%s date=%s", shortID, date)
	_, _ = libhttp.WriteAndGlog(resp, "cursor set %s to %s", shortID, date)
	return nil
}
