#!/usr/bin/env bash
set -euo pipefail

image=${1:-tyrs-hand-worker:test}

docker run --rm --entrypoint sh "$image" -c '
runtime_lock=/usr/local/share/tyrs-hand/worker-runtime.lock.json
expected_codex="$(node -p "require(\"${runtime_lock}\").codex")"
expected_docker="$(node -p "require(\"${runtime_lock}\").dockerCli")"
test "$(codex --version)" = "codex-cli ${expected_codex}"
actual_docker="$(/usr/local/libexec/tyrs-hand/docker --version | cut -d " " -f3 | tr -d ,)"
test "${actual_docker}" = "${expected_docker}"
'

for tool in mise uv corepack pnpm go rustc cargo; do
  if docker run --rm --entrypoint sh "$image" -c "command -v '$tool'"; then
    echo "Worker 不应提供项目工具链：$tool" >&2
    exit 1
  fi
done

docker run --rm --entrypoint sh "$image" -c 'command -v git && command -v rg && command -v apply_patch'
