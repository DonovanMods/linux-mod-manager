package core_test

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shrinkingSource serves files "20" and "21" of mod1 at 1.0, or only "21"
// once only21 is set: a file withdrawn upstream.
type shrinkingSource struct {
	*mockSourceWithDownloads
	only21 bool
}

func (s *shrinkingSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	b := domain.DownloadableFile{ID: "21", Name: "B", FileName: "mod1-b.zip", Version: "1.0", IsPrimary: true, Category: "MAIN"}
	if s.only21 {
		return []domain.DownloadableFile{b}, nil
	}
	return []domain.DownloadableFile{
		{ID: "20", Name: "A", FileName: "mod1-a.zip", Version: "1.0", IsPrimary: true, Category: "MAIN"}, b}, nil
}

// #639 review B5: a deploy that heals a row to another file (#139) moves
// the profile's ref with it, so the next apply has nothing to do - no
// reinstall of the file it just healed to.
func TestFileSwap_DeployHealMovesTheRefToo(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	src := &shrinkingSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(src.Close)
	src.AddDownload("20", createTestZipBytes(t, map[string]string{"mod1-a.esp": "a"}))
	src.AddDownload("21", createTestZipBytes(t, map[string]string{"mod1-b.esp": "b"}))
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	svc.RegisterSource(src)
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")

	src.only21 = true
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	require.NoError(t, os.RemoveAll(svc.GetGameCache(game).ModPath(plain)))
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	t.Logf("row files=%v variant=%q ref files=%v", row.FileIDs, row.Variant, p.Mods[0].FileIDs)

	assert.Equal(t, []string{"21"}, row.FileIDs)
	assert.Equal(t, []string{"21"}, p.Mods[0].FileIDs, "the ref follows its healed row")

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges, "no reinstall after the heal")
}

// offeredSource serves files "20" (primary unless primary22) and "22" of
// mod1 at 1.0, and "21" until drop21: the file a swap moved to, withdrawn.
type offeredSource struct {
	*mockSourceWithDownloads
	drop21 bool
}

func (s *offeredSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	files := []domain.DownloadableFile{{ID: "20", Name: "A", FileName: "mod1-a.zip", Version: "1.0", IsPrimary: !s.drop21, Category: "MAIN"}}
	if !s.drop21 {
		files = append(files, domain.DownloadableFile{ID: "21", Name: "B", FileName: "mod1-b.zip", Version: "1.0", Category: "MAIN"})
	}
	return append(files, domain.DownloadableFile{ID: "22", Name: "C", FileName: "mod1-c.zip", Version: "1.0", IsPrimary: s.drop21, Category: "MAIN"}), nil
}

// #639 review B4: a row a converge moved to a file-set entry, healed by a
// deploy to another file, lands in the entry that file's tree is - not
// skipped as a re-answer - and its row, entry and ref agree.
func TestFileSwap_DeployHealOfAFileSetRow(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	src := &offeredSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(src.Close)
	src.AddDownload("20", createTestZipBytes(t, map[string]string{"mod1-a.esp": "a"}))
	src.AddDownload("21", createTestZipBytes(t, map[string]string{"mod1-b.esp": "b"}))
	src.AddDownload("22", createTestZipBytes(t, map[string]string{"mod1-c.esp": "c"}))
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	svc.RegisterSource(src)
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	setRefFiles(t, svc, game.ID, "default", "21")
	applyProfile(t, svc, game, "default")
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	require.NotEmpty(t, row.Variant, "precondition: 21 lives in a file-set entry")

	// 21 is withdrawn and its entry is gone: the deploy heals to 22.
	src.drop21 = true
	require.NoError(t, os.RemoveAll(svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0", Variant: row.Variant})))
	res, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Skipped, "a heal of a file-set row is no re-answer")

	healed, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"22"}, healed.FileIDs)
	files, err := svc.GetGameCache(game).ListFiles(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0", Variant: healed.Variant})
	require.NoError(t, err)
	assert.Equal(t, []string{"mod1-c.esp"}, files, "the healed file's own entry, never 20's")
	assert.True(t, deployedFile(t, game, "mod1-c.esp"))
	assert.False(t, deployedFile(t, game, "mod1-a.esp"))
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"22"}, p.Mods[0].FileIDs)
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)
}
