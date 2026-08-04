package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// seedEntry stores the given files and returns the cache and version dir.
func seedEntry(t *testing.T, files map[string][]byte) (*cache.Cache, string) {
	t.Helper()
	c := cache.New(t.TempDir())
	for name, content := range files {
		require.NoError(t, c.Store("g", "src", "mod", "1.0", name, content))
	}
	return c, c.ModPath("g", "src", "mod", "1.0")
}

func TestDeployableFiles_AllRecorded_ExcludesUnclaimed(t *testing.T) {
	c, dir := seedEntry(t, map[string][]byte{
		"claimed.pak": []byte("a"),
		"stale.pak":   []byte("b"), // claimed by no manifest
	})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", []string{"claimed.pak"}))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"claimed.pak"}, files)
}

func TestDeployableFiles_RecordedZeroMembers_DeploysNothing(t *testing.T) {
	// The live #210 shape: retain-only exmodz marker (recorded, zero members)
	// plus a stale pre-#197 compiled pak.
	c, dir := seedEntry(t, map[string][]byte{"LargerResourceStacks_P.pak": []byte("pak")})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "exmodz", nil))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestDeployableFiles_BareMarker_FallsBackToUnion(t *testing.T) {
	c, dir := seedEntry(t, map[string][]byte{
		"a.pak": []byte("a"),
		"b.pak": []byte("b"),
	})
	// Legacy bare marker: completion vouched, provenance unknown.
	require.NoError(t, cache.MarkFileComplete(dir, "pak"))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.pak", "b.pak"}, files)
}

func TestDeployableFiles_MixedRecordedAndBare_FallsBackToUnion(t *testing.T) {
	c, dir := seedEntry(t, map[string][]byte{
		"a.pak": []byte("a"),
		"b.pak": []byte("b"),
	})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"a.pak"}))
	require.NoError(t, cache.MarkFileComplete(dir, "f2")) // bare

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.pak", "b.pak"}, files)
}

func TestDeployableFiles_NoMarkers_FallsBackToUnion(t *testing.T) {
	c, _ := seedEntry(t, map[string][]byte{"a.pak": []byte("a")})

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"a.pak"}, files)
}

func TestDeployableFiles_ClaimedButMissingOnDisk_Dropped(t *testing.T) {
	// A manifest may claim a member that was manually deleted; the resolver
	// returns what is actually deployable (verify owns missing-file repair).
	c, dir := seedEntry(t, map[string][]byte{"present.pak": []byte("a")})
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "f1", []string{"present.pak", "gone.pak"}))

	files, err := deployableFiles(c, "g", "src", "mod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"present.pak"}, files)
}
