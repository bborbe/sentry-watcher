# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.3.1

- fix: `cmd/check-heartbeat` no longer reports its own heartbeat-missing error to Sentry. That error is the detector *working*, and it is already delivered twice — the vault task for a human, the failed run for the cluster — so reporting it a third time spent the org's ingest quota on a known condition through the same proxy the check exists to watch, and filed an error event the analyzer pipeline could pick up as noise. `service.Options.ExcludeErrors` carries the exclusion, so unexpected failures ("the check could not run") are still reported. The DSN stays **required**: dropping it from the deployment instead would fail argument validation and the watchdog would stop watching silently

## v0.3.0

- feat: `cmd/check-heartbeat` — the detector half of the zero-events check, as its own one-shot binary. It reads Sentry for the heartbeat and, when none is visible, files exactly one vault task per UTC day and exits non-zero, so the signal reaches a human *and* the failed run reaches the cluster. It is deliberately **not** the poller: the poller's emit path is superseded by the Kafka-native `sentry-alert-consumer`, and running it would file a second copy of every `Analyze-Sentry-issue` task — different dedup key, different assignee, so the controller's `(issue-key, date)` dedup could not collapse them. This binary has no analysis path, so there is nothing to double-file
- feat: `pkg.ErrHeartbeatMissing` separates "no heartbeat is visible" from "the check could not run". Both return an error from `Verify`, and a caller that files an alert on the *absence* must branch on the sentinel — otherwise it cries wolf exactly when the Sentry API is unreachable, which is the failure mode this task family keeps rediscovering. Both absence paths wrap it; the query-failure and unparseable-`lastSeen` paths deliberately do not
- feat: the heartbeat alert task gets its own title/body templates (`tasktemplate.HeartbeatTitle` / `HeartbeatBody`) so it does not read as an analyze task — it reports an absence and links to no Sentry issue. The body is ordered cheapest-first: check whether the emitter is still running before assuming ingestion is dead, since a stopped emitter produces the identical absence. `pkg.BuildHeartbeatAlertCommand` derives the task id from the UTC date, which is what makes running this often safe — a detector stuck on a dead pipeline fails on *every* check, and a per-check id would bury the signal in its own repetition

## v0.2.1

- fix: the heartbeat scan now honours the caller's context, stopping on cancellation instead of running the issue list to completion. An unreachable or slow Sentry makes that list large, and the caller's deadline is authoritative even over a bounded in-memory loop; the cancellation is reported as a wrapped context error, distinct from both the absent-heartbeat and the unparseable-`lastSeen` outcomes
- refactor: move the heartbeat enable/disable decision out of `factory.CreateWatcher` and into the binaries' boot path, behind a new conditional-free `factory.CreateHeartbeatChecker`. `CreateWatcher` now receives a ready-built checker (or `nil`), which clears a `go-factory/no-conditional-in-body` MUST violation: a conditional in a factory means the factory is making a decision, and decisions belong at boot time or behind a provider — not in the wiring tree
- fix: bound the heartbeat check by age, so the zero-events detector can actually fire. The org-issues endpoint applies no time window of its own — `fetchPage` sends only `query`/`limit`/`cursor` — so `HEARTBEAT_QUERY=sentry-heartbeat` matched a heartbeat from *any* point in the past: the first event ever sent satisfied the check forever, and the detector stayed silent through a repeat of the 2026-09-06 blackout it exists to catch. `HeartbeatChecker` now requires a heartbeat newer than `HEARTBEAT_MAX_AGE` (default `6h`, the N the detector is specified at) and reports a stale-only result as `newest heartbeat is … past the … limit`. The bound is enforced in Go against each issue's own `lastSeen`, not in the search expression, so no Sentry search-syntax assumption is load-bearing — the sibling `statsPeriod` mechanism was already disproven on this endpoint. A present-but-unparseable `lastSeen` reports its own outcome, deliberately neither present nor absent

## v0.2.0

- feat: `cmd/heartbeat` emits the synthetic event the checker asserts on — the emitter half of the zero-events detector. It posts an `application/x-sentry-envelope` payload through `SENTRY_PROXY` when set, so the heartbeat traverses the same path as real fleet events; a heartbeat that bypassed the proxy would prove nothing about the path that broke in the 2026-09-06 blackout. The event is `level: "error"`, not a message: the checker reads the org-issues endpoint, which lists error groups only, so a message-level event would never appear and the detector would report ingestion dead forever. `HeartbeatTag` and `HeartbeatSearchExpression` are shared constants so emitter and checker cannot drift. The image now builds `/heartbeat` alongside `/main` for a CronJob to run via entrypoint override
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
