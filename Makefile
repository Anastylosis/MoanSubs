# moansubs — common dev targets.
#
# Run `make help` for the full list.

GO       ?= go
PKGS     := ./...
GOLINT   ?= golangci-lint

# Use bash for all recipes.
SHELL := /bin/bash
# Stricter shell behaviour for every recipe:
#   -u             : error on unset variables (catches typos like $$pas vs $$pass)
#   -o pipefail    : a failing command in a pipe makes the pipe fail
#   -c             : run argument as a command (required when overriding SHELLFLAGS)
.SHELLFLAGS := -u -o pipefail -c

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the moansubs binary into ./moansubs.
	$(GO) build -o moansubs ./cmd/moansubs

.PHONY: test
test: ## Run unit tests with race detector.
	# -p 1: internal/store and internal/api both TRUNCATE the same shared
	# tables when DATABASE_URL is set, so their test binaries must not run
	# concurrently against the same live Postgres — go test defaults to
	# running separate packages' binaries in parallel, which would race two
	# packages' setup TRUNCATEs against each other's in-flight assertions.
	$(GO) test -p 1 -race -count=1 $(PKGS)

.PHONY: vet
vet: ## go vet on all packages.
	$(GO) vet $(PKGS)

.PHONY: lint
lint: vet ## Run go vet + golangci-lint.
	$(GOLINT) run --timeout=5m

.PHONY: tidy
tidy: ## go mod tidy.
	$(GO) mod tidy

.PHONY: clean
clean: ## Remove built binary and test artifacts.
	rm -f moansubs moansubs.exe coverage.out test-output.txt

.PHONY: docker
docker: ## Build the docker image as moansubs:dev with version metadata from git.
	docker build \
	  --build-arg VERSION=$$(git describe --tags --always --dirty) \
	  --build-arg COMMIT=$$(git rev-parse --short HEAD) \
	  --build-arg DATE=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
	  -t moansubs:dev .
