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
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-admin ./cmd/tyrs-hand-admin && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-discord ./cmd/tyrs-hand-discord
COPY --from=web-build /src/internal/web/dist ./internal/web/dist
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-server ./cmd/tyrs-hand-server

FROM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS go-runtime

FROM node:24.14.0-bookworm-slim@sha256:d8e448a56fc63242f70026718378bd4b00f8c82e78d20eefb199224a4d8e33d8 AS runtime
RUN apt-get update && apt-get install --yes --no-install-recommends \
      git=1:2.39.5-0+deb12u3 \
      openssh-client=1:9.2p1-2+deb12u10 \
      ca-certificates=20230311+deb12u1 \
      libcap2=1:2.66-4+deb12u3+b1 \
      libgnutls30=3.7.9-2+deb12u7 \
      tini=0.19.0-1+b3 && \
    rm -rf /var/lib/apt/lists/* && \
    npm install --global --omit=dev @openai/codex@0.142.5 && \
    npm cache clean --force && \
    codex_native="$(find /usr/local/lib/node_modules/@openai/codex -path '*/vendor/*/bin/codex' -type f -print -quit)" && \
    test -n "${codex_native}" && ln -s "${codex_native}" /usr/local/bin/apply_patch && \
    rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack /opt/yarn-v1.22.22 && \
    rm -f /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
      /usr/local/bin/yarn /usr/local/bin/yarnpkg /usr/local/bin/pnpm /usr/local/bin/pnpx
RUN groupadd --gid 10001 tyrs-hand && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/tyrs-hand tyrs-hand && \
    install -d -o tyrs-hand -g tyrs-hand -m 0750 /data/repo-cache /data/worktrees /data/codex-homes /data/discord-workspaces /data/build-cache
COPY --from=go-build --chown=root:root /out/tyrs-hand-server /usr/local/bin/tyrs-hand-server
COPY --from=go-build --chown=root:root /out/tyrs-hand-worker /usr/local/bin/tyrs-hand-worker
COPY --from=go-build --chown=root:root /out/tyrs-hand-admin /usr/local/bin/tyrs-hand-admin
COPY --from=go-build --chown=root:root /out/tyrs-hand-discord /usr/local/bin/tyrs-hand-discord
COPY --from=go-runtime --chown=root:root /usr/local/go /usr/local/go
RUN ln -s /usr/local/go/bin/go /usr/local/bin/go && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
ENV PATH="/usr/local/go/bin:${PATH}"
USER 10001:10001
WORKDIR /home/tyrs-hand
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["tyrs-hand-server"]
