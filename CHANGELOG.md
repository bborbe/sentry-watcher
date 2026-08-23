# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.6.0

- feat: gate task emission on docker.io having published the golang image for the new Go version — go.dev lists a release up to a day before the image exists, so the watcher now checks the Docker Hub manifest before emitting and holds the cursor (`image_not_ready`) until the image is available.

## v0.5.3

- update Go to 1.26.6 and update dependencies, fixing GO-2026-5841, GO-2026-5970, GO-2026-6179, GO-2026-6180

## v0.5.2

- Move the built-in default title/body templates into a dedicated `pkg/tasktemplate` package that `//go:embed`s `default-title.md` + `default-body.md` (mirrors `agent-claude/pkg/prompts`). Defaults are now authored as markdown files instead of inline Go string literals; no behavior change.

## v0.5.1

- Make the built-in default task body deployment-agnostic — drop the vault-specific `[[Go - Update Version]]` runbook reference and "bborbe Go repos" wording so the watcher is reusable as-is by anyone. Deployment-specific content (e.g. a runbook link) now comes from `TASK_BODY_TEMPLATE`.

## v0.5.0

- Add `TASK_TITLE_TEMPLATE` and `TASK_BODY_TEMPLATE` envs — Go text/template overrides for the emitted task's title and body (fields: Version, Number, ReleaseKind, PreviousVersion, ReleaseNotesURL, DownloadsURL); empty = built-in bborbe defaults. Lets Quant and Octopus deployments customize task content.

## v0.4.0

- Add `TARGET_VAULT` env (standard task-routing param alongside `TASK_ASSIGNEE`/`TASK_STATUS`/`TASK_PHASE`/`TASK_SUFFIX`): sets the emitted `CreateCommand.TargetVault` (via the sender's default-vault) so the task-controller materializes the task into the named Obsidian vault (e.g. `personal` → `24 Tasks/`). Empty = controller default (openclaw).

## v0.3.0

- Add `SEED_VERSION` env (cold-start seeds a lower version so the first poll
  emits a task for the current latest) and cursor admin endpoints
  `/resetcursor`, `/setcursor/{version}`, `/trigger` (force an immediate poll)
  for operational reset / live end-to-end testing.

## v0.2.0

- Emitted-task assignee/status/phase and an optional title suffix are now
  env-configurable (`TASK_ASSIGNEE`/`TASK_STATUS`/`TASK_PHASE`/`TASK_SUFFIX`),
  defaulting to human/in_progress/todo/none. Enables distinct routing per
  deployment (quant vs octopus).

## v0.1.0

- Initial release. Polls `https://go.dev/dl/?mode=json` on an interval, computes the
  max `stable:true` Go version, and emits one deduplicated `CreateTaskCommand`
  (`task_type: go-version-update`, `assignee: human`) per new version pointing at
  the Go update runbook. Persists a single-value cursor (`LastSeenVersion`) at
  `/data/cursor.json`. Builds and publishes
  `docker.io/bborbe/go-version-watcher:<version>` via `make buca`.
