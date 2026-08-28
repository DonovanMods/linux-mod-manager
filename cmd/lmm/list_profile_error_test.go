package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoList_InvalidProfileLinkMethod_FailsLoud guards a Copilot release-
// review finding on #203: doList's own lock-state lookup used to ignore
// EVERY config.LoadProfile error (`profileYAML, _ := config.LoadProfile(...)`),
// including #172's fail-loud validation errors - an invalid link_method in
// the profile YAML would silently degrade `lmm list` (no lock info shown,
// and per #201 every mod would read as "absent from the load order")
// instead of surfacing the same load-time error every other command
// honors. Only a genuinely missing profile.yaml (domain.ErrProfileNotFound)
// is tolerable; any other error, including validation, must surface.
func TestDoList_InvalidProfileLinkMethod_FailsLoud(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	oldListProfile, oldListProfiles := listProfile, listProfiles
	listProfile, listProfiles = "", false
	t.Cleanup(func() { listProfile, listProfiles = oldListProfile, oldListProfiles })
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	profilePath := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", "default.yaml")
	require.FileExists(t, profilePath, "precondition: setupDoDeployTest/seedDeployableMod must have created the profile")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profilePath, append(data, []byte("\nlink_method: bogus\n")...), 0o644))

	err = doList(context.Background(), &cobra.Command{}, svc, game)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "link_method")
	assert.Contains(t, err.Error(), "bogus")
}

// TestDoList_MissingProfileYAML_StillLists is the tolerant leg's guard: a
// mod installed with NO profile.yaml ever written (e.g. a fresh DB row
// with no matching ProfileManager.Create/AddMod call) must still list
// successfully - only domain.ErrProfileNotFound is swallowed, unlike a
// genuine validation error (the sibling test above).
func TestDoList_MissingProfileYAML_StillLists(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	oldListProfile, oldListProfiles := listProfile, listProfiles
	listProfile, listProfiles = "", false
	t.Cleanup(func() { listProfile, listProfiles = oldListProfile, oldListProfiles })

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "a", SourceID: "src", Name: "Mod A", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	profilePath := filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", "default.yaml")
	_, statErr := os.Stat(profilePath)
	require.True(t, os.IsNotExist(statErr), "precondition: no profile.yaml should exist yet")

	out := listNonVerbose(t, svc, game)
	assert.Contains(t, out, "Mod A")
}
