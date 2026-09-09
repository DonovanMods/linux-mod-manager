package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

	// An unparseable profile file: pm.Get fails with a YAML error (not
	// ErrProfileNotFound), so the create is refused and the UpsertMod that
	// would record the ref cannot run either.
	profileDir := filepath.Join(configDir, "games", "g1", "profiles")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte("\tnot: [valid\n"), 0o644))

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
