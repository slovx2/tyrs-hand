#!/bin/sh
# 从独立仓库固定提交构建 Claude 制品；不使用宿主浮动 Node 或 Claude CLI。
set -eu
adapter_source=${1:?需要适配器仓库目录}
artifact_dir=${2:?需要制品输出目录}
project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
adapter_source=$(CDPATH= cd -- "$adapter_source" && pwd)
mkdir -p "$artifact_dir"
artifact_dir=$(CDPATH= cd -- "$artifact_dir" && pwd)
expected_commit=$(node -e 'console.log(require(process.argv[1]).commit)' "$project_root/protocol/adapter-lock.json")
actual_commit=$(git -C "$adapter_source" rev-parse HEAD)
test "$actual_commit" = "$expected_commit" || { echo '适配器提交与 adapter-lock 不匹配' >&2; exit 1; }
test -z "$(git -C "$adapter_source" status --porcelain)" || { echo '适配器构建源存在未提交修改' >&2; exit 1; }
test "$(node --version)" = 'v24.14.0' || { echo '必须使用 Node 24.14.0' >&2; exit 1; }
test "$(uname -s)" = 'Linux' || { echo '本制品必须在 Linux 构建' >&2; exit 1; }
test "$(uname -m)" = 'x86_64' || { echo '本制品必须在 amd64 构建' >&2; exit 1; }
npm ci --prefix "$adapter_source"
npm run build --prefix "$adapter_source"
npm prune --omit=dev --prefix "$adapter_source"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
mkdir -p "$stage/claude-runtime/bin" "$stage/claude-runtime/lib" "$stage/home"
cp "$(command -v node)" "$stage/claude-runtime/bin/node"
cp -R "$adapter_source/dist" "$adapter_source/node_modules" "$stage/claude-runtime/lib/"
cp "$adapter_source/package.json" "$adapter_source/LICENSE" "$stage/claude-runtime/lib/"
cp "$project_root/protocol/adapter-lock.json" "$stage/claude-runtime/adapter-lock.json"
cat > "$stage/claude-runtime/bin/claude-codex" <<'WRAPPER'
#!/bin/sh
set -eu
runtime_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec "$runtime_root/bin/node" "$runtime_root/lib/dist/src/adapter.mjs" "$@"
WRAPPER
chmod 0755 "$stage/claude-runtime/bin/claude-codex" "$stage/claude-runtime/bin/node"
env -i PATH=/usr/bin:/bin HOME="$stage/home" "$stage/claude-runtime/bin/claude-codex" --runtime-info > "$stage/claude-runtime/build.json"
node -e 'const fs=require("node:fs"); const build=JSON.parse(fs.readFileSync(process.argv[1],"utf8")); if(build.nodeVersion!=="24.14.0" || build.sdkVersion!=="0.3.282" || build.protocolVersion!=="0.147.0" || !build.cliSha256) process.exit(1)' "$stage/claude-runtime/build.json"
asset="claude-codex_${actual_commit}_linux_amd64.tar.gz"
tar -C "$stage" -czf "$artifact_dir/$asset" claude-runtime
(cd "$artifact_dir" && sha256sum "$asset" > "$asset.sha256")
echo "$asset"
