#!/usr/bin/env bash
set -euo pipefail

image=${1:-tyrs-hand-worker:test}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
volume="tyrs-hand-runtime-test-$RANDOM-$$"

cleanup() {
  docker volume rm --force "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker volume create "$volume" >/dev/null

docker run --rm "$image" docker --version
if docker run --rm "$image" docker compose version; then
  echo "Worker 不应安装 Docker Compose Plugin" >&2
  exit 1
fi

run_fixtures() {
  local network=$1
  local mode=$2
  docker run --rm --network "$network" \
    --volume "$volume:/data/worker" \
    --volume "$root/test/fixtures/runtime:/fixtures:ro" \
    --volume "$root/tools/runtime-fixture-entrypoint.sh:/runtime-fixtures:ro" \
    --entrypoint sh "$image" /runtime-fixtures "$mode"
}

run_fixtures bridge core
run_fixtures none offline
if [[ ${TYRS_HAND_TEST_TAURI:-0} == 1 ]]; then
  run_fixtures bridge tauri
  run_fixtures none tauri-offline
fi
if [[ ${TYRS_HAND_TEST_ALTERNATE:-0} == 1 ]]; then
  run_fixtures bridge alternate
  run_fixtures none alternate-offline
fi
