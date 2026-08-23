# sentry-watcher

Polls the `bborbe.sentry.io` org for unresolved issues with activity in the last
24h and publishes one deduplicated `CreateTaskCommand` per `(short-id, date)` so
the `sentry-issue-analyzer` agent can analyze each active alert.

The watcher is the **producer** (per-alert detection) front of the Sentry
pipeline. It never analyzes anything itself — it emits one "Analyze Sentry issue
<SHORT-ID> - <YYYY-MM-DD>" task per active unresolved alert into the Personal
vault, and the downstream agent owns the analysis.

## How It Works

On each poll cycle (default `24h`):

1. **Load cursor** (`/data/cursor.json`) — a per-short-id map of the last UTC
   date (`2006-01-02`) a task was emitted for each issue.
2. **Query Sentry** — `GET /api/0/organizations/bborbe/issues/?query=is:unresolved&statsPeriod=24h`
   with `Authorization: Bearer <token>`, paged to exhaustion. On query failure
   the cursor is held and the cycle records `sentry_error`.
3. **Scope filter** — only issues whose project slug is in the allowlist
   (`hell` / `nuke-dev` / `nuke-prod` / `quant`) are acted on; others record
   `project_not_in_scope`.
4. **Cold start** (empty cursor): seed the cursor with each in-scope issue's
   current date and emit nothing — avoids a burst of tasks covering all
   historical activity on first run. With `SEED_DATE` set, the first poll
   instead emits a task for every in-scope active issue dated after the seed
   (for end-to-end verification).
5. **New date** (issue date > cursor entry): build + publish one
   `CreateTaskCommand`, and advance the cursor entry on publish success.
6. **Unchanged** (issue date <= cursor entry): record `already_seen`, emit
   nothing.

Deterministic `UUID5("sentry:" + short-id + ":" + date)` gives controller dedup
— re-polling an already-emitted `(short-id, date)` is a no-op.

## Task Contract

Every emitted `CreateTaskCommand` carries this frontmatter shape:

```yaml
task_type: sentry-issue-analyzer
assignee: sentry-issue-analyzer
phase: planning
status: in_progress
stage: dev|prod
task_identifier: <UUID5("sentry:" + short-id + ":" + date)>
title: Analyze Sentry issue <SHORT-ID> - <YYYY-MM-DD>
short_id: <SHORT-ID>
project: <project slug>
date: <YYYY-MM-DD>
issue_url: <permalink>
```

Body is an operator-readable header only (title + issue URL). The agent's input
lives in the frontmatter fields.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `STAGE` | yes | — | Deployment stage (`dev` or `prod`) |
| `KAFKA_BROKERS` | yes | — | Comma-separated Kafka broker list |
| `SENTRY_API_TOKEN` | yes | — | Sentry REST API Bearer token (teamvault-sourced, referenced by name in deployment) |
| `SENTRY_PROJECTS` | no | `hell,nuke-dev,nuke-prod,quant` | Comma-separated per-cluster project slug allowlist |
| `TOPIC_PREFIX` | no | — | Kafka topic prefix for CQRS topic construction (`develop` for dev, `master` for prod) |
| `LISTEN` | no | `:9090` | HTTP listen address (`/healthz`, `/readiness`, `/metrics`) |
| `POLL_INTERVAL` | no | `24h` | Poll interval (Go duration string) |
| `CURSOR_PATH` | no | `/data/cursor.json` | Cursor persistence path (PVC mount) |
| `SEED_DATE` | no | — | UTC date (`2006-01-02`) to seed the cursor with on cold start; makes the first poll emit for every in-scope active issue dated after it |
| `SENTRY_URL` | no | `https://bborbe.sentry.io` | Base URL of the Sentry org |
| `SENTRY_ORG` | no | `bborbe` | Sentry org slug |
| `SENTRY_STATS_PERIOD` | no | `24h` | Activity window for the org issues query |
| `TARGET_VAULT` | no | — | Obsidian vault slug the task-controller materializes the task into (`personal` → `24 Tasks/`). Empty = controller default (`openclaw`) |
| `TASK_ASSIGNEE` | no | `sentry-issue-analyzer` | Frontmatter `assignee` |
| `TASK_STATUS` | no | `in_progress` | Frontmatter `status` |
| `TASK_PHASE` | no | `planning` | Frontmatter `phase` |
| `TASK_SUFFIX` | no | — | Optional title/filename suffix appended as ` - <suffix>` |
| `TASK_TITLE_TEMPLATE` | no | — | Go `text/template` title override (fields: ShortID, Date, Title, Project, IssueURL) |
| `TASK_BODY_TEMPLATE` | no | — | Go `text/template` body override (same fields) |
| `SENTRY_DSN` / `SENTRY_PROXY` | no | — | Sentry error-tracking DSN / HTTP proxy |

## HTTP Endpoints

| Path | Method | Purpose |
|---|---|---|
| `/healthz` | GET | Liveness probe (always 200 OK) |
| `/readiness` | GET | Readiness probe (always 200 OK) |
| `/metrics` | GET | Prometheus metrics |
| `/resetcursor` | POST | Delete the cursor (next poll cold-starts); dangerous-wrapped |
| `/setcursor/{shortId}/{date}` | POST | Pin a short-id's last emitted date (force re-emit by setting an earlier date); dangerous-wrapped |
| `/trigger` | POST | Force one immediate poll cycle |

## Metrics

`<watcher>_` prefix = `sentry_watcher`. All counters pre-initialised; structured
glog lines mirror metric events.

- `sentry_watcher_poll_cycle_total{result}` — `success` / `sentry_error` / `build_error`
- `sentry_watcher_published_total{status}` — `create` / `error`
- `sentry_watcher_filter_skipped_total{reason}` — `project_not_in_scope` / `already_seen` / `missing_short_id` / `invalid_date` / `build_error`
- `sentry_watcher_issues_scanned_total` — per-cycle scan size sanity check

## Deployment

Deployed via the `bborbe/maintainer` Helm chart (`watchers:` entry), configured
per stage in the maintainer values; the chart mounts a PVC at `/data` for the
cursor. `make buca` builds + pushes `docker.io/bborbe/sentry-watcher:vX.Y.Z`;
the image is mirrored into the nuke registry and rolled via keel. The
`SENTRY_API_TOKEN` secret is teamvault-rendered out-of-band and referenced by
name. Per the Watcher Writing Guide single-stage rule, deploy the daily
00:00Z scan in ONE stage only (prod canonical; enable dev only transiently for
e2e) — both stages' controllers materialize into the same Personal vault and
would fight over identical task ids.

## End-to-End Verification (force an emit)

1. Deploy dev with `SEED_DATE` set to a date before the issues' current activity
   (e.g. `2026-08-01`).
2. `POST /trigger` (or wait for the first poll) → a task per active in-scope
   issue emits.
3. Confirm the vault file: `grep -rl "Analyze Sentry issue" <personal-vault>/24 Tasks/`
   and check frontmatter (`assignee`, `task_type`, `phase`, deterministic id).
4. Remove `SEED_DATE`, `POST /resetcursor` → next poll seeds silently to the
   current state.

## Development

- `make precommit` — format + generate + test + lint/vet/vuln/trivy gate
- `cmd/run-once` — single poll cycle for local smoke-testing
- Tests are Ginkgo/Gomega with counterfeiter mocks under `mocks/`
