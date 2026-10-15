# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

- feat: `cmd/heartbeat` publishes each cycle's outcome to the push gateway as a gauge pair, so a missed emitter cycle is attributable from the cluster after the pod is gone. The emitter's only trace was `glog.V(2).Infof("heartbeat sent …")` — suppressed at the default verbosity, and lost with the pod inside about a day, which is why the 2026-10-10/11 evidence had to be reconstructed from a job reason and the proxy's log. Two outcomes, deliberately not one: `sentry_watcher_heartbeat_emitter_posted` is 1 when the cycle's POST reached the proxy and returned a response — accepted *or* rejected — and `sentry_watcher_heartbeat_emitter_not_reached_proxy` is 1 when the cycle ended before the proxy answered, so "the proxy answered and refused" is separable from "the emitter never got there", and the 2026-10-10 misses were the second. **Gauges, not counters**, and that is a contract point rather than a style choice: the value is the last cycle's state and goes 1 → 0 whenever the next cycle takes the other branch, whereas a Prometheus counter is monotonically non-decreasing by definition — typed as one it would make `rate()` and `increase()` nonsense over the series and mislead anyone reading a `_total` name as a running total, which is why neither name carries the suffix. A running total is not obtainable here in any case: a push gateway stores what it is given, and the project states the non-goal itself ("explicitly not an aggregator or distributed counter but rather a metrics cache") — its POST ("push_add") merges by metric *name* rather than adding values, so no HTTP method makes the series accumulate, and the push therefore stays on the gateway's self-healing PUT default. Both gauges are pushed together on every run, and that is what keeps the pair readable: a pushed series exists because its collector was gathered, not because a value was written, so both are present even on a cycle where neither branch set one — an alert over a missing series would read no data rather than zero and stay silent on the very case it is for. The three questions have three producers — *how many* cycles were missed belongs to kube-state-metrics (`count_over_time((kube_job_status_failed{namespace="prod",job_name=~"sentry-heartbeat-emitter-.+"} == 1)[48h:1m]) > 0`), *is anything completing at all* to the gateway's own `push_time_seconds` age, and *which way the last cycle failed* to this pair. The per-cycle record an observation window needs comes from the first of those too: a non-2xx or a transport failure returns an error from `Send`, so the Job exits non-zero and `kube_job_status_failed` carries that cycle's send outcome for the retention window, where a last-value gauge carries only the most recent one. The POST now emits exactly one boundary audit line per call — success at V0, failure at `glog.Warningf` with the wrapped error — carrying the status code and elapsed ms, replacing the V(2) line that made a successful run leave no trace at all in `kubectl logs`. Publishing never fails the emitter: a gateway that is down costs a metric, not a heartbeat, and the push carries its own 10s client bound because `push.New` otherwise installs an `&http.Client{}` with no timeout while the push runs before the emitter returns. ⚠️ Deploy ordering: `bborbe/nuke`'s `sentry-heartbeat-emitter` CronJob must set `PUSHGATEWAY_URL` **before** this image is tagged into it — the arg is `required:"true"` on the **new** image, so tagging it into a manifest that does not yet set the var makes the emitter fail to start, which is a silent stop of the emitter rather than a loud failure. This is the add-direction counterpart of the v0.5.0 note below, which warns about the drop direction for `sentry-heartbeat-check`

## v0.5.0

- refactor: retire the bespoke heartbeat alert path. `pkg.BuildHeartbeatAlertCommand`, the `HeartbeatMissingShortID` sentinel it carried, and `cmd/check-heartbeat`'s Kafka task-writing half are removed, together with the `tasktemplate.Heartbeat*` pair and the `heartbeat-title.md` / `heartbeat-body.md` files they embedded. The heartbeat alert is now raised by a Prometheus rule over `sentry_watcher_heartbeat_last_seen_timestamp_seconds`, declared as an `Alert` CR in `bborbe/nuke`, so exactly one path can raise it. What went with the bespoke path is the point of the change: a date-keyed task id that collapsed a day of failures into one task — Alertmanager's `group_interval` reimplemented worse — and an alert whose timing was welded to the check CronJob's own schedule, which is what produced the 2026-10-10 false alarm while Sentry was still retaining events. The verdict is unchanged and so is the cluster signal: `Verify` still returns `ErrHeartbeatMissing` and the run still exits non-zero on it, so only the human-facing vault task is gone — the alert fires on the age crossing the bound regardless of whether this CronJob ran at all. ⚠️ Deploy ordering: `bborbe/nuke`'s `sentry-heartbeat-check` CronJob must drop `KAFKA_BROKERS`, `TOPIC_PREFIX`, `TARGET_VAULT` and `TASK_ASSIGNEE` **after** this image is tagged into it — those args were `required:"true"`, so a manifest that drops them first leaves the previous image unable to start, which is a silent stop of the detector rather than a loud failure

## v0.4.0

- feat: `cmd/check-heartbeat` publishes the newest heartbeat's `lastSeen` to the push gateway as `sentry_watcher_heartbeat_last_seen_timestamp_seconds`, so the alerting decision stops riding on the check process's exit code. It is a **timestamp, not an age**: an age gauge freezes at its last healthy value when the pusher dies, so an alert built on it would go quiet exactly when the check does — a timestamp makes `time() - metric` keep growing instead. `HeartbeatChecker.Verify` now returns a `HeartbeatObservation` alongside its verdict, because that timestamp has to survive the *missing* verdict: a stale heartbeat's age is precisely what the alert fires on, so returning it only on success would blind the alert at the one moment it matters. A zero reading is not pushed at all — writing it would place a heartbeat in 1970 and pin the computed age at ~56 years, turning a transient query failure into a permanent alert. `PUSHGATEWAY_URL` is required rather than defaulted, so a deployment that forgets it fails startup instead of silently disabling the alert, and publishing never fails the check: a gateway that is down costs a metric, not a verdict. The push carries its own 10s client bound because `push.New` defaults to an `&http.Client{}` with **no timeout**, and the push runs *before* the verdict is acted on — so an unresponsive gateway would have blocked the run past the point where the alert task is filed, turning a metrics outage into a silent detector outage. ⚠️ Deploy ordering: `bborbe/nuke`'s `sentry-heartbeat-check` CronJob must set `PUSHGATEWAY_URL` before this image is tagged into it

## v0.3.6

- fix: the zero-events heartbeat escalation states the missing-heartbeat observation instead of asserting that Sentry stopped retaining events. The assertion was carried in three places — the title, the body's H1, and the body's prose — so correcting the title alone would have left the rendered escalation leading with a cause the check never established. It was wrong for 26 consecutive days while retention worked. The checker's own failure message carried the same claim and now states the observation too. Both the rendered-template test and the checker's error assertion now pin the absence, so a partial fix cannot pass.

## v0.3.5

- chore: update Go to 1.27.2 and github.com/IBM/sarama to v1.61.1, github.com/bborbe/agent to v0.100.0, github.com/bborbe/cqrs to v0.6.12, github.com/bborbe/cron to v1.9.2, github.com/bborbe/errors to v1.6.3, github.com/bborbe/http to v1.26.29, github.com/bborbe/kafka to v1.27.1, github.com/bborbe/log to v1.7.4, github.com/bborbe/run to v1.11.2, github.com/bborbe/sentry to v1.10.4, github.com/bborbe/service to v1.10.16, github.com/bborbe/time to v1.27.16, github.com/onsi/ginkgo/v2 to v2.33.1, github.com/onsi/gomega to v1.44.0, github.com/prometheus/client_golang to v1.25.0

## v0.3.4

- fix: bump `osv-scanner` to v2.6.0 and `golang.org/x/net` to v0.60.0 so the Linux vulnerability gates stop failing. v2.3.1 pins `golang.org/x/tools` v0.38.0, whose SSA builder aborts with `unexpected expr: *ast.KeyValueExpr` on the promoted-field composite-literal key Go 1.27 permits in the Linux stdlib, so a repo on the old pin passes locally on darwin and fails only in Linux CI. `x/net` v0.58.0 carries `GO-2026-6603/6610/6611/6612/6617`, which fail both `vulncheck` and `trivy`.

## v0.3.3

- chore: enable `release.autoRelease`, so the releaser cuts the tag on merge instead of a human doing it by hand. The manual step cost a real delay: the heartbeat-envelope fix (PR #20) merged and then sat under `## Unreleased` until someone renamed the heading and tagged by hand. The deploy is unaffected — the CronJobs running this image pin `WATCHER_TAG` explicitly in `bborbe/nuke`

## v0.3.2

- fix: the heartbeat emitter now writes a conforming envelope. `heartbeatEnvelope` emitted the envelope header and then the event object as a *second* line, but the grammar is `Item = Headers "\n" Payload` — an item is its own header line followed by the payload. Sentry therefore parsed the event as the item header and found no payload, accepted the envelope with a `200`, and retained nothing. The emitter could not see it: `Send` inspects only the HTTP status and discards the response body. So the heartbeat was invisible for the whole outage it exists to detect — `source:sentry-heartbeat` matched no event in 90 days while every emitter run reported `Complete`, and the zero-events detector read that absence as ingestion being dead. The builder now emits three lines (envelope header, `{"type":"event","length":N}`, event payload), and a test asserts the *shape* — the existing `ContainSubstring` assertions pass on both the two-line and the three-line body, which is why this shipped
- fix: `newEventID` wraps its failure with the caller's context instead of returning the error bare, and takes that context as its first argument. Adjacent to the envelope fix rather than part of it, in the same function's call path

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
