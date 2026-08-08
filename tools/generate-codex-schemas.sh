#!/usr/bin/env bash

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
codex_bin="${1:-codex}"
version="0.147.0"
output="${root}/protocol/codex-app-server/${version}"

actual="$(${codex_bin} --version)"
if [[ "${actual}" != "codex-cli ${version}" ]]; then
  echo "生成协议需要 Codex CLI ${version}，当前为 ${actual}。" >&2
  exit 1
fi

mkdir -p "${output}"
"${codex_bin}" app-server generate-ts --experimental --out "${output}/typescript"
"${codex_bin}" app-server generate-json-schema --experimental --out "${output}/json-schema"
