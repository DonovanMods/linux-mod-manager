package cache_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache_ModPath(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	path := c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0")
	expected := filepath.Join(dir, "skyrim-se", "nexusmods-12345", "1.0.0")
	assert.Equal(t, expected, path)
}

func TestCache_ModPath_GameScoped(t *testing.T) {
	dir := t.TempDir()
	c := cache.NewGameScoped(dir)

	path := c.ModPath("starrupture", "nexusmods", "35", "1.00")
	expected := filepath.Join(dir, "nexusmods-35", "1.00")
	assert.Equal(t, expected, path, "game-scoped cache omits gameID from path")
}

func TestCache_Store(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	content := []byte("test mod content")
	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "testfile.txt", content)
	require.NoError(t, err)

	// Verify file exists
	storedPath := filepath.Join(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "testfile.txt")
	data, err := os.ReadFile(storedPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestCache_Exists(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	assert.False(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))

	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "test.txt", []byte("data"))
	require.NoError(t, err)

	assert.True(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))
}

// TestCache_HasFileIDs is the #96 review round 1 finding 2 guard, reworked in
// round 2: the completeness check must be a stronger check than Exists (which
// only checks the version directory's presence, and so wrongly reports
// "cached" for a directory left PARTIALLY populated by a download run that
// broke off partway through a multi-file mod) WITHOUT being archive-blind.
// The round 1 implementation keyed off DownloadableFile.FileName, but the
// default DeployExtract flow stores an archive's EXTRACTED MEMBERS - whose
// names have nothing to do with the archive's own filename - so every
// archive-based mod read as "incomplete" and redownloaded despite a complete
// cache. HasFileIDs instead keys off the per-file completion markers
// MarkFileComplete writes at commit time.
func TestCache_HasFileIDs(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// No directory at all yet.
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}))

	// Directory exists but is completely empty (e.g. MkdirAll ran but no
	// file was ever committed) - still not complete.
	require.NoError(t, os.MkdirAll(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), 0755))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}))

	// A legacy (pre-marker) cache entry - real content, no markers - reads as
	// incomplete: one redundant redownload, which then writes the markers.
	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "SomeArchiveMember.esp", []byte("a")))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}),
		"a pre-marker cache entry must read as incomplete")

	// One of two files marked complete - a partial download.
	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1001"))
	assert.True(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001", "1002"}),
		"a partially-populated cache entry must not report having all requested files")

	// Both marked - fully cached, even though NEITHER file ID resembles any
	// on-disk name (the archive-extracted case round 1 got wrong).
	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1002"))
	assert.True(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001", "1002"}))

	// An empty ID list against a nonexistent entry is still false -
	// HasFileIDs is never weaker than Exists.
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "no-such-mod", "1.0.0", nil))

	// An empty ID list against an entry that DOES exist has nothing left to
	// verify beyond Exists itself.
	assert.True(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", nil))

	// A blank or path-bearing file ID can never be verified - the safe
	// direction is false (a redundant re-download, not a silently-trusted
	// gap, and never a marker lookup that escapes the version directory).
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001", ""}))
	assert.False(t, c.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"../../etc/passwd"}))
}

// TestCache_MarkersAreNeverContent is the #96 round 2 exclusion guard: the
// per-file completion markers MarkFileComplete writes live INSIDE the version
// directory, so every enumerator of that directory must skip them. ListFiles
// is the choke point every deploy/conflict/verify/count path goes through
// (Installer.Install/Replace/Undeploy, DetectConflicts, `lmm verify`'s
// file-count check, DownloadModResult.FilesExtracted, CloneMod); Size is the
// only other walker.
func TestCache_MarkersAreNeverContent(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "real.esp", []byte("12345")))
	sizeBefore, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, int64(5), sizeBefore)

	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1001"))

	files, err := c.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"real.esp"}, files, "markers must never be listed as mod content")

	sizeAfter, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, sizeBefore, sizeAfter, "markers must not be counted toward cache size")

	// A version directory holding ONLY markers still has zero files - `lmm
	// verify` flags "cache dir exists but has 0 files", and a marker must not
	// mask that.
	require.NoError(t, os.MkdirAll(c.ModPath("skyrim-se", "nexusmods", "67890", "2.0.0"), 0755))
	require.NoError(t, cache.MarkFileComplete(c.ModPath("skyrim-se", "nexusmods", "67890", "2.0.0"), "1001"))
	empty, err := c.ListFiles("skyrim-se", "nexusmods", "67890", "2.0.0")
	require.NoError(t, err)
	assert.Empty(t, empty, "a marker-only version directory must still count as 0 files")
}

// TestCache_CloneMod_PreservesMarkers is CloneMod's deliberate exception to
// the marker-exclusion rule: it reproduces a cache ENTRY rather than
// enumerating its mod content, and core's reinstall cache transaction
// round-trips a live entry through a staged/snapshot cache and back. Dropping
// the markers there would silently downgrade a complete entry to a
// pre-marker one and cost a redundant redownload.
func TestCache_CloneMod_PreservesMarkers(t *testing.T) {
	src := cache.New(t.TempDir())
	dst := cache.New(t.TempDir())

	require.NoError(t, src.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "real.esp", []byte("data")))
	require.NoError(t, cache.MarkFileComplete(src.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "1001"))

	require.NoError(t, src.CloneMod(dst, "skyrim-se", "nexusmods", "12345", "1.0.0"))

	files, err := dst.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"real.esp"}, files, "the clone's CONTENT must still exclude markers")
	assert.True(t, dst.HasFileIDs("skyrim-se", "nexusmods", "12345", "1.0.0", []string{"1001"}),
		"a cloned entry must stay complete - markers travel with the entry")
}

// TestCache_MarkFileComplete_UnverifiableIDs pins MarkFileComplete's refusal
// to write a marker it could never match back: a blank or path-bearing file
// ID is skipped (no error, no file), leaving HasFileIDs to report incomplete
// - the safe direction - rather than writing a marker outside the version
// directory.
func TestCache_MarkFileComplete_UnverifiableIDs(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)
	modPath := c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	require.NoError(t, cache.MarkFileComplete(modPath, ""))
	require.NoError(t, cache.MarkFileComplete(modPath, "../escape"))

	entries, err := os.ReadDir(modPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "an unverifiable file ID must not produce a marker")
	_, err = os.Stat(filepath.Join(filepath.Dir(modPath), ".lmm-file-../escape"))
	assert.Error(t, err)
}

func TestCache_ListFiles(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// Store multiple files
	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "file1.txt", []byte("1"))
	require.NoError(t, err)
	err = c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "subdir/file2.txt", []byte("2"))
	require.NoError(t, err)

	files, err := c.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Len(t, files, 2)
}

func TestCache_Delete(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "test.txt", []byte("data"))
	require.NoError(t, err)
	assert.True(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))

	err = c.Delete("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.False(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))
}

// TestCache_Delete_RemovesNowEmptyModDirectory guards #190 item 4: deleting
// a mod's only cached version used to leave the empty per-mod container
// directory (<gameID>/<source>-<modID>/) behind as litter. Delete now
// removes it too, but only once nothing is left under it.
func TestCache_Delete_RemovesNowEmptyModDirectory(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "test.txt", []byte("data")))
	modDir := filepath.Join(dir, "skyrim-se", "nexusmods-12345")
	require.DirExists(t, modDir)

	require.NoError(t, c.Delete("skyrim-se", "nexusmods", "12345", "1.0.0"))
	assert.NoDirExists(t, modDir, "the now-empty per-mod cache directory must not be left behind")
}

// TestCache_Delete_KeepsModDirectoryWhenOtherVersionsRemain is the negative
// case: a mod with more than one cached version must keep its container
// (and the other version untouched) after deleting just one.
func TestCache_Delete_KeepsModDirectoryWhenOtherVersionsRemain(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "old.txt", []byte("old")))
	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "2.0.0", "new.txt", []byte("new")))
	modDir := filepath.Join(dir, "skyrim-se", "nexusmods-12345")

	require.NoError(t, c.Delete("skyrim-se", "nexusmods", "12345", "1.0.0"))
	assert.DirExists(t, modDir, "a sibling version still lives here - the container must survive")
	assert.True(t, c.Exists("skyrim-se", "nexusmods", "12345", "2.0.0"), "the other version must be untouched")
}

// TestCache_Delete_NonexistentVersion_NoopsCleanly: deleting a version that
// was never cached (e.g. --keep-cache preserved it but a later plain delete
// targets an already-gone entry) must not error just because there's no
// container to clean up either.
func TestCache_Delete_NonexistentVersion_NoopsCleanly(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	err := c.Delete("skyrim-se", "nexusmods", "12345", "1.0.0")
	assert.NoError(t, err)
}

// TestCache_Delete_GameScoped_RemovesNowEmptyModDirectory: the game-scoped
// layout (per-game cache_path override) omits the gameID level, but the
// empty-container cleanup is purely path-relative, so it must apply there
// too.
func TestCache_Delete_GameScoped_RemovesNowEmptyModDirectory(t *testing.T) {
	dir := t.TempDir()
	c := cache.NewGameScoped(dir)

	require.NoError(t, c.Store("starrupture", "nexusmods", "35", "1.00", "file.pak", []byte("data")))
	modDir := filepath.Join(dir, "nexusmods-35")
	require.DirExists(t, modDir)

	require.NoError(t, c.Delete("starrupture", "nexusmods", "35", "1.00"))
	assert.NoDirExists(t, modDir)
}

func TestCache_Exists_ListFiles_GameScoped(t *testing.T) {
	dir := t.TempDir()
	c := cache.NewGameScoped(dir)

	err := c.Store("starrupture", "nexusmods", "35", "1.00", "file.pak", []byte("data"))
	require.NoError(t, err)
	assert.True(t, c.Exists("starrupture", "nexusmods", "35", "1.00"))

	files, err := c.ListFiles("starrupture", "nexusmods", "35", "1.00")
	require.NoError(t, err)
	assert.Len(t, files, 1)
	assert.Equal(t, "file.pak", files[0])
}

func TestCache_Size(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "file1.txt", []byte("12345")))       // 5 bytes
	require.NoError(t, c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "sub/file2.txt", []byte("1234567"))) // 7 bytes

	size, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.EqualValues(t, 12, size)
}

func TestCache_Size_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// The mod directory exists (created by Store then Delete leaves nothing,
	// so create it directly) but contains no files.
	modPath := c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	size, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.EqualValues(t, 0, size)
}

// TestCache_Size_MissingDir pins that Size on a mod version that was never
// cached is an error, not a silent 0: filepath.WalkDir fails to stat a
// nonexistent root and Size wraps that failure rather than treating "not
// cached" as "zero bytes".
func TestCache_Size_MissingDir(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	size, err := c.Size("skyrim-se", "nexusmods", "12345", "1.0.0")

	require.Error(t, err)
	assert.Zero(t, size)
	assert.Contains(t, err.Error(), "calculating cache size")
}

func TestCache_CloneMod(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src := cache.New(srcDir)
	dst := cache.New(dstDir)

	require.NoError(t, src.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "main.esp", []byte("main")))
	require.NoError(t, src.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "optional/patch.esp", []byte("patch")))

	require.NoError(t, src.CloneMod(dst, "skyrim-se", "nexusmods", "12345", "1.0.0"))

	files, err := dst.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"main.esp", "optional/patch.esp"}, files)
}

// --- #144 item 4: per-file-ID member manifests in the marker namespace ---

// TestCache_FileManifests_RoundTrip covers the write/read round trip of a
// completion marker's member manifest: MarkFileCompleteWithMembers records
// WHICH members a file ID contributed, and FileManifests reads every marker's
// manifest back keyed by file ID.
func TestCache_FileManifests_RoundTrip(t *testing.T) {
	c := cache.New(t.TempDir())
	versionDir := c.ModPath("g", "src", "mod", "1.0")

	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, "file-a", []string{"main.esp", "textures/skin.dds"}))
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, "file-b", []string{"optional.esp"}))

	manifests, err := c.FileManifests("g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Len(t, manifests, 2)

	a, ok := manifests["file-a"]
	require.True(t, ok)
	assert.True(t, a.Recorded)
	assert.ElementsMatch(t, []string{"main.esp", filepath.Join("textures", "skin.dds")}, a.Members)

	b, ok := manifests["file-b"]
	require.True(t, ok)
	assert.True(t, b.Recorded)
	assert.Equal(t, []string{"optional.esp"}, b.Members)
}

// TestCache_FileManifests_AbsenceVsEmpty pins the hard backward-compat rule:
// a legacy bare marker (pre-manifest cache entries, MarkFileComplete) must be
// distinguishable from a marker that genuinely recorded ZERO members - the
// consumer (installer's same-version undeploy) must fall back to union
// behavior on absence, but may trust an empty manifest.
func TestCache_FileManifests_AbsenceVsEmpty(t *testing.T) {
	c := cache.New(t.TempDir())
	versionDir := c.ModPath("g", "src", "mod", "1.0")

	require.NoError(t, cache.MarkFileComplete(versionDir, "legacy"))
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, "empty", nil))

	manifests, err := c.FileManifests("g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Len(t, manifests, 2)

	legacy, ok := manifests["legacy"]
	require.True(t, ok, "a bare marker still names its file ID")
	assert.False(t, legacy.Recorded, "a legacy bare marker has NO manifest - not an empty one")
	assert.Empty(t, legacy.Members)

	empty, ok := manifests["empty"]
	require.True(t, ok)
	assert.True(t, empty.Recorded, "a recorded-but-empty manifest is present, not absent")
	assert.Empty(t, empty.Members)

	// A version directory with no markers at all reads as no manifests.
	none, err := c.FileManifests("g", "src", "other-mod", "1.0")
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestCache_FileManifests_UnrepresentableMemberDegradesToBareMarker: the
// manifest body is line-oriented, so a member name carrying a newline cannot
// be recorded faithfully. The safe direction is a bare marker (completion
// still vouched for, manifest absent -> consumers fall back to union), never
// a corrupted manifest that could undeploy the wrong path.
func TestCache_FileManifests_UnrepresentableMemberDegradesToBareMarker(t *testing.T) {
	c := cache.New(t.TempDir())
	versionDir := c.ModPath("g", "src", "mod", "1.0")

	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, "weird", []string{"ok.esp", "bad\nname.esp"}))

	manifests, err := c.FileManifests("g", "src", "mod", "1.0")
	require.NoError(t, err)
	m, ok := manifests["weird"]
	require.True(t, ok, "completion must still be vouched for")
	assert.False(t, m.Recorded, "an unrepresentable member set must degrade to a bare marker")
	assert.True(t, c.HasFileIDs("g", "src", "mod", "1.0", []string{"weird"}),
		"the degraded marker still counts for completeness")
}

// TestCache_ManifestMarkersStayReservedAndComplete: a marker that carries a
// manifest BODY (no longer zero-byte) must behave exactly like the bare ones -
// hidden from every content enumerator, still honored by HasFileIDs, and
// carried through CloneMod so a reinstall round trip keeps the manifest.
func TestCache_ManifestMarkersStayReservedAndComplete(t *testing.T) {
	c := cache.New(t.TempDir())

	require.NoError(t, c.Store("g", "src", "mod", "1.0", "real.esp", []byte("12345")))
	require.NoError(t, cache.MarkFileCompleteWithMembers(c.ModPath("g", "src", "mod", "1.0"), "1001", []string{"real.esp"}))

	files, err := c.ListFiles("g", "src", "mod", "1.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"real.esp"}, files, "manifest-bearing markers must never be listed as content")

	size, err := c.Size("g", "src", "mod", "1.0")
	require.NoError(t, err)
	assert.Equal(t, int64(5), size, "manifest bytes must not count toward cache size")

	assert.True(t, c.HasFileIDs("g", "src", "mod", "1.0", []string{"1001"}))

	dst := cache.New(t.TempDir())
	require.NoError(t, c.CloneMod(dst, "g", "src", "mod", "1.0"))
	cloned, err := dst.FileManifests("g", "src", "mod", "1.0")
	require.NoError(t, err)
	m, ok := cloned["1001"]
	require.True(t, ok, "markers travel with a cloned entry")
	assert.True(t, m.Recorded, "the manifest must survive the clone, not downgrade to bare")
	assert.Equal(t, []string{"real.esp"}, m.Members)
}

// TestCache_MarkFileCompleteWithMembers_UnverifiableIDs mirrors
// TestCache_MarkFileComplete_UnverifiableIDs for the manifest writer: a blank
// or path-bearing file ID is skipped (no error, no file, no manifest).
func TestCache_MarkFileCompleteWithMembers_UnverifiableIDs(t *testing.T) {
	c := cache.New(t.TempDir())
	modPath := c.ModPath("g", "src", "mod", "1.0")
	require.NoError(t, os.MkdirAll(modPath, 0755))

	require.NoError(t, cache.MarkFileCompleteWithMembers(modPath, "", []string{"a.esp"}))
	require.NoError(t, cache.MarkFileCompleteWithMembers(modPath, "../escape", []string{"a.esp"}))

	entries, err := os.ReadDir(modPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "an unverifiable file ID must not produce a marker")
}

// TestCache_RetainedSourceName_IsReservedAndExcludedFromContent pins that a
// retained compile source (#196) written under RetainedSourceName is
// reserved bookkeeping, not a deployment member - it must never be listed,
// sized, or deployed, even though its content is a real, non-empty file
// (unlike a marker, which is metadata).
func TestCache_RetainedSourceName_IsReservedAndExcludedFromContent(t *testing.T) {
	name := cache.RetainedSourceName("file-a")
	require.True(t, strings.HasPrefix(name, cache.ReservedPrefix),
		"retained source name must live under the reserved namespace")

	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "src", "mod", "1.0", "Bear_Mount_P.pak", []byte("compiled")))
	require.NoError(t, c.Store("g", "src", "mod", "1.0", name, []byte("original exmodz bytes")))

	files, err := c.ListFiles("g", "src", "mod", "1.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"Bear_Mount_P.pak"}, files, "a retained source must never be listed as deployable content")

	size, err := c.Size("g", "src", "mod", "1.0")
	require.NoError(t, err)
	assert.Equal(t, int64(len("compiled")), size, "a retained source's bytes must not count toward cache size")
}

// TestCache_RetainedSourceName_UniquePerFileID guards against two compiled
// files in the same mod entry colliding on their retained source's name.
func TestCache_RetainedSourceName_UniquePerFileID(t *testing.T) {
	assert.NotEqual(t, cache.RetainedSourceName("file-a"), cache.RetainedSourceName("file-b"))
}

// TestCache_HasRetainedSource pins deployableFiles' narrowing gate (#210):
// present when the version dir holds a retained compile source, absent
// otherwise, and a missing directory is not an error.
func TestCache_HasRetainedSource(t *testing.T) {
	dir := t.TempDir()
	has, err := cache.HasRetainedSource(dir)
	require.NoError(t, err)
	assert.False(t, has, "an empty dir has no retained source")

	require.NoError(t, os.WriteFile(filepath.Join(dir, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))
	has, err = cache.HasRetainedSource(dir)
	require.NoError(t, err)
	assert.True(t, has, "a retained source entry must be detected")

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	has, err = cache.HasRetainedSource(missing)
	require.NoError(t, err)
	assert.False(t, has, "a missing version dir is not an error")
}

// TestCache_MergeFingerprintPath_IsReserved pins that the merged pak's
// fingerprint marker (#197) lives under the reserved namespace, like every
// other lmm bookkeeping file.
func TestCache_MergeFingerprintPath_IsReserved(t *testing.T) {
	path := cache.MergeFingerprintPath("/some/version/dir")
	if !strings.HasPrefix(filepath.Base(path), cache.ReservedPrefix) {
		t.Errorf("MergeFingerprintPath = %q, want a reserved-prefixed basename", path)
	}
}

// TestCache_MergeFingerprintPath_ExcludedFromContent proves the fingerprint
// marker is never listed as deployable content, matching every other
// reserved marker's ListFiles exclusion.
func TestCache_MergeFingerprintPath_ExcludedFromContent(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "lmm-merged", "merged-pak", "merged", "zzz_LMM_Merged_P.pak", []byte("pak-bytes")))
	versionDir := c.ModPath("g", "lmm-merged", "merged-pak", "merged")
	require.NoError(t, os.WriteFile(cache.MergeFingerprintPath(versionDir), []byte(`{"BaseIndexHash":"abc"}`), 0o644))

	files, err := c.ListFiles("g", "lmm-merged", "merged-pak", "merged")
	require.NoError(t, err)
	assert.Equal(t, []string{"zzz_LMM_Merged_P.pak"}, files, "the fingerprint marker must never be listed as deployable content")
}

// TestPruneUnclaimed_RemovesUnclaimedWhenAllRecorded proves the #210 core
// case: once every marker in the entry carries a recorded manifest AND the
// entry holds a retained source, anything no manifest claims is stale
// debris and gets removed - including the now-empty subdirectory it lived
// in - while the retained source itself, which is reserved bookkeeping
// claimed by nothing, survives.
func TestPruneUnclaimed_RemovesUnclaimedWhenAllRecorded(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "claimed.pak", []byte("a")))
	require.NoError(t, c.Store("g", "s", "m", "1.0", "stale.pak", []byte("b")))
	require.NoError(t, c.Store("g", "s", "m", "1.0", "sub/stale2.pak", []byte("c")))
	dir := c.ModPath("g", "s", "m", "1.0")
	require.NoError(t, os.WriteFile(filepath.Join(dir, cache.RetainedSourceName("f1")), []byte("zip"), 0o644))
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"claimed.pak"}))

	require.NoError(t, cache.PruneUnclaimed(dir))

	files, err := c.ListFiles("g", "s", "m", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"claimed.pak"}, files)
	// Emptied subdirectory is removed too.
	_, err = os.Stat(filepath.Join(dir, "sub"))
	require.True(t, os.IsNotExist(err))
}

// TestPruneUnclaimed_NoOpWithoutRetainedSource proves the retained-source
// gate: even with every marker recorded, an entry holding no retained
// source is left untouched - unattributed content on disk could be
// legacy/import-populated (#144's protected shapes) rather than debris, and
// only a retained source's presence is the validate+retain model's actual
// signature.
func TestPruneUnclaimed_NoOpWithoutRetainedSource(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "claimed.pak", []byte("a")))
	require.NoError(t, c.Store("g", "s", "m", "1.0", "stale.pak", []byte("b")))
	dir := c.ModPath("g", "s", "m", "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"claimed.pak"}))

	require.NoError(t, cache.PruneUnclaimed(dir))

	files, err := c.ListFiles("g", "s", "m", "1.0")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"claimed.pak", "stale.pak"}, files, "no retained source means no-op, even with all markers recorded")
}

func TestPruneUnclaimed_NoOpWithBareMarker(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "a.pak", []byte("a")))
	dir := c.ModPath("g", "s", "m", "1.0")
	require.NoError(t, cache.MarkFileComplete(dir, "f1")) // bare: provenance unknown

	require.NoError(t, cache.PruneUnclaimed(dir))

	files, err := c.ListFiles("g", "s", "m", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"a.pak"}, files)
}

func TestPruneUnclaimed_NoOpWithoutMarkers(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "a.pak", []byte("a")))
	dir := c.ModPath("g", "s", "m", "1.0")

	require.NoError(t, cache.PruneUnclaimed(dir))

	files, err := c.ListFiles("g", "s", "m", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"a.pak"}, files)
}

func TestPruneUnclaimed_NeverTouchesReservedEntries(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "s", "m", "1.0", "claimed.pak", []byte("a")))
	dir := c.ModPath("g", "s", "m", "1.0")
	// A retained source is reserved bookkeeping, claimed by nothing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", nil))
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"claimed.pak"}))

	require.NoError(t, cache.PruneUnclaimed(dir))

	_, err := os.Stat(filepath.Join(dir, cache.RetainedSourceName("exmodz")))
	require.NoError(t, err, "reserved entries are never pruned")
}
