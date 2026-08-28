# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Workflow

**Work is tracked via GitHub Issues.** All development tasks should reference or originate from a GitHub issue. When starting work, check for relevant open issues first.

**Branch model (git-flow-lite):** `main` holds released, tagged states only. `develop` is the integration branch — story branches (`feat/…`, `fix/…`) fork from `develop` and PR back into `develop` with an explicit `--base develop` (`main` is still the default branch, so a forgotten flag targets protected `main` and gets caught). Both branches are protected: PRs required, Copilot review, no force-push/deletion; the develop ruleset has a repository-admin bypass reserved for post-release fast-forward syncs, trivial hotfix back-merges, and (last resort) history repair. Full release and hotfix flows are documented in [docs/plans/2026-07-30-develop-branch-workflow-design.md](docs/plans/2026-07-30-develop-branch-workflow-design.md).

**v2 line (since 2026-08-27):** the core refactor lands on the long-lived `v2` branch, cut from `develop` at 9729783. Story branches fork from `v2` and merge back locally with `git merge --no-ff` (one revertable merge commit per unit); issues close at that merge with a comment naming the commit. PRs are optional on `v2` until the public-release milestone — open one (`--base v2`) only when an independent Copilot review is wanted. `develop`/`main` remain the v1.30.x line; never merge them wholesale into `v2` — cherry-pick wanted fixes. Worktrees: `orca-ide worktree create --base-branch v2 --issue <n>`. Ruleset "Protect v2" blocks deletion and force-push only.

**Issues close on merge into `develop`, not at release.** The tracker answers "what's left to build," so an issue whose work is merged is done — holding it open until the release tag would surface finished work on every "check for relevant open issues first" pass. Release traceability lives in `CHANGELOG.md` instead: entries carry their issue number and move from `[Unreleased]` into a dated `vX.Y.Z` section at release, which makes it a complete issue→release index. Don't duplicate that in issue state or milestones.

Because `main` is the default branch, a PR's `Fixes #N` will **not** auto-close on a merge into `develop` — **close the issue manually**, with a comment naming the merge commit and noting it is merged but not yet released (e.g. "Fixed in #229, merged to `develop` as 2a791cb. Ships with the next release batch."). Revisit this policy if the project starts taking bug reports from other people: for an external reporter, "closed" reads as "I can go download it," and closing at the `develop` merge sends them hunting through a release that doesn't have the fix.

## Before Development

**ALWAYS run `/dev-init` at the start of every new session.** This skill reads the required directive files and ensures consistent development practices.

Read these global guidance files before starting any development work:

- `~/.claude/DEV.md` - Project-agnostic development practices (test-first, fail-fast, error handling)
- `~/.claude/GO.md` - Go-specific conventions (error wrapping, context threading, table-driven tests)

**ALWAYS orchestrate subagents through Orca's `orchestration` skill** — invoke it by name; it ships with the `orca` binary. Orca is the dispatch mechanism and `superpowers:subagent-driven-development` is the review policy layered on top, so they compose rather than compete. Create worktrees with `orca-ide worktree create --base-branch develop --issue <n>`: Orca has no adopt/import, so a worktree made with plain `git worktree add` can never be dispatched into.

## Project Overview

**lmm** (Linux Mod Manager) is a terminal-based mod manager for Linux that provides a command-line interface for searching, installing, updating, and managing game mods from various sources. NexusMods and CurseForge ship as built-in sources, and user-defined custom sources (directory, manifest, api) extend that further without writing code — see the README's Custom Sources section.

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

```text
cmd/lmm/main.go           # Entry point, CLI (Cobra)
internal/
├── app/                  # Composition root: app.Open resolves paths (XDG), prepares dirs, opens core, registers sources
├── domain/               # Core types (Mod, Profile, Game) - NO external deps
├── source/               # ModSource interface + implementations
│   ├── source.go         # Interface definition
│   ├── registry.go       # Source registry
│   ├── nexusmods/        # NexusMods GraphQL client
│   ├── curseforge/       # CurseForge API client
│   ├── custom/           # User-defined sources (directory, manifest, api)
│   ├── steam/            # Steam library scanning (for 'lmm game detect')
│   └── httpclient/       # Shared HTTP client (timeouts, size caps, redirects)
├── storage/
│   ├── db/               # SQLite (mod metadata, auth tokens)
│   ├── config/           # YAML parsing (games, profiles)
│   └── cache/            # Central mod file cache
├── linker/               # Deploy strategies (symlink, hardlink, copy)
└── core/                 # Business logic orchestration
    ├── service.go        # Main service facade
    ├── installer.go      # Install/uninstall operations
    ├── updater.go        # Update checking & application
    └── profile.go        # Profile switching logic
```

**Data Flow**: CLI → app.Open → Core Service → Source Registry + Storage → Linker → Game Directory

**Key Interfaces**:

- `ModSource`: Abstraction for mod repositories (NexusMods, CurseForge, and user-defined custom sources — directory, manifest, api)
- `Linker`: Deploy strategies (symlink, hardlink, copy)

## Key Dependencies

- `github.com/spf13/cobra` - CLI framework
- `github.com/hasura/go-graphql-client` - GraphQL client for NexusMods API
- `modernc.org/sqlite` - Pure Go SQLite (no CGO)
- `gopkg.in/yaml.v3` - YAML parsing
- `github.com/stretchr/testify` - Test assertions

## File Locations

- **SQLite database**: `$XDG_DATA_HOME/lmm/lmm.db` (default `~/.local/share/lmm/lmm.db`)
- **Mod cache**: `$XDG_DATA_HOME/lmm/cache/<game-id>/<source-id>-<mod-id>/<version>/` (default `~/.local/share/lmm`; a per-game `cache_path` drops the `<game-id>` segment)
- **Download staging**: `$XDG_DATA_HOME/lmm/downloads/` (default `~/.local/share/lmm`)
- **Config**: `$XDG_CONFIG_HOME/lmm/config.yaml` (default `~/.config/lmm`)
- **Games config**: `$XDG_CONFIG_HOME/lmm/games.yaml` (default `~/.config/lmm`)
- **Custom sources**: `$XDG_CONFIG_HOME/lmm/sources/*.yaml` (default `~/.config/lmm`)
- **Profiles**: `$XDG_CONFIG_HOME/lmm/games/<game-id>/profiles/<profile>.yaml` (default `~/.config/lmm`)

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

**This project uses [Semantic Versioning](https://semver.org/).** Versions are bumped **at release time, not per story PR**:

- **Story PRs into `develop`**: no version bump. Add CHANGELOG entries under `[Unreleased]`. The `version` variable in `cmd/lmm/root.go` stays at the last released version between releases.
- **Releases (develop → main)**: cut `release/vX.Y.Z` from `develop` with a single prep commit — bump `version` in `cmd/lmm/root.go`, move `[Unreleased]` to a dated `vX.Y.Z` section, add the comparison link, and run `make man` (the genman test enforces this). PR it into `main` titled `release: vX.Y.Z`, merge with a merge commit, tag `vX.Y.Z` on the merge commit, then fast-forward develop (`git push origin main:develop`) and delete the release branch.
- **Hotfixes**: branch from `main`, PR back into `main` with its own PATCH bump + CHANGELOG section + `make man`, tag on the merge commit, then merge `main` back into `develop`.

**Version increment rules** (judged by the whole release batch):

- **MAJOR** (X.0.0): Breaking changes to CLI interface or config format
- **MINOR** (0.X.0): New features, new commands, significant enhancements
- **PATCH** (0.0.X): Bug fixes, minor improvements, documentation updates

When in doubt, bump MINOR for a batch containing any new functionality, PATCH for a fixes-only batch.

## Implementation Plans

In-flight plan documents live in `docs/plans/`; completed plans are kept for historical reference in [docs/plans/archive/](docs/plans/archive/) (the original v1.0 implementation plan is [2026-01-22-lmm-implementation.md](docs/plans/archive/2026-01-22-lmm-implementation.md)). Follow TDD: write failing test → implement → verify pass.

**Frontends are thin adapters over `internal/core`.** The CLI is the only shipped frontend on the v2 line (a local web UI, `lmm serve`, follows the core refactor). New behaviour goes into core as a method with tests; a command only parses flags, calls core, and renders. The full rationale and the phase plan are in `docs/plans/2026-08-27-v2-core-refactor-design.md`.
