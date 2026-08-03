#!/usr/bin/env bash
set -euo pipefail

pattern='(devcontainer|development[ _-]*environment|remote[ _-]*worker|codex[ _-]*relay|browser[ _-]*relay|relayurl|relayport|tyrs_browser_relay_port|tyrs_hand_(development|dev_|codex_provider|codex_auth|relay)|remoterunner|remoteprocessor|execution_nodes|worker_nodes|discord_development_environments|development_projects|development_sessions|container_id|data_volume_name|home_volume_name|network_name|runtime_uid|runtime_gid|codex_home_key|codex_home_root|daemon_status|browser_agent_relay_address|node-credential|control-codex-home|远程[[:space:]]*worker|开发环境|codex[[:space:]]*中继|浏览器[[:space:]]*中继)'

matches=$(git grep -n -I -i -E "$pattern" -- . \
  ':!internal/database/migrate_integration_test.go' \
  ':!tools/check-legacy-architecture.sh' || true)
if [[ -n $matches ]]; then
  echo '发现旧容器 Worker 架构残留：' >&2
  printf '%s\n' "$matches" >&2
  exit 1
fi
