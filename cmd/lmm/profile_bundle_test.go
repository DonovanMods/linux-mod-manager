package main

// #623 at the CLI seam: `lmm profile export --bundle` carries an author's
// own files in the .lmmc, refuses a source's mod and a container-less
// export, and `lmm profile import` of such a container lists the bundled
// items and installs them.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bundleAuthorService is a game whose profile "mine" holds a mod imported
// from a file (local) and one from a source.
func bundleAuthorService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, configDir := newProfileExportTestService(t)
	game.SourceIDs = map[string]string{"steamworkshop": "1091500"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	for _, m := range []struct{ src, id, name string }{{"local", "u-1", "My Tweaks"}, {"nexusmods", "7", "Someone's Mod"}} {
		key := cache.Key{GameID: game.ID, SourceID: m.src, ModID: m.id, Version: "1.0"}
		require.NoError(t, svc.GetGameCache(game).Store(key, "tweaks/a.ini", []byte("[T]\n")))
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:         domain.Mod{ID: m.id, SourceID: m.src, Name: m.name, Version: "1.0", GameID: game.ID},
			ProfileName: "mine", Enabled: true,
		}))
	}
	require.NoError(t, config.SaveProfile(configDir, &domain.Profile{
		Name: "mine", GameID: game.ID,
		Mods: []domain.ModReference{{SourceID: "nexusmods", ModID: "7", Version: "1.0"}, {SourceID: "local", ModID: "u-1", Version: "1.0"}},
	}))
	return svc, game
}

func TestDoProfileExport_BundleNeedsAContainer(t *testing.T) {
	svc, game := bundleAuthorService(t)
	for _, tc := range []struct {
		output string
		plain  bool
	}{{"", false}, {filepath.Join(t.TempDir(), "x.yaml"), true}} {
		setExportFlags(t, tc.output, tc.plain, core.CollectionExportOptions{Bundle: []string{"My Tweaks"}})
		err := doProfileExport(context.Background(), svc, game, "mine")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only a .lmmc container holds")
	}
}

func TestDoProfileExport_BundleRefusesASourcesMod(t *testing.T) {
	svc, game := bundleAuthorService(t)
	setExportFlags(t, filepath.Join(t.TempDir(), "x.lmmc"), false, core.CollectionExportOptions{Bundle: []string{"nexusmods:7"}})
	err := doProfileExport(context.Background(), svc, game, "mine")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrBundleSourceMod)
	assert.Contains(t, err.Error(), "bundle only files of your own")
}

func TestDoProfileExport_BundleRoundTrip(t *testing.T) {
	svc, game := bundleAuthorService(t)
	path := filepath.Join(t.TempDir(), "mine.lmmc")
	setExportFlags(t, path, false, core.CollectionExportOptions{Bundle: []string{"My Tweaks"}})
	_, stderr, err := captureStdoutStderrErr(t, func() error {
		return doProfileExport(context.Background(), svc, game, "mine")
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "share only files you made")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	recipient, configDir := collectionRecipient(t)
	oldYes := profileImportYes
	profileImportYes = true
	t.Cleanup(func() { profileImportYes = oldYes })
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionImport(context.Background(), recipient, data, "")
	})
	require.Error(t, err, "the source's mod fails on its own: the recipient has no nexusmods source")
	assert.Contains(t, stdout, "Files the collection's author bundled")
	assert.Contains(t, stdout, "My Tweaks (my-tweaks): 1 file(s)")

	saved, err := config.LoadProfile(configDir, "cyber", "mine")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 2)
	assert.Equal(t, domain.SourceCollection, saved.Mods[1].SourceID)
	recipientGame, err := recipient.GetGame("cyber")
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(recipientGame.ModPath, "tweaks", "a.ini"))
	require.NoError(t, err)
	assert.Equal(t, "[T]\n", string(got), "the bundled files are installed")

	// A plain export of the recipient's profile says its bundled item
	// needs the container.
	setExportFlags(t, "", false, core.CollectionExportOptions{})
	_, stderr, err = captureStdoutStderrErr(t, func() error {
		return doProfileExport(context.Background(), recipient, recipientGame, "mine")
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "bundled item(s) (my-tweaks) travel only in a .lmmc container")
}
