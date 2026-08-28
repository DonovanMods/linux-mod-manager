package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoModFiles_DeployCompile_ExmodzModExplainsMergedPak is the #197
// postsmoke UX fix: `lmm mod files <id>` for a validated+retained ".exmodz"
// mod (zero deployed files by design) used to print "No deployed files
// tracked... may need to be redeployed" - false, and actively misleading a
// user debugging exactly the postsmoke bug. It must instead say the mod
// participates in the profile's merged pak.
func TestDoModFiles_DeployCompile_ExmodzModExplainsMergedPak(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	installDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))
	pm := getProfileManager(svc)
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	const modID, version, fileID = "bear-mount", "1.0", "exmodz-file"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("bear-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	oldSource, oldProfile := modSource, modProfile
	modSource, modProfile = "fake-compiler", "default"
	t.Cleanup(func() { modSource, modProfile = oldSource, oldProfile })

	out := captureStdout(t, func() error {
		return doModFiles(context.Background(), svc, game, modID)
	})

	assert.Contains(t, out, "merged pak")
	assert.NotContains(t, out, "No deployed files tracked", "the old, false message must not survive alongside the new one")
	assert.NotContains(t, out, "may need to be redeployed")
}

// TestDoModFiles_NonCompile_ZeroFiles_KeepsOriginalMessage guards against
// over-broadening the #197 UX fix: a mod with genuinely zero tracked files
// for a reason OTHER THAN "it's a validated exmodz entry" (e.g. a plain
// DeployLink game with a stale/broken record) must keep the original
// "may need to be redeployed" guidance, not the merged-pak explanation.
func TestDoModFiles_NonCompile_ZeroFiles_KeepsOriginalMessage(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "other-game", Name: "Other Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		DeployMode: domain.DeployExtract, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-source": "external-other-id"},
	}
	require.NoError(t, svc.AddGame(game))
	pm := getProfileManager(svc)
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	const modID, version = "broken-mod", "1.0"
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-source", Name: "Broken Mod", Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-source", ModID: modID, Version: version}))

	oldSource, oldProfile := modSource, modProfile
	modSource, modProfile = "fake-source", "default"
	t.Cleanup(func() { modSource, modProfile = oldSource, oldProfile })

	out := captureStdout(t, func() error {
		return doModFiles(context.Background(), svc, game, modID)
	})

	assert.Contains(t, out, "No deployed files tracked")
	assert.Contains(t, out, "may need to be redeployed")
	assert.NotContains(t, out, "merged pak")
}
