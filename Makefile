SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := linux/386 linux/amd64 linux/arm linux/arm64 linux/riscv64 linux/ppc64le linux/s390x \
	darwin/amd64 darwin/arm64 windows/386 windows/amd64 windows/arm64
export CGO_ENABLED ?= 0

.DEFAULT_GOAL := help
.PHONY: help build fmt vet arch test cross check

help: ## List targets
	@grep -hE '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "%-13s %s\n", $$1, $$2}'

build: ## Build bin/portly
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/portly ./cmd/portly

fmt: ## Fail on unformatted files
	@test -z "$$(gofmt -l cmd internal tests)" || { gofmt -l cmd internal tests; exit 1; }

vet: ## Vet on every OS
	@for os in linux darwin windows; do echo "vet $$os"; GOOS=$$os go vet ./... || exit 1; done

arch: ## Architecture rules
	go test -count=1 ./tests/arch

test: ## Unit tests with race detector
	CGO_ENABLED=1 go test -race -count=1 ./internal/... ./cmd/...

cross: ## Build every release target
	@for target in $(TARGETS); do echo "build $$target"; GOOS=$${target%/*} GOARCH=$${target#*/} GOARM=6 \
		go build -trimpath -o /dev/null ./cmd/portly || exit 1; done

check: fmt vet arch test build ## Quick gate
