# asynqui developer tasks. Tools are pinned in go.mod (tool directives).
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: generate lint test test-short run build tidy

generate: ## Generate templ code
	$(GO) tool templ generate

lint: ## Run go vet and golangci-lint
	$(GO) vet ./...
	$(GO) tool golangci-lint run

test: ## Run all tests (needs Docker for Redis-backed tests)
	$(GO) test -race ./...

test-short: ## Run tests that need no Docker
	$(GO) test -short ./...

run: generate ## Run locally with config.example.yaml
	$(GO) run ./cmd/asynqui -config config.example.yaml

build: generate ## Build the binary into bin/
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/asynqui ./cmd/asynqui

tidy:
	$(GO) mod tidy
