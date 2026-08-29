package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_DefaultGame_Unset(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	got, err := svc.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestService_SetDefaultGame_PersistsAndReads(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	require.NoError(t, svc.SetDefaultGame(context.Background(), "skyrim-se"))

	got, err := svc.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "skyrim-se", got)
}

func TestService_ClearDefaultGame(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	require.NoError(t, svc.SetDefaultGame(context.Background(), "skyrim-se"))
	require.NoError(t, svc.ClearDefaultGame(context.Background()))

	got, err := svc.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestService_DefaultGame_SeesOutOfBandWrite pins that DefaultGame re-reads
// config.yaml on every call rather than caching NewService's load: several
// cmd/lmm tests (game_list_test.go, status_test.go, game_test.go) write
// config.yaml directly via config.Config.Save against an already-open
// Service/CLI state and expect the very next read to see it.
func TestService_DefaultGame_SeesOutOfBandWrite(t *testing.T) {
	dir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: dir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	require.NoError(t, (&config.Config{DefaultGame: "out-of-band"}).Save(dir))

	got, err := svc.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "out-of-band", got, "DefaultGame must re-read config.yaml, not cache NewService's load")
}

func TestServiceConfig_DefaultGame_Unset(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir()}

	got, err := cfg.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestServiceConfig_SetDefaultGame_PersistsAndReads(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir()}

	require.NoError(t, cfg.SetDefaultGame(context.Background(), "skyrim-se"))

	got, err := cfg.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "skyrim-se", got)
}

func TestServiceConfig_ClearDefaultGame(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir()}

	require.NoError(t, cfg.SetDefaultGame(context.Background(), "skyrim-se"))
	require.NoError(t, cfg.ClearDefaultGame(context.Background()))

	got, err := cfg.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestServiceConfig_DefaultGame_NoServiceRequired pins the whole point of
// this receiver: resolving/writing the default game must not require an
// open *Service (DB connection, games.yaml load, source registry) - callers
// like cmd/lmm's requireGame resolve --game before deciding whether a
// service is even needed, and must stay cheap when it's already given.
func TestServiceConfig_DefaultGame_NoServiceRequired(t *testing.T) {
	dir := t.TempDir() // no config.yaml, no games.yaml, no lmm.db
	cfg := core.ServiceConfig{ConfigDir: dir}

	got, err := cfg.DefaultGame(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a bare read must not create lmm.db or games.yaml")
}

// TestServiceConfig_SetDefaultGame_LoadFailureReportsLoadNotSave pins Task
// 22 review Important #1's second occurrence (2026-08-28): cmd/lmm's
// runGameClearDefault reads the default game through
// ServiceConfig.DefaultGame (correctly wrapped "loading config: %w" by the
// caller), then writes through ServiceConfig.ClearDefaultGame ->
// SetDefaultGame, which does its own independent config.Load before Save -
// a second read the pre-lift code never performed (it reused the
// already-loaded cfg). cmd's outer wrap around ClearDefaultGame
// (fmt.Errorf("saving config: %w", err)) mislabeled a failure of that
// second, write-step load as a save failure. SetDefaultGame must report its
// own load failure distinctly so the caller's wrap can be dropped.
func TestServiceConfig_SetDefaultGame_LoadFailureReportsLoadNotSave(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, (&config.Config{DefaultGame: "old-game"}).Save(dir))

	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Chmod(configPath, 0000))
	t.Cleanup(func() { _ = os.Chmod(configPath, 0o644) })

	cfg := core.ServiceConfig{ConfigDir: dir}
	err := cfg.SetDefaultGame(context.Background(), "new-game")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading config:", "a load failure must be reported as a load error, not a save error")
	assert.NotContains(t, err.Error(), "saving config:")
}
