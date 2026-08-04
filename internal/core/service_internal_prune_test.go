package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestCommitStagedCacheWithMarker_UnverifiableFileIDSkipsPrune: an
// unverifiable fileID writes no marker, so its just-committed members are
// unattributable - the prune must not run and delete them (#210 PR review).
func TestCommitStagedCacheWithMarker_UnverifiableFileIDSkipsPrune(t *testing.T) {
	base := t.TempDir()
	cachePath := filepath.Join(base, "entry")
	stagePath := cachePath + ".staging"
	require.NoError(t, os.MkdirAll(stagePath, 0o755))
	// Seeded state: recorded marker claiming a.pak, retained source, and the
	// new unverifiable-ID download's member new.dat (no marker possible).
	require.NoError(t, os.WriteFile(filepath.Join(stagePath, "a.pak"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stagePath, "new.dat"), []byte("n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stagePath, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))
	require.NoError(t, cache.MarkFileCompleteWithMembers(stagePath, "f1", []string{"a.pak"}))

	require.NoError(t, commitStagedCacheWithMarker(cachePath, stagePath, "unverifiable/id", []string{"new.dat"}))

	_, err := os.Stat(filepath.Join(cachePath, "new.dat"))
	require.NoError(t, err, "the unverifiable fileID's own member must survive the commit")
}
