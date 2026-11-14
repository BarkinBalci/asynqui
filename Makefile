# asynqui developer tasks. Tools (templ, golangci-lint) are pinned in
# tools/go.mod so the main go.mod stays lean.
GO      ?= go
# VERSION is stamped into main.version; asynqui -version prints it. Releases
# get theirs from GoReleaser (.goreleaser.yaml).
TOOL    := $(GO) tool -modfile=tools/go.mod
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help
.PHONY: help generate lint test test-race run build docker tidy

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

generate: ## Generate templ code
	$(TOOL) templ generate

lint: ## Run go vet and golangci-lint
	$(GO) vet ./...
	$(TOOL) golangci-lint run

test: ## Run all tests (Redis-backed tests need Docker)
	$(GO) test ./...

test-race: ## Run all tests with the race detector (needs cgo and a C compiler)
	CGO_ENABLED=1 $(GO) test -race ./...

run: generate ## Run locally against localhost:6379 with config.dev.yaml
	$(GO) run ./cmd/asynqui -config config.dev.yaml

build: generate ## Build the static binary into bin/
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/asynqui ./cmd/asynqui

docker: ## Build the local image asynqui:dev from source (Dockerfile.dev)
	docker build -f Dockerfile.dev --build-arg VERSION=$(VERSION) -t asynqui:dev .

tidy: ## Tidy go.mod and tools/go.mod
	$(GO) mod tidy
	cd tools && $(GO) mod tidy
