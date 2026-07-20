#!/usr/bin/env bash
set -euo pipefail

image=${1:-tyrs-hand-worker:test}

docker run --rm "$image" codex --version
docker run --rm "$image" /usr/local/libexec/tyrs-hand/docker --version

for tool in mise uv corepack pnpm go rustc cargo; do
  if docker run --rm --entrypoint sh "$image" -c "command -v '$tool'"; then
    echo "Worker 不应提供项目工具链：$tool" >&2
    exit 1
  fi
done

docker run --rm --entrypoint sh "$image" -c 'command -v git && command -v rg && command -v apply_patch'
