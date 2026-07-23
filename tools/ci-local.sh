#!/usr/bin/env bash

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"

for command_name in docker go node pnpm codex openssl curl; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "缺少本地 CI 依赖：${command_name}" >&2
    exit 1
  fi
done

if [[ "$(go env GOVERSION)" != "go1.26.5" ]]; then
  echo "本地 CI 需要 Go 1.26.5，当前为 $(go env GOVERSION)。" >&2
  exit 1
fi
if [[ "$(pnpm --version)" != "11.14.0" ]]; then
  echo "本地 CI 需要 pnpm 11.14.0，当前为 $(pnpm --version)。" >&2
  exit 1
fi
if [[ "$(codex --version)" != "codex-cli 0.145.0" ]]; then
  echo "本地 CI 需要 Codex CLI 0.145.0，当前为 $(codex --version)。" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "本地 CI 需要正在运行的 Docker。" >&2
  exit 1
fi

run_id="${$}-$(date +%s)"
postgres_name="tyrs-hand-ci-postgres-${run_id}"
redis_name="tyrs-hand-ci-redis-${run_id}"
server_pid=""
server_log="$(mktemp -t tyrs-hand-ci-server.XXXXXX.log)"
server_bin="$(mktemp -t tyrs-hand-ci-server.XXXXXX)"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "${server_pid}" ]] && kill -0 "${server_pid}" 2>/dev/null; then
    kill "${server_pid}" 2>/dev/null || true
    wait "${server_pid}" 2>/dev/null || true
  fi
  docker rm --force "${postgres_name}" "${redis_name}" >/dev/null 2>&1 || true
  if [[ ${status} -ne 0 ]]; then
    echo "本地 CI 失败，Server 最近日志：" >&2
    tail -n 100 "${server_log}" >&2 || true
  fi
  rm -f "${server_log}" "${server_bin}"
  exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

docker run --detach --name "${postgres_name}" \
  --env POSTGRES_DB=tyrs_hand_test \
  --env POSTGRES_USER=tyrs_hand \
  --env POSTGRES_PASSWORD=test-password \
  --publish 127.0.0.1::5432 \
  postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba >/dev/null
docker run --detach --name "${redis_name}" \
  --publish 127.0.0.1::6379 \
  redis:8.4.0-bookworm@sha256:c22af04bb576503bf16b3e34a1fd2fd82de0f765afd866d2e380145e0af30d78 >/dev/null

for _ in $(seq 1 60); do
  if docker exec "${postgres_name}" pg_isready -U tyrs_hand -d tyrs_hand_test >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "${postgres_name}" pg_isready -U tyrs_hand -d tyrs_hand_test >/dev/null

for _ in $(seq 1 60); do
  if docker exec "${redis_name}" redis-cli ping 2>/dev/null | grep -q PONG; then
    break
  fi
  sleep 1
done
docker exec "${redis_name}" redis-cli ping | grep -q PONG

postgres_port="$(docker port "${postgres_name}" 5432/tcp | sed -n '1s/.*://p')"
redis_port="$(docker port "${redis_name}" 6379/tcp | sed -n '1s/.*://p')"
export TEST_DATABASE_URL="postgres://tyrs_hand:test-password@127.0.0.1:${postgres_port}/tyrs_hand_test?sslmode=disable"
export TEST_REDIS_URL="redis://127.0.0.1:${redis_port}/1"

pnpm --dir web install --frozen-lockfile
make ci

e2e_port="${TYRS_HAND_LOCAL_E2E_PORT:-18080}"
export TYRS_HAND_DATABASE_URL="${TEST_DATABASE_URL}"
export TYRS_HAND_REDIS_URL="${TEST_REDIS_URL}"
export TYRS_HAND_HTTP_ADDR="127.0.0.1:${e2e_port}"
export TYRS_HAND_SETUP_TOKEN="integration-setup-token"
export TYRS_HAND_COOKIE_SECURE="false"
export TYRS_HAND_MASTER_KEY="$(openssl rand -base64 32)"
export E2E_SETUP_TOKEN="${TYRS_HAND_SETUP_TOKEN}"
export E2E_BASE_URL="http://127.0.0.1:${e2e_port}"

go run ./cmd/tyrs-hand-admin migrate
go build -o "${server_bin}" ./cmd/tyrs-hand-server
"${server_bin}" >"${server_log}" 2>&1 &
server_pid=$!
for _ in $(seq 1 60); do
  if curl --fail --silent "${E2E_BASE_URL}/healthz" >/dev/null; then
    break
  fi
  if ! kill -0 "${server_pid}" 2>/dev/null; then
    echo "本地 E2E Server 提前退出。" >&2
    exit 1
  fi
  sleep 1
done
curl --fail --silent "${E2E_BASE_URL}/healthz" >/dev/null

pnpm --dir web exec playwright install chromium
pnpm --dir web e2e
