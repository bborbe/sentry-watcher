# Sentry has stopped retaining events - {{.Date}}

The zero-events heartbeat did not arrive within its staleness window ({{.Title}}).

A quiet fleet and dead ingestion both produce "no new issues", so no issue count
can tell them apart. The heartbeat is the positive assertion that separates
them — and it is missing. The likeliest cause is that Sentry is not retaining
events: an over-quota org returns HTTP 200 and keeps nothing, so ingestion
failure is invisible at the client and stays invisible until someone notices the
silence.

Check, cheapest first:

1. **Is the emitter running?** A stopped heartbeat CronJob produces exactly this
   absence and is the cheaper explanation. Check the `sentry-heartbeat` CronJob's
   last schedule time and recent job history.
2. **Is `sentry-proxy` forwarding?** A dead proxy breaks the same path.
3. **Is the org over its free-tier cap?** 5,000 events/month, reset on the 5th.
   Over-quota drops are accepted-then-discarded, so the proxy logs 200s while
   nothing is retained.

Do not resolve this by lowering the heartbeat interval or the staleness window —
that narrows the signal without restoring ingestion.

Emitted once per UTC day: the detector fails on every check while ingestion is
down, and one task per check would bury the signal in its own repetition.
