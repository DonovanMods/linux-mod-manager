.PHONY: all build run install test test-race test-verbose coverage fmt vet lint clean help man

# Build variables
BINARY_NAME := lmm
BUILD_DIR := ./build
MAIN_PATH := ./cmd/lmm
VERSION := $(shell grep 'version = ' cmd/lmm/root.go | cut -d'"' -f2)
DESCRIBE := $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -ldflags "-s -w -X main.buildDescribe=$(DESCRIBE)"
# Project-local Go cache so tests run in sandboxed environments (e.g. Cursor,
# or any run with no writable HOME). `?=` so a caller that HAS a usable cache
# can point the targets at it instead: CI restores one at ~/.cache/go-build
# with actions/setup-go, and overriding this is what lets the race job read
# it rather than recompiling every dependency from cold (P1b review F7).
GOCACHE_LOCAL ?= $(CURDIR)/.go-mod/cache
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

# TEST_TIMEOUT overrides Go's 10-minute default per test BINARY, which the
# v2 suite has outgrown: under -race, cmd/lmm, internal/core and
# internal/serve each run for 9-11 minutes (serve because it drives a real
# headless browser, ~575s of it on an idle machine and rather more when a
# second worktree is running its own suite). At the default, `make check`
# fails with "panic: test timed out after 10m0s" and no failing assertion
# anywhere - a red gate that says nothing about the code. 60m is generous
# headroom on purpose: it is a ceiling for a hang, not a budget. `?=` so a
# deliberately shorter run can override it on the command line.
TEST_TIMEOUT ?= 60m

## test: Run tests (uses project GOCACHE for sandbox-friendly runs)
test:
	@GOCACHE=$(GOCACHE_LOCAL) go test -timeout $(TEST_TIMEOUT) ./...

## test-race: Run tests with the race detector (matches CI)
test-race:
	@GOCACHE=$(GOCACHE_LOCAL) go test -race -timeout $(TEST_TIMEOUT) ./...

## test-verbose: Run tests with verbose output
test-verbose:
	@GOCACHE=$(GOCACHE_LOCAL) go test -timeout $(TEST_TIMEOUT) -v ./...

## coverage: Run tests with coverage report
coverage:
	@GOCACHE=$(GOCACHE_LOCAL) go test -timeout $(TEST_TIMEOUT) -coverprofile=coverage.out ./...
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
