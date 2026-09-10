package core

// originals_internal_test.go covers the originals store itself (#350): the
// idempotence rule that makes it safe to call before every replacing write,
// the "lmm does not own it" filters, and the path refusals. White-box
// because the store is a core primitive no frontend touches - what a
// frontend sees is the originals a snapshot document carries.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func TestOriginalsStore_CapturesTheBytesAndTheRow(t *testing.T) {
	dataDir, gameDir := t.TempDir(), t.TempDir()
	store := newOriginalsStore(dataDir, "skyrim-se", nil)

	stock := filepath.Join(gameDir, "Data", "stock.esp")
	writeFileT(t, stock, "as shipped")

	require.NoError(t, store.capture(OriginalFile{
		Root: OriginalRootModPath, RelativePath: "Data/stock.esp",
		Op: OriginalOpDeploy, SourceID: "nexusmods", ModID: "42", Profile: "default",
	}, stock))

	rows, err := store.list()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, OriginalRootModPath, rows[0].Root)
	assert.Equal(t, "Data/stock.esp", rows[0].RelativePath)
	assert.Equal(t, int64(len("as shipped")), rows[0].Size)
	assert.Equal(t, OriginalOpDeploy, rows[0].Op)
	assert.Equal(t, "nexusmods", rows[0].SourceID)
	assert.Equal(t, "42", rows[0].ModID)
	assert.Equal(t, "default", rows[0].Profile)
	assert.False(t, rows[0].CapturedAt.IsZero())

	// The stored bytes are really there, under the root-split tree, and
	// the recorded checksum is theirs.
	stored := store.storedPath(OriginalRootModPath, "Data/stock.esp")
	data, err := os.ReadFile(stored)
	require.NoError(t, err)
	assert.Equal(t, "as shipped", string(data))
	sum, _, err := hashFile(stored)
	require.NoError(t, err)
	assert.Equal(t, sum, rows[0].SHA256)
}

// TestOriginalsStore_FirstOriginalWins is the rule that makes capture safe
// to call unconditionally: the SECOND write's "original" is lmm's own first
// write, so replacing the stored copy would destroy the only surviving
// stock bytes.
func TestOriginalsStore_FirstOriginalWins(t *testing.T) {
	dataDir, gameDir := t.TempDir(), t.TempDir()
	store := newOriginalsStore(dataDir, "skyrim-se", nil)
	target := filepath.Join(gameDir, "a.esp")

	writeFileT(t, target, "the real original")
	require.NoError(t, store.capture(OriginalFile{
		Root: OriginalRootModPath, RelativePath: "a.esp", Op: OriginalOpDeploy,
	}, target))

	// A mod has since deployed over it; a second flow tries to capture.
	writeFileT(t, target, "some mod's file")
	require.NoError(t, store.capture(OriginalFile{
		Root: OriginalRootModPath, RelativePath: "a.esp", Op: OriginalOpProfileOverride,
	}, target))

	rows, err := store.list()
	require.NoError(t, err)
	require.Len(t, rows, 1, "a second capture of the same path adds no row")
	assert.Equal(t, OriginalOpDeploy, rows[0].Op, "the first capture's provenance survives")

	data, err := os.ReadFile(store.storedPath(OriginalRootModPath, "a.esp"))
	require.NoError(t, err)
	assert.Equal(t, "the real original", string(data))
}

// TestOriginalsStore_TheTwoRootsDoNotCollide pins why the stored tree is
// split by root: game.ModPath and game.InstallPath are different
// directories, so the same relative path under each names two different
// files.
func TestOriginalsStore_TheTwoRootsDoNotCollide(t *testing.T) {
	dataDir, dirA, dirB := t.TempDir(), t.TempDir(), t.TempDir()
	store := newOriginalsStore(dataDir, "skyrim-se", nil)

	underMods := filepath.Join(dirA, "Data", "same.ini")
	underInstall := filepath.Join(dirB, "Data", "same.ini")
	writeFileT(t, underMods, "mod path copy")
	writeFileT(t, underInstall, "install path copy")

	require.NoError(t, store.capture(OriginalFile{
		Root: OriginalRootModPath, RelativePath: "Data/same.ini", Op: OriginalOpDeploy,
	}, underMods))
	require.NoError(t, store.capture(OriginalFile{
		Root: OriginalRootInstallPath, RelativePath: "Data/same.ini", Op: OriginalOpProfileOverride,
	}, underInstall))

	rows, err := store.list()
	require.NoError(t, err)
	require.Len(t, rows, 2)

	a, err := os.ReadFile(store.storedPath(OriginalRootModPath, "Data/same.ini"))
	require.NoError(t, err)
	b, err := os.ReadFile(store.storedPath(OriginalRootInstallPath, "Data/same.ini"))
	require.NoError(t, err)
	assert.Equal(t, "mod path copy", string(a))
	assert.Equal(t, "install path copy", string(b))
}

func TestOriginalsStore_NothingToCaptureIsNotAnError(t *testing.T) {
	dataDir, gameDir := t.TempDir(), t.TempDir()
	store := newOriginalsStore(dataDir, "skyrim-se", nil)

	t.Run("absent destination", func(t *testing.T) {
		require.NoError(t, store.capture(OriginalFile{
			Root: OriginalRootModPath, RelativePath: "missing.esp", Op: OriginalOpDeploy,
		}, filepath.Join(gameDir, "missing.esp")))
	})

	t.Run("a symlink is lmm's own deployment, not stock content", func(t *testing.T) {
		real := filepath.Join(gameDir, "cached.esp")
		writeFileT(t, real, "cache bytes")
		link := filepath.Join(gameDir, "linked.esp")
		require.NoError(t, os.Symlink(real, link))

		require.NoError(t, store.capture(OriginalFile{
			Root: OriginalRootModPath, RelativePath: "linked.esp", Op: OriginalOpDeploy,
		}, link))
	})

	t.Run("a directory is not a file being overwritten", func(t *testing.T) {
		dir := filepath.Join(gameDir, "adir")
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, store.capture(OriginalFile{
			Root: OriginalRootModPath, RelativePath: "adir", Op: OriginalOpDeploy,
		}, dir))
	})

	rows, err := store.list()
	require.NoError(t, err)
	assert.Empty(t, rows, "nothing was replaced, so nothing is recorded")
	assert.NoFileExists(t, store.manifestPath(), "and no manifest is created for a store with nothing in it")
}

// TestOriginalsStore_RefusesAnEscapingPath matters because the manifest is
// a list of paths a RESTORE will write: a row naming "../../etc/passwd"
// would be a write outside the game directory.
func TestOriginalsStore_RefusesAnEscapingPath(t *testing.T) {
	dataDir, gameDir := t.TempDir(), t.TempDir()
	store := newOriginalsStore(dataDir, "skyrim-se", nil)
	victim := filepath.Join(gameDir, "x")
	writeFileT(t, victim, "x")

	for _, bad := range []string{"../escape", "/etc/passwd", "", "..", "a/../../b"} {
		err := store.capture(OriginalFile{
			Root: OriginalRootModPath, RelativePath: bad, Op: OriginalOpDeploy,
		}, victim)
		require.Error(t, err, "path %q must be refused", bad)
	}
}

// TestOriginalsStore_ListSurvivesAMissingManifest pins that a game which
// never had a file replaced is a normal state.
func TestOriginalsStore_ListSurvivesAMissingManifest(t *testing.T) {
	rows, err := newOriginalsStore(t.TempDir(), "skyrim-se", nil).list()
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestOriginalsStore_AFailedManifestWriteLeavesNoOrphanBytes pins the
// consistency rule: stored bytes with no manifest row would be read by the
// next capture as "not captured yet" and overwritten, losing the original.
func TestOriginalsStore_AFailedManifestWriteLeavesNoOrphanBytes(t *testing.T) {
	dataDir, gameDir := t.TempDir(), t.TempDir()
	store := newOriginalsStore(dataDir, "skyrim-se", nil)
	target := filepath.Join(gameDir, "a.esp")
	writeFileT(t, target, "original")

	// A directory where the manifest file must go makes the rename fail.
	require.NoError(t, os.MkdirAll(store.manifestPath(), 0755))

	err := store.capture(OriginalFile{
		Root: OriginalRootModPath, RelativePath: "a.esp", Op: OriginalOpDeploy,
	}, target)
	require.Error(t, err)
	assert.NoFileExists(t, store.storedPath(OriginalRootModPath, "a.esp"))
}

func TestCleanOriginalRelPath_NormalisesToSlashForm(t *testing.T) {
	got, err := cleanOriginalRelPath("Data//sub/./file.esp")
	require.NoError(t, err)
	assert.Equal(t, "Data/sub/file.esp", got)
}
