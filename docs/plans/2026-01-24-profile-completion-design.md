# Profile Completion Design

## Overview

Complete the profile functionality so profiles serve as the source of truth for mod state. Installing mods adds them to the current profile, switching profiles reconciles mod state, and importing profiles can install missing mods on new machines.

## Requirements

1. Installing a mod always adds it to the current profile
2. Uninstalling a mod removes it from the current profile
3. Switching profiles installs/enables/disables mods to match target profile
4. Importing profiles previews and optionally installs missing mods
5. New commands to handle manual edits and sync issues

## Design

### 1. Auto-add mods to profile on install

When `lmm install` completes successfully, the mod is automatically added to the current profile's mod list.

**Changes:**

- `cmd/lmm/install.go`: After saving to database, call `ProfileManager.AddMod()` with the mod reference
- Handle the case where profile doesn't exist yet (create "default" profile if needed)
- The `ModReference` stored includes: sourceID, modID, and version

**Uninstall behavior:**

- When `lmm uninstall` runs, also remove the mod from the current profile via `ProfileManager.RemoveMod()`

### 2. Profile switch with mod installation

When switching profiles via `lmm profile switch <name>`, the system reconciles the current state with the target profile.

**Steps:**

1. Load target profile's mod list
2. Get currently installed/enabled mods from database
3. Calculate diff:
   - **To disable**: Mods enabled now but not in target profile
   - **To enable**: Mods in target profile, installed but disabled
   - **To install**: Mods in target profile, not installed locally
4. If mods need installing, show preview and prompt for confirmation
5. Execute changes:
   - Disable mods (undeploy symlinks, update DB)
   - Enable mods (deploy from cache, update DB)
   - Download/install missing mods (reuse existing install logic)
6. Update default profile marker

**Output example:**

```
Switching to profile: survival

Will disable 2 mods:
  - SkyUI (2847)
  - Immersive HUD (3222)

Will enable 1 mod:
  - Frostfall (11163)

Will install 2 mods:
  - Campfire (64798)
  - iNeed (10111)

Proceed? [Y/n]:
```

### 3. Profile import with mod installation

When importing a profile via `lmm profile import <file>`, the system previews and optionally installs missing mods.

**Steps:**

1. Parse the YAML file to get profile data
2. Check if profile name already exists (error if so, unless `--force`)
3. For each mod reference in the profile:
   - Check if installed locally (in database)
   - If not, add to "missing" list
4. If missing mods exist:
   - Display preview of what will be installed
   - Prompt for confirmation
   - On confirm: download/install each mod
   - On decline: still save the profile, just skip installing
5. Save profile to config

**Output example:**

```
Importing profile: survival

Found 8 mods in profile.
  5 already installed
  3 need to be downloaded:
    - Campfire v1.12.1 (nexusmods:64798)
    - iNeed v2.0 (nexusmods:10111)
    - Frostfall v3.4.1 (nexusmods:11163)

Download and install missing mods? [Y/n]:
```

**Flags:**

- `--force`: Overwrite if profile already exists
- `--no-install`: Skip the install prompt entirely (just import profile config)

### 4. Profile apply and sync commands

Two new commands to handle manual edits and out-of-sync situations.

**`lmm profile apply [name]`** - Make system match profile

- If no name given, uses current/default profile
- Same logic as profile switch but for current profile
- Use case: User edited profile YAML manually, wants to apply those changes

```
$ lmm profile apply

Applying profile: default

Will disable 1 mod:
  - Old Mod (1234)

Will install 1 mod:
  - New Mod (5678)

Proceed? [Y/n]:
```

**`lmm profile sync [name]`** - Make profile match system

- Updates profile YAML to reflect current installed/enabled mods in database
- Use case: Profile got out of sync, or migrating from pre-profile installs

```
$ lmm profile sync

Syncing profile: default

Will add to profile:
  - Recently Installed Mod (9999)

Will remove from profile:
  - Uninstalled Mod (1111)

Proceed? [Y/n]:
```

Both show preview and require confirmation.

### 5. Export format

No changes needed. Current minimal format is sufficient:

- name
- game_id
- mods (source_id, mod_id, version)

### 6. Error handling

- **Install fails during switch/import**: Continue with remaining mods, report failures at end, don't roll back successful installs
- **Mod no longer exists on source**: Skip it, warn user, continue with others
- **Network unavailable**: Fail early with clear message before making any changes
- **Version mismatch**: If profile specifies version X but source only has Y, prompt user: "Version 1.0 not found, install latest (1.2) instead?"

### 7. Edge cases

- **Empty profile**: Valid - switching to it disables all mods
- **Profile with no game set**: Reject on import, require `--game` flag
- **Circular profile switching**: Not an issue - each switch is independent

## Implementation Order

1. Auto-add/remove mods to profile on install/uninstall
2. Profile sync command (DB → profile)
3. Profile apply command (profile → system)
4. Enhanced profile switch (with install capability)
5. Enhanced profile import (with install capability)

## Files to Modify

- `cmd/lmm/install.go` - Add to profile after install
- `cmd/lmm/uninstall.go` - Remove from profile after uninstall
- `cmd/lmm/profile.go` - Add apply/sync commands, enhance import with --force/--no-install
- `internal/core/profile.go` - Add reconciliation logic, install missing mods support
- `internal/domain/profile.go` - No changes expected
