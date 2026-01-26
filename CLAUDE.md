# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Workflow

**Work is tracked via GitHub Issues.** All development tasks should reference or originate from a GitHub issue. When starting work, check for relevant open issues first.

## Before Development

**ALWAYS run `/dev-init` at the start of every new session.** This skill reads the required directive files and ensures consistent development practices.

Read these global guidance files before starting any development work:

- `~/.claude/DEV.md` - Project-agnostic development practices (test-first, fail-fast, error handling)
- `~/.claude/GO.md` - Go-specific conventions (error wrapping, context threading, table-driven tests)

## Project Overview

**lmm** (Linux Mod Manager) is a terminal-based mod manager for Linux that provides both TUI and CLI interfaces for searching, installing, updating, and managing game mods from various sources. The MVP focuses on NexusMods integration with architecture designed for future sources (ESOUI, CurseForge, etc.).

## Build Commands

```bash
# Build the binary
go build -o lmm ./cmd/lmm

# Run all tests
go test ./... -v

# Run tests for a specific package
go test ./internal/storage/db/... -v

# Format code
go fmt ./...

# Vet code
go vet ./...

# Lint with trunk
trunk check
trunk fmt
```

## Architecture

Layered monolith with interface-based extensibility:

```
cmd/lmm/main.go           # Entry point, CLI (Cobra)
internal/
├── domain/               # Core types (Mod, Profile, Game) - NO external deps
├── source/               # ModSource interface + implementations
│   ├── source.go         # Interface definition
│   ├── registry.go       # Source registry
│   └── nexusmods/        # NexusMods GraphQL client
├── storage/
│   ├── db/               # SQLite (mod metadata, auth tokens)
│   ├── config/           # YAML parsing (games, profiles)
│   └── cache/            # Central mod file cache
├── linker/               # Deploy strategies (symlink, hardlink, copy)
├── core/                 # Business logic orchestration
│   ├── service.go        # Main service facade
│   ├── installer.go      # Install/uninstall operations
│   ├── updater.go        # Update checking & application
│   └── profile.go        # Profile switching logic
└── tui/                  # Bubble Tea application
    ├── app.go            # Main model, routing
    └── views/            # Individual screens
```

**Data Flow**: CLI/TUI → Core Service → Source Registry + Storage → Linker → Game Directory

**Key Interfaces**:

- `ModSource`: Abstraction for mod repositories (NexusMods, CurseForge)
- `Linker`: Deploy strategies (symlink, hardlink, copy)

## Key Dependencies

- `github.com/charmbracelet/bubbletea` - TUI framework (Elm architecture)
- `github.com/charmbracelet/bubbles` - TUI components
- `github.com/spf13/cobra` - CLI framework
- `github.com/hasura/go-graphql-client` - GraphQL client for NexusMods API
- `modernc.org/sqlite` - Pure Go SQLite (no CGO)
- `gopkg.in/yaml.v3` - YAML parsing
- `github.com/stretchr/testify` - Test assertions

## File Locations

- **SQLite database**: `~/.local/share/lmm/lmm.db`
- **Mod cache**: `~/.local/share/lmm/cache/<game-id>/<source-id>-<mod-id>/<version>/`
- **Config**: `~/.config/lmm/config.yaml`
- **Games config**: `~/.config/lmm/games.yaml`
- **Profiles**: `~/.config/lmm/games/<game-id>/profiles/<profile>.yaml`

## Testing Strategy

| Layer   | Approach                             |
| ------- | ------------------------------------ |
| Domain  | Pure unit tests, no mocks            |
| Source  | Mock HTTP client, recorded responses |
| Storage | In-memory SQLite, temp directories   |
| Core    | Mock sources/storage, test logic     |
| Linker  | Temp directories for file operations |

Use `:memory:` for SQLite tests and `t.TempDir()` for filesystem tests.

## Domain Types

Core types in `internal/domain/` have no external dependencies:

- `Mod`: Mod from any source with metadata
- `InstalledMod`: Mod installed in a profile with update policy
- `Game`: Moddable game with paths and source mappings
- `Profile`: Collection of mods with load order
- `LinkMethod`: symlink (default), hardlink, copy
- `UpdatePolicy`: notify (default), auto, pinned

## Versioning

**This project uses [Semantic Versioning](https://semver.org/).** After completing significant work, you MUST:

1. **Bump the version** in `cmd/lmm/root.go` (the `version` variable)
2. **Update CHANGELOG.md**:
   - Move `[Unreleased]` items to a new version section with today's date
   - Add the new version to the comparison links at the bottom
3. **Commit the version bump** as a separate commit (e.g., `chore: bump version to X.Y.Z`)

**Version increment rules:**

- **MAJOR** (X.0.0): Breaking changes to CLI interface or config format
- **MINOR** (0.X.0): New features, new commands, significant enhancements
- **PATCH** (0.0.X): Bug fixes, minor improvements, documentation updates

When in doubt, bump MINOR for new functionality, PATCH for fixes.

## Implementation Plan

The detailed implementation plan is in [docs/plans/2026-01-22-lmm-implementation.md](docs/plans/2026-01-22-lmm-implementation.md). Follow TDD: write failing test → implement → verify pass.
