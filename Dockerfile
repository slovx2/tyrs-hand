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

FROM --platform=$BUILDPLATFORM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY ent ./ent
COPY internal ./internal
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-admin ./cmd/tyrs-hand-admin && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-discord ./cmd/tyrs-hand-discord
COPY --from=web-build /src/internal/web/dist ./internal/web/dist
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -buildid=" -o /out/tyrs-hand-server ./cmd/tyrs-hand-server

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 AS control
RUN apt-get update && apt-get install --yes --no-install-recommends \
      ca-certificates=20230311+deb12u1 \
      tini=0.19.0-1+b3 && \
    rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 tyrs-hand && useradd --uid 10001 --gid 10001 --create-home --home-dir /home/tyrs-hand tyrs-hand
RUN install -d -o tyrs-hand -g tyrs-hand -m 0700 /data/control/attachments
COPY --from=go-build --chown=root:root /out/tyrs-hand-server /usr/local/bin/tyrs-hand-server
COPY --from=go-build --chown=root:root /out/tyrs-hand-admin /usr/local/bin/tyrs-hand-admin
COPY --from=go-build --chown=root:root /out/tyrs-hand-discord /usr/local/bin/tyrs-hand-discord
USER 10001:10001
WORKDIR /home/tyrs-hand
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["tyrs-hand-server"]
