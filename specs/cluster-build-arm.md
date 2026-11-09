---
status: draft
created: 2026-11-09
---

## Summary

- `make build` gains a `BUILD_TARGET` selector in `Makefile.docker`. `local` — the default — keeps today's `docker build` invocation unchanged; naming a cluster instead renders and applies a Kubernetes Job, waits for it, and reports its result.
- The Job clones the requested git ref, builds `linux/amd64` with BuildKit, and pushes `docker.io/bborbe/sentry-watcher:<VERSION>` using a credential mounted inside the pod.
- The arm lives in this repo's `Makefile.docker`, not `Makefile` — `build:`, `check-version-tag`, `check-go-mod`, `upload` and `clean` all live there, and `Makefile` only includes it.
- `check-version-tag` and `check-go-mod` keep firing for local builds and stop firing for cluster builds, where HEAD's tag is meaningless because the Job clones an arbitrary ref.
- The local path's observable behaviour is unchanged: same tag, same platform, same three build args, image present in the local Docker store, `make upload` still pushes it.

## Problem

Every `bborbe/sentry-watcher` image today is cut by `docker build` on whichever machine invokes `make build`. The service runs on nukeprod and its CronJobs pin `docker.prod.nuke.benjamin-borbe.de:443/bborbe/sentry-watcher:v0.5.0` and `v0.6.0` — so a released image depends on one Mac being awake and its Docker daemon running.

This repo carries two build guards the reference implementation did not. `build:` depends on `check-version-tag`, which refuses unless `HEAD`'s exact tag equals `$(VERSION)`, and `VERSION` defaults to the newest release tag. A naive cluster arm therefore either fails before any Job runs (the guard compares the wrong tree) or silently overwrites a released image (the default resolves to that release). `build:` also depends on `check-go-mod`, which runs `go mod vendor` on the invoking machine — pointless and wrong for a build that happens inside a pod.

## Goal

`make build` with no `BUILD_TARGET` behaves exactly as it does today. `BUILD_TARGET=<cluster> make build REF=<sha> VERSION=<tag>` creates a Job in that cluster, which clones the ref, builds and pushes the image; the make target blocks until the Job reaches a terminal state and exits non-zero if it failed. `make upload` and `make clean` skip their host-side work for a cluster target, because the Job already pushed and no local image exists.

## Non-goals

- Migrating any other repository. One repo, one arm.
- Changing image naming or tagging — `$(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)` stays, and `VERSION` keeps its meaning.
- Pushing to the private registry. `docker.prod.nuke.benjamin-borbe.de:443` is produced by `nuke/sentry-watcher/Makefile`'s `MIRROR_IMAGES` step, not by this build.
- A `CLUSTER_IMAGE` variable. Unlike the pilot, this repo needs none: `DOCKER_REGISTRY ?= docker.io` and there is no `common.env`, so the local arm already composes the Docker Hub reference the cluster arm pushes.
- Build cache export/import. Cold-cache cost is recorded, not solved.
- Loading the built image back to the invoking machine. Rejected, not deferred: it keeps the push credential on the local machine.
- Multi-arch images. `linux/amd64` only, matching the cluster nodes.
- Provisioning cluster namespaces or credentials **on the build path**. The one-time bootstrap helpers `hack/build-cluster-bootstrap.sh` and `hack/dockerconfig.json.tpl` are the documented prerequisite — they are authored and committed, and never invoked by `make build`.
- A second executor (buildx Kubernetes driver, Kaniko). One executor ships.
- Changing `nuke/sentry-watcher`'s `MIRROR_IMAGES` entry or the two CronJob tag pins. A later pin bump is its own change.
- **A committed end-to-end scenario.** The cluster path needs sun access and registry credentials, which CI does not have; the operator-executable rung below is the smoke check instead of a permanently-skipped test.
- **Protecting a released tag from an explicit `VERSION`.** The arm refuses a *defaulted* `VERSION`, but `make build BUILD_TARGET=sun VERSION=v0.6.0` will overwrite that release. Guarding it would need a registry query before every build; recorded here as a known limitation, not implemented.
- **Reproducible digests.** `SOURCE_DATE_EPOCH` pinning and timestamp rewriting are not adopted; two builds of the same ref may differ.
- **A manual version bump on merge.** `.maintainer.yaml` sets `autoRelease: true`, so merging cuts the release tag and the build follows.

## Acceptance Criteria

- [ ] **`BUILD_TARGET` defaults to `local` in `Makefile.docker` and the local invocation is unchanged** — evidence: `grep -c '^BUILD_TARGET ?= local' Makefile.docker` returns `1`, and `make -n build VERSION=<local-scratch-tag>` emits a recipe containing `DOCKER_BUILDKIT=1`, `--pull`, `--no-cache`, `--rm=true`, `--platform=linux/amd64`, all three `--build-arg` flags (`DOCKER_REGISTRY`, `BUILD_GIT_COMMIT`, `BUILD_DATE`), `-f Dockerfile .` and `-t docker.io/bborbe/sentry-watcher:<local-scratch-tag>`. All three build args are asserted because the Dockerfile reads each as an image stamp.
- [ ] **The local path still produces an image in the local Docker store, with no cluster configured** — evidence: state transition — `docker image inspect docker.io/bborbe/sentry-watcher:<local-scratch-tag>` exits non-zero before the run, `KUBECONFIG=/nonexistent make build VERSION=<local-scratch-tag> ALLOW_UNTAGGED_BUILD=1` exits 0, and the same inspect then exits 0 with a non-empty `.Id`. The `KUBECONFIG` override is what proves the local arm never needs a cluster.
- [ ] **`check-version-tag` still fires for a bare local build** — evidence: negative — `git worktree add /tmp/untagged <merge-sha> && make -C /tmp/untagged build VERSION=<local-scratch-tag>` exits non-zero and stderr contains `ERROR: refusing to build <local-scratch-tag> from this tree`. The throwaway worktree is the mechanism, not decoration: `.maintainer.yaml` sets `release.autoRelease: true`, so merging this PR tags `HEAD` and the merge commit is no longer untagged by verification time. `<merge-sha>` is the PR's merge commit, taken from `gh pr view <n> --json mergeCommit --jq .mergeCommit.oid`.
- [ ] **`check-go-mod` does not run for a cluster build** — evidence: negative — `make -n build BUILD_TARGET=sun REF=<sha> VERSION=<cluster-scratch-tag>` contains no `go mod vendor`, while `make -n build VERSION=<local-scratch-tag>` does.
- [ ] **Post-Deploy (Rung-2): a cluster target builds in the cluster, not on the host** — evidence: `make build BUILD_TARGET=sun REF=<sha> VERSION=<cluster-scratch-tag>` exits 0 with no `refusing to build` line; `kubectlsun -n build get job build-<cluster-scratch-tag> -o jsonpath='{.status.succeeded}'` returns `1`; `kubectlsun -n build logs job/build-<cluster-scratch-tag> -c clone | grep -c '<sha>'` returns ≥ 1 (the init container cloned that ref); and `kubectlsun -n build logs job/build-<cluster-scratch-tag> -c build | grep -cE '^#[0-9]+ \[internal\] '` returns ≥ 1 (BuildKit step lines exist only when a build actually ran in that pod). The Job is created by this command, so no deploy-freshness gate applies. The last two probes are what separate a real cluster build from a host build with a decorative Job.
  - `deploy_check:` `kubectlsun -n build get secret buildkit-push -o jsonpath='{.type}'`
  - `deploy_target:` `kubernetes.io/dockerconfigjson`
- [ ] **Post-Deploy (Rung-2): the pushed image is the one built from `REF` and it exists only in the registry** — evidence: `crane config docker.io/bborbe/sentry-watcher:<cluster-scratch-tag> | jq -r '.config.Env[]'` shows `BUILD_GIT_COMMIT=` the short SHA of `REF` and `BUILD_DATE=` matching `^[0-9]{4}-[0-9]{2}-[0-9]{2}T`; `crane digest docker.io/bborbe/sentry-watcher:<cluster-scratch-tag>` returns a `sha256:` digest; and `docker image inspect` on that reference exits non-zero on this Mac with `No such image`. The `BUILD_DATE` shape matters because the Dockerfile default is the literal `unknown`, which the binary cannot parse.
  - `deploy_check:` `kubectlsun -n build get secret buildkit-push -o jsonpath='{.type}'`
  - `deploy_target:` `kubernetes.io/dockerconfigjson`
- [ ] **The cluster arm refuses a missing `VERSION` and an unresolvable `REF` without creating a Job** — evidence: negative, three probes across both rungs. Container rung, no cluster needed: `bash hack/build-cluster.sh` invoked with `REF=deadbeef BUILD_TARGET=sun REPO_URL=https://github.com/bborbe/sentry-watcher.git TAG=probe IMAGE=docker.io/bborbe/sentry-watcher PUSH_SECRET=buildkit-push` exits non-zero and its stderr names the ref, because `deadbeef` is not a resolvable full 40-character SHA — the refusal fires before any `kubectl` call. Operator rung: `make build BUILD_TARGET=sun REF=<sha>` with no `VERSION` exits non-zero and stderr names `VERSION`; and neither refusal creates a Job — `kubectlsun -n build get job build-<cluster-scratch-tag>` exits non-zero with `NotFound`. No Post-Deploy marker: the third probe asserts the *absence* of a resource this run never creates, so there is no deployment whose freshness could be stale.
- [ ] **Post-Deploy (Rung-2): a failing build surfaces the pod log tail** — evidence: `make build BUILD_TARGET=sun REF=<sha> VERSION=<cluster-scratch-tag> BUILD_DOCKERFILE=Dockerfile.missing` exits non-zero and its stdout contains a line from the pod log naming the missing file, without a separate `kubectl` call.
  - `deploy_check:` `kubectlsun -n build get secret buildkit-push -o jsonpath='{.type}'`
  - `deploy_target:` `kubernetes.io/dockerconfigjson`
- [ ] **`make upload` and `make clean` are no-ops for a cluster target** — evidence: negative — `make -n upload BUILD_TARGET=sun VERSION=<cluster-scratch-tag>` contains no `docker push`, `make -n clean BUILD_TARGET=sun VERSION=<cluster-scratch-tag>` contains no `docker rmi`, `docker builder prune`, `docker image prune` or `rm -rf vendor`, and both targets exit 0 for real.
- [ ] **The arm files exist in `hack/`, the Job spec is parameterized, and the credential arrives by remap** — evidence: `ls hack/` lists `build-cluster.sh`, `cluster-alias.sh`, `build-job.yaml`, `build-cluster-bootstrap.sh` and `dockerconfig.json.tpl`; `grep -c '^kind: Job' hack/build-job.yaml` returns `1`; `grep -ohE '__REPO_URL__|__REF__|__TAG__|__DOCKERFILE__|__IMAGE__|__PUSH__' hack/build-job.yaml | sort -u | wc -l` returns `6`; `grep -c '__PUSH__' hack/build-job.yaml` returns ≥ 1; and `grep -cF '.dockerconfigjson' hack/build-job.yaml` returns ≥ 1 while `grep -c 'auths' hack/build-job.yaml` returns `0` — the credential is a secret reference with an `items:` remap, never an inline value.
- [ ] **`IMAGE` is defined and resolves to the reference the local arm tags** — evidence: `grep -cE '^IMAGE ?[:?]?=' Makefile.docker` returns `1`, and `make -n build BUILD_TARGET=sun REF=<sha> VERSION=probe | grep -c 'docker.io/bborbe/sentry-watcher'` returns ≥ 1 — the rendered recipe carries the full reference, whatever argument spelling the implementation chose. This repo defines `SERVICE`, not `IMAGE`, and a verbatim copy of the reference script dies at its `require IMAGE` before rendering — but `IMAGE = bogus` would satisfy an assignment-existence probe while pushing the wrong name.
- [ ] **The Dockerfile no longer pins `-mod=vendor`** — evidence: negative — `grep -c 'mod=vendor' Dockerfile` returns `0`, and `grep -c 'go build' Dockerfile` returns `3` (all three binaries still build). `vendor/` is gitignored, so a bare cluster clone has none and an explicit vendor-mode build fails before it starts; Go selects `vendor/` automatically when it is present, so the local arm is unaffected.
- [ ] **Post-Deploy (Rung-2): the build does not push to the private registry** — evidence: negative — `kubectlsun -n build logs job/build-<cluster-scratch-tag> | grep -c 'docker.prod.nuke.benjamin-borbe.de'` returns `0`, and `crane digest docker.prod.nuke.benjamin-borbe.de:443/bborbe/sentry-watcher:<cluster-scratch-tag>` fails with `MANIFEST_UNKNOWN` (not an auth error) for a tag only this arm produced.
  - `deploy_check:` `kubectlsun -n build get secret buildkit-push -o jsonpath='{.type}'`
  - `deploy_target:` `kubernetes.io/dockerconfigjson`
- [ ] **The dark-factory config and its validation prompt are committed on the branch** — evidence: `git ls-tree -r --name-only HEAD` lists `.dark-factory.yaml` and `docs/dod.md` (committed, not merely present in the working tree), `test -s .dark-factory.yaml` exits 0, and `.dark-factory.yaml` carries each property DB 12 names — each of these returns `1`:
  - `grep -c '^pr: false' .dark-factory.yaml`
  - `grep -c '^workflow: direct' .dark-factory.yaml`
  - `grep -c '^hideGit: true' .dark-factory.yaml`
  - `grep -c '^validationCommand: "make test lint"' .dark-factory.yaml`
  - `grep -c '^validationPrompt: docs/dod.md' .dark-factory.yaml`
  - `grep -c '^autoRelease: false' .dark-factory.yaml`
  - `grep -c '^  GOFLAGS: "-buildvcs=false"' .dark-factory.yaml`
  - `grep -c '^  ROOTDIR: /workspace' .dark-factory.yaml`
  - `grep -c '^  GOPATH: /home/node/go' .dark-factory.yaml`
  - `grep -c '^  GOCACHE: /home/node/.cache/go-build' .dark-factory.yaml`
  - `grep -c '^  GOLANGCI_LINT_CACHE: /home/node/.cache/golangci-lint' .dark-factory.yaml`
  - `grep -c '^extraMounts:' .dark-factory.yaml` returns `1` **and** `grep -c '^  - src:' .dark-factory.yaml` returns `3` (an empty `extraMounts:` list satisfies a bare key grep but mounts nothing)

  and `grep -c '^## ' docs/dod.md` returns ≥ 4, so a one-byte file does not pass. `master` carries neither file; the goal's spec-route rule puts a `sentry-*`/`*-watcher` repo that lacks the config on the spec route only once the PR adds it. The `ROOTDIR` and `GOFLAGS` entries are load-bearing, not hygiene: `Makefile.precommit:1` resolves `ROOTDIR` with `git rev-parse --show-toplevel`, and `hideGit: true` masks `.git` inside the container, so without `ROOTDIR=/workspace` the `include $(ROOTDIR)/tools.env` at `:5` aborts make at parse time. `validationCommand` is what makes AC 17's `make test`/`make lint` authoritative instead of advisory.
- [ ] **`README.md` and `CHANGELOG.md` carry the new usage** — evidence: `grep -c 'BUILD_TARGET' README.md` returns ≥ 1 and `grep -c 'REF=' README.md` returns ≥ 1 (a `## Building` section naming all three inputs), and `awk '/^## Unreleased/{f=1;next} /^## /{f=0} f' CHANGELOG.md | grep -c 'cluster'` returns ≥ 1. Both are mandated by the `docs/dod.md` this spec ships; without the CHANGELOG entry the merge-time releaser has nothing to cut, since `.maintainer.yaml` sets `release.autoRelease: true`.
- [ ] **The Job name is DNS-1123 sanitized** — evidence: `make -n build BUILD_TARGET=sun REF=<sha> VERSION=v0.6.4-arm_test` renders `build-v0.6.4-arm-test` (underscore replaced, nothing else changed), and `make -n build BUILD_TARGET=sun REF=<sha> VERSION=$(printf 'a%.0s' {1..56})-$(printf 'b%.0s' {1..20})` renders a Job name of exactly 63 characters ending in `b`, with the `-` that lands on position 63 stripped. The underscore case exercises the substitution rule and this case the truncation-and-strip rule — 70 identical characters would truncate to a run of `v`s and satisfy "no trailing `-`" vacuously.
- [ ] **`make test` and `make lint` pass** — evidence: exit code 0 for both. (Not `make precommit`: its `check` target pulls trivy / osv-scanner / vulncheck over the network and asserts nothing about this change.)

## Verification

**Placeholder bindings** — every probe below uses one of these, and they are bound once per verification run, not invented per AC:

- `<sha>` — a full 40-character SHA on `origin/master` after the merge: `git rev-parse origin/master`.
- `<local-scratch-tag>` — `armlocal-<YYYYMMDD>-<sha7>`, which no prior build has produced, so AC 2's pre-build inspect failure is genuine.
- `<cluster-scratch-tag>` — `v0.6.4-arm-<YYYYMMDD>-<sha7>`, which satisfies `^[A-Za-z0-9._-]+$` and therefore sanitizes to itself as a Job name. AC 15 exercises the underscore case separately with `v0.6.4-arm_test`.
- `<merge-sha>` — `gh pr view <n> --json mergeCommit --jq .mergeCommit.oid`, where `<n>` is this PR's number.

### Container-executable (runs inside the YOLO container at prompt time)

- `make test` — the unit and integration suite exits 0
- `make lint` — exits 0
- `bash -n hack/build-cluster.sh hack/cluster-alias.sh hack/build-cluster-bootstrap.sh` — the new shell parses (the Go lint does not cover it)
- `grep -c '^BUILD_TARGET ?= local' Makefile.docker` — returns `1`
- `grep -cE '^IMAGE ?[:?]?=' Makefile.docker` — returns `1`
- `ls hack/` — lists all five names (asserted by `test -f` per name, not by a count, so a later README or fixture in `hack/` does not break the probe)
- `grep -c '^kind: Job' hack/build-job.yaml` — returns `1`
- `grep -c 'mod=vendor' Dockerfile` — returns `0`
- `grep -c 'go build' Dockerfile` — returns `3`
- `test -s .dark-factory.yaml && test -s docs/dod.md` — exits 0
- `make -n build VERSION=probe` — the default recipe still emits the unchanged `docker build` arguments, including all three `--build-arg` lines
- `make -n build BUILD_TARGET=sun REF=<sha> VERSION=probe` — the cluster recipe names `hack/build-cluster.sh`, contains no `refusing to build`, and contains no `go mod vendor`
- `make -n build BUILD_TARGET=sun REF=<sha> VERSION=v0.6.4-arm_test` — renders the Job name `build-v0.6.4-arm-test`
- `make -n upload BUILD_TARGET=sun VERSION=probe` — contains no `docker push`
- `make -n clean BUILD_TARGET=sun VERSION=probe` — contains no `docker rmi`, `docker builder prune`, `docker image prune` or `rm -rf vendor`

### Operator-executable (runs on the host after PR merge)

- `make build VERSION=<local-scratch-tag>` — refused by `check-version-tag` on an untagged tree
- `KUBECONFIG=/nonexistent make build VERSION=<local-scratch-tag> ALLOW_UNTAGGED_BUILD=1` — the local path still lands an image with no cluster configured
- `make build BUILD_TARGET=sun REF=<sha> VERSION=<cluster-scratch-tag>` — a cluster Job runs to completion
- `kubectlsun -n build get job build-<cluster-scratch-tag> -o jsonpath='{.status.succeeded}'` — returns `1`
- `kubectlsun -n build logs job/build-<cluster-scratch-tag> -c clone | grep -c <sha>` — returns ≥ 1
- `kubectlsun -n build logs job/build-<cluster-scratch-tag> -c build | grep -cE '^#[0-9]+ \[internal\] '` — returns ≥ 1
- `crane config docker.io/bborbe/sentry-watcher:<cluster-scratch-tag> | jq -r '.config.Env[]'` — shows `BUILD_GIT_COMMIT=<short sha>` and an RFC3339 `BUILD_DATE`
- `crane digest docker.io/bborbe/sentry-watcher:<cluster-scratch-tag>` — returns the digest the Job pushed
- `make build BUILD_TARGET=sun REF=<sha> VERSION=<cluster-scratch-tag> BUILD_DOCKERFILE=Dockerfile.missing` — exits non-zero with the pod log tail inline

## Desired Behavior

1. `make build` with no `BUILD_TARGET`, or `BUILD_TARGET=local`, runs the existing `docker build` recipe and leaves the image in the local Docker store under `$(DOCKER_REGISTRY)/bborbe/$(SERVICE):$(VERSION)`.
2. `BUILD_TARGET=<cluster>` renders `hack/build-job.yaml` with the run's values, applies it to the cluster's `build` namespace, and blocks until the Job reaches a terminal state.
3. The make target exits 0 when the Job succeeded and non-zero when it failed, printing the Job's pod log tail in the failure case so the cause is visible without a separate `kubectl` call.
4. `hack/build-cluster.sh` validates `REF` before rendering — it must match `^[A-Za-z0-9._/-]+$`, be exactly 40 characters, and resolve locally via `git rev-parse` — and fails with a message naming the ref otherwise. Validation lives in the script, not in the `Makefile`, so `make -n` stays dry and works in a container where `.git` is masked. The Job's init container clones exactly that ref, never the default branch.
5. The build container builds `linux/amd64` from `Dockerfile` and pushes `docker.io/bborbe/sentry-watcher:<VERSION>` with the same two stamps the local recipe passes — `BUILD_GIT_COMMIT` (the short SHA of `REF`) and `BUILD_DATE` (RFC3339) — because the Dockerfile defaults them to `none` / `unknown` and `BUILD_DATE=unknown` cannot be parsed by the binary. The clone container derives `BUILD_DATE` from the current UTC time, matching the local recipe's shape.
6. The push credential is read from a mounted secret presented as `config.json` under `DOCKER_CONFIG`; it is never read from the invoking machine.
7. `BUILD_TARGET=local` never touches a cluster: no Job is created, no cluster context is required, and the target works on a machine with no kubeconfig.
8. `check-version-tag` and `check-go-mod` run only for `BUILD_TARGET=local`; the cluster arm instead refuses a `VERSION` that came from the Makefile default, so a bare cluster build cannot overwrite the newest release tag.
9. `upload` and `clean` skip their host-side work for a non-`local` target and exit 0.
10. The Job is named `build-<VERSION>` after DNS-1123 sanitization — lowercased, everything outside `[a-z0-9.-]` replaced with `-`, truncated to 63 characters, trailing `-` and `.` stripped — so `build-v0.6.4-arm_test` becomes `build-v0.6.4-arm-test`. The name is computed in the `Makefile` as a `$(shell …)` expression and passed to the script, so `make -n` renders the sanitized name rather than the raw tag.
11. `IMAGE` resolves to `$(DOCKER_REGISTRY)/bborbe/$(SERVICE)` — the same string the local `-t` uses — so the Job pushes exactly the reference the local arm tags and `make upload` pushes.
12. `.dark-factory.yaml` and `docs/dod.md` exist on the branch: `pr: false`, `workflow: direct`, `hideGit: true`, `validationCommand: "make test lint"`, `validationPrompt: docs/dod.md`, `autoRelease: false`, the Go `env` (`GOPATH`, `GOCACHE`, `GOLANGCI_LINT_CACHE`, `GOFLAGS: -buildvcs=false`, `ROOTDIR: /workspace`) and `extraMounts`, and a DoD matching this repo's conventions.
13. `README.md` gains a `## Building` section naming `BUILD_TARGET`, `REF` and `VERSION`, and `CHANGELOG.md` carries an `## Unreleased` entry — the two deliverables this spec's own `docs/dod.md` mandates.

## Constraints

- `buca` keeps its current composition — `build upload clean apply`.
- `DOCKER_REGISTRY`, `SERVICE`, `BRANCH` and `VERSION` keep their current definitions and meanings. `VERSION` remains the image tag.
- The `local` recipe's `docker build` arguments must not change: `--pull`, `--no-cache`, `--rm=true`, `--platform=linux/amd64`, `-f Dockerfile .`, the `DOCKER_BUILDKIT=1` prefix, and all three `--build-arg` lines (`DOCKER_REGISTRY`, `BUILD_GIT_COMMIT`, `BUILD_DATE`).
- `check-version-tag` keeps its current message text and its `ALLOW_UNTAGGED_BUILD` escape; the change is the `ifeq ($(BUILD_TARGET),local)` wrapper only. `check-go-mod` gets the same wrapper.
- The three `go build` lines in `Dockerfile` lose `-mod=vendor` and nothing else; the binaries (`/main`, `/heartbeat`, `/check-heartbeat`), the base images (`golang:1.27.3`, `alpine:3.23`) and the `ENV` stamps are unchanged. The Go base image is **`1.27.3`** — a Constraint naming the wrong patch level is how a toolchain silently downgrades, since no AC reads the base image.
- The builder is `moby/buildkit:v0.34.1-rootless`, digest-pinned in `hack/build-job.yaml` exactly as the reference arm pins it. The Job sets `backoffLimit: 0` and `restartPolicy: Never`, so a failed attempt leaves exactly one pod and `kubectl logs job/<name> -c <container>` keeps working; a retry would leave two pods and break every log probe in this spec. The Job's init container is named **`clone`** and its build container **`build`** — every log probe in this spec selects by those two names, so an implementation using `git` / `buildkit` satisfies every Desired Behavior and fails four Acceptance Criteria.
- `hack/build-job.yaml` must not live under `k8s/`, so a future yaml-sweeping apply target cannot adopt it as deploy state.
- `hack/build-cluster-bootstrap.sh` and `hack/dockerconfig.json.tpl` are one-time bootstrap helpers: they create the `build` namespace and the `buildkit-push` secret. They are not on the build path — the runtime credential arrives through the Job's `items:` remap. The template renders TeamVault placeholders into `{"auths":{"https://index.docker.io/v1/":{…}}}`; no credential value is ever committed, and the template is never applied during a build.
- The arm's shape is taken from `bborbe/go-skeleton` `hack/` and `bborbe/agent-task-executor` `hack/` + `Makefile.docker`. The implementing container cannot reach either repository, so the files are authored from this spec's contract, not copied.
- The repository URL is fixed: `https://github.com/bborbe/sentry-watcher.git`. It is not derived from `git remote`, which is unavailable in the build container.
- The cluster timeout is a 3600-second constant inside `hack/build-cluster.sh`. The Dockerfile path is a make variable, `BUILD_DOCKERFILE ?= Dockerfile`.
- The `local` path must work with no kubeconfig and no cluster credentials present.
- No new runtime dependency in the built image; the change is build tooling only.

## Prerequisites

Named so the operator can check them before the first cluster run:

- `~/.kube/sun` — the context `hack/cluster-alias.sh` maps the alias `sun` onto.
- The `build` namespace on that cluster.
- A `kubernetes.io/dockerconfigjson` secret named `buildkit-push` in `build`, authenticating against `docker.io` only.
- `crane` on the invoking host, for the digest and config probes, authenticated against `docker.io` (`crane auth login docker.io`) — the probes read `docker.io/bborbe/sentry-watcher`, and an unauthenticated `crane digest` fails identically whether the tag is absent or the credential is missing.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| `BUILD_TARGET` names an unknown cluster | The make target fails before rendering, listing the accepted values | Rerun with a valid target |
| The cluster is unreachable | The make target fails with the connection error; no partial Job is left | Rerun when the cluster is available |
| `REF` does not resolve, or is shorter than 40 characters | The make target fails before rendering, naming the ref | Re-run with a full 40-character SHA that exists on the remote |
| `VERSION` is omitted on a cluster build | The make target refuses before rendering, naming `VERSION` as the missing input | Re-run with an explicit `VERSION` |
| The Job is rejected at admission (pod-security, RBAC) | The make target prints the Job's events and exits non-zero | Fix the namespace policy or the Job's security context |
| The registry push is refused (401) | The Job fails at the push; its log carries the registry error | Verify the secret's `items:` remap; recovery is confirmed by a re-run whose Job log shows `pushing manifest` and whose `crane digest` resolves |
| The registry throttles (429) | The Job fails at the push; its log carries the status code | Re-run after the window; the tag did not land, so nothing is overwritten |
| The build container is OOMKilled or evicted for ephemeral storage | The Job fails with `OOMKilled` / `Evicted` in its pod status | Raise the Job's memory or `emptyDir` size, then re-run; the recovery is confirmed by `succeeded=1` |
| The Job exceeds the 3600-second timeout | The make target fails and names the timeout it waited | Raise the constant in `hack/build-cluster.sh`, re-run, and confirm `succeeded=1` |
| `make build` is interrupted mid-wait | The Job continues in the cluster; a later `kubectl get job build-<VERSION>` reports its state | Read the Job's state, or delete it with `kubectlsun -n build delete job build-<VERSION>` and re-run |
| A previous Job with the same name exists | The make target fails before applying, naming the existing Job | `kubectlsun -n build delete job build-<VERSION>`, re-run, confirm `succeeded=1` |
| The pod's clock is skewed | `BUILD_DATE` is well-formed but wrong, so the image ships with a misleading stamp and nothing fails | Rebuild from a node with a synced clock; AC 6 checks only the RFC3339 *shape*, so it cannot catch a wrong value |
| The pinned builder image is no longer pullable | The Job fails at init with `ErrImagePull`, before any build step | Re-pin the digest in `hack/build-job.yaml` to a pullable `moby/buildkit` release, re-run, confirm `succeeded=1` |
| The Dockerfile still pins `-mod=vendor` and the clone has no `vendor/` | The build fails at the first `go build` with a vendor error | Confirm `grep -c 'mod=vendor' Dockerfile` returns `0`; recovery is a re-run whose build log reaches a `containerimage.digest` line |

## Security / Abuse

- The push credential exists only as a cluster secret, mounted into the build pod. It is never written to the invoking machine, never passed as a make variable, and never printed.
- The `items:` remap is load-bearing: without it the secret file keeps its own name while Docker looks for `config.json`, and auth fails silently in the same shape a missing credential produces. This is why `dockerconfig.json.tpl` is bootstrap-only — a template on the build path would put the credential through a rendering step.
- The build container carries no cluster credentials — it cannot create, read or modify other resources.
- `REF` is validated against a local `git rev-parse` and a 40-character shape before rendering, and passed as a clone argument rather than interpolated into a shell string, so a crafted ref cannot execute commands in the init container.
- The Job spec is a reusable, parameterized manifest; no repository name, tag or credential is hardcoded in it.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Rung | Depends on |
|---|---|---|---|---|---|
| 1 | `Makefile.docker` — the `BUILD_TARGET` selector, the `IMAGE` mapping, the `check-version-tag` / `check-go-mod` wraps, the missing-`VERSION` refusal, the `upload` / `clean` skips, and the Job-name sanitization | 1, 7, 8, 9, 10, 11 | 1, 2, 3, 4, 9, 11, 16 | container (1, 4, 9, 11, 16) + operator (2, 3) | — |
| 2 | `hack/` — author the five arm files from the contract in DBs 4–6 and Constraints; the implementing container cannot reach another repository, so the scripts and manifest are written from the spec, not copied. Includes the `Dockerfile` `-mod=vendor` removal, the `.dark-factory.yaml` / `docs/dod.md` additions, and the README / CHANGELOG entries | 4, 5, 6, 12, 13 | 10, 12, 14, 15 | container | — |
| 3 | The cluster branch of `build`: validate `REF`, render, apply, wait, report | 2, 3, 4, 5 | 5, 6, 7, 8, 13 | container (7's script-level `REF` refusal) + operator (5, 6, 7's `VERSION` refusal and `NotFound`, 8, 13) | prompts 1, 2 |
| — | Global gate, applied after every prompt | — | 17 | container | all |

Rationale: prompt 1 changes only the local recipe and the guards and is independently verifiable; prompt 2 authors static manifests, scripts and docs from the stated contract; prompt 3 is the only prompt that shells out to a cluster and needs both prior artifacts. ACs marked operator in prompts 1–3 are verified on the host after merge, not inside the container — the container inherits only the container rung.

**Size exception, recorded deliberately:** 13 Desired Behaviors × 17 Acceptance Criteria is 221, above the 50-cell guidance. Most of the ACs are negative probes on one contract (refusals, skips, absences) rather than independent surface, and the three-prompt decomposition keeps each prompt independently verifiable — the reference arm this repo follows ships as a single spec of comparable size.

## Do-Nothing Option

Every `sentry-watcher` release keeps requiring an awake Mac with a running Docker daemon, and the repo's two build guards stay untested against a cluster arm — so the next repo that follows the pattern inherits the same trap. The migration itself stays unproven for this component.
