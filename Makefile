.PHONY: all build run install test test-verbose coverage fmt vet lint clean help

# Build variables
BINARY_NAME := lmm
BUILD_DIR := ./build
MAIN_PATH := ./cmd/lmm
VERSION := $(shell grep 'version = ' cmd/lmm/root.go | cut -d'"' -f2)
LDFLAGS := -ldflags "-s -w"
# Project-local Go cache so tests run in sandboxed environments (e.g. CI, Cursor)
GOCACHE_LOCAL := $(CURDIR)/.go-mod/cache
# Trunk cache under project for sandbox-friendly lint
TRUNK_CACHE_LOCAL := $(CURDIR)/.trunk-cache

# Default target
all: build

## build: Build the binary
build:
	@echo "Building $(BINARY_NAME) v$(VERSION)..."
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
check: fmt lint vet test

## update: Update dependencies
update:
	@go get -u ./...
	@go mod tidy
	@trunk upgrade

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
