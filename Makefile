SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.10.0
TOOLS := $(CURDIR)/bin/tools
FUZZTIME ?= 10s
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := linux/386 linux/amd64 linux/arm linux/arm64 linux/riscv64 linux/ppc64le linux/s390x \
	darwin/amd64 darwin/arm64 windows/386 windows/amd64 windows/arm64
export CGO_ENABLED ?= 0

.DEFAULT_GOAL := help
.PHONY: snapshot help build fmt vet arch test cover lint vuln fuzz e2e docker-test distro-test perf cycles cross check check-all

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

e2e: ## Real listeners and real stops, only of processes the tests start
	go test -count=1 -p 1 -timeout 10m ./tests/e2e

docker-test: ## Throwaway containers only
	PORTLY_DOCKER_TESTS=1 go test -count=1 -p 1 -timeout 10m -run TestDocker -v ./tests/e2e

distro-test: build ## Static binary in local distribution images, never pulls
	./tests/distros.sh bin/portly

perf: ## Time, memory and size budgets
	PORTLY_PERF_TESTS=1 go test -count=1 -run TestBudget -v ./tests/e2e

cycles: ## Repeated stops, CYCLES=100 takes about 3 minutes
	PORTLY_STOP_CYCLES=$(or $(CYCLES),100) go test -count=1 -timeout 40m -run TestRepeatedStops ./tests/e2e

cover: ## Coverage gate
	./tests/coverage.sh

fuzz: ## Every parser for FUZZTIME, two workers
	./tests/fuzz.sh $(FUZZTIME)

lint: ## Staticcheck on every OS
	GOBIN=$(TOOLS) go install $(STATICCHECK)
	@for os in linux darwin windows; do echo "staticcheck $$os"; GOOS=$$os $(TOOLS)/staticcheck ./... || exit 1; done

vuln: ## Known vulnerabilities
	GOBIN=$(TOOLS) go install $(GOVULNCHECK)
	$(TOOLS)/govulncheck ./...

snapshot: ## Local release build with an installed GoReleaser, publishes nothing
	goreleaser release --snapshot --clean --skip=sign,sbom

cross: ## Build every release target
	@for target in $(TARGETS); do echo "build $$target"; GOOS=$${target%/*} GOARCH=$${target#*/} GOARM=6 \
		go build -trimpath -o /dev/null ./cmd/portly || exit 1; done

check: fmt vet arch test build ## Quick gate

check-all: check lint vuln cover fuzz cross e2e docker-test distro-test perf ## Everything
