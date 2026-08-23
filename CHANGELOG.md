# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

- fix: drop `statsPeriod` from the org-issues query — Sentry's org-issues
  endpoint treats `statsPeriod` as an event-recency filter, so the 24h window
  returned zero issues (real unresolved issues routinely sit 1-3 days past their
  last event). The query is now plain `query=is:unresolved`; the cursor +
  per-`(short-id, date)` dedup is the dedup mechanism. Removes the
  `SENTRY_STATS_PERIOD` env var.
- docs: correct the Deployment section — sentry-watcher deploys as a nuke-repo
  unit via the `bborbe/helm` sentry-watcher chart (go-version-notifier pattern),
  not the maintainer Helm chart.
- feat: initial release. Polls the bborbe Sentry org for unresolved issues with
  activity in the last 24h (plain REST + Bearer token) and emits one
  `CreateTaskCommand` per `(short-id, date)` titled
  `Analyze Sentry issue <SHORT-ID> - <YYYY-MM-DD>` targeting the Personal vault
  (`TARGET_VAULT=personal`) for the `sentry-issue-analyzer` agent
  (`task_type: sentry-issue-analyzer`, `assignee: sentry-issue-analyzer`).
  Per-cluster project allowlist scope filter (`hell` / `nuke-dev` /
  `nuke-prod` / `quant`), deterministic UUID5 task ids, per-short-id date cursor
  at `/data/cursor.json`, rate-limit/5xx retry hardening, `SEED_DATE` +
  `/resetcursor` + `/setcursor/{shortId}/{date}` + `/trigger` for end-to-end
  verification. Deploys via the maintainer Helm chart.
