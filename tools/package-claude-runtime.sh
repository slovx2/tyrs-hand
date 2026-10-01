#!/bin/sh
# 保留固定 Node 和 SDK；Claude CLI 来自宿主独立安装，版本由 adapter-lock 校验。
set -eu
adapter_source=${1:?需要适配器仓库目录}
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
test "$(node --version)" = 'v24.14.0' || { echo '必须使用 Node 24.14.0' >&2; exit 1; }
test "$(uname -s)" = 'Linux' || { echo '本制品必须在 Linux 构建' >&2; exit 1; }
test "$(uname -m)" = 'x86_64' || { echo '本制品必须在 amd64 构建' >&2; exit 1; }
npm ci --prefix "$adapter_source"
npm run build --prefix "$adapter_source"
npm prune --omit=dev --omit=optional --prefix "$adapter_source"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
mkdir -p "$stage/claude-runtime/bin" "$stage/claude-runtime/lib/scripts" "$stage/home"
cp "$(command -v node)" "$stage/claude-runtime/bin/node"
cp -R "$adapter_source/dist" "$adapter_source/node_modules" "$stage/claude-runtime/lib/"
cp "$adapter_source/package.json" "$adapter_source/package-lock.json" "$adapter_source/LICENSE" "$stage/claude-runtime/lib/"
# PTY 终端依赖 scripts/pty-bridge.py，适配器按 lib/dist/src/../../scripts 查找。
cp "$adapter_source/scripts/pty-bridge.py" "$stage/claude-runtime/lib/scripts/"
cp "$project_root/protocol/adapter-lock.json" "$stage/claude-runtime/adapter-lock.json"
cat > "$stage/claude-runtime/bin/claude-codex" <<'WRAPPER'
#!/bin/sh
set -eu
runtime_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec "$runtime_root/bin/node" "$runtime_root/lib/dist/src/adapter.mjs" "$@"
WRAPPER
chmod 0755 "$stage/claude-runtime/bin/claude-codex" "$stage/claude-runtime/bin/node"
# 构建验收使用隔离安装的真实固定 CLI，不把它复制进制品，不修改宿主全局安装。
cli_version=$(node -e 'console.log(require(process.argv[1]).claudeCli)' "$project_root/protocol/adapter-lock.json")
npm install --prefix "$stage/host-cli" --save-exact "@anthropic-ai/claude-code@$cli_version" --no-audit --no-fund
host_cli="$stage/host-cli/node_modules/.bin/claude"
env -i PATH=/usr/bin:/bin HOME="$stage/home" CLAUDE_CONFIG_DIR="$stage/home/claude" DISABLE_AUTOUPDATER=1 \
  CLAUDE_CODEX_CLI="$host_cli" "$stage/claude-runtime/bin/claude-codex" --runtime-info > "$stage/claude-runtime/build.json"
node --input-type=module - "$stage/claude-runtime" "$build_kind" "$adapter_source" <<'JS'
import {readFileSync,writeFileSync,readdirSync} from 'node:fs'
import {createHash} from 'node:crypto'
const [root,kind,source]=process.argv.slice(2)
const build=JSON.parse(readFileSync(`${root}/build.json`))
const pin=JSON.parse(readFileSync(`${root}/adapter-lock.json`))
if(build.nodeVersion!==pin.node || build.sdkVersion!==pin.claudeAgentSdk ||
   build.protocolVersion!==pin.codexProtocol || build.cliBuild!==`${pin.claudeCli} (Claude Code)` ||
   !build.cliSha256) throw Error('Claude 制品版本不符合 adapter-lock')
const assertNoNative=(dir)=>{
  for(const entry of readdirSync(dir,{withFileTypes:true})) {
    if(entry.name.startsWith('claude-agent-sdk-')) throw Error(`制品不应包含原生 SDK 包: ${entry.name}`)
    if(entry.isDirectory()) assertNoNative(`${dir}/${entry.name}`)
  }
}
assertNoNative(`${root}/lib/node_modules`)
const hash=createHash('sha256')
const hashSources=(dir)=>{
  for(const entry of readdirSync(`${source}/${dir}`,{withFileTypes:true}).sort((a,b)=>a.name.localeCompare(b.name))) {
    const path=`${dir}/${entry.name}`
    if(entry.isDirectory()) hashSources(path)
    else { hash.update(`${path}\0`);hash.update(readFileSync(`${source}/${path}`)) }
  }
}
hashSources('src');hashSources('packages/shared/src')
build.sourceSha256=hash.digest('hex')
build.artifactKind=kind
build.cliSource='host'
writeFileSync(`${root}/build.json`,JSON.stringify(build,null,2)+'\n')
JS
env -i PATH=/usr/bin:/bin HOME="$stage/home" "$stage/claude-runtime/bin/claude-codex" --pty-self-check
asset="claude-codex_${actual_commit}_linux_amd64.tar.gz"
if [ "$build_kind" = local-acceptance ]; then asset="claude-codex_${actual_commit}_local_linux_amd64.tar.gz"; fi
tar -C "$stage" -czf "$artifact_dir/$asset" claude-runtime
(cd "$artifact_dir" && sha256sum "$asset" > "$asset.sha256")
mkdir "$stage/unpacked"
tar -C "$stage/unpacked" -xzf "$artifact_dir/$asset"
unpacked="$stage/unpacked/claude-runtime"
env -i PATH=/usr/bin:/bin HOME="$stage/home" CLAUDE_CONFIG_DIR="$stage/home/claude" DISABLE_AUTOUPDATER=1 \
  CLAUDE_CODEX_CLI="$host_cli" "$unpacked/bin/claude-codex" --runtime-info
env -i PATH=/usr/bin:/bin HOME="$stage/home" "$unpacked/bin/claude-codex" --pty-self-check
(cd "$unpacked/lib" && env -i PATH="$unpacked/bin:/usr/bin:/bin" HOME="$stage/home" \
  CODEX_SCHEMA_DIR="$project_root/protocol/codex-app-server/0.157.1/json-schema" \
  CLAUDE_CODEX_CLI="$host_cli" "$unpacked/bin/node" --test dist/test/native-image-reference.test.mjs)
echo 'ARTIFACT PASS: 无原生 SDK 平台包，宿主 CLI 身份、解包启动、PTY、真实 SDK mock 回合'
echo "$asset"
