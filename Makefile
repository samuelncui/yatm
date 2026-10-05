.DEFAULT_GOAL := help

.PHONY: help build backend backend-linux frontend release release-check preview generate demo check test-race test-e2e test-performance-tools performance-collect performance-check

PERF_TIER ?= fast

help:
	@printf '%s\n' 'YATM development commands:' \
	  '  build         Build backend and frontend into output/' \
	  '  backend       Build backend programs' \
	  '  backend-linux Build static Linux backend programs' \
	  '  frontend      Build frontend assets' \
	  '  release       Package a main release (RELEASE_VERSION, TARGET_NAME required)' \
	  '  release-check Check clean candidate source, public history and all source gates' \
	  '  preview       Build the optional native Preview release (RELEASE_VERSION required)' \
	  '  generate      Generate Go, TypeScript and legacy protobuf code' \
	  '  demo          Start the local Demo (YATM_DEMO_RESET=1 resets its fixture)' \
	  '  check         Run Go, frontend and build-tool checks' \
	  '  test-race     Run Go race checks' \
	  '  test-e2e      Run CLI E2E against explicitly selected candidate binaries' \
	  '  test-package-tools Check the local SSH package-acceptance controller' \
	  '  test-performance-tools Check performance collection and comparison tools' \
	  '  performance-collect Collect paired benchmarks in an idle environment' \
	  '  performance-check Check the evidence in PERF_OUTPUT'

build: backend frontend

backend:
	./build/backend/build.sh

backend-linux:
	./build/backend/cross-linux.sh

frontend:
	./frontend/scripts/build.sh

release:
	./build/release/build.sh

preview:
	./previewworker/build/build.sh

generate:
	./dev/generate.sh

demo:
	./dev/demo.sh

check:
	node dev/check-documents.mjs
	go run ./dev/check-idl.go
	go test ./...
	go vet ./...
	go vet -tags=e2e ./e2e
	pnpm --dir frontend check
	node --test build/backend/*.test.mjs build/release/*.test.mjs previewworker/build/*.test.mjs e2e/ltfs-file-backend/*.test.mjs
	$(MAKE) test-performance-tools
	$(MAKE) test-package-tools
	./dev/check-shell.sh

# Run once before the platform matrix; individual packagers also enforce committed inputs.
release-check:
	node build/release/check-source.mjs
	node build/release/check-content.mjs source . HEAD
	node build/release/check-preview-fixtures.mjs
	$(MAKE) check
	$(MAKE) test-race
	CGO_ENABLED=0 go test ./internal/library ./entity ./internal/executor/... ./internal/apis ./internal/migrate/legacy ./internal/demo ./internal/preview/...
	$(MAKE) generate
	node build/release/check-source.mjs
	$(MAKE) generate
	node build/release/check-source.mjs

test-race:
	go test -race ./...

test-performance-tools:
	node --test dev/check-performance.test.mjs dev/performance/*.test.mjs frontend/scripts/compare-performance.test.mjs

# Run separately from other builds and tests; the collector rejects incomplete inputs.
performance-collect:
	node dev/performance/collect.mjs --baseline "$(PERF_BASELINE)" --candidate "$(PERF_CANDIDATE)" --harness "$(PERF_HARNESS)" --out "$(PERF_OUTPUT)" --tier "$(PERF_TIER)" --environment "$(PERF_ENVIRONMENT)" --idle

performance-check:
	node dev/check-performance.mjs "$(PERF_OUTPUT)"

test-e2e:
	@test -n "$(YATM_E2E_BIN_DIR)" || { echo 'Set YATM_E2E_BIN_DIR to the extracted candidate binaries' >&2; exit 1; }
	@if test -n "$(YATM_TEST_PREVIEW_HELPER)"; then node build/release/check-preview-fixtures.mjs; fi
	YATM_E2E_BIN_DIR="$(abspath $(YATM_E2E_BIN_DIR))" go test -tags=e2e -count=1 -v ./e2e

.PHONY: test-package-tools
test-package-tools:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s e2e -p 'test_package_*.py'
