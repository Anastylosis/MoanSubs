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
	$(GO) test -race -count=1 $(PKGS)

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
