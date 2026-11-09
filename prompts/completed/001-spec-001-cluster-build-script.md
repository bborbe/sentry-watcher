---
status: completed
spec: [001-cluster-build-arm]
summary: Authored hack/build-cluster.sh plus the missing prompt-1/2 prerequisites (Makefile.docker cluster branch, hack manifest/alias/bootstrap/template, Dockerfile vendor-pin removal, README/CHANGELOG) so all five arm files exist and every container-level verification probe passes; make precommit exited 0.
execution_id: faa09782-fb79-578d-87a3-35492d86541a-exec-001-spec-001-cluster-build-script
dark-factory-version: dev
created: "2026-11-09T20:50:00Z"
queued: "2026-11-09T20:05:51Z"
started: "2026-11-09T20:05:52Z"
completed: "2026-11-09T20:16:10Z"
branch: dark-factory/cluster-build-arm
---

<summary>
- Adds the executable cluster driver that turns the `build` target's cluster branch into a real build: it validates the requested ref, renders the Job manifest, applies it to the build namespace, waits for a terminal state, and reports the result.
- The ref is rejected before any cluster call unless it is a full 40-character SHA that resolves locally, so a bad ref cannot reach the cluster or the clone container.
- A cluster that cannot be reached, a Job that is refused at admission, a failed build, and a timed-out build all exit non-zero and print the pod log tail so the cause is visible without a separate `kubectl` call.
- A pre-existing Job with the same name is refused before applying, naming the Job the operator must delete.
- The script reads its inputs from the environment the `Makefile` already exports, so the Makefile stays the single place that computes the sanitized Job name and the image reference.
- Adds the cluster arm's timeout and reporting behaviour without adding a second executor or any credential handling on the invoking machine.
- Completes the arm: all five files under `hack/` exist after this prompt.
</summary>

<objective>
Author `hack/build-cluster.sh`, the cluster driver referenced by `Makefile.docker`'s cluster branch. It validates `REF` before any cluster interaction, resolves the cluster alias, refuses a duplicate Job, renders `hack/build-job.yaml`, applies it, waits up to a 3600-second constant for a terminal state, and reports success or the pod log tail on failure. This prompt also completes the `hack/` set and runs the container-level cluster verification.
</objective>

<context>
Read `docs/dod.md` for this project's definition of done.

Prerequisites authored by earlier prompts (read them before writing):
- `Makefile.docker` — the cluster branch exports `BUILD_TARGET`, `REF`, `REPO_URL`, `TAG`, `IMAGE`, `DOCKERFILE`, `JOB_NAME` and `PUSH_SECRET` and invokes `bash hack/build-cluster.sh`.
- `hack/build-job.yaml` — the manifest with the six named placeholders plus `__JOB_NAME__`.
- `hack/cluster-alias.sh` — sourceable `cluster_kubeconfig <name>` helper.
- `hack/build-cluster-bootstrap.sh` and `hack/dockerconfig.json.tpl` — one-time bootstrap, not on the build path.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/git-workflow.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-makefile-commands.md`
</context>

<requirements>
1. Create `hack/build-cluster.sh` with exactly this content:

```bash
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
```

2. Do NOT change `Makefile.docker`, `hack/build-job.yaml`, `hack/cluster-alias.sh`, `hack/build-cluster-bootstrap.sh`, `hack/dockerconfig.json.tpl`, `Dockerfile`, `README.md` or `CHANGELOG.md` in this prompt — they are authored by prompts 1 and 2. Only add `hack/build-cluster.sh`.

3. Confirm the `Makefile.docker` cluster branch passes exactly the variables this script reads. If the branch's exported variable names differ (for example `TAG` vs `VERSION`), report the mismatch rather than editing the Makefile: the contract above is the source of truth.

<!--
OPEN QUESTION (surface to the human reviewer at audit time):
The spec's operator rung, not this prompt, proves the real cluster behaviour:
`make build BUILD_TARGET=sun REF=<sha> VERSION=<cluster-scratch-tag>` exits 0, the Job's
`status.succeeded` is 1, the `clone` log contains the SHA, the `build` log contains BuildKit
step lines, the pushed image carries `BUILD_GIT_COMMIT=<short sha>` and an RFC3339
`BUILD_DATE`, and the private registry receives nothing. Those probes need sun access and
registry credentials, which CI does not have, so they stay on the spec's Verification
ladder. This prompt verifies only what a container can reach: script parse, the REF
refusal firing before any kubectl call, and the Makefile dry-run wiring.
-->

<!--
OPEN QUESTION: the wait loop treats a Job condition `Failed=True` as terminal in addition
to `.status.failed`. This covers admission rejections that leave no pod. If the build
namespace's controller reports failures differently, the loop falls back to the 3600s
timeout and still prints the pod log tail and Job description. The reviewer should
confirm the 3600-second constant is the intended upper bound.
-->
</requirements>

<constraints>
- `buca` keeps its current composition — `build upload clean apply`.
- `DOCKER_REGISTRY`, `SERVICE`, `BRANCH` and `VERSION` keep their current definitions and meanings. `VERSION` remains the image tag.
- `check-version-tag` keeps its current message text and its `ALLOW_UNTAGGED_BUILD` escape. `check-go-mod` is unchanged except for its selector wrapper in prompt 1.
- The local recipe's `docker build` arguments must not change: `--pull`, `--no-cache`, `--rm=true`, `--platform=linux/amd64`, `-f Dockerfile .`, the `DOCKER_BUILDKIT=1` prefix, and all three `--build-arg` lines (`DOCKER_REGISTRY`, `BUILD_GIT_COMMIT`, `BUILD_DATE`).
- The cluster timeout is a 3600-second constant inside `hack/build-cluster.sh`.
- The Dockerfile path is the make variable `BUILD_DOCKERFILE ?= Dockerfile`.
- The repository URL is fixed: `https://github.com/bborbe/sentry-watcher.git`. It is not derived from `git remote`, which is unavailable in the build container.
- The Job spec is a reusable, parameterized manifest; no repository name, tag or credential is hardcoded in it.
- `REF` is validated against a local `git rev-parse` and a 40-character shape before rendering, and passed to the clone container as an environment value consumed as `"$REF"`, never interpolated into a shell string.
- The push credential exists only as a cluster secret, mounted into the build pod. It is never written to the invoking machine, never passed as a make variable, and never printed.
- The build container carries no cluster credentials — it cannot create, read or modify other resources.
- The `local` path must work with no kubeconfig and no cluster credentials present.
- No new runtime dependency in the built image; the change is build tooling only.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run from the repo root. The container has no cluster and no `kubectl`, which is exactly what the REF-refusal probe needs.

```bash
# All five arm files exist (completes AC 10)
test -f hack/build-cluster.sh
test -f hack/cluster-alias.sh
test -f hack/build-job.yaml
test -f hack/build-cluster-bootstrap.sh
test -f hack/dockerconfig.json.tpl

# AC 7 (container rung) — REF refusal fires before any kubectl call and names the ref
bash -n hack/build-cluster.sh
if BUILD_TARGET=sun REF=deadbeef REPO_URL=https://github.com/bborbe/sentry-watcher.git TAG=probe IMAGE=docker.io/bborbe/sentry-watcher PUSH_SECRET=buildkit-push bash hack/build-cluster.sh 2>/tmp/ref.err; then
	echo 'expected the script to refuse REF=deadbeef'
	exit 1
fi
grep -q 'deadbeef' /tmp/ref.err

# Cluster wiring — the cluster recipe names the script and skips the local guards
make -n build BUILD_TARGET=sun REF=0123456789abcdef0123456789abcdef01234567 VERSION=probe > /tmp/cluster-recipe.txt
grep -q 'hack/build-cluster.sh' /tmp/cluster-recipe.txt
if grep -q 'refusing to build' /tmp/cluster-recipe.txt; then echo 'cluster recipe must not run check-version-tag'; exit 1; fi
if grep -q 'go mod vendor' /tmp/cluster-recipe.txt; then echo 'cluster recipe must not run check-go-mod'; exit 1; fi

# AC 7 (container rung) — the missing-VERSION guard is present in the cluster recipe
make -n build BUILD_TARGET=sun REF=0123456789abcdef0123456789abcdef01234567 > /tmp/no-version.txt
grep -q 'VERSION is required for BUILD_TARGET=sun' /tmp/no-version.txt

# AC 17 — global gate
make test
make lint
```

The operator-executable rung from the spec (real cluster build, pushed image stamps, private-registry absence, failing-build log tail, missing-VERSION and NotFound probes) runs on the host after merge and is not part of this prompt's verification.
</verification>
