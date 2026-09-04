BINARY  := pave
PKG     := github.com/mnorrsken/pave
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT)

.PHONY: help build run test race it lint fmt vet tidy clean dist \
	lab lab-up lab-down lab-status lab-ssh

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

# The lab: four containers with sshd on them, and a workspace of playbooks and
# roles to point pave at. Needs docker (or podman: DOCKER=podman make lab-up)
# and nothing from ansible-galaxy.
DOCKER    ?= docker
LAB_IMAGE := pave-lab
LAB_NET   := pave-lab
LAB_KEY   := lab/.ssh/id_ed25519
# name:published port. The inventory in lab/workspace has the same list.
LAB_HOSTS := web1:2221 web2:2222 db1:2223 edge1:2224

lab: lab-up build ## Start the lab and open pave on it
	./bin/$(BINARY) -root lab/workspace

lab-up: $(LAB_KEY) ## Build the lab image and start the containers
	$(DOCKER) build -t $(LAB_IMAGE) -f lab/docker/Dockerfile lab
	@$(DOCKER) network inspect $(LAB_NET) >/dev/null 2>&1 || $(DOCKER) network create $(LAB_NET) >/dev/null
	@for hp in $(LAB_HOSTS); do \
		name=$${hp%:*}; port=$${hp#*:}; \
		$(DOCKER) rm -f $(LAB_IMAGE)-$$name >/dev/null 2>&1 || true; \
		$(DOCKER) run -d --name $(LAB_IMAGE)-$$name --hostname $$name \
			--network $(LAB_NET) -p 127.0.0.1:$$port:22 $(LAB_IMAGE) >/dev/null || exit 1; \
		echo "$$name on 127.0.0.1:$$port"; \
	done
	@echo "lab up: pave -root lab/workspace"

lab-down: ## Remove the containers and the network
	@for hp in $(LAB_HOSTS); do \
		$(DOCKER) rm -f $(LAB_IMAGE)-$${hp%:*} >/dev/null 2>&1 || true; \
	done
	@$(DOCKER) network rm $(LAB_NET) >/dev/null 2>&1 || true
	@echo "lab down"

lab-status: ## What the lab is running
	@$(DOCKER) ps --filter name=$(LAB_IMAGE)- \
		--format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'

lab-ssh: ## ssh into one of them: make lab-ssh HOST=web1
	@port=$$(for hp in $(LAB_HOSTS); do [ "$${hp%:*}" = "$(HOST)" ] && echo $${hp#*:}; done); \
	[ -n "$$port" ] || { echo "set HOST to one of: $(foreach hp,$(LAB_HOSTS),$(firstword $(subst :, ,$(hp))))"; exit 1; }; \
	ssh -i $(LAB_KEY) -p $$port \
		-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		ansible@127.0.0.1

# The lab's only secret, and the only way into the containers.
$(LAB_KEY):
	@mkdir -p $(dir $(LAB_KEY))
	ssh-keygen -t ed25519 -N '' -C pave-lab -f $(LAB_KEY)
