package core_test

// #639: a profile entry whose file_ids change while its version does not
// (a hand edit, a collection revision that swaps a file, an import of an
// edited profile) is drift, as a version change is: every converge flow
// reinstalls the mod with the new files, into a cache entry of their own,
// so the old file's tree is never reused for the new set.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sameVersionSwapSource serves three files of mod1 at ONE version, 1.0:
// "20" (mod1-a.esp), "21" (mod1-b.esp) and "22" (mod1-c.esp).
type sameVersionSwapSource struct{ *mockSourceWithDownloads }

func (s *sameVersionSwapSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "20", Name: "A", FileName: mod.ID + "-a.zip", Version: "1.0", IsPrimary: true, Category: "MAIN"},
		{ID: "21", Name: "B", FileName: mod.ID + "-b.zip", Version: "1.0", Category: "MAIN"},
		{ID: "22", Name: "C", FileName: mod.ID + "-c.zip", Version: "1.0", Category: "MAIN"},
	}, nil
}

func newSameVersionSwapSource(t *testing.T) *sameVersionSwapSource {
	t.Helper()
	mock := &sameVersionSwapSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)
	mock.AddDownload("20", createTestZipBytes(t, map[string]string{"mod1-a.esp": "a"}))
	mock.AddDownload("21", createTestZipBytes(t, map[string]string{"mod1-b.esp": "b"}))
	mock.AddDownload("22", createTestZipBytes(t, map[string]string{"mod1-c.esp": "c"}))
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	return mock
}

// setRefFiles hand-edits profile's mod1 entry to pin ids.
func setRefFiles(t *testing.T, svc *core.Service, gameID, profile string, ids ...string) {
	t.Helper()
	p, err := config.LoadProfile(svc.ConfigDir(), gameID, profile)
	require.NoError(t, err)
	for i := range p.Mods {
		if p.Mods[i].ModID == "mod1" {
			p.Mods[i].FileIDs = ids
		}
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
}

func deployedFile(t *testing.T, game *domain.Game, name string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(game.ModPath, name))
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

// An apply after a hand edit swaps the file at the same version: the plan
// says it reinstalls the mod because its files changed, the apply replaces
// the old file's deployment with the new one's, from a cache entry of its
// own, and the next plan has nothing to do. Swapping back reuses the
// original entry, which was never touched.
func TestProfileApply_SameVersionFileSwapReinstalls(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	src := newSameVersionSwapSource(t)
	svc.RegisterSource(src)
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	require.True(t, deployedFile(t, game, "mod1-a.esp"))

	// An unchanged profile has nothing to do.
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges, "an unchanged profile has no changes")

	setRefFiles(t, svc, game.ID, "default", "21")
	plan, err = svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	require.False(t, plan.NoChanges, "a same-version file swap is drift")
	require.Len(t, plan.ToInstall, 1)
	entry := plan.ToInstall[0]
	assert.Equal(t, core.ReinstallFilesChanged, entry.Reason)
	assert.Equal(t, "1.0", entry.Version)
	require.Len(t, entry.Files, 1)
	assert.Equal(t, "21", entry.Files[0].ID)
	require.NotNil(t, entry.Replaces, "the live deployment of the old file is replaced")
	assert.False(t, entry.Cached, "the old file's entry does not hold the new file")

	result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Replaced)

	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"21"}, row.FileIDs)
	assert.Equal(t, "1.0", row.Version)
	assert.NotEmpty(t, row.Variant, "the new set's tree is an entry of its own")
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "the old file's member is retired")
	assert.True(t, deployedFile(t, game, "mod1-b.esp"), "the new file's member is deployed")

	gameCache := svc.GetGameCache(game)
	plain := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	assert.True(t, gameCache.HasFileIDs(plain, []string{"20"}), "the old file's entry is untouched")
	files, err := gameCache.ListFiles(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0", Variant: row.Variant})
	require.NoError(t, err)
	assert.Equal(t, []string{"mod1-b.esp"}, files, "the old tree is never reused for the new set")

	plan, err = svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges, "converged: the next apply has nothing to do")

	// Back to the first file: its untouched entry is reused, not fetched.
	downloads := src.DownloadCount()
	setRefFiles(t, svc, game.ID, "default", "20")
	plan, err = svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, core.ReinstallFilesChanged, plan.ToInstall[0].Reason)
	assert.True(t, plan.ToInstall[0].Cached, "the plain entry holds exactly the first file")
	_, err = svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, downloads, src.DownloadCount(), "nothing was downloaded")
	row, err = svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"20"}, row.FileIDs)
	assert.Empty(t, row.Variant, "the first file's tree is the plain entry it always was")
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
	assert.False(t, deployedFile(t, game, "mod1-b.esp"))
}

// File sets compare as sets: the same files in another order are no drift.
func TestProfileApply_FileOrderIsNoDrift(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20", "21"}}))
	applyProfile(t, svc, game, "default")

	setRefFiles(t, svc, game.ID, "default", "21", "20")
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)
}

// A ref that pins no files, or a row that records none, has no file set to
// compare: no drift, as before.
func TestProfileApply_UnpinnedFilesAreNoDrift(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")

	setRefFiles(t, svc, game.ID, "default")
	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)
}

// A switch to a profile that pins another file of the same version than
// the live one reinstalls it, and the switch back puts the first file back.
func TestProfileSwitch_SameVersionFileSwapReinstalls(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"21"}}))

	plan, err := svc.PlanProfileSwitch(ctx, game, "other")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1, "the live row's other file is no enable")
	assert.Empty(t, plan.ToEnable)
	assert.Equal(t, map[string]string{"src:mod1": core.ReinstallFilesChanged}, plan.ReinstallReasons)
	_, err = svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)

	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "other")
	require.NoError(t, err)
	assert.Equal(t, []string{"21"}, row.FileIDs)
	assert.False(t, deployedFile(t, game, "mod1-a.esp"))
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))

	plan, err = svc.PlanProfileSwitch(ctx, game, "default")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)
	assert.True(t, deployedFile(t, game, "mod1-a.esp"))
	assert.False(t, deployedFile(t, game, "mod1-b.esp"))
}

// An import of an edited profile over the installed one reinstalls a mod
// whose files it changes at the same version.
func TestProfileImport_SameVersionFileSwapReinstalls(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")

	data, err := config.ExportProfile(&domain.Profile{Name: "default", GameID: game.ID,
		Mods: []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"21"}}}})
	require.NoError(t, err)
	plan, err := svc.PlanImport(ctx, game, data)
	require.NoError(t, err)
	require.Len(t, plan.NeedsRedownload, 1)
	assert.Empty(t, plan.Installed)
	assert.Equal(t, map[string]string{"src:mod1": core.ReinstallFilesChanged}, plan.ReinstallReasons)

	_, err = svc.ApplyImport(ctx, game, plan, core.ProfileImportOptions{Install: true, Force: true}, nil)
	require.NoError(t, err)
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"21"}, row.FileIDs)
	assert.False(t, deployedFile(t, game, "mod1-a.esp"))
	assert.True(t, deployedFile(t, game, "mod1-b.esp"))
}

// A same-version file-only update of a mod a converge moved to a file-set
// entry keeps out of the entry another set fills: the update's tree is its
// own file's, never layered onto the first set's.
func TestUpdate_SameVersionUpdateOfASwappedModKeepsItsOwnTree(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	ctx := context.Background()
	svc.RegisterSource(newSameVersionSwapSource(t))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0", FileIDs: []string{"20"}}))
	applyProfile(t, svc, game, "default")
	setRefFiles(t, svc, game.ID, "default", "21")
	applyProfile(t, svc, game, "default")
	row, err := svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	require.NotEmpty(t, row.Variant)

	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "default", domain.Update{
		InstalledMod: *row, NewVersion: "1.0", FileIDReplacements: map[string]string{"21": "22"},
	})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	row, err = svc.GetInstalledMod(ctx, "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"22"}, row.FileIDs)
	assert.True(t, deployedFile(t, game, "mod1-c.esp"))
	assert.False(t, deployedFile(t, game, "mod1-b.esp"), "the superseded file is retired")
	assert.False(t, deployedFile(t, game, "mod1-a.esp"), "the other set's tree is not layered in")
}
