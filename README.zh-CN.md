# AniGate

版本：`0.2.0`（`semver`）

[![CI](https://github.com/Lorlds/AniGate/actions/workflows/ci.yml/badge.svg)](https://github.com/Lorlds/AniGate/actions/workflows/ci.yml)
[![Latest Release](https://img.shields.io/github/v/release/Lorlds/AniGate?label=release)](https://github.com/Lorlds/AniGate/releases/latest)
[![License](https://img.shields.io/badge/license-PolyForm%20Noncommercial-blue)](LICENSE)

[English README](README.md)

AniGate 是一个受控 MCP 网关，用来让 ChatGPT Web 或其他 MCP client 安全地使用一台远程 Linux 机器。它不是裸 shell，也不是单纯的 agent wrapper；每个能力都必须是配置允许的、有边界的、可审计的 MCP tool。

许可证：AniGate 使用 PolyForm Noncommercial License 1.0.0。非商业使用允许；商业使用需要单独授权。因为商业使用受限，它不是 OSI 认可的开源许可证。

## 产品线

### AniGate Mini: Safe MCP Preview Gateway

`anigate-mini` 只暴露读取、搜索、diff、artifact、context 和 handoff 工具。它是默认推荐入口，适合让 MCP client 安全预览 Linux workspace。

Mini tools：

- `policy.info`、`sys.info`、`context.health`
- `fs.list`、`fs.read`、`fs.stat`、`fs.tree`、`file.search`、`fs.write_preview`
- `git.status`、`git.diff`、`git.log`、`git.show`
- `artifact.list`、`artifact.read_range`、`artifact.search`、`artifact.stats`
- `handoff.create`、`handoff.resume`、`handoff.search`、`handoff.digest`

### AniGate Max: Controlled Linux MCP Workbench

`anigate-max` 暴露完整受控工作台：Mini 工具，加上执行、编辑、patch、job、agent、project、task、publish、audit、workspace snapshot 和 gate diagnostics。

`anigate` 暂时保留为 Max 的 legacy alias。

## 适合谁

- 想把自己的 Linux 主机接到 MCP client 的用户。
- 想让 ChatGPT 读取文件、看 git 状态、跑固定命令、启动配置好的 agent，但不想开放任意 shell 的用户。
- 想保留审计日志、任务记录、handoff 和长会话状态的用户。

## 五分钟开始

需要 Go 1.22 或更新版本。

```bash
git clone https://github.com/Lorlds/AniGate.git
cd AniGate
./scripts/install.sh
```

安装脚本会生成：

```text
~/.local/bin/anigate-mini
~/.local/bin/anigate-max
~/.local/bin/anigate
~/.config/anigate/anigate-mini.json
~/.config/anigate/anigate-max.json
~/.config/anigate/anigate.json
```

默认配置中，Mini 是家目录的只读视图；Max 与 legacy 配置使用独立的可写工作区
`~/anigate-workspace`（可用 `ANIGATE_MAX_WORKSPACE_DIR` 覆盖）。如需扩大 Max
的工作区范围，请在配置中显式修改。

Mini 本地 HTTP 模式：

```bash
~/.local/bin/anigate-mini http --addr 127.0.0.1:8787 --config ~/.config/anigate/anigate-mini.json
```

Max 本地 HTTP 模式：

```bash
~/.local/bin/anigate-max http --addr 127.0.0.1:8788 --config ~/.config/anigate/anigate-max.json
```

Mini stdio 模式：

```bash
~/.local/bin/anigate-mini stdio --config ~/.config/anigate/anigate-mini.json
```

HTTP MCP 地址：

```text
POST http://127.0.0.1:8787/mcp
```

## MCP Client 配置

Mini stdio 示例见：

```text
examples/mcp-client.mini.stdio.json
```

Mini HTTP 示例见：

```text
examples/mcp-client.mini.http.json
```

Max 示例见 `examples/mcp-client.max.stdio.json` 和 `examples/mcp-client.max.http.json`。把示例里的 `/home/YOUR_USER` 和 `REPLACE_WITH_AUTH_TOKEN` 换成你的真实路径和配置文件里的 `auth_token`。

## 安全模型

AniGate 的默认思路是“只开放明确配置过的能力”：

- 不提供任意 shell tool。
- 文件访问只能在 `workspaces` 配置的目录里。
- Mini 不暴露执行、编辑、agent、publish、job、project、task、audit、workspace snapshot 或 gate diagnostics 工具。
- `tools/list` 只列出当前产品线的工具；`tools/call` 在 dispatch 前做同样的产品线拦截。
- 写入、patch、agent、publish 需要更高 workspace profile。
- HTTP 监听非本机地址时必须设置 `auth_token`。
- preset 和 agent 的环境变量可以用 `env_allowlist` 限制。
- job 默认使用隔离的 `HOME`。
- 大输出会保存成本地 artifact，MCP 返回有界预览。
- push 和 PR 创建需要先调用 `publish.preview`，再用短期确认 token 执行。

## 常用命令

开发者本地验证：

```bash
make verify
```

只构建：

```bash
make build
```

列出所有 MCP tools：

```bash
make tools
```

运行 HTTP：

```bash
make run-http-mini
```

运行 stdio：

```bash
make run-stdio-mini
```

如果 race test 在你的机器上比较慢：

```bash
ANIGATE_SKIP_RACE=1 make verify
```

## 配置文件

推荐先复制示例配置：

```bash
cp configs/anigate.mini.example.json configs/anigate.local.json
```

重点字段：

- `state_dir`：job、日志、artifact、task、handoff、session 的本地状态目录。
- `auth_token`：HTTP bearer token；也可以用 `ANIGATE_AUTH_TOKEN` 环境变量。
- `workspaces`：允许访问的目录。
- `profile`：`reader`、`operator` 或 `agent`。
- `presets`：Max 允许执行的固定命令。
- `agents`：Max 允许启动的 agent wrapper。
- `projects`：Max 允许 AniGate 管理的 git 项目。

不要把包含真实 token 的本地配置提交到 Git。

## systemd 用户服务

安装后可以用用户级 systemd 服务长期运行：

```bash
mkdir -p ~/.config/systemd/user
cp docs/systemd/anigate-mini.service ~/.config/systemd/user/anigate-mini.service
systemctl --user daemon-reload
systemctl --user enable --now anigate-mini.service
```

查看日志：

```bash
journalctl --user -u anigate-mini.service -f
```

## 当前能力

核心和策略：

- `policy.info`、`sys.info`、`gate.stats`、`gate.doctor`、`context.health`

文件和搜索：

- `fs.list`、`fs.read`、`fs.stat`、`fs.tree`、`file.search`
- `fs.write_preview`、`file.edit_apply`

artifact 和大输出：

- `artifact.list`、`artifact.read_range`、`artifact.search`、`artifact.stats`

Git 和 patch：

- `git.status`、`git.diff`、`git.log`、`git.show`、`patch.apply`

审计、job 和 preset：

- `audit.events_tail`、`audit.summary`、`app.run_preset`
- `job.list`、`job.status`、`job.cancel`、`job.logs_tail`

长会话 agent：

- `agent.session_start`、`agent.message_send`、`agent.session_status`
- `agent.messages_tail`、`agent.session_list`

项目、任务和发布：

- `workspace.snapshot`
- `project.list`、`project.ensure`、`project.open`、`project.preflight`
- `project.snapshot`、`project.lock_status`
- `task.start`、`task.status`、`task.recover`、`task.digest`
- `task.finish_preview`、`task.commit_preview`、`task.commit`
- `task.timeline`、`task.search`
- `publish.preview`、`publish.branch`、`publish.pr_create`

handoff：

- `handoff.create`、`handoff.resume`、`handoff.search`、`handoff.digest`

## 产品线拦截

关键实现细节：

- `fs.write_preview` 是 Mini 安全工具：只返回 diff，不写磁盘。
- Mini 和 Max 是产品线，不是 workspace profile 的别名。
- `tools/list` 只列出当前产品线可用工具。
- `tools/call` 在 dispatch 前做产品线拦截，Mini 会拒绝直接调用 Max 工具。
- Max 继续使用 workspace profile 和 `read_only` 做第二层权限控制。
- `file.edit_apply` 和 `patch.apply` 需要非只读的 `operator` 或 `agent`
  workspace，且只在 Max 暴露。
- `app.run_preset` 需要 `operator` 或 `agent` profile。preset 仍然是配置好的
  argv 数组，AniGate 不开放任意 shell。
- `agent.*` 需要 `agent` profile，且只在 Max 暴露。
- `policy.info` 返回 `product_line`、`product_lines` 和当前产品线可用工具。

## 更多说明

- `docs/user-quickstart.md`：普通用户快速开始。
- `docs/design.md`：设计边界和安全模型。
- `docs/openai-mcp-notes.md`：OpenAI MCP 相关笔记。
- `CONTRIBUTING.md`：贡献流程和本地验证。
- `SECURITY.md`：安全问题上报方式。
