#!/bin/sh
# 仅为本地/CI 验收安装隔离的固定 CLI，不改全局 Claude，不纳入运行时制品。
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(node -e 'console.log(require(process.argv[1]).claudeCli)' "$root/protocol/adapter-lock.json")
prefix="$root/.local/toolchains/claude-$version"
npm install --prefix "$prefix" --save-exact "@anthropic-ai/claude-code@$version" --no-audit --no-fund
cli="$prefix/node_modules/.bin/claude"
if [ -n "${GITHUB_ENV:-}" ]; then
  printf 'TYRS_HAND_TEST_CLAUDE_CLI=%s\nCHA_CLAUDE_CLI=%s\n' "$cli" "$cli" >> "$GITHUB_ENV"
fi
printf '测试 CLI: %s\n' "$cli"
