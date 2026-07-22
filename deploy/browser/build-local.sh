#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
workspace_root=$(cd "$script_dir/../../.." && pwd)
playwright_root="$workspace_root/playwright"
mcp_root="$workspace_root/playwright-mcp"
output_root=${1:-"$script_dir/out"}
playwright_output="$output_root/playwright"
bridge_output="$output_root/bridge"

for repository in "$playwright_root" "$mcp_root"; do
  if [[ ! -d "$repository/.git" ]]; then
    echo "缺少同级仓库: $repository" >&2
    exit 1
  fi
done

if [[ ${TYRS_BROWSER_ALLOW_UNPINNED:-0} != 1 ]]; then
  node "$script_dir/verify-source-lock.mjs" "$script_dir/source-lock.json" "$playwright_root" "$mcp_root"
fi

if [[ ! -d "$playwright_root/node_modules" ]]; then
  npm ci --prefix "$playwright_root"
fi
npm run --prefix "$playwright_root" build-tyrs-artifacts -- "$playwright_output"
node "$mcp_root/scripts/build-tyrs-bundle.mjs" \
  "$playwright_output/playwright-core.tgz" "$bridge_output"
node "$script_dir/write-local-lock.mjs" "$playwright_output" "$bridge_output" "$output_root/browser-artifacts.lock.json"

echo "$output_root"
