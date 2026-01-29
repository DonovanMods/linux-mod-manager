# P5 Installation Hooks Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enable user-defined scripts to run before/after mod installation and uninstallation operations.

**Architecture:** Add a HookRunner to execute scripts with environment variables. Extend config parsing to read hooks from games.yaml and profiles. Add batch methods to Installer that wrap hook execution around existing Install/Uninstall logic.

**Tech Stack:** Go standard library (os/exec for scripts), existing YAML parsing (gopkg.in/yaml.v3)

---

### Task 1: Add hook types to domain

**Files:**

- Create: `internal/domain/hooks.go`
- Test: `internal/domain/hooks_test.go`

**Step 1: Write the test**

```go
// internal/domain/hooks_test.go
package domain

import (
    "testing"

    "github.com/stretchr/testify/assert"
)

func TestHookConfig_IsEmpty(t *testing.T) {
    tests := []struct {
        name     string
        config   HookConfig
        expected bool
    }{
        {"all empty", HookConfig{}, true},
        {"has before_all", HookConfig{BeforeAll: "/path/to/script"}, false},
        {"has after_each", HookConfig{AfterEach: "/path/to/script"}, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            assert.Equal(t, tt.expected, tt.config.IsEmpty())
        })
    }
}

func TestGameHooks_IsEmpty(t *testing.T) {
    tests := []struct {
        name     string
        hooks    GameHooks
        expected bool
    }{
        {"all empty", GameHooks{}, true},
        {"has install hook", GameHooks{Install: HookConfig{BeforeAll: "/path"}}, false},
        {"has uninstall hook", GameHooks{Uninstall: HookConfig{AfterAll: "/path"}}, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            assert.Equal(t, tt.expected, tt.hooks.IsEmpty())
        })
    }
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/... -run TestHookConfig -v`
Expected: FAIL - types not defined

**Step 3: Write the implementation**

```go
// internal/domain/hooks.go
package domain

// HookConfig defines scripts for a single operation type (install or uninstall)
type HookConfig struct {
    BeforeAll  string `yaml:"before_all"`
    BeforeEach string `yaml:"before_each"`
    AfterEach  string `yaml:"after_each"`
    AfterAll   string `yaml:"after_all"`
}

// IsEmpty returns true if no hooks are configured
func (h HookConfig) IsEmpty() bool {
    return h.BeforeAll == "" && h.BeforeEach == "" && h.AfterEach == "" && h.AfterAll == ""
}

// GameHooks contains all hooks for a game
type GameHooks struct {
    Install   HookConfig `yaml:"install"`
    Uninstall HookConfig `yaml:"uninstall"`
}

// IsEmpty returns true if no hooks are configured
func (h GameHooks) IsEmpty() bool {
    return h.Install.IsEmpty() && h.Uninstall.IsEmpty()
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/... -run TestHookConfig -v && go test ./internal/domain/... -run TestGameHooks -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/domain/hooks.go internal/domain/hooks_test.go
git commit -m "feat(hooks): add HookConfig and GameHooks domain types"
```

---

### Task 2: Add Hooks field to Game struct

**Files:**

- Modify: `internal/domain/game.go`

**Step 1: Add Hooks field to Game struct**

In `internal/domain/game.go`, add the Hooks field to the Game struct:

```go
// Game represents a moddable game
type Game struct {
    ID                 string            // Unique slug, e.g., "skyrim-se"
    Name               string            // Display name
    InstallPath        string            // Game installation directory
    ModPath            string            // Where mods should be deployed
    SourceIDs          map[string]string // Map source to game ID, e.g., "nexusmods" -> "skyrimspecialedition"
    LinkMethod         LinkMethod        // How to deploy mods
    LinkMethodExplicit bool              // True if LinkMethod was explicitly set in config
    CachePath          string            // Optional: custom cache path for this game's mods
    Hooks              GameHooks         // Optional: hooks for install/uninstall operations
}
```

**Step 2: Run existing tests to verify no regression**

Run: `go test ./internal/domain/... -v`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/domain/game.go
git commit -m "feat(hooks): add Hooks field to Game struct"
```

---

### Task 3: Parse hooks from games.yaml

**Files:**

- Modify: `internal/storage/config/games.go`
- Test: `internal/storage/config/games_test.go` (create if needed)

**Step 1: Write the test**

```go
// internal/storage/config/games_hooks_test.go
package config

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestLoadGames_WithHooks(t *testing.T) {
    tempDir := t.TempDir()

    gamesYAML := `games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "/path/to/skyrim"
    mod_path: "/path/to/skyrim/Data"
    sources:
      nexusmods: "skyrimspecialedition"
    hooks:
      install:
        before_all: "~/.config/lmm/hooks/backup.sh"
        after_each: "/absolute/path/fnis.sh"
        after_all: "./relative/loot.sh"
      uninstall:
        after_all: "~/.config/lmm/hooks/cleanup.sh"
`
    require.NoError(t, os.WriteFile(filepath.Join(tempDir, "games.yaml"), []byte(gamesYAML), 0644))

    games, err := LoadGames(tempDir)
    require.NoError(t, err)

    game := games["skyrim-se"]
    require.NotNil(t, game)

    // Verify hooks are parsed and paths expanded
    assert.NotEmpty(t, game.Hooks.Install.BeforeAll)
    assert.Contains(t, game.Hooks.Install.BeforeAll, "/.config/lmm/hooks/backup.sh")
    assert.Equal(t, "/absolute/path/fnis.sh", game.Hooks.Install.AfterEach)
    assert.Equal(t, "./relative/loot.sh", game.Hooks.Install.AfterAll)
    assert.NotEmpty(t, game.Hooks.Uninstall.AfterAll)
}

func TestLoadGames_NoHooks(t *testing.T) {
    tempDir := t.TempDir()

    gamesYAML := `games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "/path/to/skyrim"
    mod_path: "/path/to/skyrim/Data"
`
    require.NoError(t, os.WriteFile(filepath.Join(tempDir, "games.yaml"), []byte(gamesYAML), 0644))

    games, err := LoadGames(tempDir)
    require.NoError(t, err)

    game := games["skyrim-se"]
    require.NotNil(t, game)
    assert.True(t, game.Hooks.IsEmpty())
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/config/... -run TestLoadGames_WithHooks -v`
Expected: FAIL - hooks not parsed

**Step 3: Update GameConfig and LoadGames**

In `internal/storage/config/games.go`:

1. Add HookConfigYAML and GameHooksYAML types:

```go
// HookConfigYAML is the YAML representation of hook configuration
type HookConfigYAML struct {
    BeforeAll  string `yaml:"before_all"`
    BeforeEach string `yaml:"before_each"`
    AfterEach  string `yaml:"after_each"`
    AfterAll   string `yaml:"after_all"`
}

// GameHooksYAML is the YAML representation of game hooks
type GameHooksYAML struct {
    Install   HookConfigYAML `yaml:"install"`
    Uninstall HookConfigYAML `yaml:"uninstall"`
}
```

2. Add Hooks field to GameConfig:

```go
type GameConfig struct {
    Name        string            `yaml:"name"`
    InstallPath string            `yaml:"install_path"`
    ModPath     string            `yaml:"mod_path"`
    Sources     map[string]string `yaml:"sources"`
    LinkMethod  string            `yaml:"link_method"`
    CachePath   string            `yaml:"cache_path"`
    Hooks       GameHooksYAML     `yaml:"hooks"`
}
```

3. Update LoadGames to parse hooks:

```go
// In the loop where games are created, add:
games[id] = &domain.Game{
    ID:                 id,
    Name:               cfg.Name,
    InstallPath:        ExpandPath(cfg.InstallPath),
    ModPath:            ExpandPath(cfg.ModPath),
    SourceIDs:          cfg.Sources,
    LinkMethod:         domain.ParseLinkMethod(cfg.LinkMethod),
    LinkMethodExplicit: cfg.LinkMethod != "",
    CachePath:          ExpandPath(cfg.CachePath),
    Hooks: domain.GameHooks{
        Install: domain.HookConfig{
            BeforeAll:  ExpandPath(cfg.Hooks.Install.BeforeAll),
            BeforeEach: ExpandPath(cfg.Hooks.Install.BeforeEach),
            AfterEach:  ExpandPath(cfg.Hooks.Install.AfterEach),
            AfterAll:   ExpandPath(cfg.Hooks.Install.AfterAll),
        },
        Uninstall: domain.HookConfig{
            BeforeAll:  ExpandPath(cfg.Hooks.Uninstall.BeforeAll),
            BeforeEach: ExpandPath(cfg.Hooks.Uninstall.BeforeEach),
            AfterEach:  ExpandPath(cfg.Hooks.Uninstall.AfterEach),
            AfterAll:   ExpandPath(cfg.Hooks.Uninstall.AfterAll),
        },
    },
}
```

4. Update saveGames to preserve hooks:

```go
// In the loop where configs are created, add:
cfg := GameConfig{
    Name:        game.Name,
    InstallPath: game.InstallPath,
    ModPath:     game.ModPath,
    Sources:     game.SourceIDs,
    CachePath:   game.CachePath,
    Hooks: GameHooksYAML{
        Install: HookConfigYAML{
            BeforeAll:  game.Hooks.Install.BeforeAll,
            BeforeEach: game.Hooks.Install.BeforeEach,
            AfterEach:  game.Hooks.Install.AfterEach,
            AfterAll:   game.Hooks.Install.AfterAll,
        },
        Uninstall: HookConfigYAML{
            BeforeAll:  game.Hooks.Uninstall.BeforeAll,
            BeforeEach: game.Hooks.Uninstall.BeforeEach,
            AfterEach:  game.Hooks.Uninstall.AfterEach,
            AfterAll:   game.Hooks.Uninstall.AfterAll,
        },
    },
}
```

**Step 4: Run tests**

Run: `go test ./internal/storage/config/... -run TestLoadGames -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/storage/config/games.go internal/storage/config/games_hooks_test.go
git commit -m "feat(hooks): parse hooks from games.yaml"
```

---

### Task 4: Add HookTimeout to config

**Files:**

- Modify: `internal/storage/config/config.go`
- Test: `internal/storage/config/config_test.go`

**Step 1: Write the test**

```go
// Add to internal/storage/config/config_test.go
func TestLoad_HookTimeout(t *testing.T) {
    tempDir := t.TempDir()

    t.Run("default timeout", func(t *testing.T) {
        cfg, err := Load(tempDir)
        require.NoError(t, err)
        assert.Equal(t, 60, cfg.HookTimeout)
    })

    t.Run("custom timeout", func(t *testing.T) {
        configYAML := `hook_timeout: 120`
        require.NoError(t, os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configYAML), 0644))

        cfg, err := Load(tempDir)
        require.NoError(t, err)
        assert.Equal(t, 120, cfg.HookTimeout)
    })
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/config/... -run TestLoad_HookTimeout -v`
Expected: FAIL - HookTimeout field doesn't exist

**Step 3: Add HookTimeout to Config**

In `internal/storage/config/config.go`:

1. Add field to Config struct:

```go
type Config struct {
    DefaultLinkMethod domain.LinkMethod `yaml:"-"`
    LinkMethodStr     string            `yaml:"default_link_method"`
    DefaultGame       string            `yaml:"default_game"`
    Keybindings       string            `yaml:"keybindings"`
    CachePath         string            `yaml:"cache_path"`
    HookTimeout       int               `yaml:"hook_timeout"`
}
```

2. Update Load to set default:

```go
func Load(configDir string) (*Config, error) {
    cfg := &Config{
        DefaultLinkMethod: domain.LinkSymlink,
        Keybindings:       "vim",
        HookTimeout:       60, // Default 60 seconds
    }
    // ... rest unchanged
}
```

**Step 4: Run test**

Run: `go test ./internal/storage/config/... -run TestLoad_HookTimeout -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/storage/config/config.go internal/storage/config/config_test.go
git commit -m "feat(hooks): add HookTimeout to config with 60s default"
```

---

### Task 5: Add hooks to Profile struct and parsing

**Files:**

- Modify: `internal/domain/profile.go`
- Modify: `internal/storage/config/profiles.go`
- Test: `internal/storage/config/profiles_test.go`

**Step 1: Write the test**

```go
// internal/storage/config/profiles_hooks_test.go
package config

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestLoadProfile_WithHooks(t *testing.T) {
    tempDir := t.TempDir()
    profileDir := filepath.Join(tempDir, "games", "skyrim-se", "profiles")
    require.NoError(t, os.MkdirAll(profileDir, 0755))

    profileYAML := `name: modded
game_id: skyrim-se
mods: []
hooks:
  install:
    after_all: "" # disable game hook
  uninstall:
    after_all: "~/.config/lmm/hooks/custom-cleanup.sh"
`
    require.NoError(t, os.WriteFile(filepath.Join(profileDir, "modded.yaml"), []byte(profileYAML), 0644))

    profile, err := LoadProfile(tempDir, "skyrim-se", "modded")
    require.NoError(t, err)

    // Empty string means explicitly disabled
    assert.Equal(t, "", profile.Hooks.Install.AfterAll)
    assert.True(t, profile.HooksExplicit.Install.AfterAll)

    // Custom hook
    assert.Contains(t, profile.Hooks.Uninstall.AfterAll, "custom-cleanup.sh")
    assert.True(t, profile.HooksExplicit.Uninstall.AfterAll)

    // Unset hooks should not be marked explicit
    assert.False(t, profile.HooksExplicit.Install.BeforeAll)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/config/... -run TestLoadProfile_WithHooks -v`
Expected: FAIL - Hooks field doesn't exist on Profile

**Step 3: Add Hooks to Profile in domain**

In `internal/domain/profile.go`, add:

```go
// HookExplicitFlags tracks which hooks were explicitly set in profile config
// This allows distinguishing between "not set" (inherit from game) and "set to empty" (disable)
type HookExplicitFlags struct {
    BeforeAll  bool
    BeforeEach bool
    AfterEach  bool
    AfterAll   bool
}

// GameHooksExplicit tracks which hooks were explicitly set
type GameHooksExplicit struct {
    Install   HookExplicitFlags
    Uninstall HookExplicitFlags
}
```

Then add to Profile struct:

```go
type Profile struct {
    Name          string
    GameID        string
    Mods          []ModReference
    LinkMethod    LinkMethod
    IsDefault     bool
    Hooks         GameHooks         // Profile-level hook overrides
    HooksExplicit GameHooksExplicit // Tracks which hooks were explicitly set
}
```

**Step 4: Update profiles.go to parse hooks**

In `internal/storage/config/profiles.go`:

1. Add YAML types for hook config with pointer fields to detect explicit setting:

```go
// ProfileHookConfigYAML uses pointers to distinguish "not set" from "set to empty"
type ProfileHookConfigYAML struct {
    BeforeAll  *string `yaml:"before_all"`
    BeforeEach *string `yaml:"before_each"`
    AfterEach  *string `yaml:"after_each"`
    AfterAll   *string `yaml:"after_all"`
}

type ProfileHooksYAML struct {
    Install   ProfileHookConfigYAML `yaml:"install"`
    Uninstall ProfileHookConfigYAML `yaml:"uninstall"`
}
```

2. Add Hooks field to ProfileConfig:

```go
type ProfileConfig struct {
    Name       string               `yaml:"name"`
    GameID     string               `yaml:"game_id"`
    Mods       []ModReferenceConfig `yaml:"mods"`
    LinkMethod string               `yaml:"link_method,omitempty"`
    IsDefault  bool                 `yaml:"is_default,omitempty"`
    Hooks      ProfileHooksYAML     `yaml:"hooks,omitempty"`
}
```

3. Update LoadProfile to parse hooks:

```go
// After creating profile, add hook parsing:
profile.Hooks, profile.HooksExplicit = parseProfileHooks(cfg.Hooks)
```

4. Add helper function:

```go
func parseProfileHooks(yaml ProfileHooksYAML) (domain.GameHooks, domain.GameHooksExplicit) {
    hooks := domain.GameHooks{}
    explicit := domain.GameHooksExplicit{}

    // Install hooks
    if yaml.Install.BeforeAll != nil {
        hooks.Install.BeforeAll = ExpandPath(*yaml.Install.BeforeAll)
        explicit.Install.BeforeAll = true
    }
    if yaml.Install.BeforeEach != nil {
        hooks.Install.BeforeEach = ExpandPath(*yaml.Install.BeforeEach)
        explicit.Install.BeforeEach = true
    }
    if yaml.Install.AfterEach != nil {
        hooks.Install.AfterEach = ExpandPath(*yaml.Install.AfterEach)
        explicit.Install.AfterEach = true
    }
    if yaml.Install.AfterAll != nil {
        hooks.Install.AfterAll = ExpandPath(*yaml.Install.AfterAll)
        explicit.Install.AfterAll = true
    }

    // Uninstall hooks
    if yaml.Uninstall.BeforeAll != nil {
        hooks.Uninstall.BeforeAll = ExpandPath(*yaml.Uninstall.BeforeAll)
        explicit.Uninstall.BeforeAll = true
    }
    if yaml.Uninstall.BeforeEach != nil {
        hooks.Uninstall.BeforeEach = ExpandPath(*yaml.Uninstall.BeforeEach)
        explicit.Uninstall.BeforeEach = true
    }
    if yaml.Uninstall.AfterEach != nil {
        hooks.Uninstall.AfterEach = ExpandPath(*yaml.Uninstall.AfterEach)
        explicit.Uninstall.AfterEach = true
    }
    if yaml.Uninstall.AfterAll != nil {
        hooks.Uninstall.AfterAll = ExpandPath(*yaml.Uninstall.AfterAll)
        explicit.Uninstall.AfterAll = true
    }

    return hooks, explicit
}
```

**Step 5: Run tests**

Run: `go test ./internal/storage/config/... -run TestLoadProfile -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/domain/profile.go internal/storage/config/profiles.go internal/storage/config/profiles_hooks_test.go
git commit -m "feat(hooks): add hooks to Profile with explicit tracking for overrides"
```

---

### Task 6: Create HookRunner

**Files:**

- Create: `internal/core/hooks.go`
- Test: `internal/core/hooks_test.go`

**Step 1: Write the test**

```go
// internal/core/hooks_test.go
package core

import (
    "context"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestHookRunner_Success(t *testing.T) {
    tempDir := t.TempDir()
    scriptPath := filepath.Join(tempDir, "success.sh")
    script := `#!/bin/bash
echo "stdout message"
echo "stderr message" >&2
exit 0
`
    require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

    runner := NewHookRunner(60 * time.Second)
    ctx := context.Background()
    hc := HookContext{
        GameID:   "skyrim-se",
        GamePath: "/path/to/game",
        ModPath:  "/path/to/mods",
        HookName: "install.before_all",
    }

    result, err := runner.Run(ctx, scriptPath, hc)
    require.NoError(t, err)
    assert.Contains(t, result.Stdout, "stdout message")
    assert.Contains(t, result.Stderr, "stderr message")
    assert.Equal(t, 0, result.ExitCode)
}

func TestHookRunner_NonZeroExit(t *testing.T) {
    tempDir := t.TempDir()
    scriptPath := filepath.Join(tempDir, "fail.sh")
    script := `#!/bin/bash
echo "error occurred" >&2
exit 42
`
    require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

    runner := NewHookRunner(60 * time.Second)
    ctx := context.Background()
    hc := HookContext{GameID: "test", HookName: "test.hook"}

    result, err := runner.Run(ctx, scriptPath, hc)
    require.Error(t, err)
    assert.Equal(t, 42, result.ExitCode)
    assert.Contains(t, result.Stderr, "error occurred")
}

func TestHookRunner_Timeout(t *testing.T) {
    tempDir := t.TempDir()
    scriptPath := filepath.Join(tempDir, "slow.sh")
    script := `#!/bin/bash
sleep 10
`
    require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

    runner := NewHookRunner(100 * time.Millisecond)
    ctx := context.Background()
    hc := HookContext{GameID: "test", HookName: "test.hook"}

    _, err := runner.Run(ctx, scriptPath, hc)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "timed out")
}

func TestHookRunner_NotFound(t *testing.T) {
    runner := NewHookRunner(60 * time.Second)
    ctx := context.Background()
    hc := HookContext{GameID: "test", HookName: "test.hook"}

    _, err := runner.Run(ctx, "/nonexistent/script.sh", hc)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "not found")
}

func TestHookRunner_NotExecutable(t *testing.T) {
    tempDir := t.TempDir()
    scriptPath := filepath.Join(tempDir, "noexec.sh")
    require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi"), 0644)) // no exec bit

    runner := NewHookRunner(60 * time.Second)
    ctx := context.Background()
    hc := HookContext{GameID: "test", HookName: "test.hook"}

    _, err := runner.Run(ctx, scriptPath, hc)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "not executable")
}

func TestHookRunner_EnvVars(t *testing.T) {
    tempDir := t.TempDir()
    scriptPath := filepath.Join(tempDir, "env.sh")
    script := `#!/bin/bash
echo "GAME_ID=$LMM_GAME_ID"
echo "GAME_PATH=$LMM_GAME_PATH"
echo "MOD_PATH=$LMM_MOD_PATH"
echo "MOD_ID=$LMM_MOD_ID"
echo "MOD_NAME=$LMM_MOD_NAME"
echo "MOD_VERSION=$LMM_MOD_VERSION"
echo "HOOK=$LMM_HOOK"
`
    require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))

    runner := NewHookRunner(60 * time.Second)
    ctx := context.Background()
    hc := HookContext{
        GameID:     "skyrim-se",
        GamePath:   "/path/to/game",
        ModPath:    "/path/to/mods",
        ModID:      "12345",
        ModName:    "SkyUI",
        ModVersion: "5.2",
        HookName:   "install.after_each",
    }

    result, err := runner.Run(ctx, scriptPath, hc)
    require.NoError(t, err)
    assert.Contains(t, result.Stdout, "GAME_ID=skyrim-se")
    assert.Contains(t, result.Stdout, "MOD_ID=12345")
    assert.Contains(t, result.Stdout, "MOD_NAME=SkyUI")
    assert.Contains(t, result.Stdout, "HOOK=install.after_each")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/... -run TestHookRunner -v`
Expected: FAIL - types not defined

**Step 3: Write the implementation**

```go
// internal/core/hooks.go
package core

import (
    "bytes"
    "context"
    "fmt"
    "os"
    "os/exec"
    "time"

    "github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// HookContext provides environment information for hook scripts
type HookContext struct {
    GameID     string
    GamePath   string
    ModPath    string
    ModID      string // Empty for *_all hooks
    ModName    string // Empty for *_all hooks
    ModVersion string // Empty for *_all hooks
    HookName   string // e.g., "install.before_all"
}

// HookResult contains the output from running a hook
type HookResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
}

// HookRunner executes hook scripts with timeout and environment
type HookRunner struct {
    timeout time.Duration
}

// NewHookRunner creates a new hook runner with the given timeout
func NewHookRunner(timeout time.Duration) *HookRunner {
    return &HookRunner{timeout: timeout}
}

// Run executes a hook script and returns its output
func (r *HookRunner) Run(ctx context.Context, scriptPath string, hc HookContext) (*HookResult, error) {
    result := &HookResult{}

    // Check script exists
    info, err := os.Stat(scriptPath)
    if os.IsNotExist(err) {
        return result, fmt.Errorf("hook script not found: %s", scriptPath)
    }
    if err != nil {
        return result, fmt.Errorf("checking hook script: %w", err)
    }

    // Check script is executable
    if info.Mode()&0111 == 0 {
        return result, fmt.Errorf("hook script not executable: %s", scriptPath)
    }

    // Create timeout context
    ctx, cancel := context.WithTimeout(ctx, r.timeout)
    defer cancel()

    // Build command
    cmd := exec.CommandContext(ctx, scriptPath)
    cmd.Env = append(os.Environ(),
        "LMM_GAME_ID="+hc.GameID,
        "LMM_GAME_PATH="+hc.GamePath,
        "LMM_MOD_PATH="+hc.ModPath,
        "LMM_MOD_ID="+hc.ModID,
        "LMM_MOD_NAME="+hc.ModName,
        "LMM_MOD_VERSION="+hc.ModVersion,
        "LMM_HOOK="+hc.HookName,
    )

    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr

    // Run command
    err = cmd.Run()
    result.Stdout = stdout.String()
    result.Stderr = stderr.String()

    // Handle exit code
    if err != nil {
        if ctx.Err() == context.DeadlineExceeded {
            return result, fmt.Errorf("hook timed out after %v: %s", r.timeout, scriptPath)
        }
        if exitErr, ok := err.(*exec.ExitError); ok {
            result.ExitCode = exitErr.ExitCode()
            return result, fmt.Errorf("hook failed with exit code %d: %s", result.ExitCode, scriptPath)
        }
        return result, fmt.Errorf("running hook: %w", err)
    }

    return result, nil
}

// ResolvedHooks contains the final merged hooks for an operation
type ResolvedHooks struct {
    Install   domain.HookConfig
    Uninstall domain.HookConfig
}

// ResolveHooks merges game-level hooks with profile-level overrides
func ResolveHooks(game *domain.Game, profile *domain.Profile) *ResolvedHooks {
    if game == nil {
        return nil
    }

    resolved := &ResolvedHooks{
        Install:   game.Hooks.Install,
        Uninstall: game.Hooks.Uninstall,
    }

    if profile == nil {
        return resolved
    }

    // Apply profile overrides (only if explicitly set)
    if profile.HooksExplicit.Install.BeforeAll {
        resolved.Install.BeforeAll = profile.Hooks.Install.BeforeAll
    }
    if profile.HooksExplicit.Install.BeforeEach {
        resolved.Install.BeforeEach = profile.Hooks.Install.BeforeEach
    }
    if profile.HooksExplicit.Install.AfterEach {
        resolved.Install.AfterEach = profile.Hooks.Install.AfterEach
    }
    if profile.HooksExplicit.Install.AfterAll {
        resolved.Install.AfterAll = profile.Hooks.Install.AfterAll
    }

    if profile.HooksExplicit.Uninstall.BeforeAll {
        resolved.Uninstall.BeforeAll = profile.Hooks.Uninstall.BeforeAll
    }
    if profile.HooksExplicit.Uninstall.BeforeEach {
        resolved.Uninstall.BeforeEach = profile.Hooks.Uninstall.BeforeEach
    }
    if profile.HooksExplicit.Uninstall.AfterEach {
        resolved.Uninstall.AfterEach = profile.Hooks.Uninstall.AfterEach
    }
    if profile.HooksExplicit.Uninstall.AfterAll {
        resolved.Uninstall.AfterAll = profile.Hooks.Uninstall.AfterAll
    }

    return resolved
}
```

**Step 4: Run tests**

Run: `go test ./internal/core/... -run TestHookRunner -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/core/hooks.go internal/core/hooks_test.go
git commit -m "feat(hooks): add HookRunner with timeout, env vars, and error handling"
```

---

### Task 7: Add ResolveHooks tests

**Files:**

- Test: `internal/core/hooks_test.go`

**Step 1: Add tests for ResolveHooks**

```go
// Add to internal/core/hooks_test.go

func TestResolveHooks_GameOnly(t *testing.T) {
    game := &domain.Game{
        Hooks: domain.GameHooks{
            Install: domain.HookConfig{
                BeforeAll: "/game/before.sh",
                AfterAll:  "/game/after.sh",
            },
        },
    }

    resolved := ResolveHooks(game, nil)

    assert.Equal(t, "/game/before.sh", resolved.Install.BeforeAll)
    assert.Equal(t, "/game/after.sh", resolved.Install.AfterAll)
}

func TestResolveHooks_ProfileOverride(t *testing.T) {
    game := &domain.Game{
        Hooks: domain.GameHooks{
            Install: domain.HookConfig{
                BeforeAll: "/game/before.sh",
                AfterAll:  "/game/after.sh",
                AfterEach: "/game/each.sh",
            },
        },
    }

    profile := &domain.Profile{
        Hooks: domain.GameHooks{
            Install: domain.HookConfig{
                AfterAll: "/profile/after.sh", // override
            },
        },
        HooksExplicit: domain.GameHooksExplicit{
            Install: domain.HookExplicitFlags{
                AfterAll: true, // only this is explicitly set
            },
        },
    }

    resolved := ResolveHooks(game, profile)

    assert.Equal(t, "/game/before.sh", resolved.Install.BeforeAll) // inherited
    assert.Equal(t, "/profile/after.sh", resolved.Install.AfterAll) // overridden
    assert.Equal(t, "/game/each.sh", resolved.Install.AfterEach)    // inherited
}

func TestResolveHooks_ProfileDisable(t *testing.T) {
    game := &domain.Game{
        Hooks: domain.GameHooks{
            Install: domain.HookConfig{
                AfterAll: "/game/loot.sh",
            },
        },
    }

    profile := &domain.Profile{
        Hooks: domain.GameHooks{
            Install: domain.HookConfig{
                AfterAll: "", // explicitly disabled
            },
        },
        HooksExplicit: domain.GameHooksExplicit{
            Install: domain.HookExplicitFlags{
                AfterAll: true,
            },
        },
    }

    resolved := ResolveHooks(game, profile)

    assert.Equal(t, "", resolved.Install.AfterAll) // disabled
}

func TestResolveHooks_NilGame(t *testing.T) {
    resolved := ResolveHooks(nil, nil)
    assert.Nil(t, resolved)
}
```

**Step 2: Run tests**

Run: `go test ./internal/core/... -run TestResolveHooks -v`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/core/hooks_test.go
git commit -m "test(hooks): add ResolveHooks unit tests for merge and override behavior"
```

---

### Task 8: Add --no-hooks global flag

**Files:**

- Modify: `cmd/lmm/root.go`

**Step 1: Add the flag**

In `cmd/lmm/root.go`:

1. Add variable:

```go
var (
    // ... existing vars
    noHooks bool
)
```

2. Add flag in init():

```go
func init() {
    // ... existing flags
    rootCmd.PersistentFlags().BoolVar(&noHooks, "no-hooks", false, "skip all hook scripts")
}
```

**Step 2: Run existing tests**

Run: `go test ./cmd/lmm/... -v`
Expected: PASS

**Step 3: Commit**

```bash
git add cmd/lmm/root.go
git commit -m "feat(hooks): add --no-hooks global flag to skip hook execution"
```

---

### Task 9: Add InstallBatch and UninstallBatch to Installer

**Files:**

- Modify: `internal/core/installer.go`
- Test: `internal/core/installer_test.go`

**Step 1: Write the test**

```go
// internal/core/installer_hooks_test.go
package core

import (
    "context"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/DonovanMods/linux-mod-manager/internal/domain"
    "github.com/DonovanMods/linux-mod-manager/internal/linker"
    "github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestInstaller_InstallBatch_WithHooks(t *testing.T) {
    // Setup temp directories
    tempDir := t.TempDir()
    cacheDir := filepath.Join(tempDir, "cache")
    modDir := filepath.Join(tempDir, "mods")
    hookDir := filepath.Join(tempDir, "hooks")
    logFile := filepath.Join(tempDir, "hook.log")

    require.NoError(t, os.MkdirAll(cacheDir, 0755))
    require.NoError(t, os.MkdirAll(modDir, 0755))
    require.NoError(t, os.MkdirAll(hookDir, 0755))

    // Create hook scripts that log when called
    beforeAllScript := filepath.Join(hookDir, "before_all.sh")
    afterEachScript := filepath.Join(hookDir, "after_each.sh")
    afterAllScript := filepath.Join(hookDir, "after_all.sh")

    require.NoError(t, os.WriteFile(beforeAllScript, []byte("#!/bin/bash\necho before_all >> "+logFile), 0755))
    require.NoError(t, os.WriteFile(afterEachScript, []byte("#!/bin/bash\necho after_each:$LMM_MOD_ID >> "+logFile), 0755))
    require.NoError(t, os.WriteFile(afterAllScript, []byte("#!/bin/bash\necho after_all >> "+logFile), 0755))

    // Setup cache with a test mod
    c := cache.New(cacheDir)
    game := &domain.Game{ID: "test-game", ModPath: modDir}
    mod := &domain.Mod{ID: "123", SourceID: "test", Version: "1.0", Name: "TestMod"}

    // Create mod files in cache
    modCachePath := filepath.Join(cacheDir, "test-game", "test-123", "1.0")
    require.NoError(t, os.MkdirAll(modCachePath, 0755))
    require.NoError(t, os.WriteFile(filepath.Join(modCachePath, "test.txt"), []byte("content"), 0644))

    // Create installer
    lnk := linker.New(domain.LinkSymlink)
    installer := NewInstaller(c, lnk, nil)

    // Setup hooks
    hooks := &ResolvedHooks{
        Install: domain.HookConfig{
            BeforeAll: beforeAllScript,
            AfterEach: afterEachScript,
            AfterAll:  afterAllScript,
        },
    }

    hookRunner := NewHookRunner(60 * time.Second)
    hookCtx := HookContext{
        GameID:   game.ID,
        GamePath: game.InstallPath,
        ModPath:  game.ModPath,
    }

    opts := BatchOptions{
        Hooks:       hooks,
        HookRunner:  hookRunner,
        HookContext: hookCtx,
    }

    // Run install batch
    ctx := context.Background()
    result := installer.InstallBatch(ctx, game, []*domain.Mod{mod}, "default", opts)

    assert.Equal(t, 1, result.Succeeded)
    assert.Equal(t, 0, result.Failed)

    // Verify hooks were called in order
    logContent, err := os.ReadFile(logFile)
    require.NoError(t, err)
    assert.Contains(t, string(logContent), "before_all")
    assert.Contains(t, string(logContent), "after_each:123")
    assert.Contains(t, string(logContent), "after_all")
}

func TestInstaller_InstallBatch_BeforeAllFails(t *testing.T) {
    tempDir := t.TempDir()
    hookDir := filepath.Join(tempDir, "hooks")
    require.NoError(t, os.MkdirAll(hookDir, 0755))

    // Create failing before_all hook
    beforeAllScript := filepath.Join(hookDir, "before_all.sh")
    require.NoError(t, os.WriteFile(beforeAllScript, []byte("#!/bin/bash\nexit 1"), 0755))

    c := cache.New(filepath.Join(tempDir, "cache"))
    lnk := linker.New(domain.LinkSymlink)
    installer := NewInstaller(c, lnk, nil)

    hooks := &ResolvedHooks{
        Install: domain.HookConfig{BeforeAll: beforeAllScript},
    }

    opts := BatchOptions{
        Hooks:       hooks,
        HookRunner:  NewHookRunner(60 * time.Second),
        HookContext: HookContext{GameID: "test"},
    }

    game := &domain.Game{ID: "test", ModPath: filepath.Join(tempDir, "mods")}
    mod := &domain.Mod{ID: "123", SourceID: "test", Version: "1.0"}

    ctx := context.Background()
    result := installer.InstallBatch(ctx, game, []*domain.Mod{mod}, "default", opts)

    assert.Equal(t, 0, result.Succeeded)
    assert.NotNil(t, result.Error)
    assert.Contains(t, result.Error.Error(), "before_all")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/core/... -run TestInstaller_InstallBatch -v`
Expected: FAIL - InstallBatch doesn't exist

**Step 3: Add BatchOptions and BatchResult types, and batch methods**

In `internal/core/installer.go`, add:

```go
// BatchOptions configures batch install/uninstall operations
type BatchOptions struct {
    Hooks       *ResolvedHooks
    HookRunner  *HookRunner
    HookContext HookContext
    Force       bool // If true, continue on before_* hook failures
}

// BatchResult contains the outcome of a batch operation
type BatchResult struct {
    Succeeded int
    Failed    int
    Skipped   []SkippedMod
    Error     error // Non-nil if operation aborted (e.g., before_all failed)
}

// SkippedMod records a mod that was skipped due to hook failure
type SkippedMod struct {
    Mod    *domain.Mod
    Reason string
}

// InstallBatch installs multiple mods with hook support
func (i *Installer) InstallBatch(ctx context.Context, game *domain.Game, mods []*domain.Mod, profileName string, opts BatchOptions) BatchResult {
    result := BatchResult{}

    // Run before_all hook
    if opts.Hooks != nil && opts.Hooks.Install.BeforeAll != "" && opts.HookRunner != nil {
        hookCtx := opts.HookContext
        hookCtx.HookName = "install.before_all"

        _, err := opts.HookRunner.Run(ctx, opts.Hooks.Install.BeforeAll, hookCtx)
        if err != nil {
            if opts.Force {
                // Log warning but continue
            } else {
                result.Error = fmt.Errorf("install.before_all hook failed: %w", err)
                return result
            }
        }
    }

    // Install each mod
    for _, mod := range mods {
        // Run before_each hook
        if opts.Hooks != nil && opts.Hooks.Install.BeforeEach != "" && opts.HookRunner != nil {
            hookCtx := opts.HookContext
            hookCtx.HookName = "install.before_each"
            hookCtx.ModID = mod.ID
            hookCtx.ModName = mod.Name
            hookCtx.ModVersion = mod.Version

            _, err := opts.HookRunner.Run(ctx, opts.Hooks.Install.BeforeEach, hookCtx)
            if err != nil {
                if opts.Force {
                    // Log warning but continue
                } else {
                    result.Skipped = append(result.Skipped, SkippedMod{
                        Mod:    mod,
                        Reason: fmt.Sprintf("before_each hook failed: %v", err),
                    })
                    continue
                }
            }
        }

        // Install the mod
        if err := i.Install(ctx, game, mod, profileName); err != nil {
            result.Failed++
            continue
        }

        result.Succeeded++

        // Run after_each hook (warn only on failure)
        if opts.Hooks != nil && opts.Hooks.Install.AfterEach != "" && opts.HookRunner != nil {
            hookCtx := opts.HookContext
            hookCtx.HookName = "install.after_each"
            hookCtx.ModID = mod.ID
            hookCtx.ModName = mod.Name
            hookCtx.ModVersion = mod.Version

            _, _ = opts.HookRunner.Run(ctx, opts.Hooks.Install.AfterEach, hookCtx)
            // Ignore error - after hooks only warn
        }
    }

    // Run after_all hook (warn only on failure)
    if opts.Hooks != nil && opts.Hooks.Install.AfterAll != "" && opts.HookRunner != nil {
        hookCtx := opts.HookContext
        hookCtx.HookName = "install.after_all"

        _, _ = opts.HookRunner.Run(ctx, opts.Hooks.Install.AfterAll, hookCtx)
        // Ignore error - after hooks only warn
    }

    return result
}

// UninstallBatch uninstalls multiple mods with hook support
func (i *Installer) UninstallBatch(ctx context.Context, game *domain.Game, mods []*domain.Mod, profileName string, opts BatchOptions) BatchResult {
    result := BatchResult{}

    // Run before_all hook
    if opts.Hooks != nil && opts.Hooks.Uninstall.BeforeAll != "" && opts.HookRunner != nil {
        hookCtx := opts.HookContext
        hookCtx.HookName = "uninstall.before_all"

        _, err := opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.BeforeAll, hookCtx)
        if err != nil {
            if opts.Force {
                // Log warning but continue
            } else {
                result.Error = fmt.Errorf("uninstall.before_all hook failed: %w", err)
                return result
            }
        }
    }

    // Uninstall each mod
    for _, mod := range mods {
        // Run before_each hook
        if opts.Hooks != nil && opts.Hooks.Uninstall.BeforeEach != "" && opts.HookRunner != nil {
            hookCtx := opts.HookContext
            hookCtx.HookName = "uninstall.before_each"
            hookCtx.ModID = mod.ID
            hookCtx.ModName = mod.Name
            hookCtx.ModVersion = mod.Version

            _, err := opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.BeforeEach, hookCtx)
            if err != nil {
                if opts.Force {
                    // Log warning but continue
                } else {
                    result.Skipped = append(result.Skipped, SkippedMod{
                        Mod:    mod,
                        Reason: fmt.Sprintf("before_each hook failed: %v", err),
                    })
                    continue
                }
            }
        }

        // Uninstall the mod
        if err := i.Uninstall(ctx, game, mod, profileName); err != nil {
            result.Failed++
            continue
        }

        result.Succeeded++

        // Run after_each hook (warn only on failure)
        if opts.Hooks != nil && opts.Hooks.Uninstall.AfterEach != "" && opts.HookRunner != nil {
            hookCtx := opts.HookContext
            hookCtx.HookName = "uninstall.after_each"
            hookCtx.ModID = mod.ID
            hookCtx.ModName = mod.Name
            hookCtx.ModVersion = mod.Version

            _, _ = opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.AfterEach, hookCtx)
        }
    }

    // Run after_all hook (warn only on failure)
    if opts.Hooks != nil && opts.Hooks.Uninstall.AfterAll != "" && opts.HookRunner != nil {
        hookCtx := opts.HookContext
        hookCtx.HookName = "uninstall.after_all"

        _, _ = opts.HookRunner.Run(ctx, opts.Hooks.Uninstall.AfterAll, hookCtx)
    }

    return result
}
```

**Step 4: Run tests**

Run: `go test ./internal/core/... -run TestInstaller_InstallBatch -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/core/installer.go internal/core/installer_hooks_test.go
git commit -m "feat(hooks): add InstallBatch and UninstallBatch with hook support"
```

---

### Task 10: Add hook helper to commands

**Files:**

- Create: `cmd/lmm/hooks.go`

**Step 1: Create helper file**

```go
// cmd/lmm/hooks.go
package main

import (
    "fmt"
    "time"

    "github.com/DonovanMods/linux-mod-manager/internal/core"
    "github.com/DonovanMods/linux-mod-manager/internal/domain"
    "github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// getHookRunner returns a HookRunner with the configured timeout, or nil if hooks are disabled
func getHookRunner(svc *core.Service) *core.HookRunner {
    if noHooks {
        return nil
    }

    cfg, err := config.Load(svc.ConfigDir())
    timeout := 60 // default
    if err == nil && cfg.HookTimeout > 0 {
        timeout = cfg.HookTimeout
    }

    return core.NewHookRunner(time.Duration(timeout) * time.Second)
}

// getResolvedHooks returns merged game+profile hooks, or nil if hooks are disabled
func getResolvedHooks(game *domain.Game, profile *domain.Profile) *core.ResolvedHooks {
    if noHooks {
        return nil
    }
    return core.ResolveHooks(game, profile)
}

// makeHookContext creates a HookContext for a game
func makeHookContext(game *domain.Game) core.HookContext {
    return core.HookContext{
        GameID:   game.ID,
        GamePath: game.InstallPath,
        ModPath:  game.ModPath,
    }
}

// printHookWarnings displays any hook-related warnings from a batch result
func printHookWarnings(result core.BatchResult) {
    for _, skipped := range result.Skipped {
        fmt.Printf("  ⚠ %s - skipped: %s\n", skipped.Mod.Name, skipped.Reason)
    }
}
```

**Step 2: Add ConfigDir method to Service if not present**

Check if `svc.ConfigDir()` exists. If not, add to `internal/core/service.go`:

```go
// ConfigDir returns the configuration directory path
func (s *Service) ConfigDir() string {
    return s.configDir
}
```

**Step 3: Run build**

Run: `go build ./cmd/lmm/...`
Expected: Success

**Step 4: Commit**

```bash
git add cmd/lmm/hooks.go internal/core/service.go
git commit -m "feat(hooks): add hook helpers for CLI commands"
```

---

### Task 11: Integrate hooks into install command

**Files:**

- Modify: `cmd/lmm/install.go`

**Step 1: Update installMultipleMods to use InstallBatch**

In `cmd/lmm/install.go`, find the `installMultipleMods` function and update it to use hooks:

1. At the start of the function, get hooks:

```go
// Get hooks
profile, _ := config.LoadProfile(svc.ConfigDir(), game.ID, profileName)
hooks := getResolvedHooks(game, profile)
hookRunner := getHookRunner(svc)

opts := core.BatchOptions{
    Hooks:       hooks,
    HookRunner:  hookRunner,
    HookContext: makeHookContext(game),
    Force:       installForce,
}
```

2. Replace the manual loop with InstallBatch call, or wrap it appropriately.

Note: The current `installMultipleMods` does more than just install (conflict checking, downloading). We need to integrate hooks at the right points:

- `before_all` at the very start
- `before_each` / `after_each` around each mod's deploy step
- `after_all` at the very end

For now, a simpler approach is to run `before_all` at start and `after_all` at end in `installMultipleMods`, and run `before_each`/`after_each` in the per-mod loop.

**Step 2: Add hook calls to installMultipleMods**

Add at the start of `installMultipleMods`:

```go
// Get hooks
profile, _ := config.LoadProfile(getServiceConfig().ConfigDir, game.ID, profileName)
hooks := getResolvedHooks(game, profile)
hookRunner := getHookRunner(svc)

// Run install.before_all hook
if hooks != nil && hooks.Install.BeforeAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.before_all"
    if _, err := hookRunner.Run(ctx, hooks.Install.BeforeAll, hookCtx); err != nil {
        if !installForce {
            return fmt.Errorf("install.before_all hook failed: %w", err)
        }
        fmt.Printf("⚠ install.before_all hook failed (continuing with --force): %v\n", err)
    }
}
```

Add around each mod installation:

```go
// Run install.before_each hook
if hooks != nil && hooks.Install.BeforeEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.before_each"
    hookCtx.ModID = mod.ID
    hookCtx.ModName = mod.Name
    hookCtx.ModVersion = mod.Version
    if _, err := hookRunner.Run(ctx, hooks.Install.BeforeEach, hookCtx); err != nil {
        if !installForce {
            fmt.Printf("  ⚠ %s - skipped (before_each hook failed)\n", mod.Name)
            failed++
            continue
        }
    }
}

// ... existing install logic ...

// Run install.after_each hook (warn only)
if hooks != nil && hooks.Install.AfterEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.after_each"
    hookCtx.ModID = mod.ID
    hookCtx.ModName = mod.Name
    hookCtx.ModVersion = mod.Version
    if _, err := hookRunner.Run(ctx, hooks.Install.AfterEach, hookCtx); err != nil {
        fmt.Printf("  ⚠ %s - after_each hook failed: %v\n", mod.Name, err)
    }
}
```

Add at the end:

```go
// Run install.after_all hook (warn only)
if hooks != nil && hooks.Install.AfterAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.after_all"
    if _, err := hookRunner.Run(ctx, hooks.Install.AfterAll, hookCtx); err != nil {
        fmt.Printf("⚠ install.after_all hook failed: %v\n", err)
    }
}
```

**Step 3: Run tests**

Run: `go test ./cmd/lmm/... -v && go build ./cmd/lmm/...`
Expected: PASS and successful build

**Step 4: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "feat(hooks): integrate hooks into install command"
```

---

### Task 12: Integrate hooks into uninstall command

**Files:**

- Modify: `cmd/lmm/uninstall.go`

**Step 1: Add hook calls**

Update `runUninstall` to add hooks around the uninstall operation:

```go
// After getting the installedMod, before uninstalling:

// Get hooks
profile, _ := config.LoadProfile(getServiceConfig().ConfigDir, gameID, profileName)
hooks := getResolvedHooks(game, profile)
hookRunner := getHookRunner(service)

// Run uninstall.before_all hook
if hooks != nil && hooks.Uninstall.BeforeAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.before_all"
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.BeforeAll, hookCtx); err != nil {
        return fmt.Errorf("uninstall.before_all hook failed: %w", err)
    }
}

// Run uninstall.before_each hook
if hooks != nil && hooks.Uninstall.BeforeEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.before_each"
    hookCtx.ModID = installedMod.ID
    hookCtx.ModName = installedMod.Name
    hookCtx.ModVersion = installedMod.Version
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.BeforeEach, hookCtx); err != nil {
        return fmt.Errorf("uninstall.before_each hook failed: %w", err)
    }
}

// ... existing uninstall logic ...

// Run uninstall.after_each hook (warn only)
if hooks != nil && hooks.Uninstall.AfterEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.after_each"
    hookCtx.ModID = installedMod.ID
    hookCtx.ModName = installedMod.Name
    hookCtx.ModVersion = installedMod.Version
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.AfterEach, hookCtx); err != nil {
        fmt.Printf("⚠ uninstall.after_each hook failed: %v\n", err)
    }
}

// Run uninstall.after_all hook (warn only)
if hooks != nil && hooks.Uninstall.AfterAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.after_all"
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.AfterAll, hookCtx); err != nil {
        fmt.Printf("⚠ uninstall.after_all hook failed: %v\n", err)
    }
}
```

**Step 2: Run tests**

Run: `go test ./cmd/lmm/... -v && go build ./cmd/lmm/...`
Expected: PASS

**Step 3: Commit**

```bash
git add cmd/lmm/uninstall.go
git commit -m "feat(hooks): integrate hooks into uninstall command"
```

---

### Task 13: Integrate hooks into deploy command

**Files:**

- Modify: `cmd/lmm/deploy.go`

**Step 1: Add hook calls**

The deploy command does uninstall+install per mod. Add hooks around the batch:

1. At start of `runDeploy`, after getting game/profile, add uninstall hooks (since it undeploys first):

```go
profile, _ := config.LoadProfile(getServiceConfig().ConfigDir, game.ID, profileName)
hooks := getResolvedHooks(game, profile)
hookRunner := getHookRunner(service)

// If doing purge, run uninstall.before_all
if deployPurge && hooks != nil && hooks.Uninstall.BeforeAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.before_all"
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.BeforeAll, hookCtx); err != nil {
        if !installForce { // reuse --force logic
            return fmt.Errorf("uninstall.before_all hook failed: %w", err)
        }
        fmt.Printf("⚠ uninstall.before_all hook failed (continuing): %v\n", err)
    }
}
```

2. Before deploying mods, run install.before_all:

```go
if hooks != nil && hooks.Install.BeforeAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.before_all"
    if _, err := hookRunner.Run(ctx, hooks.Install.BeforeAll, hookCtx); err != nil {
        return fmt.Errorf("install.before_all hook failed: %w", err)
    }
}
```

3. Around each mod in the loop, add before_each/after_each for install

4. At end, run install.after_all

**Step 2: Run tests**

Run: `go build ./cmd/lmm/...`
Expected: Success

**Step 3: Commit**

```bash
git add cmd/lmm/deploy.go
git commit -m "feat(hooks): integrate hooks into deploy command"
```

---

### Task 14: Integrate hooks into purge command

**Files:**

- Modify: `cmd/lmm/purge.go`

**Step 1: Add hook calls**

Update `runPurge` to add uninstall hooks:

```go
// After getting mods list:
profile, _ := config.LoadProfile(getServiceConfig().ConfigDir, game.ID, profileName)
hooks := getResolvedHooks(game, profile)
hookRunner := getHookRunner(service)

// Run uninstall.before_all hook
if hooks != nil && hooks.Uninstall.BeforeAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.before_all"
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.BeforeAll, hookCtx); err != nil {
        if !purgeYes { // Could add --force to purge, or just warn
            fmt.Printf("⚠ uninstall.before_all hook failed: %v\n", err)
        }
    }
}

// In the loop, around each Uninstall call:
// before_each and after_each hooks

// At end:
// Run uninstall.after_all hook
if hooks != nil && hooks.Uninstall.AfterAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.after_all"
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.AfterAll, hookCtx); err != nil {
        fmt.Printf("⚠ uninstall.after_all hook failed: %v\n", err)
    }
}
```

**Step 2: Run tests**

Run: `go build ./cmd/lmm/...`
Expected: Success

**Step 3: Commit**

```bash
git add cmd/lmm/purge.go
git commit -m "feat(hooks): integrate hooks into purge command"
```

---

### Task 15: Integrate hooks into update command

**Files:**

- Modify: `cmd/lmm/update.go`

**Step 1: Add hook calls**

The update command does uninstall old + install new. Add hooks around each update:

In `applyUpdate`:

```go
// Get hooks (pass as parameter or get fresh)
profile, _ := config.LoadProfile(getServiceConfig().ConfigDir, game.ID, profileName)
hooks := getResolvedHooks(game, profile)
hookRunner := getHookRunner(service)

// Before uninstalling old version:
if hooks != nil && hooks.Uninstall.BeforeEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "uninstall.before_each"
    hookCtx.ModID = mod.ID
    hookCtx.ModName = mod.Name
    hookCtx.ModVersion = mod.Version
    if _, err := hookRunner.Run(ctx, hooks.Uninstall.BeforeEach, hookCtx); err != nil {
        return fmt.Errorf("uninstall.before_each hook failed: %w", err)
    }
}

// ... uninstall old ...

// After uninstalling old version:
if hooks != nil && hooks.Uninstall.AfterEach != "" && hookRunner != nil {
    // run after_each (warn only)
}

// Before installing new version:
if hooks != nil && hooks.Install.BeforeEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.before_each"
    hookCtx.ModID = newMod.ID
    hookCtx.ModName = newMod.Name
    hookCtx.ModVersion = newVersion
    if _, err := hookRunner.Run(ctx, hooks.Install.BeforeEach, hookCtx); err != nil {
        return fmt.Errorf("install.before_each hook failed: %w", err)
    }
}

// ... install new ...

// After installing new version:
if hooks != nil && hooks.Install.AfterEach != "" && hookRunner != nil {
    // run after_each (warn only)
}
```

**Step 2: Run tests**

Run: `go build ./cmd/lmm/...`
Expected: Success

**Step 3: Commit**

```bash
git add cmd/lmm/update.go
git commit -m "feat(hooks): integrate hooks into update command"
```

---

### Task 16: Integrate hooks into import command

**Files:**

- Modify: `cmd/lmm/import.go`

**Step 1: Add hook calls**

Update `runImport` to add install hooks around the deployment:

```go
// After setting up installer, before deploying:
profile, _ := config.LoadProfile(getServiceConfig().ConfigDir, gameID, profileName)
hooks := getResolvedHooks(game, profile)
hookRunner := getHookRunner(service)

// Run install.before_all hook
if hooks != nil && hooks.Install.BeforeAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.before_all"
    if _, err := hookRunner.Run(ctx, hooks.Install.BeforeAll, hookCtx); err != nil {
        if !importForce {
            return fmt.Errorf("install.before_all hook failed: %w", err)
        }
        fmt.Printf("⚠ install.before_all hook failed (continuing with --force): %v\n", err)
    }
}

// Run install.before_each hook
if hooks != nil && hooks.Install.BeforeEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.before_each"
    hookCtx.ModID = result.Mod.ID
    hookCtx.ModName = result.Mod.Name
    hookCtx.ModVersion = result.Mod.Version
    if _, err := hookRunner.Run(ctx, hooks.Install.BeforeEach, hookCtx); err != nil {
        if !importForce {
            return fmt.Errorf("install.before_each hook failed: %w", err)
        }
    }
}

// ... existing Install call ...

// Run install.after_each hook (warn only)
if hooks != nil && hooks.Install.AfterEach != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.after_each"
    hookCtx.ModID = result.Mod.ID
    hookCtx.ModName = result.Mod.Name
    hookCtx.ModVersion = result.Mod.Version
    if _, err := hookRunner.Run(ctx, hooks.Install.AfterEach, hookCtx); err != nil {
        fmt.Printf("⚠ install.after_each hook failed: %v\n", err)
    }
}

// Run install.after_all hook (warn only)
if hooks != nil && hooks.Install.AfterAll != "" && hookRunner != nil {
    hookCtx := makeHookContext(game)
    hookCtx.HookName = "install.after_all"
    if _, err := hookRunner.Run(ctx, hooks.Install.AfterAll, hookCtx); err != nil {
        fmt.Printf("⚠ install.after_all hook failed: %v\n", err)
    }
}
```

**Step 2: Run tests**

Run: `go build ./cmd/lmm/...`
Expected: Success

**Step 3: Commit**

```bash
git add cmd/lmm/import.go
git commit -m "feat(hooks): integrate hooks into import command"
```

---

### Task 17: Run all tests and lint

**Step 1: Run all tests**

Run: `go test ./... -v`
Expected: All PASS

**Step 2: Run linter**

Run: `trunk check`
Expected: No errors (warnings acceptable)

**Step 3: Fix any issues**

If there are failures, fix them.

**Step 4: Commit fixes if needed**

```bash
git add -A
git commit -m "fix: address test and lint issues for hooks"
```

---

### Task 18: Update version and changelog

**Files:**

- Modify: `cmd/lmm/root.go`
- Modify: `CHANGELOG.md`

**Step 1: Bump version**

In `cmd/lmm/root.go`, update:

```go
version = "0.12.0"
```

**Step 2: Update CHANGELOG.md**

Add new section:

```markdown
## [0.12.0] - 2026-01-28

### Added

- Installation hooks support - run custom scripts before/after mod operations
- New hook points: `install.before_all`, `install.before_each`, `install.after_each`, `install.after_all` and corresponding `uninstall.*` hooks
- Hooks configurable per-game in `games.yaml` with profile-level overrides
- Environment variables passed to hook scripts: `LMM_GAME_ID`, `LMM_MOD_ID`, `LMM_MOD_NAME`, etc.
- Contextual failure handling: `before_*` hooks abort on failure, `after_*` hooks warn only
- `--no-hooks` global flag to skip all hooks at runtime
- `hook_timeout` config option (default: 60 seconds)
```

**Step 3: Commit**

```bash
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 0.12.0"
```

---

### Task 19: Final verification

**Step 1: Build and test**

Run: `go build ./cmd/lmm && go test ./... -v`
Expected: All pass

**Step 2: Manual smoke test**

```bash
# Create a test hook
mkdir -p ~/.config/lmm/hooks
echo '#!/bin/bash
echo "Hook $LMM_HOOK called for game $LMM_GAME_ID"
' > ~/.config/lmm/hooks/test.sh
chmod +x ~/.config/lmm/hooks/test.sh

# Test with --no-hooks
./lmm --no-hooks list --game your-game

# Verify hook is skipped (no output from hook)
```

**Step 3: Done**

All tasks complete. Ready for code review.
