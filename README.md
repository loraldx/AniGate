# AniGate

Version: `0.2.0` (`semver`)

[![CI](https://github.com/Lorlds/AniGate/actions/workflows/ci.yml/badge.svg)](https://github.com/Lorlds/AniGate/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/Lorlds/AniGate?label=release)](https://github.com/Lorlds/AniGate/releases/latest)
[![License](https://img.shields.io/badge/license-PolyForm%20Noncommercial-blue)](LICENSE)

[中文 README](README.zh-CN.md)

AniGate is a controlled MCP gateway from ChatGPT Web to remote Linux. It is not
a raw shell and it is not just an agent wrapper: every capability is an
allowlisted, bounded, auditable tool.

## Product Lines

### AniGate Mini: Safe MCP Preview Gateway

`anigate-mini` exposes only read, search, diff, artifact, context, and handoff
tools. It is the recommended default for previewing a Linux workspace from an
MCP client.

Mini tools:

- `policy.info`, `sys.info`, `context.health`
- `fs.list`, `fs.read`, `fs.stat`, `fs.tree`, `file.search`,
  `fs.write_preview`
- `git.status`, `git.diff`, `git.log`, `git.show`
- `artifact.list`, `artifact.read_range`, `artifact.search`, `artifact.stats`
- `handoff.create`, `handoff.resume`, `handoff.search`, `handoff.digest`

### AniGate Max: Controlled Linux MCP Workbench

`anigate-max` exposes the complete controlled workbench: Mini tools plus
execution, mutation, job management, agents, projects, tasks, publishing, audit,
workspace snapshot, and gate diagnostics.

`anigate` remains a legacy alias for Max.

License: AniGate is source-available under the PolyForm Noncommercial License
1.0.0. Noncommercial use is permitted; commercial use requires separate
permission. Because commercial use is restricted, this is not an OSI-approved
open-source license.

## Language Strategy

- Go is the core runtime: single binary, stdlib-first, file-backed state.
- Rust is reserved for future high-performance or stronger-isolation helper
  components such as an indexer or runner.
- TypeScript is reserved for MCP client examples, schemas, demos, and web-side
  tooling. Permission decisions stay in Go.

## Current Tools

Core and policy:

- `policy.info`, `sys.info`, `gate.stats`, `gate.doctor`, `context.health`

Filesystem and search:

- `fs.list`, `fs.read`, `fs.stat`, `fs.tree`, `file.search`,
  `fs.write_preview`, `file.edit_apply`

Artifact and bounded-output handling:

- `artifact.list`, `artifact.read_range`, `artifact.search`, `artifact.stats`

Git and patch:

- `git.status`, `git.diff`, `git.log`, `git.show`, `patch.apply`

Audit, jobs, and presets:

- `audit.events_tail`, `audit.summary`, `app.run_preset`, `job.list`,
  `job.status`, `job.cancel`, `job.logs_tail`

Long-running agent sessions:

- `agent.session_start`, `agent.message_send`, `agent.session_status`,
  `agent.messages_tail`, `agent.session_list`

Project/task/publish:

- `workspace.snapshot`
- `project.list`, `project.ensure`, `project.open`, `project.preflight`,
  `project.snapshot`, `project.lock_status`
- `task.start`, `task.status`, `task.recover`, `task.digest`,
  `task.finish_preview`, `task.commit_preview`, `task.commit`,
  `task.timeline`, `task.search`
- `publish.preview`, `publish.branch`, `publish.pr_create`

Conversation handoff:

- `handoff.create`, `handoff.resume`, `handoff.search`, `handoff.digest`

## Product Enforcement

Mini and Max are product lines, not workspace profile aliases.

- `fs.write_preview` is Mini-safe: it returns a diff and does not write disk.
- `tools/list` only lists tools available in the selected product line.
- `tools/call` applies the product gate before dispatch, so Mini rejects direct
  calls to Max tools such as `file.edit_apply`, `patch.apply`, `agent.*`, and
  `publish.*`.
- Max still uses workspace profile and `read_only` as a second authorization
  layer.
- `file.edit_apply` and `patch.apply` require a non-read-only `operator` or
  `agent` workspace in Max.
- `app.run_preset` requires `operator` or `agent` profile. Preset commands are
  still configured argv arrays; AniGate does not expose arbitrary shell.
- `agent.*` requires `agent` profile in Max.
- `policy.info` reports `product_line`, `product_lines`, and the tools exposed
  by the current product line.

## Design Boundaries

- No arbitrary shell tool.
- File access is limited to configured workspaces.
- Remote Git projects must be declared in the `projects` allowlist.
- Applications run only through named presets in config.
- Agent calls run only through configured argv wrappers.
- Preset and agent env vars are explicit and can be constrained by
  `env_allowlist`.
- Jobs run with isolated `HOME` when `isolated_home` is true.
- Large outputs are written to local artifacts; ChatGPT receives bounded
  previews and follow-up tool names.
- Push and PR creation require `publish.preview` and a short-lived confirmation
  token.
- Direct Web GPT edits are explicit Max actions through `file.edit_apply`;
  Mini remains read/search/preview only.
- Jobs, logs, events, artifacts, tasks, handoffs, and sessions are file-backed;
  no database is required.

AniMonitor remains separate. AniMonitor observes, notifies, digests, and
archives. AniGate reads, invokes, executes, hands off, and can later push task
events to an AniMonitor webhook.

## Quick Start

Install the three binaries and generate Mini, Max, and legacy configs:

```bash
git clone https://github.com/Lorlds/AniGate.git
cd AniGate
./scripts/install.sh
```

The installer writes configs to:

```text
~/.config/anigate/anigate-mini.json
~/.config/anigate/anigate-max.json
~/.config/anigate/anigate.json
```

By default the Mini config exposes a read-only view of your home directory,
while the Max and legacy configs get a dedicated writable workspace at
`~/anigate-workspace` (override with `ANIGATE_MAX_WORKSPACE_DIR`). Widen the
Max workspace deliberately by editing the config.

Run Mini over local HTTP:

```bash
~/.local/bin/anigate-mini http --addr 127.0.0.1:8787 --config ~/.config/anigate/anigate-mini.json
```

Run Max over local HTTP:

```bash
~/.local/bin/anigate-max http --addr 127.0.0.1:8788 --config ~/.config/anigate/anigate-max.json
```

Or run Mini stdio mode for a local MCP client:

```bash
~/.local/bin/anigate-mini stdio --config ~/.config/anigate/anigate-mini.json
```

Developer checkout:

```bash
make verify
make build
./bin/anigate-mini version
./bin/anigate-mini stdio --config configs/anigate.mini.example.json
```

Max HTTP mode:

```bash
./bin/anigate-max http --addr 127.0.0.1:8788 --config configs/anigate.max.example.json
```

Then POST JSON-RPC requests to the selected `/mcp` endpoint. For ChatGPT Web,
expose the MCP endpoint with HTTPS or OpenAI Secure MCP Tunnel.

Useful files for new users:

- `README.zh-CN.md`: Chinese quick start and safety notes.
- `docs/user-quickstart.md`: step-by-step setup.
- `CONTRIBUTING.md`: contribution workflow and local verification.
- `SECURITY.md`: supported versions and vulnerability reporting.
- `examples/mcp-client.mini.stdio.json`: Mini stdio MCP client example.
- `examples/mcp-client.mini.http.json`: Mini HTTP MCP client example.
- `examples/mcp-client.max.stdio.json`: Max stdio MCP client example.
- `examples/mcp-client.max.http.json`: Max HTTP MCP client example.
- `docs/systemd/anigate-mini.service`: optional Mini user-level systemd service.
- `docs/systemd/anigate-max.service`: optional Max user-level systemd service.
- `scripts/verify.sh`: local test/build/smoke verification.
- `scripts/install.sh`: local install and config generation.

Release binaries are built by GitHub Actions for `v*` tags.

## Configuration

Copy `configs/anigate.mini.example.json` or `configs/anigate.max.example.json`
and adjust. `configs/anigate.example.json` is kept as a legacy Max example.

- `workspaces`: allowed roots for `fs.*`, `file.search`, `git.*`, project
  worktrees, and agents.
- `profile`: `reader`, `operator`, or `agent`.
- `auth_token`: optional HTTP bearer token; `ANIGATE_AUTH_TOKEN` also works.
- `env_allowlist`: optional allowlist for preset/agent env keys.
- `isolated_home`: give jobs a private `HOME` under `state_dir`.
- `presets`: named commands and typed args that `app.run_preset` may execute.
- `agents`: named long-running conversation wrappers.
- `projects`: allowlisted remote Git repositories.
- `state_dir`: file-backed jobs, logs, events, artifacts, tasks, handoffs, and
  sessions.

Preset commands are argv arrays and are executed directly with `exec.Command`;
AniGate does not invoke `/bin/sh -c`.

## Long Agent Conversations

For ChatGPT Web to keep a long interaction with the Linux side, AniGate uses
file-backed sessions:

```text
agent.session_start -> session_id
agent.message_send  -> job_id
job.status          -> poll running/done/failed/cancelled
agent.messages_tail -> read durable conversation output
```

Use `task_id` with `agent.session_start` to bind an agent session to a project
task worktree. The session survives process restarts because messages are stored
under `state_dir/agents/messages`.

Example wrappers:

```json
["codex", "exec", "{prompt}"]
["claude", "-p", "{prompt}"]
```

Use `async: true` for long jobs, then poll `job.status` and
`agent.messages_tail`.

## Context Handoff

AniGate cannot see ChatGPT Web's full token count. It estimates context pressure
from AniGate state: tool outputs, artifacts, jobs, tasks, agent transcript
length, and audit events.

When `context.health` returns `yellow` or `red`, call `handoff.create`. It
creates a compact package and a next-chat prompt. In the new chat, call
`handoff.resume` first, then use `handoff.search`, `artifact.search`, or
`task.recover` only when needed. This keeps older work searchable without
dumping all history into the new conversation.

## Project Flow

```text
project.list
project.ensure
task.start
agent.session_start task_id=<task>
task.finish_preview
task.commit_preview
task.commit
publish.preview
publish.branch or publish.pr_create
```

`project.ensure` only clones/fetches configured allowlist remotes. `task.start`
creates a branch/worktree named `anigate/<task_id>-<slug>`. `publish.branch`
and `publish.pr_create` require the token returned by `publish.preview`.
`publish.preview` rejects dirty task worktrees; call `task.commit_preview` and
`task.commit` first.
