package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #312: on a successful install whose PROFILE write fails (the profile YAML
// is unloadable here; EACCES and a cancelled create behave the same way),
// core records "could not create profile"/"could not update profile" on
// InstallResult.Notes and `--json` carries both - but the plain readout
// printed neither and then claimed "Added to profile: default" for a ref
// that was never written.
func TestDoInstall_ProfileWriteFailure_PrintsNotesAndSuppressesAddedToProfile(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Test Mod", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", Name: "Test Mod", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("main", []byte("mod-bytes"))

	// A profile file lmm can read and cannot write: the directory and the
	// file are read-only, so the UpsertMod that would record the ref fails.
	// (An unreadable profile file refuses the install outright since #462:
	// lmm cannot tell which profile is active.)
	if os.Getuid() == 0 {
		t.Skip("root can write a read-only file")
	}
	profileDir := filepath.Join(configDir, "games", "g1", "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	path := filepath.Join(profileDir, "default.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: default\ngame_id: g1\nmods: []\nis_default: true\n"), 0o444))
	require.NoError(t, os.Chmod(profileDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(profileDir, 0o755) })

	out := captureStdout(t, func() error { return doInstall(context.Background(), svc, game, nil) })

	assert.Contains(t, out, "✓ Installed: Test Mod v1.0", "the install itself still succeeded")
	assert.Contains(t, out, "could not update profile", "the profile-write failure must be visible without -v")
	assert.Contains(t, out, "NOT added to profile: default", "the ref was never written - do not claim it was")
	assert.NotContains(t, out, "  Added to profile:", "the affirmative line must not print")
}

// The happy path is unchanged: no notes, and the "Added to profile" line
// still prints.
func TestDoInstall_ProfileWriteSucceeds_StillSaysAddedToProfile(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Test Mod", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", Name: "Test Mod", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("main", []byte("mod-bytes"))

	out := captureStdout(t, func() error { return doInstall(context.Background(), svc, game, nil) })

	assert.Contains(t, out, "Added to profile: default")
	assert.NotContains(t, out, "could not update profile")
}

// TestShowInstallPlan_NamesADependencyItSwitchesBackOn is #431 fix round 2's
// R8 at the CLI: a dependency the profile had switched off is named before
// the confirmation prompt, with the profile and the mod that needs it,
// because the user did not ask for it by name.
func TestShowInstallPlan_NamesADependencyItSwitchesBackOn(t *testing.T) {
	plan := &core.InstallPlan{
		Profile: "survival",
		Mod:     domain.Mod{ID: "root", SourceID: "src", Name: "Root Mod"},
		Dependencies: []domain.Mod{
			{ID: "lib", SourceID: "src", Name: "Some Library"},
			{ID: "other", SourceID: "src", Name: "Other Library"},
		},
		ReenabledDependencies: []domain.ModReference{{SourceID: "src", ModID: "lib"}},
	}
	out := captureStdout(t, func() error {
		showInstallPlan(nil, plan)
		return nil
	})
	assert.Contains(t, out, `Note: Some Library is switched off in profile "survival". Installing Root Mod switches it back on there, because Root Mod depends on it.`)
	assert.NotContains(t, out, "Other Library is switched off")
}
