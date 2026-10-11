GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test test-integration lint fmt vet tidy check clean

all: check build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/loom ./cmd/loom

test:
	go test -race ./...

# Calls the real Anthropic API. Needs ANTHROPIC_API_KEY; costs money.
test-integration:
	go test -race -tags integration -count=1 ./...

lint:
	$(GOLANGCI_LINT) run ./...

fmt:
	$(GOLANGCI_LINT) fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

check: vet lint test

clean:
	rm -rf bin
