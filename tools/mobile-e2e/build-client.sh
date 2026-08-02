#!/usr/bin/env bash
set -euo pipefail

platform="${1:?用法：build-client.sh android|ios}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
client="${root}/client"
app_id="com.tyrshand.app.dev"

if [[ "${platform}" != "android" && "${platform}" != "ios" ]]; then
  echo "平台必须是 android 或 ios" >&2
  exit 1
fi
if [[ "$(pnpm --version)" != "11.14.0" ]]; then
  echo "需要 pnpm 11.14.0" >&2
  exit 1
fi

pnpm --dir "${client}" install --frozen-lockfile
APP_ENV=development pnpm --dir "${client}" exec expo prebuild --clean --platform "${platform}" --no-install

if [[ "${platform}" == "android" ]]; then
  android_sdk_root="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}"
  if [[ -z "${android_sdk_root}" ]]; then
    android_sdk_root="$(cd "$(dirname "$(command -v adb)")/.." && pwd)"
  fi
  if [[ ! -d "${android_sdk_root}/platform-tools" ]]; then
    echo "无法从 ANDROID_HOME/ANDROID_SDK_ROOT/adb 定位 Android SDK" >&2
    exit 1
  fi
  export ANDROID_HOME="${android_sdk_root}"
  android_serial="${ANDROID_SERIAL:-}"
  if [[ -z "${android_serial}" ]]; then
    android_emulators="$(adb devices | awk '$1 ~ /^emulator-/ && $2 == "device" { print $1 }')"
    android_emulator_count="$(printf '%s\n' "${android_emulators}" | awk 'NF { count++ } END { print count + 0 }')"
    if [[ "${android_emulator_count}" -ne 1 ]]; then
      echo "需要且只能有一个 Android 模拟器在线；不会向真实设备安装 E2E 应用" >&2
      exit 1
    fi
    android_serial="${android_emulators}"
  fi
  if [[ "${android_serial}" != emulator-* ]]; then
    echo "ANDROID_SERIAL 必须指向 emulator-*，不会向真实设备安装 E2E 应用" >&2
    exit 1
  fi
  export ANDROID_SERIAL="${android_serial}"
  (
    cd "${client}/android"
    ./gradlew --no-daemon --stacktrace assembleRelease
  )
  apk="${client}/android/app/build/outputs/apk/release/app-release.apk"
  test -f "${apk}"
  adb install -r "${apk}"
  adb shell pm path "${app_id}" >/dev/null
  exit 0
fi

workspace="$(find "${client}/ios" -maxdepth 1 -name '*.xcworkspace' -print -quit)"
test -n "${workspace}"
scheme="$(basename "${workspace}" .xcworkspace)"
derived="${client}/.e2e-build/ios"
xcodebuild -workspace "${workspace}" -scheme "${scheme}" -configuration Release \
  -sdk iphonesimulator -derivedDataPath "${derived}" CODE_SIGNING_ALLOWED=NO build
app="$(find "${derived}/Build/Products" -path '*Release-iphonesimulator/*.app' -print -quit)"
test -n "${app}"
xcrun simctl install booted "${app}"
xcrun simctl get_app_container booted "${app_id}" app >/dev/null
