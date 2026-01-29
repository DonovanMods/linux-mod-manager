package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestScript creates an executable script in the temp directory
func createTestScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(scriptPath, []byte(content), 0755))
	return scriptPath
}

func TestInstallBatch_Success(t *testing.T) {
	// Setup directories
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	// Create cache with two mods
	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "200", "2.0", "mod2.esp", []byte("mod2")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.Mod{
		{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"},
		{ID: "200", SourceID: "nexusmods", Name: "Mod Two", Version: "2.0", GameID: "skyrim"},
	}
	versions := []string{"1.0", "2.0"}

	// Create hook scripts that track calls
	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "before_each $LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "after_each $LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "after_all" >> `+callLog+`
exit 0`)

	hooks := &core.ResolvedHooks{
		Install: domain.HookConfig{
			BeforeAll:  beforeAllScript,
			BeforeEach: beforeEachScript,
			AfterEach:  afterEachScript,
			AfterAll:   afterAllScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{
		GameID:   game.ID,
		GamePath: game.InstallPath,
		ModPath:  game.ModPath,
	}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
	}

	result, err := installer.InstallBatch(context.Background(), game, mods, versions, "default", opts)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify all mods installed
	assert.Len(t, result.Installed, 2)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)

	// Verify files were deployed
	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.NoError(t, err)
	_, err = os.Lstat(filepath.Join(gameDir, "mod2.esp"))
	assert.NoError(t, err)

	// Verify hook call order
	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	expectedLog := "before_all\nbefore_each 100\nafter_each 100\nbefore_each 200\nafter_each 200\nafter_all\n"
	assert.Equal(t, expectedLog, string(logContent))
}

func TestInstallBatch_BeforeAllFails_AbortsUnlessForce(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.Mod{
		{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"},
	}

	// Create failing before_all script
	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
exit 1`)

	hooks := &core.ResolvedHooks{
		Install: domain.HookConfig{
			BeforeAll: beforeAllScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{GameID: game.ID, GamePath: gameDir, ModPath: gameDir}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)

	// Without Force, should return error
	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
		Force:       false,
	}
	_, err := installer.InstallBatch(context.Background(), game, mods, []string{"1.0"}, "default", opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before_all")

	// With Force, should continue (mod should be installed)
	opts.Force = true
	result, err := installer.InstallBatch(context.Background(), game, mods, []string{"1.0"}, "default", opts)
	require.NoError(t, err)
	assert.Len(t, result.Installed, 1)
}

func TestInstallBatch_BeforeEachFails_SkipsMod(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "200", "2.0", "mod2.esp", []byte("mod2")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.Mod{
		{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"},
		{ID: "200", SourceID: "nexusmods", Name: "Mod Two", Version: "2.0", GameID: "skyrim"},
	}

	// Create before_each that fails only for mod 100
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "100" ]; then
    exit 1
fi
exit 0`)

	hooks := &core.ResolvedHooks{
		Install: domain.HookConfig{
			BeforeEach: beforeEachScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{GameID: game.ID, GamePath: gameDir, ModPath: gameDir}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
	}

	result, err := installer.InstallBatch(context.Background(), game, mods, []string{"1.0", "2.0"}, "default", opts)
	require.NoError(t, err)

	// Mod 100 should be skipped, mod 200 should be installed
	assert.Len(t, result.Installed, 1)
	assert.Equal(t, "200", result.Installed[0].ID)
	assert.Len(t, result.Skipped, 1)
	assert.Equal(t, "100", result.Skipped[0].Mod.ID)
	assert.Contains(t, result.Skipped[0].Reason, "before_each")
}

func TestInstallBatch_AfterEachFails_WarnsButContinues(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "200", "2.0", "mod2.esp", []byte("mod2")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.Mod{
		{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"},
		{ID: "200", SourceID: "nexusmods", Name: "Mod Two", Version: "2.0", GameID: "skyrim"},
	}

	// Create after_each that fails for mod 100
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "100" ]; then
    exit 1
fi
exit 0`)

	hooks := &core.ResolvedHooks{
		Install: domain.HookConfig{
			AfterEach: afterEachScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{GameID: game.ID, GamePath: gameDir, ModPath: gameDir}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
	}

	result, err := installer.InstallBatch(context.Background(), game, mods, []string{"1.0", "2.0"}, "default", opts)
	require.NoError(t, err)

	// Both mods should be installed (after_each failure is non-fatal)
	assert.Len(t, result.Installed, 2)
	assert.Empty(t, result.Skipped)
	// But we should have a warning error
	assert.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0].Error(), "after_each")
}

func TestInstallBatch_AfterAllFails_WarnsButReturnsSuccess(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.Mod{
		{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"},
	}

	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
exit 1`)

	hooks := &core.ResolvedHooks{
		Install: domain.HookConfig{
			AfterAll: afterAllScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{GameID: game.ID, GamePath: gameDir, ModPath: gameDir}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
	}

	result, err := installer.InstallBatch(context.Background(), game, mods, []string{"1.0"}, "default", opts)
	require.NoError(t, err)

	// Mod should be installed
	assert.Len(t, result.Installed, 1)
	// But we should have a warning error
	assert.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0].Error(), "after_all")
}

func TestInstallBatch_NoHooks(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.Mod{
		{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"},
	}

	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	opts := core.BatchOptions{} // No hooks

	result, err := installer.InstallBatch(context.Background(), game, mods, []string{"1.0"}, "default", opts)
	require.NoError(t, err)
	assert.Len(t, result.Installed, 1)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)
}

func TestUninstallBatch_Success(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "200", "2.0", "mod2.esp", []byte("mod2")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.InstalledMod{
		{Mod: domain.Mod{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"}},
		{Mod: domain.Mod{ID: "200", SourceID: "nexusmods", Name: "Mod Two", Version: "2.0", GameID: "skyrim"}},
	}

	// Install first
	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, installer.Install(context.Background(), game, &mods[0].Mod, "default"))
	require.NoError(t, installer.Install(context.Background(), game, &mods[1].Mod, "default"))

	// Create hook scripts that track calls
	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "before_each $LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "after_each $LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "after_all" >> `+callLog+`
exit 0`)

	hooks := &core.ResolvedHooks{
		Uninstall: domain.HookConfig{
			BeforeAll:  beforeAllScript,
			BeforeEach: beforeEachScript,
			AfterEach:  afterEachScript,
			AfterAll:   afterAllScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{
		GameID:   game.ID,
		GamePath: game.InstallPath,
		ModPath:  game.ModPath,
	}

	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
	}

	result, err := installer.UninstallBatch(context.Background(), game, mods, "default", opts)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify all mods uninstalled
	assert.Len(t, result.Uninstalled, 2)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)

	// Verify files were removed
	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(gameDir, "mod2.esp"))
	assert.True(t, os.IsNotExist(err))

	// Verify hook call order
	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	expectedLog := "before_all\nbefore_each 100\nafter_each 100\nbefore_each 200\nafter_each 200\nafter_all\n"
	assert.Equal(t, expectedLog, string(logContent))
}

func TestUninstallBatch_BeforeAllFails_AbortsUnlessForce(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.InstalledMod{
		{Mod: domain.Mod{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"}},
	}

	// Install first
	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, installer.Install(context.Background(), game, &mods[0].Mod, "default"))

	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
exit 1`)

	hooks := &core.ResolvedHooks{
		Uninstall: domain.HookConfig{
			BeforeAll: beforeAllScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{GameID: game.ID, GamePath: gameDir, ModPath: gameDir}

	// Without Force, should return error
	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
		Force:       false,
	}
	_, err := installer.UninstallBatch(context.Background(), game, mods, "default", opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before_all")

	// With Force, should continue
	opts.Force = true
	result, err := installer.UninstallBatch(context.Background(), game, mods, "default", opts)
	require.NoError(t, err)
	assert.Len(t, result.Uninstalled, 1)
}

func TestUninstallBatch_BeforeEachFails_SkipsMod(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "200", "2.0", "mod2.esp", []byte("mod2")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.InstalledMod{
		{Mod: domain.Mod{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"}},
		{Mod: domain.Mod{ID: "200", SourceID: "nexusmods", Name: "Mod Two", Version: "2.0", GameID: "skyrim"}},
	}

	// Install first
	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, installer.Install(context.Background(), game, &mods[0].Mod, "default"))
	require.NoError(t, installer.Install(context.Background(), game, &mods[1].Mod, "default"))

	// Create before_each that fails only for mod 100
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "100" ]; then
    exit 1
fi
exit 0`)

	hooks := &core.ResolvedHooks{
		Uninstall: domain.HookConfig{
			BeforeEach: beforeEachScript,
		},
	}
	runner := core.NewHookRunner(60 * time.Second)
	hookCtx := core.HookContext{GameID: game.ID, GamePath: gameDir, ModPath: gameDir}

	opts := core.BatchOptions{
		Hooks:       hooks,
		HookRunner:  runner,
		HookContext: hookCtx,
	}

	result, err := installer.UninstallBatch(context.Background(), game, mods, "default", opts)
	require.NoError(t, err)

	// Mod 100 should be skipped, mod 200 should be uninstalled
	assert.Len(t, result.Uninstalled, 1)
	assert.Equal(t, "200", result.Uninstalled[0].ID)
	assert.Len(t, result.Skipped, 1)
	assert.Equal(t, "100", result.Skipped[0].Mod.ID)
}

func TestUninstallBatch_NoHooks(t *testing.T) {
	cacheDir := t.TempDir()
	gameDir := t.TempDir()

	modCache := cache.New(cacheDir)
	require.NoError(t, modCache.Store("skyrim", "nexusmods", "100", "1.0", "mod1.esp", []byte("mod1")))

	game := &domain.Game{
		ID:          "skyrim",
		Name:        "Skyrim",
		InstallPath: gameDir,
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
	}

	mods := []*domain.InstalledMod{
		{Mod: domain.Mod{ID: "100", SourceID: "nexusmods", Name: "Mod One", Version: "1.0", GameID: "skyrim"}},
	}

	// Install first
	installer := core.NewInstaller(modCache, linker.New(domain.LinkSymlink), nil)
	require.NoError(t, installer.Install(context.Background(), game, &mods[0].Mod, "default"))

	opts := core.BatchOptions{} // No hooks

	result, err := installer.UninstallBatch(context.Background(), game, mods, "default", opts)
	require.NoError(t, err)
	assert.Len(t, result.Uninstalled, 1)
	assert.Empty(t, result.Skipped)
	assert.Empty(t, result.Errors)
}
