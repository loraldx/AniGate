# Security Policy

AniGate is designed to expose a controlled MCP gateway, not a raw shell. Security
reports are taken seriously because the project mediates access to local files,
Git repositories, jobs, agents, and publish actions.

## Supported Versions

Only the latest release is actively supported for security fixes.

| Version | Supported |
| --- | --- |
| `0.2.x` | Yes |
| `< 0.2.0` | No |

## Report a Vulnerability

Please do not open a public GitHub issue for suspected vulnerabilities.

Use GitHub private vulnerability reporting if it is available for the
repository. If it is not available, open a minimal public issue asking for a
private security contact without including exploit details.

Useful details to include privately:

- AniGate version and commit.
- Transport used: stdio or HTTP.
- Relevant config shape with secrets removed.
- Exact tool or endpoint involved.
- Expected boundary.
- Observed bypass or failure.
- Minimal reproduction steps.

Do not include secrets, real auth tokens, private remote URLs, cookies, or
private repository content.

## Security Boundaries

Reports are especially useful around:

- Workspace path traversal or symlink escape.
- HTTP auth bypass.
- Leaked credentials in logs, artifacts, errors, or audit events.
- Unbounded output paths.
- Unintended shell execution.
- Patch path validation.
- Publish confirmation token reuse.
- Agent or preset environment leakage.
- Job cancellation that leaves child processes running.

## Disclosure

The preferred process is:

1. Private report.
2. Maintainer acknowledgement.
3. Fix and regression test.
4. Release.
5. Public advisory or changelog entry when appropriate.
