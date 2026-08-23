# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
