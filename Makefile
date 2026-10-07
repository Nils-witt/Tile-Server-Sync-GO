BINARY_NAME := Tile-Server-Sync-GO
GO          ?= go

# Mirrors .goreleaser.yaml's ldflags so a local build reports a real version.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: all build go-build frontend run test vet lint audit check clean help

all: check build

## build: Build the SPA, then the binary that embeds it (frontend/dist)
build: frontend go-build

## go-build: Build only the Go binary (uses whatever is in frontend/dist)
go-build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/$(BINARY_NAME)

## frontend: Install frontend deps and build frontend/dist
frontend:
	npm --prefix frontend ci
	npm --prefix frontend run build

## run: Build everything and run against config.yaml
run: build
	./$(BINARY_NAME) -config config.yaml

## test: Run the Go test suite with the race detector
test:
	$(GO) test -race ./...

## vet: Run go vet
vet:
	$(GO) vet ./...

## lint: Run golangci-lint (see .golangci.yml)
lint:
	golangci-lint run

## audit: Scan dependencies for known vulnerabilities
audit:
	govulncheck ./...

## check: vet + test + lint + audit (what CI and the pre-commit hook run)
check: vet test lint audit

## clean: Remove the built binary and the SPA build output
clean:
	rm -f $(BINARY_NAME)
	git checkout -- frontend/dist/index.html
	find frontend/dist -mindepth 1 -maxdepth 1 ! -name index.html -exec rm -rf {} +

## help: List targets
help:
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
