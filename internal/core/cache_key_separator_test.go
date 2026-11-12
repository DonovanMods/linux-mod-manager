package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDownloadMod_VersionWithVariantSeparatorIsRefused (#603): a source
// version carrying cache.VariantSeparator would spell the same cache
// directory as another version's variant, so the download refuses it with
// the cache's typed error before anything is written - no entry, no
// staging debris.
func TestDownloadMod_VersionWithVariantSeparatorIsRefused(t *testing.T) {
	cacheDir := t.TempDir()
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: cacheDir}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: filepath.Join(t.TempDir(), "mods"), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	mod := &domain.Mod{ID: "123", SourceID: "test", Name: "At Mod", Version: "1.0@beta", GameID: "testgame"}
	file := &domain.DownloadableFile{ID: "file1", Name: "At File", FileName: "at.zip"}
	mock.AddDownload(file.ID, []byte("payload"))

	_, err = svc.DownloadModForTest(context.Background(), "test", game, mod, file, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, cache.ErrVersionSeparator)

	key := cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}
	entry := svc.GetGameCache(game).ModPath(key)
	assert.NoDirExists(t, entry)
	assert.NoDirExists(t, entry+".staging")
	_, statErr := os.Stat(filepath.Dir(entry))
	assert.True(t, os.IsNotExist(statErr), "nothing is created for the mod at all")
}

// TestApplyAdopt_ResolvedVersionWithVariantSeparatorIsRefused (#603): a
// copy-mode adoption writes a cache entry at the resolved source file's
// version, so a version carrying cache.VariantSeparator fails that item -
// counted and reported like any per-item failure - and writes no entry.
func TestApplyAdopt_ResolvedVersionWithVariantSeparatorIsRefused(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1"}}
	src.files = []domain.DownloadableFile{{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0@beta"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Scan.Untracked, 1)
	require.NotNil(t, plan.Scan.Untracked[0].ResolvedFile)

	result, err := svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err, "one refused entry does not fail the whole adopt")
	assert.Equal(t, 0, result.Adopted)
	assert.Equal(t, 1, result.Failed)
	require.Len(t, result.Failures, 1)
	assert.Contains(t, result.Failures[0].Reason, "variant separator")
	assert.NoDirExists(t, filepath.Join(svc.GlobalCacheDir(), "g1", "acme-source-42"))
}

// TestVerify_Fix_VersionRepairToASeparatorVersionIsRefused (#603): verify
// --fix's version repair renames the cache entry to the source's version,
// minting an entry there, so a source version carrying
// cache.VariantSeparator stops the repair before any write - the record and
// the cache entry stay as they were, and the reason reaches the finding.
func TestVerify_Fix_VersionRepairToASeparatorVersionIsRefused(t *testing.T) {
	svc, game := newFixTestGame(t)
	svc.RegisterSource(&scriptedVersionSource{
		mockSource: newMockSource("test-src"),
		filesByModID: map[string][]domain.DownloadableFile{
			"mod1": {{ID: "2", Version: "1.0@beta", IsPrimary: true}},
		},
	})
	gameCache := svc.GetGameCache(game)
	recorded := cache.Key{GameID: game.ID, SourceID: "test-src", ModID: "mod1", Version: "1.5"}
	require.NoError(t, gameCache.Store(recorded, "2", []byte("plugin content")))
	seedVerifyMod(t, svc, game, "test-src", "mod1", "Mod One", "1.5", []string{"2"}, false)
	require.NoError(t, svc.NewProfileManager().UpsertMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.5", FileIDs: []string{"2"}}))

	result, _ := runVersionRepairFix(t, svc, game)

	assert.Equal(t, 1, result.Issues, "the refused rename repairs nothing")
	f := mismatchFinding(t, result.Findings)
	assert.Equal(t, "version_mismatch", f.Status)
	assert.Contains(t, f.Note, "variant separator")
	assert.Equal(t, "1.5", installedVersion(t, svc, game, "default"))
	assert.True(t, gameCache.Exists(recorded))
	refused := recorded
	refused.Version = "1.0@beta"
	assert.NoDirExists(t, gameCache.ModPath(refused))
}

// TestImportArchive_EnrichedVersionWithSeparatorIsNotRenamedTo (#603): an
// --id import renames its cache entry to the source's version, minting an
// entry there. A source version carrying cache.VariantSeparator is refused
// and takes the failed-rename path the import already has: a warning note
// naming the refusal, the original entry untouched, and no entry at the
// refused version.
func TestImportArchive_EnrichedVersionWithSeparatorIsNotRenamedTo(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0@beta", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", AcceptConflicts: true}, nil)
	require.Error(t, err)
	assert.False(t, result.Renamed)
	require.NotEmpty(t, result.Notes)
	assert.Contains(t, result.Notes[0], "Warning: could not rename cache entry: ")
	assert.Contains(t, result.Notes[0], "variant separator")

	gameCache := svc.GetGameCache(game)
	assert.FileExists(t, filepath.Join(gameCache.ModPath(cache.Key{GameID: "g1", SourceID: "acme-source", ModID: "999", Version: "unknown"}), "mymod.esp"))
	assert.NoDirExists(t, gameCache.ModPath(cache.Key{GameID: "g1", SourceID: "acme-source", ModID: "999", Version: "2.0@beta"}))
}
