#!/usr/bin/env bash
set -euo pipefail

image=${1:-tyrs-hand-development:test}

contract="$(docker image inspect --format '{{index .Config.Labels "ai.tyrs-hand.development.contract"}}' "$image")"
[[ "$contract" == "1" ]]

identity="$(docker run --rm --entrypoint sh "$image" -c 'printf "%s:%s:%s" "$(id -un)" "$(id -u):$(id -g)" "$HOME"')"
[[ "$identity" == "developer:1000:1000:/home/developer" ]]

docker run --rm --entrypoint sh "$image" -c '
set -eu
for tool in git git-lfs ssh sshd sftp sudo curl jq rg fd zip unzip cmake ninja clang pkg-config mise uv node corepack pnpm python3 go rustc cargo codex apply_patch tyrs-hand-dev; do
  command -v "$tool" >/dev/null
done
sudo -n true
test "$(readlink -f /usr/local/bin/apply_patch)" = "$(readlink -f /usr/local/bin/codex)"
test -x /opt/tyrs-hand/codex/bin/codex
test "$(mise --version | awk "{print \$1}")" = "2026.7.7"
test "$(uv --version | awk "{print \$2}")" = "0.11.29"
test "$(node --version)" = "v24.14.0"
test "$(npm --version)" = "11.18.0"
test "$(corepack --version)" = "0.35.0"
test "$(pnpm --version)" = "11.14.0"
test "$(python3 --version)" = "Python 3.13.14"
test "$(go version | awk "{print \$3}")" = "go1.26.5"
test "$(rustc --version | awk "{print \$2}")" = "1.97.1"
test "$(codex --version)" = "codex-cli 0.145.0"
test "$(tyrs-hand-dev codex status)" = "bundled"
! find /etc/ssh -maxdepth 1 -type f -name "ssh_host_*_key" -print -quit | grep -q .
'
