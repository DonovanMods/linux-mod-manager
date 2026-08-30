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

**ALWAYS orchestrate subagents through Orca's `orchestration` skill** — invoke it by name; it ships with the `orca` binary. Orca is the dispatch mechanism and `superpowers:subagent-driven-development` is the review policy layered on top, so they compose rather than compete. Create worktrees with `orca-ide worktree create --base-branch v2 --issue <n>` (during the v2 line; `develop` otherwise): Orca has no adopt/import, so a worktree made with plain `git worktree add` can never be dispatched into.

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
cmd/lmm/main.go           # Entry point, CLI (Cobra); imports exactly app/core/domain/source (hard rule, no allow-list)
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
└── core/                 # Business logic orchestration (flat package, 49 files); frontends never reach past it
    ├── service.go         # Service facade: construction, ServiceConfig, the query/mutation concurrency contract
    ├── ops.go             # beginOp: the Service's single mutation-serialization slot
    ├── plan.go            # ErrStalePlan + installedSnapshot: the freshness precondition every Apply re-checks
    ├── errors.go          # Typed errors a frontend branches on: ConflictError, ErrConfirmationRequired, ErrInteractiveOnly
    ├── events.go          # EventSink wire envelope + the Op/EventType/FlowPhase vocabulary
    ├── queries.go         # Read-only query types: ModList, StatusReport, SearchReport, GameListEntry, VerifyReport
    ├── moddetail.go       # ModDetail: mod metadata + local install state for `lmm mod show` (#86)
    ├── settings.go        # SettingsResult: `lmm game set-default`/`clear-default`'s --json document
    ├── phases.go          # DeployPhase vocabulary shared by deploy/switch/apply progress events
    ├── hooks.go           # HookContext/HookResult + hook script execution (runHook)
    ├── hooks_resolve.go   # Resolves a flow's merged game/profile hook config into a HookRunner
    ├── selection.go       # File-selection policy: filter/sort by category, primary-file pick, sameFileIDSet
    ├── resolve.go         # ResolveVersionFiles: version -> file matching against a source's file list
    ├── conflicts.go       # File-conflict detection: ConflictModRef/ConflictReport for `lmm conflicts`
    ├── converge.go        # convergeDeployedFiles: remove-only reconciliation of deployed state (#168/#212)
    ├── deployable.go      # deployableFiles: the deploy-direction file resolver (#210)
    ├── overrides.go       # ApplyProfileOverrides: profile config-override files written to the game dir
    ├── merged_pak.go      # DeployCompile merged-artifact sync (singleton synthetic mod per game/profile, #197)
    ├── downloader.go      # HTTP downloads with retry/backoff + checksum verification
    ├── extractor.go       # Archive extraction (.zip native, .7z/.rar via system tools)
    ├── dependencies.go    # DependencyResolver: mod dependency ordering + cycle detection
    ├── filename_parser.go # NexusMods-style filename parsing (name/mod ID/version)
    ├── changelog.go       # CleanChangelog: strips HTML markup from changelog text for terminal display
    ├── staging.go         # Staging directory resolution for in-flight downloads/extraction
    ├── installer.go       # Installer: low-level cache/link/DB engine behind install & update flows
    ├── importer.go        # Importer: low-level cache/extract engine behind the archive-import flow
    ├── updater.go         # Updater: source-registry update-check primitives (CheckUpdates)
    │
    ├── install.go         # install flow: PlanInstall/ApplyInstall (`lmm install`)
    ├── deploy.go          # deploy flow: DeployOptions/PlanDeploy/ApplyDeploy/DeployProfile (`lmm deploy`)
    ├── uninstall.go       # uninstall flow: PlanUninstall/UninstallMod (`lmm uninstall`)
    ├── purge.go           # purge flow: the shared purgeSpec/purgeMods loop + PurgeProfile (`lmm purge`)
    ├── update.go          # update flow: PlanUpdate/ApplyUpdate (`lmm update`)
    ├── rollback.go        # rollback flow: PlanRollback/ApplyRollback (`lmm update rollback`)
    ├── switch.go          # profile-switch flow: PlanProfileSwitch/ApplyProfileSwitch (`lmm profile switch`)
    ├── profile_apply.go   # profile-apply flow: PlanProfileApply/ApplyProfileApply (`lmm profile apply`)
    ├── profile_sync.go    # profile-sync flow: PlanProfileSync/ApplyProfileSync (`lmm profile sync`)
    ├── profile_import.go  # profile-import flow: ImportPlan/PlanImport/ApplyImport (`lmm profile import`)
    ├── profile_reorder.go # profile-reorder flow: ReorderProfileMods/ResolveReorder (`lmm profile reorder`)
    ├── profile.go         # ProfileManager: ctx-threaded profile CRUD (Ruling 11) + ProfileResult
    ├── adopt.go           # adopt flow: ScanLocal/PlanAdopt/ApplyAdopt (`lmm import` scan mode)
    ├── import_archive.go  # archive-import flow: PlanImportArchive/ApplyImportArchive (`lmm import <archive>`)
    ├── archive_listing.go # archive listing + the member normalisation the plan and the ingest share
    ├── game_detect.go     # game-detect flow: GameFromDetected/ApplyGameDetect (`lmm game detect`)
    ├── mod_toggle.go      # mod enable/disable flow: EnableMod/DisableMod
    ├── mod_edit.go        # mod-edit flow: PlanRelinkMod/ApplyRelinkMod (`lmm mod edit`)
    ├── mod_settings.go    # mod lock/unlock/set-update/convert flows -> ModSettingResult
    ├── mod_files.go       # `lmm mod files`: ModFileEntry/ModFilesReport
    ├── verify.go          # verify engine: VerifyTier/VerifyResult (`lmm verify`)
    ├── verify_helpers.go  # verify engine internals: retained-source / mismatch detection
    └── verify_repair.go   # verify --fix repair actions: redownload, checksum backfill
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

| Layer                | Approach                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Domain               | Pure unit tests, no mocks                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| Source               | Mock HTTP client, recorded responses                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| Storage              | In-memory SQLite, temp directories                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| Core                 | Mock sources/storage, test logic                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| Linker               | Temp directories for file operations                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| JSON contract        | Every `--json`-emitting type has a golden under `internal/{core,domain,app}/testdata/json/` (`internal/{core,domain,app}/json_golden_test.go`) plus a CLI-facing golden under `cmd/lmm/testdata/json_golden/` (`cmd/lmm/json_golden_test.go`); each rewrites from live output behind its own `-update-*` flag (`-update-json-goldens`, `-update-app-json-goldens`, `-update-json-cli`, and per-command flags like `-update-deploy-dry-run`) rather than a shared one, so a re-record touches only the goldens you meant to change |
| Boundary             | `cmd/lmm/boundary_test.go`'s `TestCheckBoundary` enforces the hard rule directly — `cmd/lmm` may import only `internal/{app,core,domain,source}` — with no allow-list escape hatch; a new dependency either belongs in that list or the logic it needs moves into `internal/core`                                                                                                                                                                                                                                                 |
| Doc comments         | A `go/ast` ratchet (`internal/{core,domain,app}/doc_comment_test.go`, sharing `internal/testutil.UndocumentedExports`) fails the build if any exported identifier in a package's non-test files has no doc comment                                                                                                                                                                                                                                                                                                                |
| `Details()` coverage | `cmd/lmm/details_coverage_test.go`'s `TestDetailsTypesAreCovered` walks `internal/core` and `cmd/lmm` for every type implementing the `--json` error envelope's `Details() any` extension point and requires a named test pinning its wire shape — an implementer with no entry (or a stale one) fails the build                                                                                                                                                                                                                  |
| Cancellation         | `internal/core/cancellation_ruling16_test.go` and `..._internal_test.go` assert that cancelling mid-mutation always finishes a DB write's paired profile write rather than splitting the two (Ruling 16)                                                                                                                                                                                                                                                                                                                          |

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

**Frontends are thin adapters over `internal/core`.** The CLI is the only shipped frontend on the v2 line. New behaviour goes into core as a method with tests; a command only parses flags, calls core, and renders — every mutation is a Plan/Apply pair, or (for a handful of single-step flows with nothing to preview, e.g. `EnableMod`/`DisableMod`, the mod lock/unlock/set-update/convert settings) a single beginOp-gated call — and Apply never calls back into the frontend (no confirmation callbacks; a mid-flight decision is a typed error the caller answers by re-running Apply with the matching Options field, per v2 Phase 3 Ruling 1). The full rationale and the phase plan are in `docs/plans/2026-08-27-v2-core-refactor-design.md`. `cmd/lmm` imports exactly `internal/app`, `internal/core`, `internal/domain`, and `internal/source` — enforced as a hard rule by `cmd/lmm/boundary_test.go` (no allow-list escape hatch); a future `lmm serve` is another frontend over the same core.
