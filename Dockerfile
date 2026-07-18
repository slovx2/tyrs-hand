# syntax=docker/dockerfile:1.19.0
FROM node:24.14.0-bookworm-slim@sha256:d8e448a56fc63242f70026718378bd4b00f8c82e78d20eefb199224a4d8e33d8 AS web-build
WORKDIR /src/web
RUN npm install --global pnpm@11.14.0
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
COPY web/tools ./tools
RUN pnpm install --frozen-lockfile
COPY web ./
RUN pnpm build

FROM golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
COPY --from=web-build /src/internal/web/dist ./internal/web/dist
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-server ./cmd/tyrs-hand-server && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-worker ./cmd/tyrs-hand-worker && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-admin ./cmd/tyrs-hand-admin

FROM node:24.14.0-bookworm-slim@sha256:d8e448a56fc63242f70026718378bd4b00f8c82e78d20eefb199224a4d8e33d8 AS runtime
RUN apt-get update && apt-get install --yes --no-install-recommends \
      git=1:2.39.5-0+deb12u3 \
      openssh-client=1:9.2p1-2+deb12u10 \
      ca-certificates=20230311+deb12u1 \
      tini=0.19.0-1+b3 && \
    rm -rf /var/lib/apt/lists/* && \
    npm install --global --omit=dev @openai/codex@0.142.5 && \
    npm cache clean --force
RUN groupadd --gid 10001 tyrs-hand && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/tyrs-hand tyrs-hand && \
    install -d -o tyrs-hand -g tyrs-hand -m 0750 /data/repo-cache /data/worktrees /data/codex-homes /data/build-cache
COPY --from=go-build --chown=root:root /out/tyrs-hand-server /usr/local/bin/tyrs-hand-server
COPY --from=go-build --chown=root:root /out/tyrs-hand-worker /usr/local/bin/tyrs-hand-worker
COPY --from=go-build --chown=root:root /out/tyrs-hand-admin /usr/local/bin/tyrs-hand-admin
USER 10001:10001
WORKDIR /home/tyrs-hand
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["tyrs-hand-server"]
