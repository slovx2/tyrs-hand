#!/bin/sh
set -eu

mode=${1:-core}
data_root=/data/worker
fixture_root=/fixtures

materialize() {
  name=$1
  suffix=$2
  cache="$data_root/repo-cache/fixture-$name/repository.git"
  workspace="$data_root/workspaces/github/$name-$suffix"
  if [ ! -d "$cache" ]; then
    source="/tmp/source-$name"
    cp -R "$fixture_root/$name" "$source"
    git -C "$source" init --initial-branch=main >/dev/null
    git -C "$source" config user.name "Runtime Fixture"
    git -C "$source" config user.email "runtime-fixture@example.invalid"
    git -C "$source" add --all
    git -C "$source" commit --message "fixture" >/dev/null
    mkdir -p "$(dirname "$cache")"
    git clone --bare "$source" "$cache" >/dev/null
  fi
  mkdir -p "$(dirname "$workspace")"
  git --git-dir="$cache" worktree add -b "fixture/$name/$suffix" "$workspace" HEAD >/dev/null
  printf '%s\n' "$workspace"
}

assert_clean() {
  workspace=$1
  git -C "$workspace" diff --exit-code
  test -z "$(git -C "$workspace" status --porcelain --untracked-files=no)"
}

run_core_fixture() {
  name=$1
  suffix=$2
  workspace=$(materialize "$name" "$suffix")
  case "$name" in
    python-uv)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- python test_fixture.py
      ;;
    python-requirements|python-requirements-conflict)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- python check.py
      ;;
    pnpm-single)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- pnpm run check
      ;;
    go-module)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- go test ./...
      ;;
    cargo-crate)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- cargo test --locked
      ;;
    no-runtime)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- sh -c true
      ;;
    auto-detect-root)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- python check.py
      test ! -e "$workspace/nested/node_modules"
      ;;
    runtime-monorepo)
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" --cwd api -- python check.py
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" --cwd web -- pnpm run check
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" --cwd service -- go test ./...
      tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" --cwd native -- cargo test --locked
      ;;
  esac
  assert_clean "$workspace"
}

assert_degraded() {
  name=$1
  workspace=$(materialize "$name" degraded)
  output=$(tyrs-hand-runtime prepare --data-root "$data_root" --workspace "$workspace")
  printf '%s\n' "$output" | grep '"status":"degraded"'
}

run_tauri() {
  suffix=$1
  workspace=$(materialize tauri-pnpm-cargo "$suffix")
  tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- pnpm run build
  tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" --cwd src-tauri -- cargo build --locked
  assert_clean "$workspace"
}

run_alternate() {
  suffix=$1
  workspace=$(materialize runtime-alternate "$suffix")
  tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- python -c 'import sys; assert sys.version_info[:3] == (3, 14, 6)'
  tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- node -e 'if (process.versions.node !== "22.23.1") process.exit(1)'
  tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- sh -c 'test "$(pnpm --version)" = "11.14.0"'
  tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- sh -c 'go version | grep "go1.25.12"'
  tyrs-hand-runtime exec --data-root "$data_root" --workspace "$workspace" -- sh -c 'rustc --version | grep "rustc 1.96.0"'
  assert_clean "$workspace"
}

case "$mode" in
  core)
    for name in python-uv python-requirements python-requirements-conflict pnpm-single go-module cargo-crate no-runtime auto-detect-root runtime-monorepo; do
      run_core_fixture "$name" cold
    done
    for name in invalid-yaml unknown-field non-exact-runtime missing-lockfile missing-toolchain-version dependency-command-failure; do
      assert_degraded "$name"
    done
    ;;
  offline)
    for name in python-uv python-requirements python-requirements-conflict pnpm-single go-module cargo-crate no-runtime auto-detect-root runtime-monorepo; do
      run_core_fixture "$name" offline
    done
    ;;
  tauri)
    run_tauri cold
    ;;
  tauri-offline)
    run_tauri offline
    ;;
  alternate)
    run_alternate matrix
    ;;
  alternate-offline)
    run_alternate matrix-offline
    ;;
  *)
    printf 'unknown runtime fixture mode: %s\n' "$mode" >&2
    exit 2
    ;;
esac
