// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"

	"github.com/bborbe/sentry-watcher/pkg"
)

// TriggerHandler handles POST /trigger.
type TriggerHandler = libhttp.WithError

// NewTriggerHandler returns a handler that invokes the watcher's Poll once
// immediately, so an operator can force a poll cycle without waiting for the
// interval tick (e.g. for live end-to-end testing after a cursor reset/set).
func NewTriggerHandler(watcher pkg.Watcher) TriggerHandler {
	return &triggerHandler{watcher: watcher}
}

type triggerHandler struct {
	watcher pkg.Watcher
}

func (h *triggerHandler) ServeHTTP(
	ctx context.Context,
	resp http.ResponseWriter,
	_ *http.Request,
) error {
	if err := h.watcher.Poll(ctx); err != nil {
		return errors.Wrapf(ctx, err, "trigger poll")
	}
	glog.Warningf("poll triggered via /trigger")
	_, _ = libhttp.WriteAndGlog(resp, "poll triggered")
	return nil
}
