#!/usr/bin/env bash
set -euo pipefail

# Cluster build arm for sentry-watcher.
# Contract (environment variables):
#   BUILD_TARGET  cluster alias, e.g. sun (required)
#   REF           full 40-character commit SHA to build (required)
#   TAG           image tag / VERSION (required)
#   IMAGE         image repository, e.g. docker.io/bborbe/sentry-watcher (required)
#   REPO_URL      git remote (default https://github.com/bborbe/sentry-watcher.git)
#   DOCKERFILE    Dockerfile path (default Dockerfile)
#   JOB_NAME      DNS-1123 Job name (default sanitized build-<TAG>)
#   PUSH_SECRET   dockerconfigjson secret name (default buildkit-push)
#   NAMESPACE     namespace (default build)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TIMEOUT=3600
NAMESPACE="${NAMESPACE:-build}"
PUSH_SECRET="${PUSH_SECRET:-buildkit-push}"
REPO_URL="${REPO_URL:-https://github.com/bborbe/sentry-watcher.git}"
DOCKERFILE="${DOCKERFILE:-Dockerfile}"
BUILD_TARGET="${BUILD_TARGET:-}"
REF="${REF:-}"
TAG="${TAG:-}"
IMAGE="${IMAGE:-}"

die() { echo "ERROR: $*" >&2; exit 1; }

# 1. REF validation runs first, before any cluster interaction.
[ -n "$REF" ] || die "REF is required (a full 40-character commit SHA)"
printf '%s' "$REF" | grep -Eq '^[A-Za-z0-9._/-]+$' || die "REF '$REF' contains characters outside [A-Za-z0-9._/-]"
[ "${#REF}" -eq 40 ] || die "REF '$REF' is not a full 40-character SHA"
git rev-parse --verify --quiet "${REF}^{commit}" >/dev/null || die "REF '$REF' does not resolve in this repository"

# 2. Required inputs.
[ -n "$TAG" ] || die "TAG is required (the image tag / VERSION)"
[ -n "$IMAGE" ] || die "IMAGE is required (the image repository)"

# 3. Cluster alias resolution, before rendering.
# shellcheck source=hack/cluster-alias.sh
source "$SCRIPT_DIR/cluster-alias.sh"
KUBECONFIG_PATH="$(cluster_kubeconfig "$BUILD_TARGET")" || exit 1
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH" -n "$NAMESPACE")

# 4. Default Job name, DNS-1123 sanitized.
if [ -z "${JOB_NAME:-}" ]; then
	JOB_NAME="$(printf '%s' "build-$TAG" | tr 'A-Z' 'a-z' | sed 's/[^a-z0-9.-]/-/g' | cut -c1-63 | sed 's/[-.]*$//')"
fi

# 5. Refuse a duplicate Job, and fail early when the cluster is unreachable.
GET_ERR="$(mktemp)"
trap 'rm -f "$GET_ERR"' EXIT
if ! "${KUBECTL[@]}" get job "$JOB_NAME" >/dev/null 2>"$GET_ERR"; then
	if ! grep -q 'NotFound' "$GET_ERR"; then
		cat "$GET_ERR" >&2
		die "cannot reach cluster '$BUILD_TARGET'"
	fi
else
	die "Job '$JOB_NAME' already exists — delete it with 'kubectl --kubeconfig $KUBECONFIG_PATH -n $NAMESPACE delete job $JOB_NAME' and re-run"
fi

# 6. Render the manifest.
RENDERED="$(mktemp)"
trap 'rm -f "$GET_ERR" "$RENDERED"' EXIT
sed \
	-e "s|__JOB_NAME__|$JOB_NAME|g" \
	-e "s|__REPO_URL__|$REPO_URL|g" \
	-e "s|__REF__|$REF|g" \
	-e "s|__TAG__|$TAG|g" \
	-e "s|__DOCKERFILE__|$DOCKERFILE|g" \
	-e "s|__IMAGE__|$IMAGE|g" \
	-e "s|__PUSH__|$PUSH_SECRET|g" \
	"$SCRIPT_DIR/build-job.yaml" > "$RENDERED"

# 7. Apply.
if ! "${KUBECTL[@]}" apply -f "$RENDERED"; then
	echo "ERROR: failed to create Job '$JOB_NAME'; events:" >&2
	"${KUBECTL[@]}" get events --sort-by=.lastTimestamp >&2 || true
	"${KUBECTL[@]}" describe job "$JOB_NAME" >&2 || true
	exit 1
fi

# 8. Wait for a terminal state (complete or failed) within TIMEOUT seconds.
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
	succeeded="$("${KUBECTL[@]}" get job "$JOB_NAME" -o jsonpath='{.status.succeeded}' 2>/dev/null || true)"
	failed="$("${KUBECTL[@]}" get job "$JOB_NAME" -o jsonpath='{.status.failed}' 2>/dev/null || true)"
	conditions="$("${KUBECTL[@]}" get job "$JOB_NAME" -o jsonpath='{range .status.conditions[*]}{.type}={.status};{end}' 2>/dev/null || true)"
	if [ "$succeeded" = "1" ]; then
		echo "Job '$JOB_NAME' succeeded"
		exit 0
	fi
	if { [ -n "$failed" ] && [ "$failed" != "0" ]; } || printf '%s' "$conditions" | grep -q 'Failed=True'; then
		echo "ERROR: Job '$JOB_NAME' failed; pod log tail:" >&2
		"${KUBECTL[@]}" logs "job/$JOB_NAME" -c build --tail=200 >&2 || true
		"${KUBECTL[@]}" logs "job/$JOB_NAME" -c clone --tail=200 >&2 || true
		"${KUBECTL[@]}" describe job "$JOB_NAME" >&2 || true
		exit 1
	fi
	if [ "$(date +%s)" -ge "$deadline" ]; then
		echo "ERROR: Job '$JOB_NAME' did not reach a terminal state within ${TIMEOUT}s; pod log tail:" >&2
		"${KUBECTL[@]}" logs "job/$JOB_NAME" -c build --tail=200 >&2 || true
		"${KUBECTL[@]}" logs "job/$JOB_NAME" -c clone --tail=200 >&2 || true
		"${KUBECTL[@]}" describe job "$JOB_NAME" >&2 || true
		exit 1
	fi
	sleep 5
done
