#!/bin/sh
set -eu

: "${TYRS_HAND_WORKER_USER:=${SUDO_USER:-$(id -un)}}"
: "${TYRS_HAND_WORKER_CONTROL_URL:?必须提供 Control HTTPS URL}"
: "${TYRS_HAND_WORKER_PUBLIC_KEYS_FILE:?必须提供至少包含一个公钥的文件}"

worker_binary=${TYRS_HAND_WORKER_BINARY:-}
release_version=${TYRS_HAND_RELEASE_VERSION:-}
enrollment_token=${TYRS_HAND_WORKER_ENROLLMENT_TOKEN:-}
credential_source=${TYRS_HAND_WORKER_CREDENTIAL_SOURCE:-}
if [ -z "${worker_binary}" ] && [ -z "${release_version}" ]; then
  echo "下载 Release 时必须提供精确 TYRS_HAND_RELEASE_VERSION" >&2
  exit 1
fi
if { [ -z "${enrollment_token}" ] && [ -z "${credential_source}" ]; } ||
  { [ -n "${enrollment_token}" ] && [ -n "${credential_source}" ]; }; then
  echo "Enrollment Token 与现有 Worker Credential 必须且只能提供一个" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "安装系统服务必须以 root 运行" >&2
  exit 1
fi

if [ -z "${worker_binary}" ]; then
  version_body=${release_version#v}
  case "${version_body}" in
    *[!0-9.]*|'')
      echo "TYRS_HAND_RELEASE_VERSION 必须是精确 vX.Y.Z" >&2
      exit 1
      ;;
  esac
  old_ifs=${IFS}
  IFS=.
  set -- ${version_body}
  IFS=${old_ifs}
  if [ "${release_version#v}" = "${release_version}" ] || [ "$#" -ne 3 ]; then
    echo "TYRS_HAND_RELEASE_VERSION 必须是精确 vX.Y.Z" >&2
    exit 1
  fi
  for component in "$@"; do
    case "${component}" in ''|*[!0-9]*) echo "TYRS_HAND_RELEASE_VERSION 必须是精确 vX.Y.Z" >&2; exit 1 ;; esac
  done
else
  case "${worker_binary}" in /*) ;; *) echo "TYRS_HAND_WORKER_BINARY 必须是绝对路径" >&2; exit 1 ;; esac
  test -f "${worker_binary}"
  test -x "${worker_binary}"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "${os}" in linux|darwin) ;; *) echo "仅支持 Linux 和 macOS" >&2; exit 1 ;; esac
case "${arch}" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "不支持架构 ${arch}" >&2; exit 1 ;; esac

worker_user=${TYRS_HAND_WORKER_USER}
case "${os}" in
  darwin)
    worker_home=$(dscl . -read "/Users/${worker_user}" NFSHomeDirectory | awk '{print $2}')
    detected_shell=$(dscl . -read "/Users/${worker_user}" UserShell | awk '{print $2}')
    ;;
  linux)
    passwd_entry=$(getent passwd "${worker_user}")
    worker_home=$(printf '%s' "${passwd_entry}" | cut -d: -f6)
    detected_shell=$(printf '%s' "${passwd_entry}" | cut -d: -f7)
    ;;
esac
test -n "${worker_home}"
test -d "${worker_home}"
test -s "${TYRS_HAND_WORKER_PUBLIC_KEYS_FILE}"
if [ -n "${credential_source}" ]; then
  case "${credential_source}" in /*) ;; *) echo "TYRS_HAND_WORKER_CREDENTIAL_SOURCE 必须是绝对路径" >&2; exit 1 ;; esac
  test -f "${credential_source}"
  test -s "${credential_source}"
fi

case "${TYRS_HAND_WORKER_CONTROL_URL}" in
  https://*|http://localhost:*|http://127.0.0.1:*) ;;
  *) echo "Control 必须使用 HTTPS；仅本机开发允许 HTTP" >&2; exit 1 ;;
esac

validate_env_value() {
  case "$2" in
    *"'"*|*"
"*) echo "$1 包含不支持的引号或换行" >&2; exit 1 ;;
  esac
}

repository=${TYRS_HAND_RELEASE_REPOSITORY:-slovx2/tyrs-hand}
case "${repository}" in *[!A-Za-z0-9_./-]*|*..*|/*|*/|*/*/*) echo "Release 仓库名无效" >&2; exit 1 ;; esac
if [ -z "${worker_binary}" ]; then
  if ! command -v cosign >/dev/null 2>&1; then
    echo "下载 Release 制品需要 cosign 3.9.2，请先安装该精确版本" >&2
    exit 1
  fi
  asset="tyrs-hand-worker_${release_version#v}_${os}_${arch}.tar.gz"
  base="https://github.com/${repository}/releases/download/${release_version}"
  temporary=$(mktemp -d)
  trap 'rm -rf "${temporary}"' EXIT
  curl --fail --location --silent --show-error --retry 5 -o "${temporary}/${asset}" "${base}/${asset}"
  curl --fail --location --silent --show-error --retry 5 -o "${temporary}/${asset}.sha256" "${base}/${asset}.sha256"
  curl --fail --location --silent --show-error --retry 5 -o "${temporary}/${asset}.sigstore.json" "${base}/${asset}.sigstore.json"
  case "${os}" in
    darwin) (cd "${temporary}" && shasum -a 256 -c "${asset}.sha256") ;;
    linux) (cd "${temporary}" && sha256sum -c "${asset}.sha256") ;;
  esac
  cosign verify-blob \
    --bundle "${temporary}/${asset}.sigstore.json" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    --certificate-identity-regexp "^https://github.com/${repository}/.github/workflows/release-worker\\.yml@refs/tags/v[0-9]+\\.[0-9]+\\.[0-9]+$" \
    "${temporary}/${asset}"
  tar -xzf "${temporary}/${asset}" -C "${temporary}"
  worker_binary=${temporary}/tyrs-hand-worker
fi
if [ -x /usr/local/bin/tyrs-hand-worker ]; then
  install -d -o root -g root -m 0700 /usr/local/lib/tyrs-hand/worker-backups
  binary_stamp=$(date +%Y%m%d%H%M%S)
  cp -p /usr/local/bin/tyrs-hand-worker \
    "/usr/local/lib/tyrs-hand/worker-backups/tyrs-hand-worker.${binary_stamp}"
  binary_backup_index=0
  for backup in $(ls -1t /usr/local/lib/tyrs-hand/worker-backups/tyrs-hand-worker.*); do
    binary_backup_index=$((binary_backup_index + 1))
    if [ "${binary_backup_index}" -gt 4 ]; then
      rm -- "${backup}"
    fi
  done
fi
install -m 0755 "${worker_binary}" /usr/local/bin/tyrs-hand-worker

case "${os}" in
  darwin) state_root="${worker_home}/Library/Application Support/Tyrs Hand/worker" ;;
  linux) state_root="${worker_home}/.local/share/tyrs-hand/worker" ;;
esac
install -d -o "${worker_user}" -g "$(id -gn "${worker_user}")" -m 0700 \
  "${state_root}/ssh" "${state_root}/control-state" "${worker_home}/tyrs-hand/workspaces"
install -o "${worker_user}" -g "$(id -gn "${worker_user}")" -m 0600 \
  "${TYRS_HAND_WORKER_PUBLIC_KEYS_FILE}" "${state_root}/ssh/authorized_keys"
credential_file=${state_root}/control-state/worker-credential
if [ -n "${credential_source}" ]; then
  install -o "${worker_user}" -g "$(id -gn "${worker_user}")" -m 0600 \
    "${credential_source}" "${credential_file}"
fi
worker_group=$(id -gn "${worker_user}")
worker_id=${TYRS_HAND_WORKER_ID:-$(hostname)}
worker_role=${TYRS_HAND_WORKER_ROLE:-all}
worker_jobs=${TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS:-6}
worker_codex_home=${CODEX_HOME:-${worker_home}/.codex}
worker_shell=${TYRS_HAND_WORKER_SHELL:-${detected_shell:-/bin/sh}}
worker_path=$(sudo -iu "${worker_user}" "${worker_shell}" -lc 'printf %s "$PATH"')
worker_codex_bin=${TYRS_HAND_CODEX_BIN:-}
if [ -z "${worker_codex_bin}" ]; then
  worker_codex_bin=$(sudo -iu "${worker_user}" "${worker_shell}" -lc 'command -v codex')
fi
case "${worker_codex_bin}" in /*) ;; *) echo "未找到用户系统 Codex，请设置绝对路径 TYRS_HAND_CODEX_BIN" >&2; exit 1 ;; esac
worker_workspace=${worker_home}/tyrs-hand/workspaces
worker_keys=${state_root}/ssh/authorized_keys
worker_listen=${TYRS_HAND_WORKER_SSH_LISTEN_ADDR:-:2222}
worker_browser_mcp_url=${TYRS_HAND_BROWSER_MCP_URL:-}
worker_browser_token_file=${TYRS_HAND_BROWSER_MCP_TOKEN_FILE:-}
worker_browser_agent=${TYRS_HAND_BROWSER_AGENT_ADDRESS:-127.0.0.1:8934}
worker_browser_files=${TYRS_HAND_BROWSER_FILES_ROOT:-${worker_home}/.local/share/tyrs-hand/browser-files}
for pair in \
  "Control URL:${TYRS_HAND_WORKER_CONTROL_URL}" "Worker ID:${worker_id}" \
  "Enrollment Token:${enrollment_token}" "Home:${worker_home}" \
  "Codex Home:${worker_codex_home}" "Codex Bin:${worker_codex_bin}" \
  "Shell:${worker_shell}" "PATH:${worker_path}" "Workspace:${worker_workspace}" \
  "Authorized Keys:${worker_keys}" "SSH Listen:${worker_listen}" \
  "Browser MCP URL:${worker_browser_mcp_url}" "Browser Token:${worker_browser_token_file}" \
  "Browser Agent:${worker_browser_agent}" "Browser Files:${worker_browser_files}"; do
  validate_env_value "${pair%%:*}" "${pair#*:}"
done

install -d -o root -g "${worker_group}" -m 0750 /etc/tyrs-hand
if [ -f /etc/tyrs-hand/worker.env ]; then
  install -d -o root -g root -m 0700 /etc/tyrs-hand/worker.env-backups
  stamp=$(date +%Y%m%d%H%M%S)
  cp -p /etc/tyrs-hand/worker.env "/etc/tyrs-hand/worker.env-backups/worker.env.${stamp}"
  backup_index=0
  for backup in $(ls -1t /etc/tyrs-hand/worker.env-backups/worker.env.*); do
    backup_index=$((backup_index + 1))
    if [ "${backup_index}" -gt 4 ]; then
      rm -- "${backup}"
    fi
  done
fi
{
  printf "TYRS_HAND_ENV='production'\n"
  printf "TYRS_HAND_WORKER_CONTROL_URL='%s'\n" "${TYRS_HAND_WORKER_CONTROL_URL}"
  printf "TYRS_HAND_WORKER_ID='%s'\n" "${worker_id}"
  printf "TYRS_HAND_WORKER_ROLE='%s'\n" "${worker_role}"
  printf "TYRS_HAND_WORKER_MAX_CONCURRENT_JOBS='%s'\n" "${worker_jobs}"
  if [ -n "${enrollment_token}" ]; then
    printf "TYRS_HAND_WORKER_ENROLLMENT_TOKEN='%s'\n" "${enrollment_token}"
  fi
  printf "TYRS_HAND_WORKER_PROTOCOL_VERSION='22'\n"
  printf "TYRS_HAND_CODEX_BIN='%s'\n" "${worker_codex_bin}"
  printf "TYRS_HAND_WORKER_HOME='%s'\n" "${worker_home}"
  printf "TYRS_HAND_WORKER_CODEX_HOME='%s'\n" "${worker_codex_home}"
  printf "TYRS_HAND_WORKER_SHELL='%s'\n" "${worker_shell}"
  printf "TYRS_HAND_WORKER_WORKSPACE_ROOT='%s'\n" "${worker_workspace}"
  printf "TYRS_HAND_WORKER_AUTHORIZED_KEYS_FILE='%s'\n" "${worker_keys}"
  printf "TYRS_HAND_WORKER_SSH_LISTEN_ADDR='%s'\n" "${worker_listen}"
  printf "TYRS_HAND_BROWSER_AGENT_ADDRESS='%s'\n" "${worker_browser_agent}"
  printf "TYRS_HAND_BROWSER_FILES_ROOT='%s'\n" "${worker_browser_files}"
  if [ -n "${worker_browser_mcp_url}" ]; then
    printf "TYRS_HAND_BROWSER_MCP_URL='%s'\n" "${worker_browser_mcp_url}"
  fi
  if [ -n "${worker_browser_token_file}" ]; then
    printf "TYRS_HAND_BROWSER_MCP_TOKEN_FILE='%s'\n" "${worker_browser_token_file}"
  fi
  printf "PATH='%s'\n" "${worker_path}"
} > /etc/tyrs-hand/worker.env
chown root:"${worker_group}" /etc/tyrs-hand/worker.env
chmod 0640 /etc/tyrs-hand/worker.env

install -d -m 0755 /usr/local/libexec
install -m 0755 "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/tyrs-hand-worker-run" \
  /usr/local/libexec/tyrs-hand-worker-run
sudo -u "${worker_user}" /usr/local/libexec/tyrs-hand-worker-run doctor

if [ "${os}" = linux ]; then
  sed "s/@WORKER_USER@/${worker_user}/g" \
    "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/tyrs-hand-worker.service" \
    > /etc/systemd/system/tyrs-hand-worker.service
  systemctl daemon-reload
  systemctl enable tyrs-hand-worker.service
  systemctl restart tyrs-hand-worker.service
else
  sed "s/@WORKER_USER@/${worker_user}/g" \
    "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/ai.tyrs-hand.worker.plist" \
    > /Library/LaunchDaemons/ai.tyrs-hand.worker.plist
  launchctl bootout system/ai.tyrs-hand.worker 2>/dev/null || true
  launchctl bootstrap system /Library/LaunchDaemons/ai.tyrs-hand.worker.plist
fi

attempt=0
while [ "${attempt}" -lt 30 ] && [ ! -s "${credential_file}" ]; do
  attempt=$((attempt + 1))
  sleep 1
done
if [ -s "${credential_file}" ]; then
  next_env=$(mktemp /etc/tyrs-hand/worker.env.XXXXXX)
  sed '/^TYRS_HAND_WORKER_ENROLLMENT_TOKEN=/d' /etc/tyrs-hand/worker.env > "${next_env}"
  chown root:"${worker_group}" "${next_env}"
  chmod 0640 "${next_env}"
  mv "${next_env}" /etc/tyrs-hand/worker.env
  echo "Worker 已安装并注册；一次性 Enrollment Token 已从配置删除。"
else
  echo "Worker 已安装，但 30 秒内未确认注册。请检查服务日志并手动删除 Enrollment Token。" >&2
fi
