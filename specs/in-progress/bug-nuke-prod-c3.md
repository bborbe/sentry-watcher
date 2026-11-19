---
status: approved
approved: 2026-11-19T01:27:42.275963002Z
kind: bug
---

## Summary

Sentry issue `NUKE-PROD-C3` is a real bug in `bborbe/sentry-watcher` at `bborbe/sentry-watcher/pkg/watcher.go:206`, filed automatically by the sentry-fix-agent from the High/High real bug deep verdict — that verdict is the approval gate, so this spec is approved rather than drafted.

## Problem

The `sentry-heartbeat` issue is a deliberately emitted synthetic liveness event (by design, every 6h), but the live `sentry-watcher` poller (v0.7.4) has no heartbeat skip in its per-issue filter chain (`skipReason`), so it emits an Analyze-Sentry-issue task for the unresolved 24h-active heartbeat; the Kafka consumer skips it (v0.2.0+, prod v0.2.5) but the poller does not.

## Reproduction

The implicated source location from the deep verdict is `bborbe/sentry-watcher/pkg/watcher.go:206` in `bborbe/sentry-watcher` (resolution rule: `candidate position 1 (bborbe/sentry-watcher)`), Sentry issue `NUKE-PROD-C3`.

## Recommended Fix

Add a `skipped_heartbeat` guard to `sentry-watcher`'s `skipReason` filter chain (match issue title/logger `sentry-heartbeat`), mirroring `sentry-alert-consumer` `pkg/consumer.go:143`; do not silence or remove the heartbeat emitter.
