package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupListProfilesTest builds a *core.Service plus a game with two
// profiles ("default", the CreateOrResetDefault-created IsDefault profile,
// and "extra") for runListProfiles coverage - v2 Phase 2 Task 22
// characterization: 'lmm list --profiles' had no test coverage at all
// before this file, needed before re-pointing its config.ListProfiles/
// config.LoadProfile calls at ProfileManager.
func setupListProfilesTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	configDir = t.TempDir()
	dataDir = t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	pm := svc.NewProfileManager()
	_, err = pm.CreateOrResetDefault(game.ID)
	require.NoError(t, err)
	_, err = pm.Create(game.ID, "extra")
	require.NoError(t, err)

	return svc, game
}

func TestRunListProfiles_ListsAndMarksDefault(t *testing.T) {
	svc, game := setupListProfilesTest(t)

	out := captureStdout(t, func() error {
		return runListProfiles(&cobra.Command{}, svc, game.ID, game.Name)
	})

	assert.Contains(t, out, "Profiles for Game (g1):")
	assert.Contains(t, out, "default (default)")
	assert.Contains(t, out, "extra")
	assert.NotContains(t, out, "extra (default)")
}

func TestRunListProfiles_JSONOutput(t *testing.T) {
	svc, game := setupListProfilesTest(t)
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return runListProfiles(&cobra.Command{}, svc, game.ID, game.Name)
	})

	assert.Contains(t, out, `"game_id": "g1"`)
	assert.Contains(t, out, `"default"`)
	assert.Contains(t, out, `"extra"`)
}

func TestRunListProfiles_NoProfiles(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	game := &domain.Game{ID: "g2", Name: "Empty Game"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	out := captureStdout(t, func() error {
		return runListProfiles(&cobra.Command{}, svc, game.ID, game.Name)
	})

	assert.Contains(t, out, "No profiles for Empty Game.")
}

// TestRunListProfiles_MalformedProfileStillListed pins a pre-existing
// tolerance: a profile whose YAML fails to load (e.g. #172's fail-loud
// link_method validation) still appears BY NAME in the listing, just
// without the "(default)" marker that requires successfully loading it -
// unlike doList's own profile read (list_profile_error_test.go), which
// fails loud on the very same error. runListProfiles enumerates
// filenames only; it was never wired to fail on a single bad profile.
func TestRunListProfiles_MalformedProfileStillListed(t *testing.T) {
	svc, game := setupListProfilesTest(t)

	profilePath := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", "extra.yaml")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profilePath, append(data, []byte("\nlink_method: bogus\n")...), 0o644))

	out := captureStdout(t, func() error {
		return runListProfiles(&cobra.Command{}, svc, game.ID, game.Name)
	})

	assert.Contains(t, out, "default (default)")
	assert.Contains(t, out, "extra")
	assert.NotContains(t, out, "extra (default)")
}
