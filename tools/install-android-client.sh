#!/usr/bin/env bash

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
client="${root}/client"
expected_app_id="com.tyrshand.app"
app_env="production"
preview_mode="false"
build_cache_key="production"
install_label="正式"
device=""

usage() {
  echo "用法：$0 [--device <adb-serial>] [--dev | --dev-real]" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --device)
      if [[ $# -lt 2 || -z "$2" ]]; then
        usage
        exit 2
      fi
      device="$2"
      shift 2
      ;;
    --dev)
      expected_app_id="com.tyrshand.app.dev"
      app_env="development"
      preview_mode="true"
      build_cache_key="preview"
      install_label="预览数据"
      shift
      ;;
    --dev-real)
      expected_app_id="com.tyrshand.app.dev"
      app_env="development"
      preview_mode="false"
      build_cache_key="development-real"
      install_label="真实服务 E2E"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

if [[ "$(pnpm --version)" != "11.14.0" ]]; then
  echo "需要 pnpm 11.14.0" >&2
  exit 1
fi

adb_bin="$(command -v adb)"
android_sdk_root="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}"
if [[ -z "${android_sdk_root}" ]]; then
  android_sdk_root="$(cd "$(dirname "${adb_bin}")/.." && pwd)"
fi
if [[ ! -d "${android_sdk_root}/platform-tools" ]]; then
  echo "无法从 ANDROID_HOME/ANDROID_SDK_ROOT/adb 定位 Android SDK" >&2
  exit 1
fi
export ANDROID_HOME="${android_sdk_root}"

apkanalyzer_bin="$(command -v apkanalyzer || true)"
if [[ -z "${apkanalyzer_bin}" ]]; then
  apkanalyzer_bin="${android_sdk_root}/cmdline-tools/latest/bin/apkanalyzer"
fi
if [[ ! -x "${apkanalyzer_bin}" ]]; then
  echo "Android SDK 中缺少 apkanalyzer" >&2
  exit 1
fi

adb_args=()
if [[ -n "${device}" ]]; then
  adb_args=(-s "${device}")
fi

"${adb_bin}" "${adb_args[@]}" get-state >/dev/null
pnpm --dir "${client}" install --frozen-lockfile
build_env=(
  "APP_ENV=${app_env}"
  "EXPO_PUBLIC_TYRS_HAND_PREVIEW=${preview_mode}"
  "NODE_ENV=production"
  "CI=1"
)
env "${build_env[@]}" pnpm --dir "${client}" exec expo prebuild --clean --platform android --no-install

build_gradle="${client}/android/app/build.gradle"
patched_gradle="$(mktemp "${build_gradle}.XXXXXX")"
trap 'rm -f "${patched_gradle}"' EXIT
awk '
  /^[[:space:]]*buildTypes[[:space:]]*\{/ { in_build_types = 1 }
  in_build_types && /^[[:space:]]*release[[:space:]]*\{/ && !patched {
    print
    print "            debuggable true"
    patched = 1
    next
  }
  { print }
  END { if (!patched) exit 42 }
' "${build_gradle}" >"${patched_gradle}" || {
  echo "无法在生成的 Android Release buildType 中启用 debuggable" >&2
  exit 1
}
mv "${patched_gradle}" "${build_gradle}"

build_cache_root="${root}/.local/android-build/${build_cache_key}"
gradle_project_cache="${client}/android/.gradle-${build_cache_key}"
metro_tmp="${build_cache_root}/metro-tmp"
mkdir -p "${gradle_project_cache}" "${metro_tmp}"

(
  cd "${client}/android"
  env "${build_env[@]}" "TMPDIR=${metro_tmp}" ./gradlew \
    --no-daemon \
    --no-build-cache \
    --project-cache-dir "${gradle_project_cache}" \
    --stacktrace \
    assembleRelease
)

apk="${client}/android/app/build/outputs/apk/release/app-release.apk"
if [[ ! -f "${apk}" ]]; then
  echo "Release APK 未生成：${apk}" >&2
  exit 1
fi

actual_app_id="$("${apkanalyzer_bin}" manifest application-id "${apk}")"
if [[ "${actual_app_id}" != "${expected_app_id}" ]]; then
  echo "APK 包名错误：期望 ${expected_app_id}，实际 ${actual_app_id}" >&2
  exit 1
fi
if [[ "$("${apkanalyzer_bin}" manifest debuggable "${apk}")" != "true" ]]; then
  echo "APK 不可调试" >&2
  exit 1
fi
if ! unzip -Z1 "${apk}" | awk '
  $0 == "assets/index.android.bundle" { found = 1 }
  END { if (!found) exit 1 }
'; then
  echo "APK 未内置 JavaScript bundle" >&2
  exit 1
fi

"${adb_bin}" "${adb_args[@]}" install -r "${apk}"
"${adb_bin}" "${adb_args[@]}" shell pm path "${expected_app_id}" >/dev/null

echo "已安装 ${expected_app_id}：Release、可调试、内置 JavaScript（${install_label}）"
echo "APK：${apk}"
