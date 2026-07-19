#!/usr/bin/env bash

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
toolchain_root="${root}/.local/toolchains"
node_version="24.14.0"
pnpm_version="11.14.0"
codex_version="0.142.5"

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)
    platform="darwin-arm64"
    node_sha256="a1a54f46a750d2523d628d924aab61758a51c9dad3e0238beb14141be9615dd3"
    ;;
  Darwin-x86_64)
    platform="darwin-x64"
    node_sha256="f2879eb810e25993a0578e5d878930266fd2eafcffe9f2839b3d8db354d4879e"
    ;;
  Linux-aarch64)
    platform="linux-arm64"
    node_sha256="f44740cd218de8127f1c44c41510a3a740fa5c9c8d1cdce1c3bedada79f3cde7"
    ;;
  Linux-x86_64)
    platform="linux-x64"
    node_sha256="dbf5b8665dec15e59e6359a517fefb47b23fdb9152d8def975b9bca3dfc6d355"
    ;;
  *)
    echo "本地工具链暂不支持 $(uname -s)-$(uname -m)。" >&2
    exit 1
    ;;
esac

node_archive="node-v${node_version}-${platform}.tar.gz"
node_dir="${toolchain_root}/node-v${node_version}-${platform}"
if [[ ! -x "${node_dir}/bin/node" ]]; then
  mkdir -p "${toolchain_root}"
  temporary_dir="$(mktemp -d "${toolchain_root}/.node.XXXXXX")"
  trap 'rm -rf "${temporary_dir}"' EXIT
  curl --fail --location --silent --show-error \
    "https://nodejs.org/dist/v${node_version}/${node_archive}" \
    --output "${temporary_dir}/${node_archive}"
  actual_sha256="$(openssl dgst -sha256 "${temporary_dir}/${node_archive}" | awk '{print $NF}')"
  if [[ "${actual_sha256}" != "${node_sha256}" ]]; then
    echo "Node ${node_version} 下载文件校验失败。" >&2
    exit 1
  fi
  tar -xzf "${temporary_dir}/${node_archive}" -C "${temporary_dir}"
  mv "${temporary_dir}/node-v${node_version}-${platform}" "${node_dir}"
  rm -rf "${temporary_dir}"
  trap - EXIT
fi

export PATH="${node_dir}/bin:${PATH}"
pnpm_dir="${toolchain_root}/pnpm-${pnpm_version}"
if [[ ! -x "${pnpm_dir}/bin/pnpm" ]]; then
  "${node_dir}/bin/npm" install --global --prefix "${pnpm_dir}" "pnpm@${pnpm_version}"
fi
codex_dir="${toolchain_root}/codex-${codex_version}"
if [[ ! -x "${codex_dir}/bin/codex" ]]; then
  "${node_dir}/bin/npm" install --global --prefix "${codex_dir}" "@openai/codex@${codex_version}"
fi
export PATH="${pnpm_dir}/bin:${codex_dir}/bin:${PATH}"

go_root="$(go env GOROOT)"
if [[ "$(${go_root}/bin/go env GOVERSION)" != "go1.26.5" ]]; then
  echo "本地工具链需要 Go 1.26.5。" >&2
  exit 1
fi
export PATH="${go_root}/bin:${PATH}"

if [[ "$(node --version)" != "v${node_version}" ]] || \
  [[ "$(pnpm --version)" != "${pnpm_version}" ]] || \
  [[ "$(codex --version)" != "codex-cli ${codex_version}" ]]; then
  echo "本地 Node、pnpm 或 Codex 工具链版本不正确。" >&2
  exit 1
fi

exec "$@"
