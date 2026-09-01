BINARY  := pave
PKG     := github.com/mnorrsken/pave
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT)

.PHONY: help build run test race it lint fmt vet tidy clean dist

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build ./bin/pave
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/$(BINARY)

run: build ## Build and run
	./bin/$(BINARY)

test: ## Unit tests (no network, no ansible)
	go test ./...

race: ## Unit tests under the race detector
	go test -race ./...

it: ## Integration tests: needs a real ansible on PATH
	PAVE_IT=1 go test -count=1 ./...

lint: vet ## go vet plus golangci-lint when it is installed
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || \
		echo "golangci-lint not installed, skipping"

vet: ## go vet
	go vet ./...

fmt: ## Format
	gofmt -w cmd internal

tidy: ## Tidy go.mod
	go mod tidy

# No windows: the runner needs a pty to make ansible colour its output and
# prompt for passwords, and there is no pty on windows.
dist: ## Cross-compile release binaries into ./dist
	@mkdir -p dist
	@for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/$(BINARY)-$$os-$$arch ./cmd/$(BINARY) || exit 1; \
	done

clean: ## Remove build output
	rm -rf bin dist
