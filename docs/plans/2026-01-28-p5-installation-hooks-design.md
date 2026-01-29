# P5 Installation Hooks Design

**Date**: 2026-01-28
**Priority**: P5 (after Auto-Dependencies)
**Goal**: Run user-defined scripts at key points during mod operations

---

## Overview

Enable users to run custom scripts before/after mod installation and uninstallation. Hooks are configured per-game with optional per-profile overrides. Uses a beforeAll/afterAll/beforeEach/afterEach pattern familiar from test frameworks.

## Hook Points

| Hook                    | When                       | Use Case                         |
| ----------------------- | -------------------------- | -------------------------------- |
| `install.before_all`    | Once before batch starts   | Close game, create backup        |
| `install.before_each`   | Before each mod            | Validation, mod-specific prep    |
| `install.after_each`    | After each mod             | Run FNIS for animation mods      |
| `install.after_all`     | Once after batch completes | LOOT sort, regenerate load order |
| `uninstall.before_all`  | Once before batch starts   | Close game                       |
| `uninstall.before_each` | Before each mod            | Cleanup prep                     |
| `uninstall.after_each`  | After each mod             | Mod-specific cleanup             |
| `uninstall.after_all`   | Once after batch completes | Regenerate load order            |

## Configuration

### Game-level hooks (`games.yaml`)

```yaml
games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "/path/to/skyrim"
    mod_path: "/path/to/skyrim/Data"
    sources:
      nexusmods: "skyrimspecialedition"
    hooks:
      install:
        before_all: ~/.config/lmm/hooks/backup.sh
        after_each: ~/.config/lmm/hooks/fnis.sh
        after_all: ~/.config/lmm/hooks/loot-sort.sh
      uninstall:
        after_all: ~/.config/lmm/hooks/loot-sort.sh
```

### Profile-level overrides (`profiles/<name>.yaml`)

```yaml
name: minimal
mods: [...]
hooks:
  install:
    after_all: "" # disable LOOT sort for this profile
```

**Merge behavior**: Profile hooks merge with game hooks. Empty string `""` disables a hook. Unspecified hooks inherit from game.

### Global timeout (`config.yaml`)

```yaml
hook_timeout: 60 # seconds, default 60
```

## Environment Variables

Scripts receive these environment variables:

| Variable          | Description                       | Example                             |
| ----------------- | --------------------------------- | ----------------------------------- |
| `LMM_GAME_ID`     | Game identifier                   | `skyrim-se`                         |
| `LMM_GAME_PATH`   | Game install path                 | `/home/user/.steam/.../Skyrim`      |
| `LMM_MOD_PATH`    | Mod deployment path               | `/home/user/.steam/.../Skyrim/Data` |
| `LMM_MOD_ID`      | Mod ID (`*_each` hooks only)      | `12604`                             |
| `LMM_MOD_NAME`    | Mod name (`*_each` hooks only)    | `SkyUI`                             |
| `LMM_MOD_VERSION` | Mod version (`*_each` hooks only) | `5.2SE`                             |
| `LMM_HOOK`        | Hook being run                    | `install.after_each`                |

## Failure Behavior

| Hook Type     | On Failure                     | Rationale                  |
| ------------- | ------------------------------ | -------------------------- |
| `before_all`  | Abort entire operation         | Pre-condition not met      |
| `before_each` | Skip that mod, continue others | One mod might be special   |
| `after_each`  | Warn and continue              | Mod already installed      |
| `after_all`   | Warn only                      | All mods already installed |

**`--force` flag**: Bypasses `before_*` hook failures (treats as warnings).

**`--no-hooks` flag**: Global flag to skip all hooks at runtime.

## Command Integration

| Command     | Hooks Fired                                  |
| ----------- | -------------------------------------------- |
| `install`   | `install.*` hooks around batch               |
| `uninstall` | `uninstall.*` hooks (batch of 1)             |
| `import`    | `install.*` hooks (batch of 1)               |
| `purge`     | `uninstall.*` hooks around batch             |
| `deploy`    | `uninstall.*` then `install.*` (two batches) |
| `update`    | `uninstall.*` then `install.*` per mod       |

## New Installer Methods

```go
type BatchOptions struct {
    Hooks       *ResolvedHooks
    HookRunner  *HookRunner
    HookContext HookContext
}

func (i *Installer) InstallBatch(ctx context.Context, game *domain.Game,
    mods []*domain.Mod, profileName string, opts BatchOptions) BatchResult

func (i *Installer) UninstallBatch(ctx context.Context, game *domain.Game,
    mods []*domain.Mod, profileName string, opts BatchOptions) BatchResult
```

## Code Organization

### New Files

| File                          | Purpose                                                         |
| ----------------------------- | --------------------------------------------------------------- |
| `internal/core/hooks.go`      | `HookRunner`, `HookConfig`, `ResolvedHooks` types and execution |
| `internal/core/hooks_test.go` | Unit tests for hook runner                                      |

### Modified Files

| File                                 | Changes                                  |
| ------------------------------------ | ---------------------------------------- |
| `internal/storage/config/games.go`   | Add `Hooks` field to `GameConfig`        |
| `internal/storage/config/profile.go` | Add `Hooks` field to profile YAML        |
| `internal/storage/config/config.go`  | Add `HookTimeout` to app config          |
| `internal/domain/game.go`            | Add `Hooks` field to `Game` struct       |
| `internal/core/installer.go`         | Add `InstallBatch()`, `UninstallBatch()` |
| `cmd/lmm/root.go`                    | Add `--no-hooks` global flag             |
| `cmd/lmm/install.go`                 | Use `InstallBatch()` with hooks          |
| `cmd/lmm/uninstall.go`               | Use `UninstallBatch()` with hooks        |
| `cmd/lmm/deploy.go`                  | Use batch methods with hooks             |
| `cmd/lmm/purge.go`                   | Use `UninstallBatch()` with hooks        |
| `cmd/lmm/update.go`                  | Use batch methods with hooks             |
| `cmd/lmm/import.go`                  | Use `InstallBatch()` with hooks          |

## Edge Cases

| Scenario              | Handling                                 |
| --------------------- | ---------------------------------------- |
| Hook script not found | Error, abort/warn based on hook type     |
| Script not executable | Error, abort/warn based on hook type     |
| Script times out      | Kill process, error with timeout message |
| Script exits non-zero | Error includes exit code and stderr      |
| Empty hook path `""`  | Hook disabled (profile override)         |
| No hooks configured   | No-op, operation proceeds normally       |
| Hook path with `~`    | Expand to home directory                 |
| Relative hook path    | Resolve relative to config directory     |

## Testing Strategy

### Unit Tests

- `TestHookRunner_Success` - Script runs, returns stdout/stderr
- `TestHookRunner_ExitCode` - Non-zero exit returns error with code
- `TestHookRunner_Timeout` - Script killed after timeout
- `TestHookRunner_NotFound` - Missing script returns clear error
- `TestHookRunner_NotExecutable` - Non-executable returns clear error
- `TestHookRunner_EnvVars` - All env vars passed correctly
- `TestResolveHooks_*` - Game/profile merge scenarios

### Integration Tests

- `TestInstall_WithHooks` - Hooks fire in correct order
- `TestInstall_BeforeAllFails` - Operation aborts
- `TestInstall_BeforeEachFails` - Mod skipped, others continue
- `TestInstall_AfterEachFails` - Warning logged, continues
- `TestInstall_NoHooksFlag` - `--no-hooks` skips all hooks

### Test Fixtures

- `testdata/hooks/success.sh` - exits 0
- `testdata/hooks/fail.sh` - exits 1
- `testdata/hooks/slow.sh` - sleeps longer than timeout
- `testdata/hooks/env-check.sh` - prints env vars
