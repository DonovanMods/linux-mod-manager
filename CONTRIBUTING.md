# Contributing to lmm

Thank you for your interest in contributing to lmm (Linux Mod Manager).

## Development setup

- **Go**: 1.21 or later (see `go.mod` for the exact version).
- **Build**: `go build -o lmm ./cmd/lmm`
- **Tests**: `go test ./...`
- **Format**: `go fmt ./...`
- **Vet**: `go vet ./...`
- **Lint**: `trunk check` and `trunk fmt` (if [Trunk](https://trunk.io) is configured).

## Workflow

1. **Issues**: Development work is tracked via GitHub Issues. Check open issues before starting.
2. **Tests**: Prefer test-first development. Use table-driven tests and in-memory SQLite / temp dirs where appropriate.
3. **Architecture**: See [CLAUDE.md](CLAUDE.md) and [docs/PRD.md](docs/PRD.md) for architecture and domain overview.

## Submitting changes

1. Create a branch from `main`.
2. Make focused commits with clear messages (e.g. `feat: add Steam game detect`, `fix: verify --fix for local mods`).
3. Ensure `go test ./...` and `go build ./cmd/lmm` succeed.
4. Open a Pull Request with a short description and reference any related issues.

## Code style

- Follow standard Go conventions and the project’s existing style.
- Error handling: wrap errors with `fmt.Errorf("context: %w", err)` where useful.
- Tests: use `testify/require` for setup and `testify/assert` for expectations when appropriate.

## Documentation

- Update [CHANGELOG.md](CHANGELOG.md) for user-facing changes under `[Unreleased]`.
- Update this file or [README.md](README.md) if you change build, test, or contribution steps.
