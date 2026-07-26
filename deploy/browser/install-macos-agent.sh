#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
agent_label=ai.tyrs-hand.browser-agent
agent_root="$HOME/Library/Application Support/Tyrs Hand/browser-agent"
launch_agent="$HOME/Library/LaunchAgents/$agent_label.plist"
log_root="$HOME/Library/Logs/Tyrs Hand"

replace_link() {
  local link=$1 target=$2 temporary_link="$1.tmp.$$"
  rm -f "$temporary_link"
  ln -s "$target" "$temporary_link"
  /bin/mv -fh "$temporary_link" "$link"
}

read_json_value() {
  local file=$1 key=$2
  [[ -f $file ]] || return 1
  /usr/bin/plutil -extract "$key" raw -o - "$file" 2>/dev/null
}

usage() {
  echo "用法：" >&2
  echo "  $0 install <agent.tgz> <ssh-host> <ssh-port> <ssh-user> <identity-file> <known-hosts-file> <extension-id>" >&2
  echo "  $0 <status|rollback|uninstall>" >&2
  exit 2
}

[[ $(uname -s) == Darwin ]] || { echo "仅支持 macOS" >&2; exit 1; }
operation=${1:-}
case "$operation" in
  install) ;;
  status)
    curl --fail --silent --show-error http://127.0.0.1:8931/health
    echo
    exit 0
    ;;
  rollback)
    [[ -f $launch_agent ]] || { echo "Browser Agent 尚未安装" >&2; exit 1; }
    current=$(readlink "$agent_root/current" || true)
    previous=$(readlink "$agent_root/previous" || true)
    [[ -d $current && -d $previous ]] || { echo "没有可回滚版本" >&2; exit 1; }
    replace_link "$agent_root/current" "$previous"
    replace_link "$agent_root/previous" "$current"
    launchctl kickstart -k "gui/$(id -u)/$agent_label"
    echo "桌面端 Browser Agent 已回滚。"
    exit 0
    ;;
  uninstall)
    launchctl bootout "gui/$(id -u)" "$launch_agent" 2>/dev/null || true
    rm -f "$launch_agent"
    rm -rf "$agent_root"
    echo "桌面端 Browser Agent 已卸载。"
    exit 0
    ;;
  *) usage ;;
esac

[[ $# == 8 ]] || usage
bundle=$2
ssh_host=$3
ssh_port=$4
ssh_user=$5
identity_file=$6
known_hosts_file=$7
extension_id=$8
[[ -f $bundle && -f $identity_file && -f $known_hosts_file ]] || { echo "安装文件或 SSH 文件不存在" >&2; exit 1; }
[[ $ssh_port =~ ^[0-9]+$ && $extension_id =~ ^[a-p]{32}$ ]] || { echo "SSH 端口或扩展 ID 无效" >&2; exit 1; }
(( ssh_port >= 1 && ssh_port <= 65535 )) || { echo "SSH 端口超出范围" >&2; exit 1; }

mkdir -p "$agent_root/releases" "$log_root" "$HOME/Library/LaunchAgents"
temporary=$(mktemp -d "$agent_root/releases/.install.XXXXXX")
trap 'rm -rf "$temporary"' EXIT
tar -xzf "$bundle" -C "$temporary"
source_root=$(find "$temporary" -mindepth 1 -maxdepth 1 -type d | head -1)
[[ -x $source_root/node && -f $source_root/app/src/main.mjs && -f $source_root/app/tyrs-browser-extension.crx &&
  -f $source_root/app/browser-agent-release.json ]] || {
  echo "Browser Agent bundle 不完整" >&2; exit 1;
}
version=$(read_json_value "$source_root/app/browser-agent-release.json" agentVersion || true)
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || { echo "Agent 版本无效" >&2; exit 1; }
destination="$agent_root/releases/$version"
[[ ! -e $destination ]] || { echo "Agent 版本已安装: $version" >&2; exit 1; }
mv "$source_root" "$destination"
chmod -R go-w "$destination"

config="$agent_root/config.json"
extension_token=$(read_json_value "$config" extensionToken || true)
instance_id=$(read_json_value "$config" instanceId || true)
[[ -n $extension_token ]] || extension_token=$(/usr/bin/openssl rand -hex 32)
[[ -n $instance_id ]] || instance_id=$(/usr/bin/uuidgen | tr '[:upper:]' '[:lower:]')
"$destination/node" - "$config" "$extension_id" "$extension_token" \
  "$agent_root/current/app/tyrs-browser-extension.crx" "$instance_id" "$ssh_host" "$ssh_port" \
  "$ssh_user" "$identity_file" "$known_hosts_file" <<'NODE'
const fs = require('fs');
const [file, extensionId, extensionToken, extensionCrxPath, instanceId,
  host, port, user, identityFile, knownHostsFile] = process.argv.slice(2);
fs.writeFileSync(file, JSON.stringify({ extensionId, extensionToken,
  extensionCrxPath, instanceId, publicPort: 8931, relayPort: 8932,
  ssh: { host, port: Number(port), user, identityFile, knownHostsFile } }, null, 2) + '\n', { mode: 0o600 });
NODE
chmod 0600 "$config"

if [[ -L $agent_root/current && -d $(readlink "$agent_root/current") ]]; then
  replace_link "$agent_root/previous" "$(readlink "$agent_root/current")"
fi
replace_link "$agent_root/current" "$destination"
"$destination/node" "$script_dir/prepare-unpacked-extension.mjs" \
  "$destination/app/tyrs-browser-extension.crx" "$agent_root/unpacked-extension" "$extension_id"

"$destination/node" - "$launch_agent" "$agent_label" "$agent_root" "$config" "$log_root" <<'NODE'
const fs = require('fs');
const [file, label, root, config, logs] = process.argv.slice(2);
fs.writeFileSync(file, JSON.stringify({
  Label: label,
  ProgramArguments: [`${root}/current/node`, `${root}/current/app/src/main.mjs`],
  EnvironmentVariables: { TYRS_BROWSER_AGENT_CONFIG: config },
  RunAtLoad: true,
  KeepAlive: true,
  ThrottleInterval: 5,
  StandardOutPath: `${logs}/browser-agent.log`,
  StandardErrorPath: `${logs}/browser-agent.log`,
}, null, 2) + '\n');
NODE
/usr/bin/plutil -convert xml1 "$launch_agent"
chmod 0644 "$launch_agent"
launchctl bootout "gui/$(id -u)" "$launch_agent" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$launch_agent"
launchctl kickstart -k "gui/$(id -u)/$agent_label"
trap - EXIT
rm -rf "$temporary"
echo "桌面端 Browser Agent $version 已安装。"
echo "请在 chrome://extensions 开启开发者模式并加载：$agent_root/unpacked-extension"
