#!/bin/sh
# Entrypoint for the sentry-watcher image.
#
# MODE=once runs a single Sentry poll cycle and exits — the shape a
# Schedule-driven Job needs, where the Job must start, do the pass and finish.
# Any other value (the default) runs the continuous poll loop, which is what the
# long-running StatefulSet needs.
#
# Both binaries ship in this image so the one-shot path cannot drift from the
# watcher's tag or query, the same reason /heartbeat and /check-heartbeat live
# here.
set -eu

if [ "${MODE:-}" = "once" ]; then
	exec /run-once "$@"
fi

exec /main "$@"
