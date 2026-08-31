.PHONY: all build run install test test-race test-verbose coverage fmt vet lint clean help man css

# Build variables
BINARY_NAME := lmm
BUILD_DIR := ./build
MAIN_PATH := ./cmd/lmm
VERSION := $(shell grep 'version = ' cmd/lmm/root.go | cut -d'"' -f2)
DESCRIBE := $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -ldflags "-s -w -X main.buildDescribe=$(DESCRIBE)"
# Project-local Go cache so tests run in sandboxed environments (e.g. CI, Cursor)
GOCACHE_LOCAL := $(CURDIR)/.go-mod/cache
# Trunk cache under project for sandbox-friendly lint
TRUNK_CACHE_LOCAL := $(CURDIR)/.trunk-cache

# Default target
all: build

## build: Build the binary
build:
	@echo "Building $(BINARY_NAME) v$(VERSION)$(if $(DESCRIBE), ($(DESCRIBE)))..."
	@go build $(LDFLAGS) -o $(BINARY_NAME) $(MAIN_PATH)

## build-debug: Build with debug symbols
build-debug:
	@echo "Building $(BINARY_NAME) v$(VERSION) (debug)..."
	@go build -o $(BINARY_NAME) $(MAIN_PATH)

## run: Run the application
run:
	@go run $(MAIN_PATH) $(ARGS)

## install: Install to GOPATH/bin
install:
	@echo "Installing $(BINARY_NAME)..."
	@go install $(LDFLAGS) $(MAIN_PATH)

## test: Run tests (uses project GOCACHE for sandbox-friendly runs)
test:
	@GOCACHE=$(GOCACHE_LOCAL) go test ./...

## test-race: Run tests with the race detector (matches CI)
test-race:
	@GOCACHE=$(GOCACHE_LOCAL) go test -race ./...

## test-verbose: Run tests with verbose output
test-verbose:
	@GOCACHE=$(GOCACHE_LOCAL) go test -v ./...

## coverage: Run tests with coverage report
coverage:
	@GOCACHE=$(GOCACHE_LOCAL) go test -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## fmt: Format code (uses project GOCACHE for sandbox-friendly runs)
fmt:
	@GOCACHE=$(GOCACHE_LOCAL) go fmt ./...

## vet: Vet code (uses project GOCACHE for sandbox-friendly runs)
vet:
	@GOCACHE=$(GOCACHE_LOCAL) go vet ./...

## lint: Run linter (trunk, uses project cache for sandbox-friendly runs)
lint:
	@XDG_CACHE_HOME=$(TRUNK_CACHE_LOCAL) trunk check

## lint-fix: Run linter and fix issues
lint-fix:
	@XDG_CACHE_HOME=$(TRUNK_CACHE_LOCAL) trunk fmt

## check: Run fmt, vet, and tests
check: fmt lint vet test-race

## update: Update dependencies
update:
	@go get -u ./...
	@go mod tidy
	@trunk upgrade

## man: Regenerate man pages from the command tree
man:
	@GOCACHE=$(GOCACHE_LOCAL) go run $(MAIN_PATH) gen-man docs/man/man1

## css: Rebuild internal/serve/static/app.css from the Tailwind utility
## classes referenced in internal/serve/templates/*.gohtml and static/app.js.
## DEV-TIME ONLY: uses the standalone tailwindcss CLI
## (https://github.com/tailwindlabs/tailwindcss/releases) - never fetched or
## vendored by this build, so install it once yourself and re-run this
## target whenever a template's class names change. The output is committed,
## so `lmm serve` never depends on Node or this CLI at build or runtime. If
## the CLI is not on PATH, internal/serve/static/app.css stays a hand-written
## stopgap covering only the classes currently in use (see that file's own
## header comment).
css:
	@tailwindcss --help >/dev/null 2>&1 || { echo "tailwindcss (standalone CLI) not usable; install it from https://github.com/tailwindlabs/tailwindcss/releases and re-run 'make css'" >&2; echo "(a shim on PATH that errors when actually run - e.g. a version manager with no version selected - counts as not usable; 'command -v' alone can't tell the difference)" >&2; exit 1; }
	@tailwindcss -c internal/serve/tailwind.config.js -i internal/serve/static/app.src.css -o internal/serve/static/app.css --minify

## clean: Remove build artifacts
clean:
	@rm -f $(BINARY_NAME)
	@rm -f coverage.out coverage.html
	@rm -rf $(BUILD_DIR)
	@echo "Cleaned."

## version: Show version
version:
	@echo $(VERSION)

## help: Show this help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed 's/^/  /'
