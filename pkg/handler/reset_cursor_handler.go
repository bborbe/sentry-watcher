// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
	"os"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
)

// ResetCursorHandler handles POST /resetcursor.
type ResetCursorHandler = libhttp.WithError

// NewResetCursorHandler returns a handler that deletes the cursor file at
// cursorPath, so the next poll cold-starts (re-seeds). A missing file is treated
// as success (already reset).
//
// Wrap with libhttp.NewDangerousHandlerWrapper at the call site to require a
// passphrase — the bare handler does not enforce auth.
func NewResetCursorHandler(cursorPath string) ResetCursorHandler {
	return &resetCursorHandler{cursorPath: cursorPath}
}

type resetCursorHandler struct {
	cursorPath string
}

func (h *resetCursorHandler) ServeHTTP(
	ctx context.Context,
	resp http.ResponseWriter,
	_ *http.Request,
) error {
	if err := os.Remove(h.cursorPath); err != nil && !os.IsNotExist(err) {
		return errors.Wrapf(ctx, err, "remove cursor file path=%s", h.cursorPath)
	}
	glog.Warningf("cursor reset (file removed) path=%s", h.cursorPath)
	_, _ = libhttp.WriteAndGlog(resp, "cursor reset path=%s", h.cursorPath)
	return nil
}
