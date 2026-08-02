PNPM ?= pnpm
LOCAL_IMAGE ?= tyrs-hand:local

.PHONY: dependencies generate generate-check format format-check vet lint web-check client-install client-check client-export client-e2e-contract client-e2e-android client-e2e-ios test test-unit test-race test-integration test-runtime-image test-coverage web-install web-build build build-local image-local ci ci-local

dependencies:
	go mod download
	go mod verify
	$(MAKE) client-install

generate:
	go generate ./...
	$(PNPM) --dir web generate:api
	$(PNPM) --dir client generate:api

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

client-install:
	$(PNPM) --dir client install --frozen-lockfile

client-check:
	$(PNPM) --dir client typecheck
	$(PNPM) --dir client lint
	$(PNPM) --dir client test

client-export:
	$(PNPM) --dir client export:ios
	$(PNPM) --dir client export:android

client-e2e-contract:
	$(PNPM) --dir client e2e:contract

client-e2e-android:
	./tools/mobile-e2e/run.sh android

client-e2e-ios:
	TYRS_HAND_E2E_NATIVE_SERVICES=1 ./tools/mobile-e2e/run.sh ios

test: test-unit

test-unit:
	node --test deploy/browser/*.test.mjs
	go test ./...
	$(PNPM) --dir web test:run

test-race:
	go test -race ./internal/...

test-integration:
	go test -p=1 -tags=integration ./internal/database ./internal/devcontainer ./internal/discordintegration ./internal/httpapi ./test/integration

test-runtime-image:
	./tools/test-worker-runtime.sh $(LOCAL_IMAGE)-worker
	./tools/test-development-runtime.sh $(LOCAL_IMAGE)-development

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
	docker build --target development --load --tag $(LOCAL_IMAGE)-development .

ci:
	$(MAKE) dependencies
	$(MAKE) generate-check
	$(MAKE) format-check
	$(MAKE) vet
	$(MAKE) lint
	$(MAKE) web-check
	$(MAKE) client-check
	$(MAKE) client-e2e-contract
	$(MAKE) test-unit
	$(MAKE) test-race
	$(MAKE) test-integration
	$(MAKE) test-coverage
	$(MAKE) build
	$(MAKE) client-export

ci-local:
	./tools/with-local-toolchain.sh ./tools/ci-local.sh
