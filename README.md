# go-version-watcher

Polls [`https://go.dev/dl/?mode=json`](https://go.dev/dl/?mode=json) on an interval, computes the maximum `stable:true` Go version, and publishes one deduplicated `CreateTaskCommand` to Kafka per new version so a downstream task system can act on it.

The emitted task title and body are plain, deployment-agnostic defaults out of the box; set `TASK_TITLE_TEMPLATE` / `TASK_BODY_TEMPLATE` (Go `text/template`) to inject deployment-specific content — e.g. a link to your own update runbook.

The watcher is the **producer** (Layer 1 detection) half of the pipeline. It never modifies any repo. It emits one global "new Go released" signal; deciding which repos need a bump is a future Layer 2 fan-out service.

## How It Works

On each poll cycle:

1. **Load cursor** (`/data/cursor.json`) — a single `last_seen_version` string.
2. **Query go.dev** — parse the JSON release list, keep `stable:true` entries, and compute the **max** version by parsed `(major, minor, patch)` integers (`go1.26.10 > go1.26.9`). On query/parse failure the cursor is held and the cycle records `go_dev_error`.
3. **Cold start** (empty cursor): seed the cursor to the current max and emit nothing — avoids a spurious "update" task on first run.
4. **New version** (`max > cursor`): classify `release_kind` (`minor` if the major/minor differs, else `patch`), publish one `CreateTaskCommand`, and advance the cursor on publish success.
5. **Unchanged** (`max <= cursor`): record `version_unchanged`, emit nothing.

Deterministic `UUID5("go-version:" + version)` gives controller dedup — re-polling an already-seen version is a no-op.

## Task Contract

Every emitted `CreateTaskCommand` carries this frontmatter shape:

```yaml
task_type: go-version-update
assignee: human
phase: planning
status: in_progress
stage: dev|prod
task_identifier: <UUID5("go-version:" + new_version)>
title: Update Go to <X.Y.Z>
new_version: go1.27.0
previous_version: go1.26.5
release_kind: minor        # minor | patch
release_notes_url: https://go.dev/doc/devel/release#go1.27.0
```

Body is an operator-readable header only (title + release-notes URL + downloads URL). The built-in default is deployment-agnostic; override it per deployment with `TASK_BODY_TEMPLATE` to add e.g. a runbook link.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `STAGE` | yes | — | Deployment stage (`dev` or `prod`) |
| `KAFKA_BROKERS` | yes | — | Comma-separated Kafka broker list |
| `LISTEN` | no | `:9090` | HTTP listen address (`/healthz`, `/readiness`, `/metrics`) |
| `POLL_INTERVAL` | no | `24h` | Poll interval (Go duration string) |
| `CURSOR_PATH` | no | `/data/cursor.json` | Cursor persistence path (PVC mount) |
| `TOPIC_PREFIX` | no | — | Kafka topic prefix for CQRS topic construction |
| `SENTRY_DSN` | no | — | Sentry DSN for error tracking |
| `SENTRY_PROXY` | no | — | HTTP proxy URL for Sentry transport |

## HTTP Endpoints

| Path | Method | Purpose |
|---|---|---|
| `/healthz` | GET | Liveness probe (always 200 OK) |
| `/readiness` | GET | Readiness probe (always 200 OK) |
| `/metrics` | GET | Prometheus metrics |

## Metrics

| Metric | Cardinality | Purpose |
|---|---|---|
| `go_version_watcher_poll_cycle_total{result}` | `result=success\|go_dev_error` | Poll health |
| `go_version_watcher_published_total{status}` | `status=create\|error` | Task emission |
| `go_version_watcher_filter_skipped_total{reason}` | `reason=version_unchanged` | Filter visibility |

## Development

```bash
make test          # run unit tests
make generate      # regenerate counterfeiter mocks
make precommit     # format + lint + test + security checks
```

## Smoke Test (`cmd/run-once`)

Single Poll cycle against real dev Kafka, then exits.

```bash
cd cmd/run-once
make run-once KAFKA_BROKERS=<brokers>
```

## License

BSD 2-Clause License. See [LICENSE](LICENSE).
