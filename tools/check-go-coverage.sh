#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mkdir -p coverage

core_packages="github.com/slovx2/tyrs-hand/internal/auth,github.com/slovx2/tyrs-hand/internal/codex,github.com/slovx2/tyrs-hand/internal/config,github.com/slovx2/tyrs-hand/internal/githubtools,github.com/slovx2/tyrs-hand/internal/orchestrator,github.com/slovx2/tyrs-hand/internal/queue,github.com/slovx2/tyrs-hand/internal/security,github.com/slovx2/tyrs-hand/internal/settings,github.com/slovx2/tyrs-hand/internal/tools"

go test -tags=integration \
  -covermode=atomic \
  -coverpkg="$core_packages" \
  -coverprofile=coverage/go.out \
  ./internal/codex \
  ./internal/config \
  ./internal/githubtools \
  ./internal/orchestrator \
  ./internal/security \
  ./internal/settings \
  ./internal/tools \
  ./internal/worker \
  ./test/integration

coverage=$(go tool cover -func=coverage/go.out | awk '/^total:/ {gsub("%", "", $3); print $3}')
threshold=80.0
awk -v actual="$coverage" -v required="$threshold" 'BEGIN {
  if (actual + 0 < required + 0) {
    printf "Go 核心业务代码覆盖率 %.1f%%，低于要求 %.1f%%\n", actual, required > "/dev/stderr"
    exit 1
  }
  printf "Go 核心业务代码覆盖率 %.1f%%，达到要求 %.1f%%\n", actual, required
}'
