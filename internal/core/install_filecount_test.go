package core_test

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #316 re-verification (review M1): FilesDeployed must be the CACHE ENTRY's
// own file count, read once, on the cold fill and the warm hit alike.
//
// Widening the original one-member-per-archive fixture to two members each
// proved nothing - with N archives of M members, a per-archive-member
// accumulator sums to exactly N*M, the entry's own count, at every width.
// What separates the three candidate shapes is OVERLAP: both archives here
// carry a member at the same relative path, so they collapse into one file
// in the entry. The entry really holds 3 files, and:
//
//	entry count (correct)      3
//	per-file accumulator       4  (2 + 2, blind to the collapse)
//	cumulative entry re-read   5 or 6 (the historical #303 bug: the whole
//	                           entry's listing added once per planned file)
//
// so this fixture fails on either wrong shape rather than only on the
// historical one - both were mutation-tested, and the per-file accumulator
// that the older two-members-each fixture let through is now caught. Both
// paths are driven in one test and the two counts are compared directly,
// not each asserted against a literal in isolation.
func TestApplyInstall_MultiMemberArchives_ColdAndWarmAgree(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	src := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "f1", Name: "File 1", FileName: "mod1-f1.zip", IsPrimary: true},
			{ID: "f2", Name: "File 2", FileName: "mod1-f2.zip"},
		},
	}
	defer src.Close()
	svc.RegisterSource(src)
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	for _, f := range []struct {
		id      string
		members map[string]string
	}{
		{"f1", map[string]string{"a1.esp": "a1", "shared.txt": "from f1"}},
		{"f2", map[string]string{"b1.esp": "b1", "shared.txt": "from f2"}},
	} {
		zipBytes, err := os.ReadFile(createTestZip(t, t.TempDir(), f.members))
		require.NoError(t, err)
		src.AddDownload(f.id, zipBytes)
	}

	// Cold: nothing cached, both archives are fetched and extracted.
	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	plan.Files = src.files
	cold, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	cached, err := svc.GetGameCache(game).ListFiles("g1", "src", "mod1", "1.0")
	require.NoError(t, err)
	require.Len(t, cached, 3, "sanity: two archives of two members each, sharing one path - the entry holds their union")
	assert.Equal(t, 3, cold.FilesDeployed, "the cold fill reports the cache entry's own file count, not the sum of what each archive contributed")

	// Warm: the entry survives (KeepCache), so nothing is downloaded.
	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "mod1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	plan2, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	plan2.Files = src.files
	warm, err := svc.ApplyInstall(context.Background(), game, plan2, core.InstallOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, cold.FilesDeployed, warm.FilesDeployed,
		"cold and warm must report the same count for the same cache entry")
	assert.Equal(t, 3, warm.FilesDeployed)
}
