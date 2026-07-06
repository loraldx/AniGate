# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 语言偏好 (Language Preference)

默认使用中文与用户交流（解释、总结、提问均用中文）。代码、标识符、注释、提交信息和仓库文档保持英文，除非用户另有要求。

## What AniGate Is

A controlled MCP gateway from ChatGPT Web to a remote Linux workspace. It is **not** a raw shell and must never expose one: every capability is a narrow, allowlisted, bounded, auditable MCP tool. Go 1.22, stdlib-only (zero dependencies in go.mod — adding one needs clear justification), single binary per product line, file-backed state (no database). The JSON-RPC/MCP layer is hand-rolled (no MCP SDK).

AniMonitor is a separate project: it observes/notifies/digests. AniGate reads/invokes/executes. Integration stays event/webhook-based; do not merge them.

## Commands

```bash
make verify          # THE local gate: mod verify, test, vet, race, build all 3 binaries,
                     # tool-surface checks, HTTP MCP smoke test. Run before PRs.
make build           # builds bin/anigate-mini, bin/anigate-max, bin/anigate
make test            # go test ./...
make race            # go test -race ./...   (ANIGATE_SKIP_RACE=1 skips race in verify.sh)
go test ./internal/anigate -run TestFSRead   # run a single test
make tools           # print product-filtered tool lists for Mini and Max
make run-http-mini   # ./bin/anigate-mini http --addr 127.0.0.1:8787 --config configs/anigate.mini.example.json
make run-stdio-max   # stdio mode with example config
```

Tests shell out to real `git` (and spawn real subprocesses), so `git` must be on PATH. CI (`.github/workflows/ci.yml`, Go 1.22.x) re-runs the verify steps inline (minus the HTTP smoke test). Releases: push a `v*` tag; no version injection at build time.

**Version bumps require editing both** the `VERSION` file and the `Version` const in `internal/anigate/version.go` — `TestVersionFileMatchesConstant` fails otherwise.

## Architecture

### Entry points and dispatch

The three `cmd/*/main.go` files are 3-line mains that call `anigate.RunCLI(os.Args[1:], productLine)` with different `ProductLine` constants. `anigate-mini` → Mini; `anigate-max` and `anigate` (legacy alias, must keep the full Max surface) → Max. Subcommands: `version`, `stdio`, `http`, `tools` (`internal/anigate/cli.go`).

Request flow: `ServeStdio` (`mcp.go`, newline-delimited JSON, 10MB cap) or `ServeHTTP` (`http.go`, POST /mcp, 10MB cap, bearer-token auth; refuses to listen on non-loopback without `auth_token`) → `dispatchJSON` → `Service.CallTool`, a single hand-written switch on tool name in `service.go`. The tool catalog is the static `allTools()` slice with inline JSON schemas.

### Adding a tool — the checklist

A tool handler is `func (s *Service) name(args map[string]any) (map[string]any, error)`; args are decoded with the `stringArg`/`intArgDefault`/`boolArg` helpers (JSON numbers arrive as float64). The schemas in `allTools()` are documentation only — **nothing validates incoming args against them**; handlers re-validate everything.

Adding/removing a tool touches:
1. `allTools()` in `service.go` (schema)
2. the `CallTool` switch in `service.go` (dispatch)
3. `miniToolNames` in `product.go` **if** it belongs in Mini (no map entry = silently Max-only)
4. Three independent hard-coded tool-count/name assertions: `scripts/verify.sh` (21 Mini / 56 Max / 56 legacy), `.github/workflows/ci.yml` (duplicated inline, not derived from verify.sh), and `service_test.go` (`TestMiniProductToolsArePreviewCore` has the exact ordered Mini list; `TestMaxProductToolsRemainComplete` asserts the Max count)

Mini must never expose execution/mutation families: `agent.*`, `publish.*`, `file.edit_apply`, `patch.apply`, `app.run_preset`, `job.*`, `project.*`, `task.*`, `audit.*`, `workspace.snapshot`, `gate.*` (grepped in verify.sh and CI).

### Authorization layers (in order)

1. **HTTP token auth** (HTTP mode only).
2. **Product gate**: `Service.Tools()` filters tools/list for Mini; `requireToolForProduct` runs first in `CallTool`. It deliberately returns nil for *unknown* tool names so the switch produces "unknown tool" instead of a misleading product-line error — keep that ordering.
3. **Workspace profile ladder** (`workspaceAllows` in `policy.go`): profiles are `reader`/`operator`/`agent`; `write` needs `!ReadOnly` AND operator/agent; `agent.*` needs the agent profile. `read_only:true` blocks only the `write` need — presets can still execute. Default profile when omitted in config is `reader`. **Handlers do their own permission checks — there is no central write gate.** A new mutating tool must call `s.workspaceAllows(workspace, "write")` itself or it silently bypasses policy. Note: the preset "operate" check is duplicated inline in `JobManager.RunPreset` (`jobs.go`) — keep it in sync with `workspaceAllows`.
4. **Path confinement**: `pathPolicy.resolve` (`pathpolicy.go`) symlink-resolves and rejects anything escaping the workspace root. Symlinks are only resolved for paths that already exist.

### Error conventions

Tool failures become MCP tool results with `IsError:true` and plain text — **never** JSON-RPC errors. RPC error codes are reserved for protocol failures only (-32700/-32602/-32601). There is no typed error hierarchy. Every `tools/call` is appended to the audit event log, success or failure.

### File-backed state

Everything lives under `state_dir` (default `<root>/.anigate/state`): `jobs/<id>.json`, `logs/<id>.log`, `artifacts/<id>.{json,txt}`, `agents/sessions/` + `agents/messages/*.ndjson`, `tasks/`, `handoffs/`, `publish_tokens/`, `home/` (isolated HOME), and `events.ndjson` (append-only audit stream). No cross-process locking; JSON records use write-to-`.tmp`-then-rename — keep that pattern for new record types. All IDs come from `newJobID()` (timestamp + random hex) and every ID must be gated with `validName()` before being joined into a state_dir path — that is the only path-traversal defense. State files are 0700/0600; workspace files written by `file.edit_apply` are 0644.

Large tool outputs spill to artifacts via `boundedTextArtifact` (`artifact.go`): truncated inline preview + an ArtifactRef with suggested follow-up tools. Size limits are three distinct config knobs clamped at call sites: `MaxReadBytes`, `MaxJobLogBytes` (1 MiB), `MaxArtifactBytes` (4 MiB).

Heads-up: `context.go` contains only `contextWithBackground()`; the actual `context.health`, handoff, and `workspace.snapshot` logic lives in `handoff.go`.

### Subprocess execution

No shell, ever: presets, agents, and git all run as argv arrays via `exec.CommandContext` (git/gh with a 15s timeout). The environment is built **from scratch**: PATH only (plus `HOME=<state_dir>/home` when `isolated_home`, plus literal env pairs from config validated against `env_allowlist` at config-load time, not exec time). Host env is never inherited. Remote URLs are redacted in errors. Async jobs intentionally use `context.Background()` — do not "fix" this to the request context or async agent jobs die when the RPC returns. Job cancellation relies on an in-memory map, so after a restart, `running` job records are orphaned.

### Project/task/publish flow

`project.ensure` (allowlisted remotes only; force-resets `origin` URL to config on every call) → `task.start` (worktree + branch `anigate/<id>-<slug>` under `.anigate-worktrees/`) → `task.commit_preview`/`task.commit` (gated by a diff SHA-256 fingerprint recomputed at commit time) → `publish.preview` (rejects dirty worktrees; issues a single-use 30-min token) → `publish.branch`/`publish.pr_create`. The publish token is deleted **before** the push runs — that is the single-use guarantee; do not move the deletion after. Pushes to default/main/master are refused by name.

### Config

`LoadConfig` order: explicit path > `ANIGATE_CONFIG` > `DefaultConfig(cwd)`; `auth_token` falls back to `ANIGATE_AUTH_TOKEN`. Relative `state_dir`/workspace paths resolve against the **config file's directory**, not cwd (the example configs rely on `"path": ".."`). Validation only runs inside `LoadConfig` — `NewServiceWithProductLine` on a hand-built Config skips all cross-reference checks. Preset arg substitution: `{name}` placeholders, typed args (string/int/bool/string_array); `{prompt}` is reserved for agent commands.

## Tests

One white-box file: `internal/anigate/service_test.go` (package `anigate`). Tests call unexported methods directly with `map[string]any` args rather than going through JSON-RPC (except the `dispatchJSON` tests). `testService(t)` builds a real Max service in `t.TempDir()`; `testServiceWithProduct(t, ProductLineMini)` selects the product line. Tests mutate `svc.cfg.Workspaces` and rebuild `svc.policy` to simulate profiles. Style is scenario functions asserting on result maps (e.g. `got["count"].(int)`), not table-driven. Git tests build real bare remotes + clones.

Add tests for anything touching security boundaries (path confinement, product gating, profiles, publish tokens).

## Docs to keep in sync

User-facing changes → update both `README.md` and `README.zh-CN.md`. Release-worthy changes → `CHANGELOG.md`. `AGENTS.md` and `docs/design.md` carry the product-boundary rules summarized above.
