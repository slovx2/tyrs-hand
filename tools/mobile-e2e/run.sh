#!/usr/bin/env bash
set -euo pipefail

platform="${1:?用法：run.sh android|ios}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
lane="${TYRS_HAND_E2E_LANE:-protocol}"

"${root}/tools/mobile-e2e/install-maestro.sh"
"${root}/tools/mobile-e2e/build-client.sh" "${platform}"
args=(--platform "${platform}" --lane "${lane}" --app-id com.tyrshand.app.dev)
if [[ -n "${TYRS_HAND_E2E_FLOW:-}" ]]; then
  args+=(--flow "${TYRS_HAND_E2E_FLOW}")
fi
exec node "${root}/tools/mobile-e2e/mobile-runner.mjs" "${args[@]}"
