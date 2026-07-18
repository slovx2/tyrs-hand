.PHONY: generate generate-check format format-check lint test test-unit test-race test-integration test-coverage web-install web-build build

generate:
	go generate ./...
	cd web && pnpm generate:api

generate-check: generate
	git diff --exit-code -- ent internal/openapi internal/bootstrap/wire_gen.go web/src/api/schema.ts

format:
	find cmd internal ent tools -name '*.go' -print0 | xargs -0 gofmt -w
	cd web && pnpm format

format-check:
	test -z "$$(gofmt -l cmd internal ent tools)"
	cd web && pnpm format:check

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
	cd web && pnpm lint

test: test-unit

test-unit:
	go test ./...
	cd web && pnpm test:run

test-race:
	go test -race ./internal/...

test-integration:
	go test -tags=integration ./test/integration/...

test-coverage:
	./tools/check-go-coverage.sh

web-install:
	cd web && corepack pnpm install --frozen-lockfile

web-build:
	cd web && corepack pnpm build

build: web-build
	go build ./cmd/tyrs-hand-server ./cmd/tyrs-hand-worker ./cmd/tyrs-hand-admin
