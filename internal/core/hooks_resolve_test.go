package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHookRunner_HonoursConfigTimeout pins hookRunner's config.yaml
// hook_timeout wiring (Task 2, #286): mirrors the pre-lift CLI's
// getHookRunner (cmd/lmm/hooks.go) exactly - default 60s when config.yaml
// is absent, the configured value otherwise.
func TestHookRunner_HonoursConfigTimeout(t *testing.T) {
	t.Run("default 60s when config.yaml is absent", func(t *testing.T) {
		svc := newOpsService(t)

		runner, err := svc.hookRunner(context.Background())
		require.NoError(t, err)
		require.NotNil(t, runner)
		assert.Equal(t, 60*time.Second, runner.timeout)
	})

	t.Run("custom hook_timeout from config.yaml", func(t *testing.T) {
		configDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("hook_timeout: 5\n"), 0o644))
		svc, err := NewService(ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, svc.Close()) })

		runner, err := svc.hookRunner(context.Background())
		require.NoError(t, err)
		require.NotNil(t, runner)
		assert.Equal(t, 5*time.Second, runner.timeout)
	})
}

// TestResolvedHooks_MergesGameAndProfile pins resolvedHooks' merge
// behavior: an explicit profile-level override wins over the game-level
// hook, matching ResolveHooks exactly. The profile YAML is written
// directly (not via config.SaveProfile/ProfileManager, matching
// profiles_hooks_test.go's TestLoadProfile_WithHooks): SaveProfile never
// round-trips Hooks/HooksExplicit (#295, filed separately - out of scope
// for Task 2), so seeding through it would silently produce an empty
// profile-level override and defeat this test.
func TestResolvedHooks_MergesGameAndProfile(t *testing.T) {
	svc := newOpsService(t)
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: t.TempDir(),
		Hooks: domain.GameHooks{Install: domain.HookConfig{BeforeAll: "game-before-all"}},
	}

	profileDir := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	profileYAML := "name: default\ngame_id: " + game.ID + "\nmods: []\nhooks:\n  install:\n    before_all: \"profile-before-all\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte(profileYAML), 0o644))

	hooks, err := svc.resolvedHooks(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, hooks)
	assert.Equal(t, "profile-before-all", hooks.GetInstallBeforeAll(), "an explicit profile override must win over the game-level hook")
}

// TestResolvedHooks_ToleratesMissingProfile pins resolvedHooks' swallow-
// on-failure behavior: a profile that fails to load (here, one that was
// never created) resolves game-level hooks only, mirroring the pre-lift
// CLI's getResolvedHooks exactly (it swallows ANY profile-load error, not
// just domain.ErrProfileNotFound).
func TestResolvedHooks_ToleratesMissingProfile(t *testing.T) {
	svc := newOpsService(t)
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: t.TempDir(),
		Hooks: domain.GameHooks{Install: domain.HookConfig{BeforeAll: "game-before-all"}},
	}

	hooks, err := svc.resolvedHooks(context.Background(), game, "does-not-exist")
	require.NoError(t, err)
	require.NotNil(t, hooks)
	assert.Equal(t, "game-before-all", hooks.GetInstallBeforeAll(), "a missing profile must fall back to game-level hooks only")
}

// TestResolvedHooks_EmptyProfileNameSkipsProfileLoad pins the pre-lift
// CLI's profileName == "" short-circuit (getResolvedHooks never even
// attempts a profile load in that case): game-level hooks only.
func TestResolvedHooks_EmptyProfileNameSkipsProfileLoad(t *testing.T) {
	svc := newOpsService(t)
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: t.TempDir(),
		Hooks: domain.GameHooks{Install: domain.HookConfig{BeforeAll: "game-before-all"}},
	}

	hooks, err := svc.resolvedHooks(context.Background(), game, "")
	require.NoError(t, err)
	require.NotNil(t, hooks)
	assert.Equal(t, "game-before-all", hooks.GetInstallBeforeAll())
}

// TestHookContextFor_MirrorsMakeHookContext pins hookContextFor against the
// pre-lift CLI's makeHookContext (cmd/lmm/hooks.go): GameID/GamePath/
// ModPath only, HookName left empty for the caller to fill in per hook.
func TestHookContextFor_MirrorsMakeHookContext(t *testing.T) {
	game := &domain.Game{ID: "g1", InstallPath: "/install", ModPath: "/mods"}
	assert.Equal(t, HookContext{GameID: "g1", GamePath: "/install", ModPath: "/mods"}, hookContextFor(game))
}
