# syntax=docker/dockerfile:1.19.0
FROM --platform=$BUILDPLATFORM node:24.14.0-bookworm-slim@sha256:d8e448a56fc63242f70026718378bd4b00f8c82e78d20eefb199224a4d8e33d8 AS web-build
WORKDIR /src/web
RUN npm install --global pnpm@11.14.0
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
COPY web/tools ./tools
RUN pnpm install --frozen-lockfile
COPY web/index.html web/tsconfig.app.json web/tsconfig.json web/tsconfig.node.json web/vite.config.ts ./
COPY web/src ./src
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY ent ./ent
COPY internal ./internal
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-worker ./cmd/tyrs-hand-worker && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-runtime ./cmd/tyrs-hand-runtime && \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-reply-hook ./cmd/tyrs-hand-reply-hook && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-admin ./cmd/tyrs-hand-admin && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-discord ./cmd/tyrs-hand-discord
COPY --from=web-build /src/internal/web/dist ./internal/web/dist
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-server ./cmd/tyrs-hand-server

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS control
RUN apt-get update && apt-get install --yes --no-install-recommends \
      git=1:2.39.5-0+deb12u3 \
      openssh-client=1:9.2p1-2+deb12u10 \
      ca-certificates=20230311+deb12u1 \
      tini=0.19.0-1+b3 && \
    rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 tyrs-hand && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/tyrs-hand tyrs-hand
COPY --from=go-build --chown=root:root /out/tyrs-hand-server /usr/local/bin/tyrs-hand-server
COPY --from=go-build --chown=root:root /out/tyrs-hand-admin /usr/local/bin/tyrs-hand-admin
COPY --from=go-build --chown=root:root /out/tyrs-hand-discord /usr/local/bin/tyrs-hand-discord
USER 10001:10001
WORKDIR /home/tyrs-hand
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["tyrs-hand-server"]

FROM node:24.14.0-bookworm-slim@sha256:d8e448a56fc63242f70026718378bd4b00f8c82e78d20eefb199224a4d8e33d8 AS worker
ARG TARGETARCH
COPY internal/devenv/worker-runtime.lock.json /usr/local/share/tyrs-hand/worker-runtime.lock.json
RUN set -eux; \
    runtime_lock=/usr/local/share/tyrs-hand/worker-runtime.lock.json; \
    test "$(node -p "require('${runtime_lock}').defaults.node")" = "$(node --version | sed 's/^v//')"; \
    packages="$(node -e 'const p=require(process.argv[1]).systemPackages; console.log(Object.entries(p).map(([name, version]) => `${name}=${version}`).join(" "))' "${runtime_lock}")"; \
    apt-get update && apt-get install --yes --no-install-recommends ${packages} && \
    rm -rf /var/lib/apt/lists/* && \
    codex_version="$(node -p "require('${runtime_lock}').codex")"; \
    corepack_version="$(node -p "require('${runtime_lock}').corepack")"; \
    npm install --global --omit=dev "@openai/codex@${codex_version}" "corepack@${corepack_version}" && \
    npm cache clean --force && \
    codex_native="$(find /usr/local/lib/node_modules/@openai/codex -path '*/vendor/*/bin/codex' -type f -print -quit)" && \
    test -n "${codex_native}" && ln -s "${codex_native}" /usr/local/bin/apply_patch && \
    rm -rf /usr/local/lib/node_modules/npm /opt/yarn-v1.22.22 && \
    rm -f /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/yarn /usr/local/bin/yarnpkg /usr/local/bin/pnpm /usr/local/bin/pnpx
RUN set -eux; \
    runtime_lock=/usr/local/share/tyrs-hand/worker-runtime.lock.json; \
    mise_version="$(node -p "require('${runtime_lock}').mise")"; \
    uv_version="$(node -p "require('${runtime_lock}').uv")"; \
    mise_sha="$(node -p "require('${runtime_lock}').downloads.mise['${TARGETARCH}']")"; \
    uv_sha="$(node -p "require('${runtime_lock}').downloads.uv['${TARGETARCH}']")"; \
    test -n "${mise_sha}"; test -n "${uv_sha}"; \
    case "${TARGETARCH}" in \
      amd64) mise_arch=x64; uv_arch=x86_64 ;; \
      arm64) mise_arch=arm64; uv_arch=aarch64 ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    mise_asset="mise-v${mise_version}-linux-${mise_arch}"; \
    curl --fail --location --silent --show-error --output /tmp/mise "https://github.com/jdx/mise/releases/download/v${mise_version}/${mise_asset}"; \
    echo "${mise_sha}  /tmp/mise" | sha256sum --check --strict; \
    install -m 0755 /tmp/mise /usr/local/bin/mise; \
    uv_asset="uv-${uv_arch}-unknown-linux-gnu.tar.gz"; \
    curl --fail --location --silent --show-error --output /tmp/uv.tar.gz "https://github.com/astral-sh/uv/releases/download/${uv_version}/${uv_asset}"; \
    echo "${uv_sha}  /tmp/uv.tar.gz" | sha256sum --check --strict; \
    tar -xzf /tmp/uv.tar.gz -C /tmp; \
    install -m 0755 "/tmp/uv-${uv_arch}-unknown-linux-gnu/uv" /usr/local/bin/uv; \
    install -m 0755 "/tmp/uv-${uv_arch}-unknown-linux-gnu/uvx" /usr/local/bin/uvx; \
    rm -rf /tmp/mise /tmp/uv.tar.gz "/tmp/uv-${uv_arch}-unknown-linux-gnu"
RUN groupadd --gid 10001 tyrs-hand && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/tyrs-hand tyrs-hand && \
    install -d -o tyrs-hand -g tyrs-hand -m 0750 /data/worker
COPY --from=go-build --chown=root:root /out/tyrs-hand-worker /usr/local/bin/tyrs-hand-worker
COPY --from=go-build --chown=root:root /out/tyrs-hand-runtime /usr/local/bin/tyrs-hand-runtime
COPY --from=go-build --chown=root:root /out/tyrs-hand-reply-hook /usr/local/bin/tyrs-hand-reply-hook
COPY --from=go-build --chown=root:root /out/tyrs-hand-admin /usr/local/bin/tyrs-hand-admin
USER 10001:10001
WORKDIR /home/tyrs-hand
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["tyrs-hand-worker"]
