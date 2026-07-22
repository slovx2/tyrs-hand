PNPM ?= pnpm
LOCAL_IMAGE ?= tyrs-hand:local

.PHONY: dependencies generate generate-check format format-check vet lint web-check test test-unit test-race test-integration test-runtime-image test-coverage web-install web-build build build-local image-local ci ci-local

dependencies:
	go mod download
	go mod verify

generate:
	go generate ./...
	$(PNPM) --dir web generate:api

generate-check:
	@before="$$(mktemp)"; after="$$(mktemp)"; \
	trap 'rm -f "$$before" "$$after"' EXIT; \
	git diff --binary >"$$before"; \
	$(MAKE) generate; \
	git diff --binary >"$$after"; \
	cmp --silent "$$before" "$$after" || { \
		echo '生成代码不是最新状态，请提交生成后的文件。' >&2; \
		diff --unified "$$before" "$$after" || true; \
		exit 1; \
	}

format:
	find cmd internal ent tools -name '*.go' -print0 | xargs -0 gofmt -w
	$(PNPM) --dir web format

format-check:
	test -z "$$(gofmt -l cmd internal ent tools)"
	$(PNPM) --dir web format:check

vet:
	go vet ./...

lint:
	GOTOOLCHAIN=local go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
	GOTOOLCHAIN=local go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.10 .github/workflows/*.yml
	$(PNPM) --dir web lint

web-check:
	$(PNPM) --dir web typecheck

test: test-unit

test-unit:
	node --test deploy/browser/*.test.mjs
	go test ./...
	$(PNPM) --dir web test:run

test-race:
	go test -race ./internal/...

test-integration:
	go test -p=1 -tags=integration ./internal/devcontainer ./internal/discordintegration ./internal/httpapi ./test/integration

test-runtime-image:
	./tools/test-worker-runtime.sh $(LOCAL_IMAGE)-worker

test-coverage:
	./tools/check-go-coverage.sh

web-install:
	$(PNPM) --dir web install --frozen-lockfile

web-build:
	$(PNPM) --dir web build

build: web-build
	go build ./cmd/tyrs-hand-server ./cmd/tyrs-hand-worker ./cmd/tyrs-hand-admin ./cmd/tyrs-hand-discord ./cmd/tyrs-hand-reply-hook

build-local:
	./tools/with-local-toolchain.sh $(MAKE) web-install build

image-local:
	docker build --target control --load --tag $(LOCAL_IMAGE)-control .
	docker build --target worker --load --tag $(LOCAL_IMAGE)-worker .

ci:
	$(MAKE) dependencies
	$(MAKE) generate-check
	$(MAKE) format-check
	$(MAKE) vet
	$(MAKE) lint
	$(MAKE) web-check
	$(MAKE) test-unit
	$(MAKE) test-race
	$(MAKE) test-integration
	$(MAKE) test-coverage
	$(MAKE) build

ci-local:
	./tools/with-local-toolchain.sh ./tools/ci-local.sh
