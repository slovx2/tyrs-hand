#!/usr/bin/env bash
set -euo pipefail

platform="${1:?用法：run.sh android|ios}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
lane="protocol"
if [[ "${platform}" == "android" ]]; then
  lane="real-codex"
fi

"${root}/tools/mobile-e2e/install-maestro.sh"
"${root}/tools/mobile-e2e/build-client.sh" "${platform}"
exec node "${root}/tools/mobile-e2e/mobile-runner.mjs" \
  --platform "${platform}" --lane "${lane}" --app-id com.tyrshand.app.dev
