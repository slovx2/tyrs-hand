#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
desktop_user=${1:?请提供 Ubuntu 桌面用户名}
lock_path=${2:-"$script_dir/browser-artifacts.lock.json"}
if [[ $EUID -ne 0 ]]; then
  echo "请使用 root 运行宿主浏览器安装脚本" >&2
  exit 1
fi
node_bin=/usr/local/bin/node
if [[ ! -x $node_bin ]]; then
  echo "宿主缺少 $node_bin" >&2
  exit 1
fi
release=$($node_bin -e 'const lock=require(process.argv[1]); process.stdout.write(lock.release)' "$lock_path")
extension_id=$($node_bin -e 'const lock=require(process.argv[1]); process.stdout.write(lock.extensionId)' "$lock_path")
if [[ ! $release =~ ^tyrs-v[0-9]+\.[0-9]+\.[0-9]+$ ]] || [[ ! $extension_id =~ ^[a-p]{32}$ ]]; then
  echo "浏览器制品锁中的版本或扩展 ID 无效" >&2
  exit 1
fi
release_dir="/opt/tyrs-hand/browser/releases/$release"
desktop_uid=$(id -u "$desktop_user")
desktop_gid=$(id -g "$desktop_user")

"$script_dir/prepare-host.sh" "$desktop_user"
download_dir=$(mktemp -d "/opt/tyrs-hand/browser/releases/.${release}.XXXXXX")
cleanup() {
  rm -rf "$download_dir"
}
trap cleanup EXIT

$node_bin "$script_dir/fetch-release.mjs" "$lock_path" "$download_dir"
tar -xzf "$download_dir/tyrs-browser-bridge-bundle.tgz" -C "$download_dir"

rm -rf "$release_dir"
mv "$download_dir" "$release_dir"
trap - EXIT
chown -R "$desktop_uid:$desktop_gid" "$release_dir"
chmod -R go-w "$release_dir"

temporary_link="/opt/tyrs-hand/browser/releases/.current-$release"
ln -sfn "$release_dir" "$temporary_link"
mv -Tf "$temporary_link" /opt/tyrs-hand/browser/releases/current

install -m 0644 /dev/null /opt/tyrs-hand/browser/browser.env
cat > /opt/tyrs-hand/browser/browser.env <<EOF
TYRS_BROWSER_MCP_HOST=0.0.0.0
TYRS_BROWSER_MCP_PORT=8931
TYRS_BROWSER_RELAY_PORT=8932
TYRS_BROWSER_INTERNAL_MCP_PORT=8933
TYRS_BROWSER_ALLOWED_CIDRS=127.0.0.0/8,172.16.0.0/12
TYRS_BROWSER_MCP_TOKEN_FILE=/opt/tyrs-hand/browser/browser_mcp_token
TYRS_BROWSER_EXTENSION_TOKEN_FILE=/opt/tyrs-hand/browser/browser_extension_token
TYRS_BROWSER_EXTENSION_ID=$extension_id
TYRS_BROWSER_RELEASE_DIR=/opt/tyrs-hand/browser/releases/current
TYRS_BROWSER_FILES_ROOT=/opt/tyrs-hand/browser-files
EOF
chown "$desktop_uid:$desktop_gid" /opt/tyrs-hand/browser/browser.env

install -d -m 0755 /etc/opt/chrome/policies/managed
$node_bin "$script_dir/generate-policy.mjs" "$lock_path" \
  /opt/tyrs-hand/browser/browser_extension_token \
  /etc/opt/chrome/policies/managed/tyrs-browser.json
chmod 0644 /etc/opt/chrome/policies/managed/tyrs-browser.json

user_service_dir="/home/$desktop_user/.config/systemd/user"
install -d -o "$desktop_uid" -g "$desktop_gid" -m 0755 "$user_service_dir"
install -o "$desktop_uid" -g "$desktop_gid" -m 0644 \
  "$script_dir/tyrs-browser-bridge.service" "$user_service_dir/tyrs-browser-bridge.service"

runtime_dir="/run/user/$desktop_uid"
if [[ ! -S "$runtime_dir/bus" ]]; then
  echo "桌面用户 D-Bus 尚未运行: $runtime_dir/bus" >&2
  exit 1
fi
runuser -u "$desktop_user" -- env XDG_RUNTIME_DIR="$runtime_dir" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" systemctl --user daemon-reload
runuser -u "$desktop_user" -- env XDG_RUNTIME_DIR="$runtime_dir" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" systemctl --user enable --now tyrs-browser-bridge.service
runuser -u "$desktop_user" -- env XDG_RUNTIME_DIR="$runtime_dir" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" systemctl --user --no-pager status tyrs-browser-bridge.service
