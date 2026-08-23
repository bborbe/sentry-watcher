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

// Watcher polls the Sentry org for active unresolved issues and publishes one
// CreateTaskCommand per (short-id, date).
type Watcher interface {
	// Poll runs one scan cycle. Safe to call repeatedly on an interval.
	Poll(ctx context.Context) error
}

// NewWatcher wires the watcher's collaborators.
//
// projectAllowlist is the per-cluster project scope filter (e.g. hell,
// nuke-dev, nuke-prod, quant): issues outside it are skipped.
//
// seedDate, when non-empty, makes the first poll (cold start) emit a task for
// every in-scope active issue whose date is after the seed date, instead of
// seeding silently — mirroring go-version's SEED_VERSION for end-to-end
// verification. Empty means seed the cursor to the current state and emit
// nothing on cold start.
func NewWatcher(
	client SentryClient,
	publisher TaskPublisher,
	metrics Metrics,
	cursorPath string,
	cfg TaskConfig,
	projectAllowlist []string,
	seedDate string,
) Watcher {
	return &watcher{
		client:           client,
		publisher:        publisher,
		metrics:          metrics,
		cursorPath:       cursorPath,
		cfg:              cfg,
		projectAllowlist: projectAllowlist,
		seedDate:         seedDate,
	}
}

type watcher struct {
	client           SentryClient
	publisher        TaskPublisher
	metrics          Metrics
	cursorPath       string
	cfg              TaskConfig
	projectAllowlist []string
	seedDate         string
}

// Poll implements Watcher. One cycle:
//  1. Load cursor (cold-start safe).
//  2. Query Sentry for unresolved issues active in the stats period — on error
//     hold the cursor, record sentry_error, return nil.
//  3. Cold start (empty cursor): with no seedDate, seed the cursor with each
//     in-scope issue's current date and emit nothing; with a seedDate, fall
//     through so the first poll emits for every issue dated after the seed.
//  4. For each in-scope issue, run the filter chain (scope, missing short-id,
//     already-seen) — skipped issues record filter_skipped and emit nothing.
//  5. Emit one task per (short-id, date); advance the cursor entry only when
//     the publish succeeds (controller dedup absorbs re-emit next cycle).
//  6. Persist the cursor atomically and record success.
func (w *watcher) Poll(ctx context.Context) error {
	cursor, err := LoadCursor(ctx, w.cursorPath)
	if err != nil {
		return errors.Wrapf(ctx, err, "load cursor path=%s", w.cursorPath)
	}

	issues, err := w.client.UnresolvedIssues(ctx)
	if err != nil {
		w.metrics.IncPollCycle("sentry_error")
		glog.Warningf("poll cycle aborted: sentry query failed err=%v", err)
		return nil
	}
	w.metrics.IncIssuesScanned(len(issues))

	coldStart := len(cursor.Seen) == 0
	if coldStart && w.seedDate == "" {
		w.seedCursor(ctx, cursor, issues)
		w.metrics.IncPollCycle("success")
		return nil
	}

	for _, issue := range issues {
		if reason := w.skipReason(ctx, issue, cursor, coldStart); reason != "" {
			w.metrics.IncFilterSkipped(reason)
			continue
		}
		if err := w.emit(ctx, cursor, issue); err != nil {
			w.metrics.IncFilterSkipped("build_error")
			glog.Errorf("emit task failed shortID=%s err=%v", issue.ShortID, err)
			continue
		}
	}

	if err := SaveCursor(ctx, w.cursorPath, cursor); err != nil {
		glog.Warningf("save cursor failed path=%s err=%v", w.cursorPath, err)
	}
	w.metrics.IncPollCycle("success")
	return nil
}

// seedCursor records each in-scope issue's current date on cold start without
// emitting a task (avoids a burst of tasks covering all historical activity on
// first run).
func (w *watcher) seedCursor(ctx context.Context, cursor *Cursor, issues []Issue) {
	seeded := 0
	for _, issue := range issues {
		if !w.inScope(issue.Project.Slug) || issue.ShortID == "" {
			continue
		}
		date, err := IssueDate(ctx, issue.LastSeen)
		if err != nil {
			glog.Warningf(
				"skip seed shortID=%s lastSeen=%q err=%v",
				issue.ShortID,
				issue.LastSeen,
				err,
			)
			continue
		}
		cursor.Seen[issue.ShortID] = date
		seeded++
	}
	if err := SaveCursor(ctx, w.cursorPath, cursor); err != nil {
		glog.Warningf("save cursor failed on cold-start path=%s err=%v", w.cursorPath, err)
	}
	glog.V(2).Infof("cold-start seed issues=%d", seeded)
}

// skipReason runs the per-issue filter chain and returns the skip metric label,
// or "" when the issue should be emitted. In seedDate cold-start mode the
// already-seen rule compares the issue date against the configured seed date;
// otherwise against the cursor's last emitted date for the short-id.
func (w *watcher) skipReason(
	ctx context.Context,
	issue Issue,
	cursor *Cursor,
	coldStart bool,
) string {
	if !w.inScope(issue.Project.Slug) {
		return "project_not_in_scope"
	}
	if issue.ShortID == "" {
		return "missing_short_id"
	}
	date, err := IssueDate(ctx, issue.LastSeen)
	if err != nil {
		glog.Warningf(
			"skip issue shortID=%s lastSeen=%q err=%v",
			issue.ShortID,
			issue.LastSeen,
			err,
		)
		return "invalid_date"
	}
	if coldStart && w.seedDate != "" {
		if date <= w.seedDate {
			return "already_seen"
		}
		return ""
	}
	if lastEmitted, seen := cursor.Seen[issue.ShortID]; seen && date <= lastEmitted {
		return "already_seen"
	}
	return ""
}

// emit builds and publishes one task for the issue's current date, advancing
// the cursor entry only when the publish succeeds.
func (w *watcher) emit(ctx context.Context, cursor *Cursor, issue Issue) error {
	date, err := IssueDate(ctx, issue.LastSeen)
	if err != nil {
		return errors.Wrapf(ctx, err, "extract date for %s", issue.ShortID)
	}
	cmd, err := BuildCreateCommand(ctx, issue, date, w.cfg)
	if err != nil {
		return errors.Wrapf(ctx, err, "build create command for %s", issue.ShortID)
	}
	if !w.publisher.PublishCreate(ctx, cmd) {
		return nil
	}
	cursor.Seen[issue.ShortID] = date
	return nil
}

func (w *watcher) inScope(projectSlug string) bool {
	for _, p := range w.projectAllowlist {
		if p == projectSlug {
			return true
		}
	}
	return false
}
