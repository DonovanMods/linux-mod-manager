package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fomodPlusPlainSource serves an installer file and two plain files of
// mod1 at 1.0.
type fomodPlusPlainSource struct{ *mockSourceWithDownloads }

func (s *fomodPlusPlainSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "installer", Name: "Installer", FileName: "installer.zip", IsPrimary: true, Version: "1.0"},
		{ID: "x", Name: "X", FileName: "x.zip", Version: "1.0"},
		{ID: "y", Name: "Y", FileName: "y.zip", Version: "1.0"},
	}, nil
}

// #639 review B3: a FOMOD mod moved to a file-set entry is repaired and
// rebuilt into that entry, never asked to re-answer its installer.
func TestFileSwap_FomodRowInAFileSetEntryIsRepairedAndRebuilt(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	src := &fomodPlusPlainSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	src.AddDownload("installer", fomodZip(t, "4K", "v1"))
	src.AddDownload("x", createTestZipBytes(t, map[string]string{"x.esp": "x"}))
	src.AddDownload("y", createTestZipBytes(t, map[string]string{"y.esp": "y"}))
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	ctx := context.Background()

	plan, err := svc.PlanInstall(ctx, game, "default", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{TargetFileIDs: []string{"installer", "x"},
		Fomod: map[string]domain.FomodChoices{"src:mod1": answer("4K")}}, nil)
	require.NoError(t, err)
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	base := row.Variant
	t.Logf("base variant %q files %v", base, row.FileIDs)

	setRefFiles(t, svc, "g1", "default", "installer", "y")
	ap, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	_, err = svc.ApplyProfileApply(ctx, game, ap, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	row, err = svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	t.Logf("after swap variant %q files %v", row.Variant, row.FileIDs)
	require.NotEqual(t, base, row.Variant)
	_, err = os.Lstat(filepath.Join(gameDir, "y.esp"))
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(gameDir, "x.esp"))
	require.True(t, os.IsNotExist(err))

	ap, err = svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, ap.NoChanges, "converged")

	// verify --fix re-fetches the installer of a row in a file-set entry
	// (its checksum is blank): the installer resolves to the row's base
	// tree, which IS the row's tree, so it is no re-answer.
	c := svc.GetGameCache(game)
	require.NoError(t, svc.SaveFileChecksum(ctx, "src", "mod1", "g1", "default", "installer", ""))
	res, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	var repaired bool
	for _, f := range res.Findings {
		assert.NotContains(t, f.Note+f.FixableReason, "answer", "no installer question: %+v", f)
		if f.FileID == "installer" && strings.Contains(f.Repair, "fresh download") {
			repaired = true
		}
	}
	assert.True(t, repaired, "the installer was re-fetched into the row's entry")
	after, err := svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, row.Variant, after.Variant)
	_, err = os.Stat(filepath.Join(c.ModPath(cacheKeyOf(after)), "y.esp"))
	assert.NoError(t, err, "the entry still holds y")

	// Deploy rebuild: remove the whole entry and deploy
	require.NoError(t, os.RemoveAll(c.ModPath(cacheKeyOf(after))))
	dres, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	t.Logf("deploy err=%v res=%+v", err, dres)
	require.NoError(t, err)
	assert.Empty(t, dres.Skipped, "no re-answer: the rebuild is the row's tree")
	_, err = os.Stat(filepath.Join(c.ModPath(cacheKeyOf(after)), "y.esp"))
	assert.NoError(t, err, "deploy rebuilt the file-set entry")
}
