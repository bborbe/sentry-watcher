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
// store is the injected CursorStore (typically NewFileCursorStore) — the
// watcher never calls the package-level LoadCursor/SaveCursor directly, so Poll
// is testable without real filesystem I/O.
//
// projectAllowlist is the per-cluster project scope filter (e.g. hell,
// nuke-dev, nuke-prod, quant): issues outside it are skipped.
//
// seedDate, when non-empty, makes the first poll (cold start) emit a task for
// every in-scope active issue whose date is after the seed date, instead of
// seeding silently — mirroring go-version's SEED_VERSION for end-to-end
// verification. Empty means seed the cursor to the current state and emit
// nothing on cold start.
// heartbeat, when non-nil, is verified at the top of every poll cycle to
// confirm Sentry is still retaining events. Nil disables the check, which is
// the correct configuration for a deployment with no heartbeat emitter — an
// enabled check with no emitter would alert on every cycle.
func NewWatcher(
	client SentryClient,
	publisher TaskPublisher,
	metrics Metrics,
	store CursorStore,
	cfg TaskConfig,
	projectAllowlist []string,
	seedDate string,
	heartbeat HeartbeatChecker,
) Watcher {
	return &watcher{
		client:           client,
		publisher:        publisher,
		metrics:          metrics,
		store:            store,
		cfg:              cfg,
		projectAllowlist: projectAllowlist,
		seedDate:         seedDate,
		heartbeat:        heartbeat,
	}
}

type watcher struct {
	client           SentryClient
	publisher        TaskPublisher
	metrics          Metrics
	store            CursorStore
	cfg              TaskConfig
	projectAllowlist []string
	seedDate         string
	heartbeat        HeartbeatChecker
}

// Poll implements Watcher. One cycle:
//  1. Load cursor (cold-start safe).
//  2. Query Sentry for unresolved issues active in the stats period — on error
//     hold the cursor, record sentry_error, return nil.
//  3. Cold start (empty cursor): with no seedDate, seed the cursor with each
//     in-scope issue's current date and emit nothing; with a seedDate, seed the
//     baseline (every in-scope issue → seedDate) and fall through so the first
//     poll emits for every issue dated after the seed while pre-seed issues stay
//     suppressed on this AND subsequent polls.
//  4. For each in-scope issue, run the filter chain (scope, missing short-id,
//     already-seen) — skipped issues record filter_skipped and emit nothing.
//  5. Emit one task per (short-id, date); advance the cursor entry only when
//     the publish succeeds (controller dedup absorbs re-emit next cycle).
//  6. Persist the cursor atomically and record success.
func (w *watcher) Poll(ctx context.Context) error {
	w.checkHeartbeat(ctx)

	cursor, err := w.store.Load(ctx)
	if err != nil {
		return errors.Wrapf(ctx, err, "load cursor")
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
	if coldStart && w.seedDate != "" {
		w.seedBaseline(cursor, issues)
	}

	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			glog.V(2).Infof("poll cycle aborted mid-loop: %v", err)
			return nil
		}
		if reason := w.skipReason(ctx, issue, cursor); reason != "" {
			w.metrics.IncFilterSkipped(reason)
			continue
		}
		if err := w.emit(ctx, cursor, issue); err != nil {
			w.metrics.IncFilterSkipped("build_error")
			glog.Errorf("emit task failed shortID=%s err=%v", issue.ShortID, err)
			continue
		}
	}

	if err := w.store.Save(ctx, cursor); err != nil {
		glog.Warningf("save cursor failed err=%v", err)
	}
	w.metrics.IncPollCycle("success")
	return nil
}

// checkHeartbeat records the zero-events signal when a configured heartbeat
// check finds no heartbeat event in Sentry — the case where ingestion has
// stopped retaining events, which an issue count alone cannot detect.
//
// It never aborts the poll. A missing heartbeat is a signal to record, not a
// reason to skip the cycle: the watcher's own scan still has value, and
// aborting here would suppress the normal task emission on the very cycles
// where a real alert might also be arriving.
func (w *watcher) checkHeartbeat(ctx context.Context) {
	if w.heartbeat == nil {
		return
	}
	if _, err := w.heartbeat.Verify(ctx); err != nil {
		w.metrics.IncPollCycle("heartbeat_missing")
		glog.Errorf("heartbeat check failed: %v", err)
	}
}

// seedCursor records each in-scope issue's current date on cold start without
// emitting a task (avoids a burst of tasks covering all historical activity on
// first run).
func (w *watcher) seedCursor(ctx context.Context, cursor *Cursor, issues []Issue) {
	seeded := 0
	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			return
		}
		if !w.inScope(issue.Project.Slug) || issue.ShortID == "" {
			continue
		}
		date, err := IssueDate(ctx, issue.LastSeen)
		if err != nil {
			// V(2)-gated: a corrupt lastSeen on one issue must not flood the log.
			glog.V(2).Infof(
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
	if err := w.store.Save(ctx, cursor); err != nil {
		glog.Warningf("save cursor failed on cold-start err=%v", err)
	}
	glog.V(2).Infof("cold-start seed issues=%d", seeded)
}

// seedBaseline records the seed date as the last-emitted baseline for every
// in-scope issue on a seed-date cold start, so the normal already-seen
// comparison suppresses pre-seed issues on this AND subsequent polls (mirrors
// go-version's SEED_VERSION baseline — the seed is the floor, pre-baseline is
// never emitted).
func (w *watcher) seedBaseline(cursor *Cursor, issues []Issue) {
	for _, issue := range issues {
		if !w.inScope(issue.Project.Slug) || issue.ShortID == "" {
			continue
		}
		cursor.Seen[issue.ShortID] = w.seedDate
	}
	glog.V(2).Infof("cold-start seed baseline date=%s issues=%d", w.seedDate, len(cursor.Seen))
}

// skipReason runs the per-issue filter chain and returns the skip metric label,
// or "" when the issue should be emitted. The already-seen rule compares the
// issue date against the cursor's last emitted date for the short-id (on a
// seed-date cold start that baseline is the configured seed date).
func (w *watcher) skipReason(ctx context.Context, issue Issue, cursor *Cursor) string {
	if !w.inScope(issue.Project.Slug) {
		return "project_not_in_scope"
	}
	if issue.ShortID == "" {
		return "missing_short_id"
	}
	date, err := IssueDate(ctx, issue.LastSeen)
	if err != nil {
		// V(2)-gated: a corrupt lastSeen on one issue must not flood the log.
		glog.V(2).Infof(
			"skip issue shortID=%s lastSeen=%q err=%v",
			issue.ShortID,
			issue.LastSeen,
			err,
		)
		return "invalid_date"
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
