package core

// #658: a link the reseed drops from a cache entry is reported on the
// acquiring flow's sink, naming the mod, by every acquire constructor -
// profile fill's own (acquireSupplied) included - and only once the commit
// has really dropped it.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFillAcquire_WarnsAboutALeftoverLinkAfterTheCommit(t *testing.T) {
	svc := newBareService(t)
	svc.RegisterSource(&swapTestSource{id: "demo", name: "Demo"})
	gameCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "g1", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "10", SourceID: "demo", Version: "1", Name: "Fill Mod"}
	file := &domain.DownloadableFile{ID: "101", FileName: "second.zip"}

	// The entry holds an earlier file and a link an extraction from before
	// #658 left beside it.
	key := cache.Key{GameID: "g1", SourceID: "demo", ModID: "10", Version: "1"}
	require.NoError(t, gameCache.Store(key, "first.esp", []byte("first")))
	entry := gameCache.ModPath(key)
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(entry, "leftover")))

	downloads := t.TempDir()
	archive := writeListingZip(t, filepath.Join(downloads, "second.zip"), "second.esp")
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	sum := md5.Sum(data)
	sup := suppliedArchive{dir: downloads, name: "second.zip", display: "second.zip", size: int64(len(data)), md5: hex.EncodeToString(sum[:])}

	var warnings []string
	sink := func(e Event) {
		if w, ok := e.(WarningEvent); ok {
			warnings = append(warnings, w.Message)
		}
	}
	a, err := svc.acquireSupplied(context.Background(), gameCache, "demo", game, mod, file, sup, fomodInput{}, sink)
	require.NoError(t, err)
	defer a.release()
	assert.Empty(t, warnings, "nothing is dropped, so nothing is reported, before the commit")

	_, err = svc.commitAcquired(context.Background(), a)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(entry, "leftover"))
	assert.True(t, os.IsNotExist(err), "the commit dropped the link")
	assert.FileExists(t, filepath.Join(entry, "first.esp"), "the reseed kept the earlier file")
	assert.FileExists(t, filepath.Join(entry, "second.esp"))
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "leftover")
	assert.Contains(t, warnings[0], "Fill Mod")
}

// A commit that fails after the reseed reports nothing: the entry still
// holds the link.
func TestCommit_AFailedCommitWarnsAboutNothing(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)
	key := cache.Key{GameID: "g1", SourceID: "src", ModID: "modX", Version: "1.0"}
	require.NoError(t, gameCache.Store(key, "keep.esp", []byte("keep")))
	entry := gameCache.ModPath(key)
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(entry, "leftover")))

	game := &domain.Game{ID: "g1", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "modX", SourceID: "src", Version: "1.0", Name: "Failing Mod"}
	file := &domain.DownloadableFile{ID: "second", FileName: "second.zip"}
	archive := writeListingZip(t, filepath.Join(t.TempDir(), "second.zip"), "x/y.esp")

	var warnings []string
	sink := func(e Event) {
		if w, ok := e.(WarningEvent); ok {
			warnings = append(warnings, w.Message)
		}
	}
	a, err := svc.acquireLocal(context.Background(), gameCache, game, mod, file, archive, fomodInput{}, sink)
	require.NoError(t, err)
	defer a.release()
	// The merge into staging fails: a file squats where the archive's
	// directory goes.
	require.NoError(t, os.WriteFile(filepath.Join(entry, "x"), []byte("squatter"), 0o644))

	_, err = svc.commitAcquired(context.Background(), a)
	require.Error(t, err)
	assert.Empty(t, warnings, "%s", strings.Join(warnings, "\n"))
	_, err = os.Lstat(filepath.Join(entry, "leftover"))
	assert.NoError(t, err, "the failed commit left the entry as it was")
}
