#!/usr/bin/env bash
set -euo pipefail

version="2.3.0"
sha256="aaf524c6bcd456013855b1337464f964d9a65e2fb88861affea9b4c014644e50"
install_root="${TYRS_HAND_MAESTRO_ROOT:-${HOME}/.local}"
launcher="${install_root}/bin/maestro"
maestro_home="${install_root}/share/maestro/${version}"

if [[ -x "${launcher}" ]] && [[ "$("${launcher}" --version 2>/dev/null)" == *"${version}"* ]]; then
  exit 0
fi

temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT
archive="${temporary}/maestro.zip"
url="https://github.com/mobile-dev-inc/maestro/releases/download/cli-${version}/maestro.zip"
curl --fail --location --retry 4 --output "${archive}" "${url}"
actual="$(shasum -a 256 "${archive}" | awk '{print $1}')"
if [[ "${actual}" != "${sha256}" ]]; then
  echo "Maestro ${version} 校验和不匹配：${actual}" >&2
  exit 1
fi
unzip -q "${archive}" -d "${temporary}/unpacked"
source_root="${temporary}/unpacked"
if [[ -d "${source_root}/maestro" ]]; then
  source_root="${source_root}/maestro"
fi
test -x "${source_root}/bin/maestro"
mkdir -p "${install_root}/bin" "$(dirname "${maestro_home}")"
rm -rf "${maestro_home}"
cp -R "${source_root}" "${maestro_home}"
cat >"${launcher}" <<EOF
#!/usr/bin/env bash
exec "${maestro_home}/bin/maestro" "\$@"
EOF
chmod +x "${launcher}"
"${launcher}" --version
