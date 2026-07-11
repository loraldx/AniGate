#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PREFIX=${PREFIX:-"$HOME/.local"}
BINDIR=${BINDIR:-"$PREFIX/bin"}
CONFIG_DIR=${ANIGATE_CONFIG_DIR:-"${XDG_CONFIG_HOME:-"$HOME/.config"}/anigate"}
STATE_DIR=${ANIGATE_STATE_DIR:-"${XDG_STATE_HOME:-"$HOME/.local/state"}/anigate"}
MINI_CONFIG_FILE=${ANIGATE_MINI_CONFIG_FILE:-"$CONFIG_DIR/anigate-mini.json"}
MAX_CONFIG_FILE=${ANIGATE_MAX_CONFIG_FILE:-"$CONFIG_DIR/anigate-max.json"}
LEGACY_CONFIG_FILE=${ANIGATE_CONFIG_FILE:-"$CONFIG_DIR/anigate.json"}
WORKSPACE_DIR=${ANIGATE_WORKSPACE_DIR:-"$HOME"}
# Max gets write+agent access, so its default workspace is a dedicated
# directory instead of the whole home directory. Widen deliberately.
MAX_WORKSPACE_DIR=${ANIGATE_MAX_WORKSPACE_DIR:-"$HOME/anigate-workspace"}
TOKEN=${ANIGATE_AUTH_TOKEN:-}

if [ -z "$TOKEN" ]; then
  if command -v openssl >/dev/null 2>&1; then
    TOKEN=$(openssl rand -hex 24)
  elif [ -r /dev/urandom ]; then
    TOKEN=$(od -vN 24 -An -tx1 /dev/urandom | tr -d ' \n')
  else
    echo "error: no CSPRNG available (need openssl or /dev/urandom); set ANIGATE_AUTH_TOKEN explicitly" >&2
    exit 1
  fi
fi

write_mini_config() {
  file=$1
  state=$2
  cat >"$file" <<EOF
{
  "state_dir": "$state",
  "auth_token": "$TOKEN",
  "max_read_bytes": 65536,
  "max_search_file_bytes": 262144,
  "max_search_results": 50,
  "max_job_log_bytes": 1048576,
  "max_artifact_bytes": 4194304,
  "env_allowlist": [],
  "isolated_home": true,
  "workspaces": [
    {
      "name": "home",
      "path": "$WORKSPACE_DIR",
      "read_only": true,
      "profile": "reader"
    }
  ],
  "projects": [],
  "presets": [],
  "agents": []
}
EOF
}

write_max_config() {
  file=$1
  state=$2
  cat >"$file" <<EOF
{
  "state_dir": "$state",
  "auth_token": "$TOKEN",
  "max_read_bytes": 65536,
  "max_search_file_bytes": 262144,
  "max_search_results": 50,
  "max_job_log_bytes": 1048576,
  "max_artifact_bytes": 4194304,
  "env_allowlist": ["OPENAI_API_KEY", "ANTHROPIC_API_KEY"],
  "isolated_home": true,
  "workspaces": [
    {
      "name": "workspace",
      "path": "$MAX_WORKSPACE_DIR",
      "read_only": false,
      "profile": "agent"
    }
  ],
  "presets": [
    {
      "name": "sys_uptime",
      "description": "Show system uptime",
      "workspace": "workspace",
      "cwd": ".",
      "command": ["uptime"],
      "timeout_sec": 10,
      "async": false
    }
  ],
  "agents": [
    {
      "name": "echo_agent",
      "description": "Development placeholder that echoes the session prompt",
      "provider": "echo",
      "workspace": "workspace",
      "cwd": ".",
      "command": ["printf", "{prompt}"],
      "timeout_sec": 30,
      "max_history_messages": 20
    }
  ],
  "projects": []
}
EOF
}

mkdir -p "$BINDIR" "$CONFIG_DIR" "$STATE_DIR" "$MAX_WORKSPACE_DIR"

cd "$ROOT"
for name in anigate-mini anigate-max anigate; do
  echo "==> building $BINDIR/$name"
  go build -trimpath -ldflags "-s -w" -o "$BINDIR/$name" "./cmd/$name"
done

if [ ! -f "$MINI_CONFIG_FILE" ]; then
  echo "==> writing $MINI_CONFIG_FILE"
  write_mini_config "$MINI_CONFIG_FILE" "$STATE_DIR/mini"
  chmod 600 "$MINI_CONFIG_FILE"
else
  echo "==> keeping existing $MINI_CONFIG_FILE"
fi

if [ ! -f "$MAX_CONFIG_FILE" ]; then
  echo "==> writing $MAX_CONFIG_FILE"
  write_max_config "$MAX_CONFIG_FILE" "$STATE_DIR/max"
  chmod 600 "$MAX_CONFIG_FILE"
else
  echo "==> keeping existing $MAX_CONFIG_FILE"
fi

if [ ! -f "$LEGACY_CONFIG_FILE" ]; then
  echo "==> writing $LEGACY_CONFIG_FILE"
  write_max_config "$LEGACY_CONFIG_FILE" "$STATE_DIR/legacy"
  chmod 600 "$LEGACY_CONFIG_FILE"
else
  echo "==> keeping existing $LEGACY_CONFIG_FILE"
fi

cat <<EOF

AniGate installed.

Binaries:
  $BINDIR/anigate-mini
  $BINDIR/anigate-max
  $BINDIR/anigate  (legacy alias for Max)

Configs:
  Mini: $MINI_CONFIG_FILE (read-only view of $WORKSPACE_DIR)
  Max:  $MAX_CONFIG_FILE (writable agent workspace: $MAX_WORKSPACE_DIR)
  Legacy Max: $LEGACY_CONFIG_FILE

Max defaults to the dedicated workspace above; widen it deliberately by
editing the config or re-running with ANIGATE_MAX_WORKSPACE_DIR set.

Try Mini stdio mode:
  $BINDIR/anigate-mini stdio --config "$MINI_CONFIG_FILE"

Try Mini local HTTP mode:
  $BINDIR/anigate-mini http --addr 127.0.0.1:8787 --config "$MINI_CONFIG_FILE"

Try Max local HTTP mode:
  $BINDIR/anigate-max http --addr 127.0.0.1:8788 --config "$MAX_CONFIG_FILE"

HTTP token header:
  Authorization: Bearer <token from the selected config file>

Add this to PATH if needed:
  export PATH="$BINDIR:\$PATH"
EOF
