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

// #316 re-verification: the multi-archive fixture that first exposed the
// cold-vs-warm over-count used ONE member per archive, so a per-file
// accumulator and a per-entry count differ only by the number of archives.
// This widens it to two archives of TWO members each - where an accumulator
// would report 2 + 4 = 6 against the entry's real 4 - and drives the same
// mod down the cold (download) and warm (cache-hit) paths in one test, so
// the two are compared rather than each merely being asserted.
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
		{"f1", map[string]string{"a1.esp": "a1", "a2.esp": "a2"}},
		{"f2", map[string]string{"b1.esp": "b1", "b2.esp": "b2"}},
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
	require.Len(t, cached, 4, "sanity: two archives of two members each")
	assert.Equal(t, 4, cold.FilesDeployed, "the cold fill reports the cache entry's own file count")

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
	assert.Equal(t, 4, warm.FilesDeployed)
}
