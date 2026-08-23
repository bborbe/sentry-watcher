// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../mocks/watcher.go --fake-name Watcher . Watcher

// Watcher polls go.dev for the max stable Go version and publishes a
// CreateTaskCommand when it advances beyond the cursor.
type Watcher interface {
	// Poll runs one scan cycle. Safe to call repeatedly on an interval.
	Poll(ctx context.Context) error
}

// NewWatcher wires the watcher's collaborators.
//
// seedVersion, when non-empty, is the Go version the cursor is seeded with on
// cold start instead of the current latest, so the first poll can emit a task
// for the current latest. Empty means seed to latest and emit nothing.
//
// imageChecker gates task emission on docker.io actually publishing the golang
// image for the new version (go.dev releases 12h–1day before the image exists).
func NewWatcher(
	client GoDevClient,
	imageChecker ImageChecker,
	publisher TaskPublisher,
	metrics Metrics,
	cursorPath string,
	cfg TaskConfig,
	seedVersion string,
) Watcher {
	return &watcher{
		client:       client,
		imageChecker: imageChecker,
		publisher:    publisher,
		metrics:      metrics,
		cursorPath:   cursorPath,
		cfg:          cfg,
		seedVersion:  seedVersion,
	}
}

type watcher struct {
	client       GoDevClient
	imageChecker ImageChecker
	publisher    TaskPublisher
	metrics      Metrics
	cursorPath   string
	cfg          TaskConfig
	seedVersion  string
}

// Poll implements Watcher. One cycle:
//  1. Load cursor (cold-start safe).
//  2. Query go.dev for the max stable version — on error hold the cursor,
//     record go_dev_error, return nil.
//  3. Cold start (empty cursor): with no seedVersion, seed cursor to latest and
//     emit nothing; with a seedVersion, seed cursor to that lower version and
//     fall through to the comparison so the first poll emits a task for latest.
//  4. latest > cursor: check the docker image for latest exists — if not yet
//     published, hold the cursor, record image_not_ready, retry next cycle.
//  5. Image exists: build + publish one task; advance cursor on publish success.
//  6. latest <= cursor: record version_unchanged (no task).
//  7. Record success.
func (w *watcher) Poll(ctx context.Context) error {
	cursor, err := LoadCursor(ctx, w.cursorPath)
	if err != nil {
		return errors.Wrapf(ctx, err, "load cursor path=%s", w.cursorPath)
	}

	latest, err := w.client.LatestStable(ctx)
	if err != nil {
		w.metrics.IncPollCycle("go_dev_error")
		glog.Warningf("poll cycle aborted: go.dev query failed err=%v", err)
		return nil
	}

	if cursor.LastSeenVersion == "" {
		if w.seedVersion == "" {
			w.seedCursor(ctx, cursor, latest)
			w.metrics.IncPollCycle("success")
			return nil
		}
		if err := w.seedFromConfigured(ctx, cursor); err != nil {
			return errors.Wrapf(ctx, err, "seed cursor from configured version")
		}
		// Fall through: compare latest against the seeded version so a task is
		// emitted when latest > seedVersion and the cursor advances to latest.
	}

	previous, err := ParseVersion(ctx, cursor.LastSeenVersion)
	if err != nil {
		return errors.Wrapf(ctx, err, "parse stored cursor version %q", cursor.LastSeenVersion)
	}

	if previous.Less(latest) {
		if err := w.emitWhenImageReady(ctx, cursor, previous, latest); err != nil {
			return err
		}
	} else {
		w.metrics.IncFilterSkipped("version_unchanged")
		glog.V(2).Infof("version unchanged latest=%s cursor=%s", latest, previous)
	}

	w.metrics.IncPollCycle("success")
	return nil
}

// emitWhenImageReady gates task emission on docker.io having published the
// image for latest. When the image is not yet available (go.dev releases 12h–
// 1day before the image exists) the cursor is held and the next cycle retries.
func (w *watcher) emitWhenImageReady(
	ctx context.Context,
	cursor *Cursor,
	previous, latest Version,
) error {
	exists, err := w.imageChecker.ImageExists(ctx, latest)
	if err != nil {
		w.metrics.IncPollCycle("image_check_error")
		glog.Warningf("image check failed latest=%s err=%v", latest, err)
		return nil
	}
	if !exists {
		w.metrics.IncFilterSkipped("image_not_ready")
		glog.V(2).
			Infof("image not ready latest=%s cursor=%s — holding, retry next cycle", latest, previous)
		w.metrics.IncPollCycle("success")
		return nil
	}
	if err := w.emit(ctx, cursor, previous, latest); err != nil {
		w.metrics.IncPollCycle("build_error")
		glog.Errorf("emit task failed latest=%s: %v", latest, err)
		return nil
	}
	return nil
}

// seedCursor records the current max stable version on cold start without
// emitting a task (avoids a spurious update task on first run).
func (w *watcher) seedCursor(ctx context.Context, cursor *Cursor, latest Version) {
	cursor.LastSeenVersion = latest.String()
	if err := SaveCursor(ctx, w.cursorPath, cursor); err != nil {
		glog.Warningf("save cursor failed on cold-start path=%s err=%v", w.cursorPath, err)
	}
	glog.V(2).Infof("cold-start seed version=%s", latest)
}

// seedFromConfigured seeds the cold-start cursor with the configured
// seedVersion (a version lower than the current latest) and persists it, so the
// caller can fall through to the normal comparison and emit a task for latest.
// Returns an error if seedVersion is not a valid Go version.
func (w *watcher) seedFromConfigured(ctx context.Context, cursor *Cursor) error {
	if _, err := ParseVersion(ctx, w.seedVersion); err != nil {
		return errors.Wrapf(ctx, err, "parse seed version %q", w.seedVersion)
	}
	cursor.LastSeenVersion = w.seedVersion
	if err := SaveCursor(ctx, w.cursorPath, cursor); err != nil {
		glog.Warningf("save cursor failed on cold-start seed path=%s err=%v", w.cursorPath, err)
	}
	glog.V(2).Infof("cold-start seed from configured version=%s", w.seedVersion)
	return nil
}

// emit builds and publishes one task for the advance from previous to latest,
// advancing the cursor only when the publish succeeds. A build error (e.g. a
// template-execution failure) is returned so the caller can skip cleanly
// without advancing the cursor.
func (w *watcher) emit(ctx context.Context, cursor *Cursor, previous, latest Version) error {
	releaseKind := "patch"
	if latest.Major != previous.Major || latest.Minor != previous.Minor {
		releaseKind = "minor"
	}
	cmd, err := BuildCreateCommand(ctx, latest.String(), previous.String(), releaseKind, w.cfg)
	if err != nil {
		return errors.Wrapf(ctx, err, "build create command for %s", latest)
	}
	if !w.publisher.PublishCreate(ctx, cmd) {
		return nil
	}
	cursor.LastSeenVersion = latest.String()
	if err := SaveCursor(ctx, w.cursorPath, cursor); err != nil {
		// Task was already published; controller dedup absorbs re-emit next cycle.
		glog.Warningf("save cursor failed post-publish path=%s err=%v", w.cursorPath, err)
	}
	return nil
}
