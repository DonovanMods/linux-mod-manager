package core

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
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

	result, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
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

	first, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	require.NotEmpty(t, first.Checksum)

	unchanged, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, first.Checksum, unchanged.Checksum,
		"re-ingesting an unchanged source directory must reproduce the same digest")

	require.NoError(t, os.WriteFile(filepath.Join(modDir, "Config", "items.xml"), []byte("<items changed/>"), 0644))
	contentDrift, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.NotEqual(t, first.Checksum, contentDrift.Checksum,
		"changing a member file's content must change the digest")

	require.NoError(t, os.WriteFile(filepath.Join(modDir, "extra.txt"), []byte("new member"), 0644))
	memberDrift, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
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

	result, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, 0, result.FilesExtracted)
	assert.Empty(t, result.Checksum, "an empty member set has nothing to fingerprint - no digest")
}

// TestIngestLocalToCacheDirectory_ReingestDropsRemovedMembers is THE #166
// regression test: re-ingesting a directory source into an EXISTING cache
// entry for the same (source, mod, version) must REPLACE the entry, not
// overlay it - a member deleted from the source directory must disappear
// from the committed cache, from the "main" marker's member manifest, and
// from the digest, which must land on exactly the value a fresh ingest of
// the shrunk source produces (so verify --fix / reinstalls converge).
func TestIngestLocalToCacheDirectory_ReingestDropsRemovedMembers(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "BiggerBackpack")
	require.NoError(t, os.MkdirAll(filepath.Join(modDir, "Config"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte("<xml/>"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "Config", "items.xml"), []byte("<items/>"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "stale.txt"), []byte("to be removed"), 0644))

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Version: "1.2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "BiggerBackpack"}

	first, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	require.Equal(t, 3, first.FilesExtracted)

	require.NoError(t, os.Remove(filepath.Join(modDir, "stale.txt")))

	second, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, 2, second.FilesExtracted, "the removed member must not be counted after re-ingest")

	files, err := gameCache.ListFiles("7dtd", "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	assert.NotContains(t, files, "stale.txt", "a member deleted from the source must not survive re-ingest in the committed cache")
	assert.Len(t, files, 2)

	manifests, err := gameCache.FileManifests("7dtd", "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	require.True(t, manifests["main"].Recorded)
	assert.NotContains(t, manifests["main"].Members, "stale.txt", "the marker manifest must not attribute the removed member")

	// The digest must converge on what a FRESH ingest of the shrunk source
	// produces - the symmetry verify --fix and reinstalls depend on (#164).
	freshCache := cache.New(t.TempDir())
	fresh, err := svc.ingestLocalToCache(context.Background(), freshCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, fresh.Checksum, second.Checksum,
		"re-ingest over an existing entry must produce the same digest as a fresh ingest of the same source")
}

// TestIngestLocalToCacheDirectory_MembersIncludeDereferencedSymlinks pins the
// member-set consistency half of #166: copyDir DEREFERENCES in-root symlinks
// into regular files, so the cached (and therefore deployed - ListFiles)
// view includes them. The marker manifest and digest must attribute those
// same files, not silently skip them the way a walk of the SOURCE directory
// (whose entries are still symlinks) would.
func TestIngestLocalToCacheDirectory_MembersIncludeDereferencedSymlinks(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "LinkedMod")
	require.NoError(t, os.MkdirAll(filepath.Join(modDir, "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "data", "real.txt"), []byte("real"), 0644))
	require.NoError(t, os.Symlink(filepath.Join(modDir, "data", "real.txt"), filepath.Join(modDir, "alias.txt")))

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "LinkedMod", SourceID: "my-mods", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "LinkedMod"}

	result, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, modDir)
	require.NoError(t, err)

	files, err := gameCache.ListFiles("7dtd", "my-mods", "LinkedMod", "1.0")
	require.NoError(t, err)
	require.Contains(t, files, "alias.txt", "copyDir materializes the symlink as a regular cached file")

	manifests, err := gameCache.FileManifests("7dtd", "my-mods", "LinkedMod", "1.0")
	require.NoError(t, err)
	require.True(t, manifests["main"].Recorded)
	assert.Contains(t, manifests["main"].Members, "alias.txt",
		"the manifest must attribute every cached member, dereferenced symlinks included")
	assert.Equal(t, 2, result.FilesExtracted)
	assert.NotEmpty(t, result.Checksum)
}

func TestIngestLocalToCacheArchiveCopyMode(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	archive := filepath.Join(t.TempDir(), "coolmod-2.0.zip")
	require.NoError(t, os.WriteFile(archive, []byte("zipbytes"), 0644))

	game := &domain.Game{ID: "hytale", DeployMode: domain.DeployCopy}
	mod := &domain.Mod{ID: "coolmod-2.0", SourceID: "my-mods", Version: "2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "coolmod-2.0.zip"}

	result, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, archive)
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

	result, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, tempFile)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)

	declaredPath := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "declared.zip")
	_, err = os.Stat(declaredPath)
	assert.NoError(t, err, "cached file must be named after the declared file.FileName, not the temp path's basename")

	staleBasenamePath := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "tmp-download-xyz.bin")
	_, err = os.Stat(staleBasenamePath)
	assert.True(t, os.IsNotExist(err), "cached file must NOT be named after localPath's basename when file.FileName is declared")
}

// TestIngestLocalToCacheArchiveCopyMode_TraversalFileNameSanitized is the
// ingestLocalToCache sibling of the #196 review traversal fix: file.FileName
// is SOURCE-CONTROLLED (any custom directory/manifest/api ModSource can
// declare it) and must never be trusted as a path component verbatim - an
// entry like "../evil.zip" must not let the cached file land outside the
// mod's own cache version directory.
func TestIngestLocalToCacheArchiveCopyMode_TraversalFileNameSanitized(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	tempFile := filepath.Join(t.TempDir(), "tmp-download-xyz.bin")
	require.NoError(t, os.WriteFile(tempFile, []byte("zipbytes"), 0644))

	game := &domain.Game{ID: "hytale", DeployMode: domain.DeployCopy}
	mod := &domain.Mod{ID: "coolmod-2.0", SourceID: "my-mods", Version: "2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "../evil-traversal.zip"}

	result, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, tempFile)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)

	sanitizedPath := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "evil-traversal.zip")
	_, err = os.Stat(sanitizedPath)
	assert.NoError(t, err, "the cached file must land under the sanitized (Base'd) name inside the version directory")

	// The version directory's PARENT (my-mods-coolmod-2.0/) is exactly one
	// level up from the "2.0" version dir the unsanitized "../evil-*.zip"
	// would have climbed into.
	escapedPath := filepath.Join(gameCache.ModPath("hytale", "my-mods", "coolmod-2.0", "2.0"), "..", "evil-traversal.zip")
	_, err = os.Stat(escapedPath)
	assert.True(t, os.IsNotExist(err), "a traversal filename must never write outside the mod's own cache version directory")
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

	_, _, err := prepareStaging(context.Background(), gameCache, game, mod)
	require.Error(t, err)

	_, statErr := os.Stat(expectedStagePath)
	assert.True(t, os.IsNotExist(statErr), "prepareStaging must not leave partial staging debris behind on a copyDir failure")
}

func TestIngestLocalToCacheMissingPath(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	game := &domain.Game{ID: "7dtd"}
	mod := &domain.Mod{ID: "x", SourceID: "my-mods", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "x"}

	_, err := svc.ingestLocalToCache(context.Background(), gameCache, game, mod, file, filepath.Join(t.TempDir(), "gone"))
	assert.Error(t, err)
}

// cancelAfterFirstEntry reports itself live for the FIRST ctx.Err() call and
// cancelled for every call after it. copyDirFollowing checks ctx once per
// directory entry, so wrapping a real cancellable context this way stops a
// directory ingest deterministically AFTER the first member has been copied
// and BEFORE the second is touched - the mid-copy window a real Ctrl-C lands
// in - without a timing race or a sleep. Not goroutine-safe by design: the
// copy it instruments runs on the calling goroutine only.
type cancelAfterFirstEntry struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelAfterFirstEntry) Err() error {
	if c.checks > 0 {
		c.cancel()
	}
	c.checks++
	return c.Context.Err()
}

// TestIngestLocalToCacheDirectory_CancelledMidCopy pins the per-entry ctx
// check in copyDirFollowing, which is the ONLY cancellation guard below
// ApplyInstall's/ApplyUpdate's per-file loops on the directory-source
// local-ingest path: nothing else in ingestLocalToCache's copy branch
// consults ctx (the archive branch has extractIntoStaging, the copy branch
// had nothing). Cancelling mid-copy must abort with the ctx error AND leave
// no cache entry behind - in particular none marked complete, which is what
// makes a later install/verify re-ingest instead of trusting half a mod.
func TestIngestLocalToCacheDirectory_CancelledMidCopy(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "BiggerBackpack")
	require.NoError(t, os.MkdirAll(modDir, 0755))
	for _, name := range []string{"a.xml", "b.xml", "c.xml"} {
		require.NoError(t, os.WriteFile(filepath.Join(modDir, name), []byte(name), 0644))
	}

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Version: "1.2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "BiggerBackpack"}

	inner, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelAfterFirstEntry{Context: inner, cancel: cancel}

	result, err := svc.ingestLocalToCache(ctx, gameCache, game, mod, file, modDir)
	require.ErrorIs(t, err, context.Canceled, "a cancelled directory ingest must surface the ctx error, not copy on to completion")
	assert.Nil(t, result)
	assert.Greater(t, ctx.checks, 1, "the guard must be consulted per entry, not once for the whole copy")

	assert.False(t, gameCache.Exists("7dtd", "my-mods", "BiggerBackpack", "1.2.0"),
		"a cancelled ingest must publish no cache entry")
	assert.False(t, gameCache.HasFileIDs("7dtd", "my-mods", "BiggerBackpack", "1.2.0", []string{"main"}),
		"a cancelled ingest must leave no completion marker - a partial entry marked complete would be trusted by install/verify")
	_, statErr := os.Stat(gameCache.ModPath("7dtd", "my-mods", "BiggerBackpack", "1.2.0") + ".staging")
	assert.True(t, os.IsNotExist(statErr), "the staging directory must be cleaned up on a cancelled ingest")
}
