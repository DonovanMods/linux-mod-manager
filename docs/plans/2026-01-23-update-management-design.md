# Update Management Design

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement complete update management with per-mod update policies, actual update application, and rollback capability.

**Date:** 2026-01-23

---

## Overview

Currently, `lmm update` can check for available updates but cannot apply them. This design adds:

1. Actual update application (download new version, redeploy)
2. Per-mod update policies (auto/notify/pin) with CLI control
3. Rollback to previous version
4. Dry-run mode

## Data Model

### Schema Changes (Migration V2)

Add `previous_version` column to `installed_mods` to enable rollback:

```sql
ALTER TABLE installed_mods ADD COLUMN previous_version TEXT;
```

The cache already stores mods by version (`<game>/<source>-<mod>/<version>/`), so old versions remain available for rollback as long as they exist in cache.

### Version Tracking

- `version`: Current installed version
- `previous_version`: Version before last update (NULL if never updated)

When updating: `previous_version = version`, then `version = new_version`
When rolling back: swap `version` and `previous_version`

---

## User Flow

### Check for Updates

```
$ lmm update --game skyrim-se

Checking 5 mod(s) for updates...

MOD                      CURRENT    AVAILABLE   POLICY
---                      -------    ---------   ------
SkyUI                    5.2.0      5.3.0       notify
SKSE64                   2.2.3      2.2.4       auto ✓
Better Dialogue          1.8.0      1.8.1       pinned

2 update(s) available.
1 auto-update applied: SKSE64 2.2.3 → 2.2.4
```

### Apply Specific Update

```
$ lmm update skyui --game skyrim-se

Updating SkyUI 5.2.0 → 5.3.0...
  Downloading SkyUI-5.3.0.zip...
  [████████████████████] 100% (2.1 MB)
  Extracting to cache...
  Deploying to game directory...

✓ Updated: SkyUI 5.2.0 → 5.3.0
  Previous version preserved for rollback
```

### Dry Run

```
$ lmm update --game skyrim-se --dry-run

Would apply the following updates:

MOD                      CURRENT    NEW
---                      -------    ---
SKSE64 (auto)           2.2.3      2.2.4
SkyUI                   5.2.0      5.3.0

2 update(s) would be applied. Use without --dry-run to apply.
```

### Change Update Policy

```
$ lmm mod set-update skyui --game skyrim-se --auto
✓ SkyUI update policy: auto

$ lmm mod set-update skyui --game skyrim-se --pin
✓ SkyUI update policy: pinned (v5.3.0)
```

### Rollback

```
$ lmm update rollback skyui --game skyrim-se

Rolling back SkyUI 5.3.0 → 5.2.0...
  Undeploying current files...
  Deploying previous version...

✓ Rolled back: SkyUI 5.3.0 → 5.2.0
```

---

## Implementation

### Task 1: Database Migration V2

**File:** `internal/storage/db/migrations.go`

Add `migrateV2` to add `previous_version` column:

```go
func migrateV2(d *DB) error {
    _, err := d.Exec(`ALTER TABLE installed_mods ADD COLUMN previous_version TEXT`)
    return err
}
```

Update `currentVersion` and migrations slice.

### Task 2: Database Layer Updates

**File:** `internal/storage/db/mods.go`

Add methods:

```go
// UpdateModVersion updates a mod's version, preserving previous for rollback
func (d *DB) UpdateModVersion(sourceID, modID, gameID, profileName, newVersion string) error

// GetInstalledMod retrieves a single installed mod
func (d *DB) GetInstalledMod(sourceID, modID, gameID, profileName string) (*domain.InstalledMod, error)

// SwapModVersions swaps version and previous_version (for rollback)
func (d *DB) SwapModVersions(sourceID, modID, gameID, profileName string) error
```

Update `GetInstalledMods` and `SaveInstalledMod` to handle `previous_version`.

### Task 3: Updater Enhancement

**File:** `internal/core/updater.go`

Add methods:

```go
// ApplyUpdate downloads and deploys a mod update
func (u *Updater) ApplyUpdate(ctx context.Context, svc *Service, game *domain.Game,
    installed *domain.InstalledMod, update *domain.Update, progressFn ProgressFunc) error

// Rollback reverts to the previous version
func (u *Updater) Rollback(ctx context.Context, svc *Service, game *domain.Game,
    installed *domain.InstalledMod) error
```

### Task 4: Service Layer

**File:** `internal/core/service.go`

Add methods:

```go
// UpdateMod updates a single mod to a new version
func (s *Service) UpdateMod(ctx context.Context, sourceID string, game *domain.Game,
    installed *domain.InstalledMod, newVersion string, progressFn ProgressFunc) error

// RollbackMod reverts a mod to its previous version
func (s *Service) RollbackMod(ctx context.Context, game *domain.Game,
    installed *domain.InstalledMod) error
```

### Task 5: Update Command Enhancement

**File:** `cmd/lmm/update.go`

Add:

- `--dry-run` flag
- Actual update application when `--all` is used
- Single mod update when mod ID is provided
- Show update policy in output

### Task 6: New `mod set-update` Command

**File:** `cmd/lmm/mod.go`

New command structure:

```
lmm mod set-update <mod-id> --game <game> [--auto|--notify|--pin]
```

### Task 7: New `update rollback` Subcommand

**File:** `cmd/lmm/update.go` or new `cmd/lmm/rollback.go`

New command:

```
lmm update rollback <mod-id> --game <game>
```

---

## Files Summary

| File                                | Action | Purpose                               |
| ----------------------------------- | ------ | ------------------------------------- |
| `internal/storage/db/migrations.go` | Modify | Add V2 migration for previous_version |
| `internal/storage/db/mods.go`       | Modify | Add version management methods        |
| `internal/storage/db/mods_test.go`  | Modify | Test new methods                      |
| `internal/core/updater.go`          | Modify | Add ApplyUpdate, Rollback             |
| `internal/core/updater_test.go`     | Modify | Test new methods                      |
| `internal/core/service.go`          | Modify | Add UpdateMod, RollbackMod            |
| `internal/core/service_test.go`     | Modify | Test new methods                      |
| `cmd/lmm/update.go`                 | Modify | Add --dry-run, actual updates         |
| `cmd/lmm/mod.go`                    | Create | mod set-update subcommand             |
| `cmd/lmm/update_test.go`            | Modify | Test CLI changes                      |

---

## Verification

1. `go test ./...` - All tests pass
2. Manual testing flow:
   - Install a mod: `lmm install "mod name" -g <game>`
   - Check for updates: `lmm update -g <game>`
   - Dry run: `lmm update -g <game> --dry-run`
   - Change policy: `lmm mod set-update <mod> -g <game> --pin`
   - Apply update: `lmm update <mod> -g <game>`
   - Rollback: `lmm update rollback <mod> -g <game>`
3. Verify previous version preserved in cache
4. Verify database tracks previous_version correctly
