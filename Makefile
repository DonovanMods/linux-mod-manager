.PHONY: all build run install test test-verbose coverage fmt vet lint clean help

# Build variables
BINARY_NAME := lmm
BUILD_DIR := ./build
MAIN_PATH := ./cmd/lmm
VERSION := $(shell grep 'version = ' cmd/lmm/root.go | cut -d'"' -f2)
LDFLAGS := -ldflags "-s -w"

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

## test: Run tests
test:
	@go test ./...

## test-verbose: Run tests with verbose output
test-verbose:
	@go test -v ./...

## coverage: Run tests with coverage report
coverage:
	@go test -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## fmt: Format code
fmt:
	@go fmt ./...

## vet: Vet code
vet:
	@go vet ./...

## lint: Run linter (trunk)
lint:
	@trunk check

## lint-fix: Run linter and fix issues
lint-fix:
	@trunk fmt

## check: Run fmt, vet, and tests
check: fmt vet test

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
