# P3 Auto-Dependencies Design

**Date**: 2026-01-28
**Priority**: P3 (after Checksum Verification and Conflict Detection)
**Goal**: Automatically resolve and install mod dependencies during installation

---

## Overview

When installing a mod, automatically resolve and install its required dependencies. Uses existing `DependencyResolver` for cycle detection and topological ordering.

## Command Interface

```bash
lmm install "skyui" --game skyrim-se
```

**New flags:**

- `--no-deps` - Skip dependency installation (install only the requested mod)

**Existing flags used:**

- `-y` / `--yes` - Auto-confirm dependency installation

**Example output:**

```
Resolving dependencies for SkyUI (12604)...

Install plan (2 mods):
  1. SKSE64 v2.2.6 (ID: 30379) [dependency]
  2. SkyUI v5.2SE (ID: 12604) [target]

Install 2 mod(s)? [Y/n]:
```

## Design Decisions

| Decision                | Choice              | Rationale                                           |
| ----------------------- | ------------------- | --------------------------------------------------- |
| Where to add logic      | Install command     | Keeps change scoped, reuses existing infrastructure |
| File selection for deps | Auto-select primary | Avoids complex multi-mod file selection UX          |
| Unavailable deps        | Warn and continue   | External deps (SKSE) aren't on NexusMods            |
| Version matching        | Install latest      | NexusMods doesn't provide version constraints       |
| Already-installed deps  | Skip                | Don't auto-upgrade dependencies                     |
| Partial failure         | Continue others     | Report failures in summary                          |

## Command Flow

```
Current:  select mod → select files → download → deploy

New:      select mod → resolve deps → show install plan → confirm → download all → deploy all
```

**Insertion point:** After mod selection, before file selection.

**Detailed flow:**

1. User selects mod to install
2. Fetch dependencies via `source.GetDependencies()`
3. Filter out already-installed dependencies
4. If missing deps exist and `--no-deps` not set:
   - Show install plan (deps + target mod in order)
   - Prompt for confirmation (or auto-confirm with `-y`)
5. For each mod in install order:
   - Auto-select primary file
   - Download and deploy
6. Save all to database/profile

## Dependency Resolution Logic

**Helper function:**

```go
func resolveDependencies(ctx context.Context, svc *core.Service, source string, mod *domain.Mod, installedIDs map[string]bool) (*installPlan, error)
```

**Data structure:**

```go
type installPlan struct {
    mods    []*domain.Mod  // In install order (deps first)
    missing []string       // Dependencies that couldn't be fetched
}
```

**Algorithm:**

1. Call `source.GetDependencies(ctx, mod)` → `[]ModReference`
2. For each dependency:
   - Skip if already in `installedIDs`
   - Fetch full mod info via `service.GetMod()`
   - Recursively resolve its dependencies
3. Use existing `DependencyResolver.Resolve()` for topological order
4. Return ordered list (dependencies first, target mod last)

## Behavior Matrix

| Scenario         | `--no-deps` | `-y`  | Behavior                        |
| ---------------- | ----------- | ----- | ------------------------------- |
| Has missing deps | false       | false | Show plan, prompt               |
| Has missing deps | false       | true  | Show plan, auto-confirm         |
| Has missing deps | true        | any   | Skip deps, install target only  |
| No missing deps  | any         | any   | Install target only (no prompt) |

## Edge Cases

| Edge Case                        | Handling                                         |
| -------------------------------- | ------------------------------------------------ |
| Circular dependency              | `ErrDependencyLoop` → clear error message, abort |
| Dependency not on NexusMods      | Warn and continue (external deps like SKSE)      |
| Dependency fetch fails (network) | Warn and continue                                |
| Dependency already installed     | Skip, don't reinstall                            |
| Dependency has older version     | Skip (don't auto-upgrade deps)                   |
| Target mod already installed     | Existing behavior: uninstall old, install new    |
| One dependency fails to install  | Continue with others, report in summary          |
| Local mod as target              | No dependency resolution                         |

## Conflict Handling

Reuse existing conflict detection:

- Check conflicts for all mods in install plan
- Show all conflicts grouped by mod
- Single confirmation for entire plan
- `--force` bypasses for all

## Modified Files

### `cmd/lmm/install.go`

- Add `--no-deps` flag
- Add `resolveDependencies()` helper function
- Add `installPlan` struct
- Modify `runInstall()` to call dependency resolution
- Reuse `installMultipleMods()` for batch installation

## Testing Strategy

**Unit tests:**

- `resolveDependencies()` with mock source
- Cycle detection (reuse existing patterns)
- `--no-deps` flag behavior
- Already-installed filtering

**Integration tests:**

- Multi-mod install with temp directories
- Verify all files deployed in correct order

**Test fixtures:**

- Mock mod with 2 dependencies (one installed, one not)
- Mock mod with circular dependency
- Mock mod with unavailable dependency

## Code Locations

- `internal/core/dependencies.go` - Existing resolver (no changes needed)
- `internal/source/nexusmods/nexusmods.go` - Existing `GetDependencies()` (no changes needed)
- `cmd/lmm/install.go` - Main changes here
