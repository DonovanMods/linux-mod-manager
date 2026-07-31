package core

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLocalIngestService(t *testing.T) (*Service, *cache.Cache) {
	t.Helper()
	svc := &Service{extractor: NewExtractor()}
	return svc, cache.New(t.TempDir())
}

func TestIngestLocalToCacheDirectory(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "BiggerBackpack")
	require.NoError(t, os.MkdirAll(filepath.Join(modDir, "Config"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte("<xml/>"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "Config", "items.xml"), []byte("<items/>"), 0644))

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Version: "1.2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "BiggerBackpack"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, 2, result.FilesExtracted)
	assert.NotEmpty(t, result.Checksum, "#164: a directory ingest must produce a checksum so installs/verify can persist it")

	files, err := gameCache.ListFiles("7dtd", "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	assert.Len(t, files, 2)
}

// TestIngestLocalToCacheDirectory_ChecksumDeterministicAndDriftSensitive pins
// the #164 symmetry requirement for the directory-ingest digest: re-ingesting
// an UNCHANGED source directory must reproduce the stored value bit-for-bit
// (so a later `verify --fix` or reinstall converges instead of looping), and
// changing a member's content - or the member set itself - must change it (so
// the stored value is a real drift fingerprint, not a constant).
func TestIngestLocalToCacheDirectory_ChecksumDeterministicAndDriftSensitive(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "BiggerBackpack")
	require.NoError(t, os.MkdirAll(filepath.Join(modDir, "Config"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte("<xml/>"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "Config", "items.xml"), []byte("<items/>"), 0644))

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Version: "1.2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "BiggerBackpack"}

	first, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	require.NotEmpty(t, first.Checksum)

	unchanged, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, first.Checksum, unchanged.Checksum,
		"re-ingesting an unchanged source directory must reproduce the same digest")

	require.NoError(t, os.WriteFile(filepath.Join(modDir, "Config", "items.xml"), []byte("<items changed/>"), 0644))
	contentDrift, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.NotEqual(t, first.Checksum, contentDrift.Checksum,
		"changing a member file's content must change the digest")

	require.NoError(t, os.WriteFile(filepath.Join(modDir, "extra.txt"), []byte("new member"), 0644))
	memberDrift, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.NotEqual(t, contentDrift.Checksum, memberDrift.Checksum,
		"adding a member file must change the digest")
}

// TestIngestLocalToCacheDirectory_EmptyMemberSet_NoChecksum: a directory with
// no regular files has no content to fingerprint - the ingest still succeeds
// but reports no checksum, and callers (verify --fix, install) must then stay
// honest about NOT having persisted one rather than claiming success (#164).
func TestIngestLocalToCacheDirectory_EmptyMemberSet_NoChecksum(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "EmptyMod-1.0")
	require.NoError(t, os.MkdirAll(modDir, 0755))

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "EmptyMod-1.0", SourceID: "my-mods", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "EmptyMod-1.0"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, 0, result.FilesExtracted)
	assert.Empty(t, result.Checksum, "an empty member set has nothing to fingerprint - no digest")
}

func TestIngestLocalToCacheArchiveCopyMode(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	archive := filepath.Join(t.TempDir(), "coolmod-2.0.zip")
	require.NoError(t, os.WriteFile(archive, []byte("zipbytes"), 0644))

	game := &domain.Game{ID: "hytale", DeployMode: domain.DeployCopy}
	mod := &domain.Mod{ID: "coolmod-2.0", SourceID: "my-mods", Version: "2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "coolmod-2.0.zip"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, archive)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	assert.Equal(t, fmt.Sprintf("%x", md5.Sum([]byte("zipbytes"))), result.Checksum,
		"#164: a file/archive ingest must report the MD5 of the source file, matching the HTTP download path's MD5-of-archive semantics")

	cached := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "coolmod-2.0.zip")
	_, err = os.Stat(cached)
	assert.NoError(t, err)
}

// TestIngestLocalToCacheArchiveCopyModeUsesDeclaredFileName is a regression
// test for #52 item 12: ingestLocalToCache's copy-mode branch names the
// cached file filepath.Base(localPath) - the TEMP file's name - instead of
// file.FileName, the caller's declared name. In practice these usually
// match (the caller derives file.FileName from the same path), which is why
// TestIngestLocalToCacheArchiveCopyMode above never caught it; this test
// deliberately mismatches them, so the cached file name must come from the
// declared file.FileName, not the source path's basename.
func TestIngestLocalToCacheArchiveCopyModeUsesDeclaredFileName(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	// The on-disk temp file is named differently from what the source
	// declared as this file's name.
	tempFile := filepath.Join(t.TempDir(), "tmp-download-xyz.bin")
	require.NoError(t, os.WriteFile(tempFile, []byte("zipbytes"), 0644))

	game := &domain.Game{ID: "hytale", DeployMode: domain.DeployCopy}
	mod := &domain.Mod{ID: "coolmod-2.0", SourceID: "my-mods", Version: "2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "declared.zip"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, tempFile)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)

	declaredPath := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "declared.zip")
	_, err = os.Stat(declaredPath)
	assert.NoError(t, err, "cached file must be named after the declared file.FileName, not the temp path's basename")

	staleBasenamePath := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "tmp-download-xyz.bin")
	_, err = os.Stat(staleBasenamePath)
	assert.True(t, os.IsNotExist(err), "cached file must NOT be named after localPath's basename when file.FileName is declared")
}

// TestPrepareStagingCleansPartialStagingOnCopyFailure is a regression test
// for a reviewer-caught behavior break in the #52 item 11 extraction:
// pre-refactor, the caller armed `defer os.RemoveAll(stagePath)` BEFORE the
// Exists/copyDir step, so a copyDir failure partway through (stagePath
// already partially populated on disk) still got cleaned up on return.
// prepareStaging's first cut returned ("", "", err) on a copyDir failure
// instead - callers only register their defer AFTER checking the error, so
// it never runs, and they don't even have stagePath's value to clean up
// manually. Partial staging debris then leaks (self-healing only if the
// exact same source/id/version is retried, since prepareStaging always
// RemoveAlls stagePath on entry).
//
// Reproduced by seeding an existing cache entry with a subdirectory whose
// permissions are stripped to 0000: copyDir's top-level MkdirAll(stagePath)
// (and, per copyDirFollowing's recursion, MkdirAll(stagePath/sealed) too)
// succeed before os.ReadDir(cachePath/sealed) hits EACCES and aborts the
// whole copy - leaving exactly the kind of partial stagePath this guards
// against.
func TestPrepareStagingCleansPartialStagingOnCopyFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks, so this EACCES setup can't fail")
	}

	gameCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "g1"}
	mod := &domain.Mod{ID: "modX", SourceID: "src", Version: "1.0"}

	require.NoError(t, gameCache.Store("g1", "src", "modX", "1.0", "sealed/inner.txt", []byte("x")))
	cachePath := gameCache.ModPath("g1", "src", "modX", "1.0")
	sealedDir := filepath.Join(cachePath, "sealed")
	require.NoError(t, os.Chmod(sealedDir, 0000))
	t.Cleanup(func() { _ = os.Chmod(sealedDir, 0755) }) // restore before TempDir's own cleanup removes it

	expectedStagePath := cachePath + ".staging"

	_, _, err := prepareStaging(gameCache, game, mod)
	require.Error(t, err)

	_, statErr := os.Stat(expectedStagePath)
	assert.True(t, os.IsNotExist(statErr), "prepareStaging must not leave partial staging debris behind on a copyDir failure")
}

func TestIngestLocalToCacheMissingPath(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	game := &domain.Game{ID: "7dtd"}
	mod := &domain.Mod{ID: "x", SourceID: "my-mods", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "x"}

	_, err := svc.ingestLocalToCache(gameCache, game, mod, file, filepath.Join(t.TempDir(), "gone"))
	assert.Error(t, err)
}
