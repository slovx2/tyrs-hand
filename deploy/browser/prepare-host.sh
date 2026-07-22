#!/usr/bin/env bash
set -euo pipefail

desktop_user=${1:?请提供 Ubuntu 桌面用户名}
desktop_uid=$(id -u "$desktop_user")
desktop_gid=$(id -g "$desktop_user")

install -d -o 10001 -g 10001 -m 0755 /opt/tyrs-hand/ssh-agent
install -d -o "$desktop_uid" -g "$desktop_gid" -m 0777 /opt/tyrs-hand/browser-files
install -d -o "$desktop_uid" -g "$desktop_gid" -m 0750 /opt/tyrs-hand/browser
install -d -o "$desktop_uid" -g "$desktop_gid" -m 0755 /opt/tyrs-hand/browser/releases

if [[ ! -s /opt/tyrs-hand/browser/browser_mcp_token ]]; then
  openssl rand -hex 32 > /opt/tyrs-hand/browser/browser_mcp_token
fi
if [[ ! -s /opt/tyrs-hand/browser/browser_extension_token ]]; then
  openssl rand -hex 32 > /opt/tyrs-hand/browser/browser_extension_token
fi
chown "$desktop_uid:$desktop_gid" /opt/tyrs-hand/browser/browser_*_token
chmod 0444 /opt/tyrs-hand/browser/browser_mcp_token
chmod 0400 /opt/tyrs-hand/browser/browser_extension_token
