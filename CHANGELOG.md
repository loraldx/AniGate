# Changelog

## Unreleased

- Path confinement now rejects any resolved workspace path inside `state_dir`,
  so a caller cannot reach publish tokens, job/task/artifact records, or the
  audit stream through a workspace tool even when `state_dir` sits inside a
  workspace root (as the shipped configs do). Closes the forge-state-records
  vector behind the artifact arbitrary-read and forgeable publish token.
- Fail closed when a task worktree's cleanliness cannot be verified:
  `publish.preview` now surfaces git failures instead of treating them as a
  clean tree.
- Routed `project.ensure` maintenance commands (`remote set-url`, `fetch`)
  through the sanitized runner so credentialed remote URLs cannot leak into
  error messages.
- Unified all git/gh helper subprocesses behind one runner with sanitized
  errors; clone/fetch/push/gh now get a 120s network timeout instead of the
  15s local bound.
- Bound publish confirm tokens to the worktree HEAD they previewed; commits
  landing after `publish.preview` invalidate the token. Token files are
  written atomically and expired tokens are swept on the next preview.
- Clamped out-of-range limit arguments to their documented maximums instead
  of silently resetting to small defaults; capped `task.search` and
  `handoff.search` results.
- Oversized stdio frames now fail with a protocol error instead of killing
  the server, matching HTTP per-request behavior.
- `file.search` no longer stops matching silently after lines over 64 KiB.
- `validName` rejects dot-only IDs, `audit.summary` reports the newest
  failures, and `fs.write_preview` only treats genuinely missing files as
  creatable.
- HTTP mode shuts down gracefully on SIGINT/SIGTERM and exits 0.
- JSON-RPC conformance: response `id` is always echoed, `resources/list` and
  `prompts/list` return their empty collections, and `initialize` negotiates
  the protocol version.
- CI now runs `scripts/verify.sh` directly; releases verify the tag matches
  `VERSION` and publish sha256 checksums.
- Installer hardening: CSPRNG-only token generation, a dedicated default Max
  workspace directory instead of the whole home directory, and a distinct
  port (8789) for the legacy systemd unit.

## 0.2.0

- Split AniGate into two product lines: `anigate-mini` for safe preview and
  `anigate-max` for the full controlled Linux workbench.
- Kept `anigate` as a legacy alias for Max.
- Added product-line filtering for `tools/list` and product-line gating before
  `tools/call` dispatch.
- Added Mini and Max example configs, MCP client examples, and systemd service
  examples.
- Updated install, verify, CI, and release packaging to build all three
  binaries.
- Replaced the old Mini/Max workspace-level documentation with product-line
  documentation.

## 0.1.3

- Documented the Mini and Max capability levels in English and Chinese README.
- Added `capability_levels` to `policy.info` so clients can inspect the
  Mini/Max workspace-policy mapping.
- Made `fs.write_preview` Mini-safe by authorizing it as read permission; it
  still returns a diff only and does not write disk.
- Added a regression test proving Mini reader workspaces can preview but cannot
  edit, patch, run presets, or start agents.
- Added `go mod verify` to CI.

## 0.1.2

- Added beginner-friendly install and verification scripts.
- Added `Makefile` targets for build, install, test, race, and verification.
- Added Chinese README and a step-by-step user quickstart.
- Added stdio and HTTP MCP client configuration examples.
- Added a user-level systemd service example.
- Added GitHub Actions CI and tag-based release binary publishing.
- Updated the example `sandboxes` workspace path for the new repository
  location so it still points at `/home/lorald/sandboxes`.

## 0.1.1

- Added PolyForm Noncommercial License 1.0.0 and documented AniGate as
  source-available, noncommercial software rather than OSI-approved open source.
- Made publish confirmation tokens one-time use for `publish.branch` and
  `publish.pr_create`.
- Redacted credential-bearing remote URLs from external command error text.
- Removed a full credential-URL-shaped test fixture from source text while
  keeping redaction coverage.

## 0.1.0

- Added Max-only `file.edit_apply` for explicit direct Web GPT single-file
  edits when no configured agent should be used.
- Added `task.commit_preview` and `task.commit` so task worktrees can be
  committed before publish.
- Made `publish.preview` reject dirty task worktrees and point callers to the
  commit flow.
- Added `gate.doctor` and extended `project.preflight` with structured checks.
- Preserved business `next` actions when artifact follow-up actions are added.
- Hardened HTTP startup by requiring auth tokens for non-loopback listeners and
  added stricter server timeouts.
- Tightened new AniGate state, log, artifact, task, handoff, and session file
  permissions.

## 0.0.1.280626

- Added artifact-backed large output handling with `artifact.list`,
  `artifact.read_range`, `artifact.search`, and `artifact.stats`.
- Added bounded output envelopes for file reads, git output, job logs, audit
  tails, and agent message tails.
- Added `context.health` and `handoff.create/resume/search/digest` for
  multi-chat continuation from ChatGPT Web without loading full history.
- Added `workspace.snapshot` and `gate.stats`.
- Added allowlisted remote Git project tools: `project.list`,
  `project.ensure`, `project.open`, `project.preflight`, `project.snapshot`,
  and `project.lock_status`.
- Added file-backed task lifecycle tools: `task.start`, `task.status`,
  `task.recover`, `task.digest`, `task.finish_preview`, `task.timeline`, and
  `task.search`.
- Added guarded publish flow: `publish.preview`, `publish.branch`, and
  `publish.pr_create`.
- Added `env_allowlist`, `isolated_home`, `max_artifact_bytes`, and project
  config.
- Added task-bound agent sessions via `task_id`.
- Documented the Go/Rust/TypeScript language strategy.

## 0.0.1.260626

- Created independent AniGate repo.
- Added Mini MCP gateway with stdio and HTTP `/mcp` transports.
- Added bounded tools: `sys.info`, `fs.list`, `fs.read`, `file.search`,
  `app.run_preset`, `job.status`, and `job.logs_tail`.
- Added read-only `git.status` and `git.diff`.
- Added read-only `audit.events_tail`.
- Added file-backed job logs and event log.
- Added `policy.info`, `fs.stat`, `fs.tree`, `fs.write_preview`.
- Added `git.log`, `git.show`, and guarded `patch.apply`.
- Added HTTP bearer token auth for `/mcp`.
- Added workspace profiles: `reader`, `operator`, `agent`.
- Added typed preset arguments.
- Added `job.list` and in-process `job.cancel`.
- Added `audit.summary`.
- Added file-backed agent sessions for long ChatGPT Web to Linux conversations.
