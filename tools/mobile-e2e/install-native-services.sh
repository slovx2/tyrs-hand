#!/usr/bin/env bash
set -euo pipefail

# iOS runner 没有 Docker；源码与 SHA256 一并固定，避免 Homebrew 自动升级。
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
prefix="${root}/.local/mobile-services/pg18.3-redis8.4.0-openssl3.5.4"
build_dir="$(mktemp -d /tmp/tyrs-mobile-services.XXXXXX)"
trap 'rm -rf "${build_dir}"' EXIT
mkdir -p "${prefix}"
jobs="$(sysctl -n hw.ncpu)"
cd "${build_dir}"

# pgcrypto 是正式迁移的依赖，不能为测试删掉扩展或改写迁移。
curl --fail --location --retry 3 --output openssl.tar.gz \
  https://github.com/openssl/openssl/releases/download/openssl-3.5.4/openssl-3.5.4.tar.gz
printf '%s  %s\n' 967311f84955316969bdb1d8d4b983718ef42338639c621ec4c34fddef355e99 \
  openssl.tar.gz | shasum -a 256 --check
tar -xf openssl.tar.gz
(
  cd openssl-3.5.4
  ./Configure --prefix="${prefix}/openssl" --libdir=lib shared no-tests
  make -j "${jobs}"
  make install_sw
)

# 官方摘要：https://ftp.postgresql.org/pub/source/v18.3/postgresql-18.3.tar.bz2.sha256
curl --fail --location --retry 3 --output postgresql.tar.bz2 \
  https://ftp.postgresql.org/pub/source/v18.3/postgresql-18.3.tar.bz2
printf '%s  %s\n' d95663fbbf3a80f81a9d98d895266bdcb74ba274bcc04ef6d76630a72dee016f \
  postgresql.tar.bz2 | shasum -a 256 --check
tar -xf postgresql.tar.bz2
(
  cd postgresql-18.3
  CPPFLAGS="-I${prefix}/openssl/include" \
    LDFLAGS="-L${prefix}/openssl/lib -Wl,-rpath,${prefix}/openssl/lib" \
    PKG_CONFIG_PATH="${prefix}/openssl/lib/pkgconfig" \
    ./configure --prefix="${prefix}" --without-icu --without-readline --without-zlib --with-ssl=openssl
  make -j "${jobs}"
  make install
  make -C contrib/pgcrypto -j "${jobs}"
  make -C contrib/pgcrypto install
)
# 官方摘要：https://github.com/redis/redis-hashes/blob/master/README
curl --fail --location --retry 3 --output redis.tar.gz \
  https://download.redis.io/releases/redis-8.4.0.tar.gz
printf '%s  %s\n' ca909aa15252f2ecb3a048cd086469827d636bf8334f50bb94d03fba4bfc56e8 \
  redis.tar.gz | shasum -a 256 --check
tar -xf redis.tar.gz
make -C redis-8.4.0 -j "${jobs}" MALLOC=libc BUILD_TLS=no
make -C redis-8.4.0 PREFIX="${prefix}" install
"${prefix}/bin/postgres" --version | grep -Fx 'postgres (PostgreSQL) 18.3'
"${prefix}/bin/redis-server" --version | grep -F 'v=8.4.0 '
if [[ -n "${GITHUB_PATH:-}" ]]; then printf '%s\n' "${prefix}/bin" >> "${GITHUB_PATH}"; fi
