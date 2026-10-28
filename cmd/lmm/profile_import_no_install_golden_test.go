package main

// #472: `lmm profile import --no-install` said a mod "will be added" and
// then "Skipped installing" it. Every pending mod now gets one line saying
// what happens to it - the saved profile lists it, nothing installs or
// deploys it - pinned here as a golden transcript.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateProfileImportGoldens = flag.Bool("update-profile-import", false,
	"rewrite `lmm profile import` goldens from current output")

func TestDoProfileImport_NoInstallGolden(t *testing.T) {
	ctx := context.Background()
	svc, game, src := setupDoProfileImportTest(t)
	profileImportNoInstall = true
	src.AddMod(&domain.Mod{ID: "fresh", SourceID: "test-src", Name: "Fresh", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "fresh.esp", IsPrimary: true}})
	// "cached" is installed under another profile: its bytes are here.
	_, err := getProfileManager(svc).Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "cached", "1.0", "cached.esp", []byte("c")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "cached", SourceID: "test-src", Name: "Cached", Version: "1.0", GameID: "g1"},
		ProfileName: "other", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{
		{SourceID: "test-src", ModID: "cached", Version: "1.0"},
		{SourceID: "test-src", ModID: "fresh", Version: "2.0"},
	})

	out := captureStdout(t, func() error {
		return doProfileImport(ctx, svc, game, data)
	})

	path := filepath.Join("testdata", "profile_import_golden", "no_install.golden")
	if *updateProfileImportGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing - record it with -update-profile-import")
	assert.Equal(t, string(want), out)
	assert.NotContains(t, out, "will be added")
	assert.NotContains(t, out, "Skipped installing")

	saved, err := getProfileManager(svc).Get(ctx, "g1", "target")
	require.NoError(t, err)
	assert.Len(t, saved.Mods, 2, "both mods are recorded in the profile")
	rows, err := svc.GetInstalledMods(ctx, "g1", "target")
	require.NoError(t, err)
	assert.Empty(t, rows, "and nothing was installed")
	entries, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "or deployed")
}
