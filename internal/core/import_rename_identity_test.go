package core_test

// #614 review items 3 and 7: an archive import resolved against its source
// renames the cache entry onto the resolved identity and restamps it; an
// entry whose restamp did not happen (a stale marker) still reads as its
// directory's plain entry.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportArchive_WithID_RestampsTheRenamedEntry(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	src.files = []domain.DownloadableFile{{ID: "55", FileName: "mymod.zip", Version: "2.0", IsPrimary: true}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"mymod.esp": "data"})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999", Force: true}, nil)
	require.NoError(t, err)
	require.True(t, result.Renamed, "the fixture must take the rename")

	gameCache := svc.GetGameCache(game)
	key := cache.Key{GameID: "g1", SourceID: "acme-source", ModID: "999", Version: result.Mod.Version}
	dir := gameCache.ModPath(key)
	require.NoError(t, cache.CheckKey(dir, key), "the renamed entry's marker names its new key")
	assert.FileExists(t, filepath.Join(dir, cache.KeyMarkerName))

	// A restamp that did not happen leaves the old key's marker: stale, so
	// the entry is still its directory's plain one, and verify is clean.
	require.NoError(t, cache.StampKey(dir, cache.Key{Version: "unknown"}))
	assert.True(t, gameCache.Exists(key))
	verified, err := svc.VerifyForTest(context.Background(), game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Zero(t, verified.Issues, "%+v", verified.Findings)
}
