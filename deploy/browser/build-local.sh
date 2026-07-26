#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
workspace_root=$(cd "$script_dir/../../.." && pwd)
playwright_root="$workspace_root/playwright"
mcp_root="$workspace_root/playwright-mcp"
output_root=${1:-"$script_dir/out"}
extension_crx=${2:-${TYRS_BROWSER_EXTENSION_CRX:-}}
extension_id=${3:-${TYRS_BROWSER_EXTENSION_ID:-}}
playwright_output="$output_root/playwright"
bridge_output="$output_root/bridge"
agent_output="$output_root/agent"

if [[ ! -f $extension_crx || ! $extension_id =~ ^[a-p]{32}$ ]]; then
  echo "本地构建 Browser Agent 需要已签名 CRX 和扩展 ID：$0 [output] <extension.crx> <extension-id>" >&2
  exit 1
fi

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
install -m 0644 "$extension_crx" "$playwright_output/tyrs-browser-extension.crx"
node "$mcp_root/scripts/build-tyrs-bundle.mjs" \
  "$playwright_output/playwright-core.tgz" "$bridge_output"
node "$mcp_root/scripts/build-browser-agent.mjs" \
  "$playwright_output/playwright-core.tgz" \
  "$playwright_output/tyrs-browser-extension.crx" \
  "$playwright_output/playwright-artifacts.json" "$agent_output"
node "$script_dir/write-local-lock.mjs" "$playwright_output" "$bridge_output" \
  "$agent_output" "$extension_id" "$output_root/browser-artifacts.lock.json"

echo "$output_root"
