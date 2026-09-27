#!/usr/bin/env bash
set -euo pipefail

platform="${1:?用法：run.sh android|ios [--install-only]}"
if [[ $# -gt 2 || ( $# -eq 2 && "${2:-}" != "--install-only" ) ]]; then
  echo "验收必须安装本轮 APK；这里只允许 --install-only，独立构建请使用 build-client.sh" >&2
  exit 1
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
lane="${TYRS_HAND_E2E_LANE:-dual-engine}"
gui_enabled=$(node "${root}/tools/mobile-e2e/gui-policy.mjs" "${platform}")
if [[ "${gui_enabled}" == false ]]; then
  exit 0
fi
test "${gui_enabled}" = true

"${root}/tools/mobile-e2e/install-maestro.sh"
"${root}/tools/mobile-e2e/build-client.sh" "$@"
args=(--platform "${platform}" --lane "${lane}" --app-id com.tyrshand.app.dev)
if [[ -n "${TYRS_HAND_E2E_FLOW:-}" ]]; then
  args+=(--flow "${TYRS_HAND_E2E_FLOW}")
fi
exec node "${root}/tools/mobile-e2e/mobile-runner.mjs" "${args[@]}"
