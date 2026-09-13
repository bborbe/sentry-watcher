# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

- feat: the heartbeat check runs at the top of every poll cycle and records `poll_cycle_total{result="heartbeat_missing"}` when no heartbeat event is visible — the zero-events signal, distinct from `sentry_error` because the check itself succeeded. Enabled by `HEARTBEAT_QUERY` (empty = disabled, so a deployment without an emitter does not report ingestion dead every cycle). The check never aborts the poll: a missing heartbeat is a signal to record, not a reason to skip a cycle that may also be carrying a real alert
- feat: `pkg.HeartbeatChecker` verifies Sentry is actually retaining events. A quiet fleet and dead ingestion both yield "no new issues", so an issue count cannot separate them; the checker asserts a deliberately-tagged heartbeat event is visible instead, where absence is unambiguous. A query failure is reported as a query error, never as a missing heartbeat — a check that could not run is not evidence of failure
- feat: `SENTRY_QUERY` (config `SentryConfig.Query`) overrides the org-issues search expression; empty keeps the existing `is:unresolved`. The query was hardcoded in `fetchPage`, so no caller could search for anything else — a zero-events detector needs a tag search to assert a heartbeat event arrived, which an unresolved-issue count cannot express
- chore: add `goUpdate.autoUpdate` to `.maintainer.yaml` so github-update-go-agent keeps the repo in the fleet sweep — the key was absent and read as `false`, silently excluding it
- fix: bump errcheck to v1.20.0 for Go 1.27 compatibility
- fix: `make build` refuses to stamp a version onto a tree that is not that version's tag (`check-version-tag`, escape hatch `ALLOW_UNTAGGED_BUILD=1`). `VERSION` defaults to the newest tag repo-wide, so an operator-run build from an untagged or older tree silently republishes under the newest tag. The guard compares `git describe --exact-match HEAD` against `$(VERSION)` and exits non-zero on mismatch.

## v0.1.3

- chore: schedule the scan with a real cron expression via `github.com/bborbe/cron`
  instead of a 24h interval ticker. The old `time.NewTicker` started at process
  start drifted the daily poll to the last pod restart; the new `CRON_EXPRESSION`
  (default `0 0 0 * * ?` = daily 00:00Z, strict 6-field seconds-first — a 5-field
  expression is rejected at startup) anchors it to wall clock. Replaces the
  `POLL_INTERVAL` env var. The first scan fires at the next match; manual scans
  still go through `POST /trigger`.

## v0.1.2

- fix: stop pagination when Sentry's Link header marks the next link
  `results="false"` — Sentry still emits a `rel="next"` URL on the final page,
  and following the phantom cursor re-fetched the same empty page forever
  (found live in the dev e2e: the poll looped every ~500ms until the maxPages
  backstop). The results flag is now the pagination terminator.

## v0.1.1

- fix: drop `statsPeriod` from the org-issues query — Sentry's org-issues
  endpoint treats `statsPeriod` as an event-recency filter, so the 24h window
  returned zero issues (real unresolved issues routinely sit 1-3 days past their
  last event). The query is now plain `query=is:unresolved`; the cursor +
  per-`(short-id, date)` dedup is the dedup mechanism. Removes the
  `SENTRY_STATS_PERIOD` env var.

## v0.1.0

- feat: initial release. Polls the bborbe Sentry org for unresolved issues with
  activity in the last 24h (plain REST + Bearer token) and emits one
  `CreateTaskCommand` per `(short-id, date)` titled
  `Analyze Sentry issue <SHORT-ID> - <YYYY-MM-DD>` targeting the Personal vault
  (`TARGET_VAULT=personal`) for the `sentry-issue-analyzer` agent
  (`task_type: sentry-issue-analyzer`, `assignee: sentry-issue-analyzer`).
- docs: correct the Deployment section — sentry-watcher deploys as a nuke-repo
  unit via the `bborbe/helm` sentry-watcher chart (go-version-notifier pattern),
  not the maintainer Helm chart.
