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
export EXPO_PUBLIC_TYRS_HAND_PREVIEW_PERF=true
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
  # 只按实际模拟器 ABI 构建，避免无关架构的 native debug metadata 合并耗尽堆。
  android_abi="$(adb -s "${android_serial}" shell getprop ro.product.cpu.abi)"
  android_abi="${android_abi//$'\r'/}"
  case "${android_abi}" in
    arm64-v8a|armeabi-v7a|x86|x86_64) ;;
    *) echo "模拟器返回空或不支持的 Android ABI" >&2; exit 1 ;;
  esac
  (
    cd "${client}/android"
    ./gradlew --no-daemon --stacktrace "-PreactNativeArchitectures=${android_abi}" assembleRelease
  )
  apk="${client}/android/app/build/outputs/apk/release/app-release.apk"
  test -f "${apk}"
  # Gradle 准备完 SDK 后，使用固定 React Native 0.81.5 对应的 buildTools 检查产物。
  android_aapt="${android_sdk_root}/build-tools/36.0.0/aapt"
  test -x "${android_aapt}"
  # 检查最终 APK，不能仅凭 Gradle 接受参数认定没有混入其他 ABI。
  node - "${android_aapt}" "${apk}" "${android_abi}" <<'JS'
const { execFileSync, spawn } = require('node:child_process');
const [aapt, apk, expected] = process.argv.slice(2);
const badging = execFileSync(aapt, ['dump', 'badging', apk], { encoding: 'utf8' });
const nativeLines = badging.split(/\r?\n/).filter(line => line.startsWith('native-code:'));
if (nativeLines.length !== 1 || nativeLines[0] !== `native-code: '${expected}'`) {
  throw new Error('APK native-code 必须且只能包含目标模拟器 ABI');
}
const entries = execFileSync('unzip', ['-Z1', apk], { encoding: 'utf8' }).split(/\r?\n/);
const libraries = entries.filter(entry => entry.startsWith('lib/') && entry.endsWith('.so'));
if (!libraries.length || libraries.some(entry => {
  const match = entry.match(/^lib\/([^/]+)\/[^/]+\.so$/);
  return !match || match[1] !== expected;
})) {
  throw new Error('APK 原生 .so 目录必须且只能包含目标模拟器 ABI');
}
// 流式检查每个库的 ELF 头，避免只改目录名的错误产物通过，也不将整库载入堆。
const elfIdentity = { 'arm64-v8a': [2, 183], 'armeabi-v7a': [1, 40], x86: [1, 3], x86_64: [2, 62] };
async function verifyLibrary(library) {
  const header = await new Promise((resolve, reject) => {
    const child = spawn('unzip', ['-p', apk, library], { stdio: ['ignore', 'pipe', 'ignore'] });
    const prefix = Buffer.alloc(20);
    let length = 0;
    child.stdout.on('data', chunk => {
      if (length < prefix.length) length += chunk.copy(prefix, length, 0, prefix.length - length);
    });
    child.on('error', reject);
    child.on('close', code => {
      if (code !== 0 || length !== prefix.length) reject(new Error('APK 原生库读取失败或 ELF 头不完整'));
      else resolve(prefix);
    });
  });
  const [elfClass, machine] = elfIdentity[expected];
  if (header.subarray(0, 4).toString('hex') !== '7f454c46' || header[4] !== elfClass ||
    header[5] !== 1 || header.readUInt16LE(18) !== machine) {
    throw new Error('APK 原生 .so 的实际 ELF 架构与目标模拟器 ABI 不一致');
  }
}
(async () => {
  for (const library of libraries) await verifyLibrary(library);
  console.log(JSON.stringify({ emulatorABI: expected, apkNativeCode: expected,
    nativeLibraries: libraries.length, passed: true }));
})().catch(error => { console.error(error); process.exitCode = 1; });
JS
  adb -s "${android_serial}" install -r "${apk}"
  adb -s "${android_serial}" shell pm path "${app_id}" >/dev/null
  exit 0
fi

if [[ "$(pod _1.16.2_ --version)" != "1.16.2" ]]; then
  echo "iOS E2E 需要 CocoaPods 1.16.2" >&2
  exit 1
fi
# prebuild --no-install 只生成项目，真实原生依赖必须安装后才存在 xcworkspace。
(
  cd "${client}/ios"
  pod _1.16.2_ install
)
workspace="$(find "${client}/ios" -maxdepth 1 -name '*.xcworkspace' -print -quit)"
test -n "${workspace}"
scheme="$(basename "${workspace}" .xcworkspace)"
derived="${client}/.e2e-build/ios"
xcodebuild -workspace "${workspace}" -scheme "${scheme}" -configuration Release \
  -sdk iphonesimulator -derivedDataPath "${derived}" CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- build
app="$(find "${derived}/Build/Products" -path '*Release-iphonesimulator/*.app' -print -quit)"
test -n "${app}"
# 模拟器的有效身份由链接器嵌入 Mach-O，常规签名字典可能为空；必须校验最终二进制。
signing_diagnostics="$derived/signing-diagnostics"
mkdir -p "$signing_diagnostics"
signing_stage="verify-signature"
trap 'signing_exit=$?; printf "{\"stage\":\"%s\",\"status\":\"failed\",\"exitCode\":%s}\n" "$signing_stage" "$signing_exit" > "$signing_diagnostics/status.json"; printf "iOS 构建校验失败：%s（exit %s）\n" "$signing_stage" "$signing_exit" >&2; exit "$signing_exit"' ERR
signing_progress() {
  signing_stage="$1"
  printf '{"stage":"%s","status":"running"}\n' "$signing_stage" > "$signing_diagnostics/status.json"
}
signing_progress verify-signature
codesign --verify --deep --strict "${app}"
signing_progress inspect-bundle
test "$(plutil -extract CFBundleIdentifier raw -o - "$app/Info.plist")" = "$app_id"
bundle_executable="$(plutil -extract CFBundleExecutable raw -o - "$app/Info.plist")"
test -n "$bundle_executable"
test -f "$app/$bundle_executable"
signing_progress list-architectures
simulator_architecture_names="$(xcrun lipo -archs "$app/$bundle_executable")"
test -n "$simulator_architecture_names"
read -r -a simulator_architectures <<< "$simulator_architecture_names"
test "${#simulator_architectures[@]}" -gt 0
for architecture in "${simulator_architectures[@]}"; do
  if [[ "$architecture" != arm64 && "$architecture" != x86_64 ]]; then
    echo "iOS 模拟器二进制包含不支持的架构" >&2
    false
  fi
  signing_progress "extract-entitlements-$architecture"
  dump="$derived/simulator-entitlements-$architecture.txt"
  entitlements="$derived/simulator-entitlements-$architecture.plist"
  # 不使用 --macho：该输出按机器字序显示；通用模式按原始字节顺序输出。
  xcrun llvm-objdump --arch="$architecture" --full-contents --section=__entitlements \
    "$app/$bundle_executable" > "$dump"
  node - "$dump" "$entitlements" <<'JS'
const fs = require('node:fs');
const text = fs.readFileSync(process.argv[2], 'utf8');
if ((text.match(/^Contents of section __TEXT,__entitlements:$/gm) ?? []).length !== 1) {
  throw new Error('最终 Mach-O 缺少唯一的模拟器 entitlements section');
}
const chunks = [];
for (const line of text.split('\n')) {
  const match = line.match(/^\s*[0-9a-fA-F]+\s+(.+)$/);
  if (!match) continue;
  const words = match[1].split(/\s{2,}/, 1)[0].trim().split(/\s+/);
  if (!words.every(word => /^(?:[0-9a-fA-F]{2}){1,4}$/.test(word))) {
    throw new Error('模拟器 entitlements section 字节无效');
  }
  chunks.push(Buffer.from(words.join(''), 'hex'));
}
if (!chunks.length) throw new Error('模拟器 entitlements section 为空');
fs.writeFileSync(process.argv[3], Buffer.concat(chunks));
JS
  signing_progress "read-identity-$architecture"
  application_identifier="$(plutil -extract application-identifier raw -o - "$entitlements")"
  signing_progress "validate-identity-$architecture"
  if [[ "$application_identifier" != "$app_id" && "$application_identifier" != *."$app_id" ]]; then
    echo "iOS 模拟器二进制缺少与应用匹配的 application-identifier" >&2
    false
  fi
  node -e 'require("node:fs").writeFileSync(process.argv[3],JSON.stringify({
    architecture:process.argv[1],applicationIdentifier:process.argv[2],passed:true})+"\n")' \
    "$architecture" "$application_identifier" "$signing_diagnostics/identity-$architecture.json"
done
signing_progress install-app
xcrun simctl install booted "${app}"
signing_progress verify-install
xcrun simctl get_app_container booted "${app_id}" app >/dev/null
printf '{"stage":"verify-install","status":"completed"}\n' > "$signing_diagnostics/status.json"
trap - ERR
