#!/bin/sh
# 默认只构建 adapter-lock 固定的干净提交；本地验收必须显式声明。
set -eu
adapter_source=${1:?需要适配器源码目录}
artifact_dir=${2:?需要制品输出目录}
project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
adapter_source=$(CDPATH= cd -- "$adapter_source" && pwd)
mkdir -p "$artifact_dir"
artifact_dir=$(CDPATH= cd -- "$artifact_dir" && pwd)
expected_commit=$(node -e 'console.log(require(process.argv[1]).commit)' "$project_root/protocol/adapter-lock.json")
actual_commit=$(git -C "$adapter_source" rev-parse HEAD)
build_kind=release
case "${3:-}" in
  --local-acceptance) build_kind=local-acceptance ;;
  '')
    test "$actual_commit" = "$expected_commit" || { echo '适配器提交与 adapter-lock 不匹配' >&2; exit 1; }
    test -z "$(git -C "$adapter_source" status --porcelain)" || { echo '适配器构建源存在未提交修改' >&2; exit 1; }
    ;;
  *) echo '未知构建选项' >&2; exit 1 ;;
esac
test "$(node --version)" = 'v24.14.0'
test "$(uname -s)-$(uname -m)" = 'Linux-x86_64'
npm ci --prefix "$adapter_source" --no-audit --no-fund
npm ci --prefix "$adapter_source/packages/pi" --no-audit --no-fund
npm run build --prefix "$adapter_source/packages/pi"
stage=$(mktemp -d)
trap 'rm -r -- "$stage"' EXIT HUP INT TERM
runtime="$stage/pi-runtime"
mkdir -p "$runtime/bin" "$runtime/lib/scripts" "$stage/home"
cp "$(command -v node)" "$runtime/bin/node"
cp -R "$adapter_source/packages/pi/dist" "$adapter_source/packages/pi/node_modules" "$runtime/lib/"
cp "$adapter_source/packages/pi/package.json" "$adapter_source/packages/pi/package-lock.json" "$runtime/lib/"
cp "$adapter_source/LICENSE" "$runtime/"
cp "$project_root/protocol/adapter-lock.json" "$runtime/adapter-lock.json"
cp "$adapter_source/packages/pi/README.md" "$runtime/README.md"
cp "$adapter_source/scripts/pty-bridge.py" "$runtime/lib/scripts/"
cat > "$runtime/bin/pi-codex" <<'WRAPPER'
#!/bin/sh
set -eu
runtime_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec "$runtime_root/bin/node" "$runtime_root/lib/dist/pi/src/adapter.mjs" "$@"
WRAPPER
chmod 0755 "$runtime/bin/pi-codex" "$runtime/bin/node"
# 仅用构建依赖中的真实官方 CLI 检查版本；不安装、复制用户全局 CLI。
cat > "$stage/pi-test-cli" <<'WRAPPER'
#!/bin/sh
exec "$PI_TEST_RUNTIME/bin/node" "$PI_TEST_RUNTIME/lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js" "$@"
WRAPPER
chmod 0755 "$stage/pi-test-cli"
env -i PATH=/usr/bin:/bin HOME="$stage/home" PI_CLI="$stage/pi-test-cli" PI_TEST_RUNTIME="$runtime" \
  "$runtime/bin/pi-codex" --runtime-info > "$runtime/build.json"
env -i PATH=/usr/bin:/bin HOME="$stage/home" "$runtime/bin/pi-codex" --pty-self-check
test ! -d "$runtime/lib/node_modules/@anthropic-ai/claude-agent-sdk"
node --input-type=module - "$runtime" "$adapter_source" "$build_kind" "$actual_commit" <<'JS'
import {readFileSync,writeFileSync,readdirSync} from 'node:fs'
import {createHash} from 'node:crypto'
const root=process.argv[2]
const source=process.argv[3]
const info=JSON.parse(readFileSync(`${root}/build.json`))
const pin=JSON.parse(readFileSync(`${root}/adapter-lock.json`))
if(info.engine!=='pi' || info.nodeVersion!==pin.node || info.protocolVersion!==pin.codexProtocol ||
   info.sdkVersion!==pin.piCodingAgent || info.cliBuild!==pin.piCli ||
   info.pluginVersions['@narumitw/pi-plan-mode']!==pin.piPlanMode ||
   info.pluginVersions['@narumitw/pi-tui-kit']!==pin.piTuiKit ||
   info.pluginVersions['@gotgenes/pi-subagents']!==pin.piSubagents)
  throw Error('Pi 制品版本不符合 adapter-lock')
info.lockSha256=createHash('sha256').update(readFileSync(`${root}/lib/package-lock.json`)).digest('hex')
info.target='linux-amd64'
info.artifactKind=process.argv[4]
info.adapterCommit=process.argv[5]
const hash=createHash('sha256')
for(const directory of ['packages/pi/src','packages/shared/src']) {
  for(const file of readdirSync(`${source}/${directory}`).filter(name=>name.endsWith('.mts')).sort()) {
    hash.update(`${directory}/${file}\0`);hash.update(readFileSync(`${source}/${directory}/${file}`))
  }
}
info.sourceSha256=hash.digest('hex')
writeFileSync(`${root}/build.json`,JSON.stringify(info,null,2)+'\n')
JS
asset="pi-codex_${actual_commit}_linux_amd64.tar.gz"
if [ "$build_kind" = local-acceptance ]; then asset="pi-codex_${actual_commit}_local_linux_amd64.tar.gz"; fi
tar -C "$stage" -czf "$artifact_dir/$asset" pi-runtime
(cd "$artifact_dir" && sha256sum "$asset" > "$asset.sha256")
mkdir "$stage/unpacked"
tar -C "$stage/unpacked" -xzf "$artifact_dir/$asset"
env -i PATH=/usr/bin:/bin HOME="$stage/home" PI_CLI="$stage/pi-test-cli" PI_TEST_RUNTIME="$stage/unpacked/pi-runtime" \
  "$stage/unpacked/pi-runtime/bin/pi-codex" --runtime-info
env -i PATH=/usr/bin:/bin HOME="$stage/home" "$stage/unpacked/pi-runtime/bin/pi-codex" --pty-self-check
node "$adapter_source/packages/pi/test/artifact-smoke.mjs" "$stage/unpacked/pi-runtime"
echo "$artifact_dir/$asset"
