package core_test

// #662: a flow that replays a profile's stored file ids never refuses a set
// whose main files mix versions. A user's reference heals to one version -
// previewed by the plan, recorded by the apply, and quiet on the next run;
// a collection's reference installs the curator's files as they are, with a
// note.

import (
	"context"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mixedRootIDs is a stored set whose main files mix versions: the 2.0 main
// file, the 1.0 one (OLD_VERSION, a main file) and the 1.0 optional patch.
var mixedRootIDs = []string{"root-main-1", "root-main-2", "root-opt-1"}

// rootHeal is what healing mixedRootIDs at 2.0 previews.
func rootHeal() core.StoredFilesHeal {
	return core.StoredFilesHeal{SourceID: "src", ModID: "root", Name: "Root", Version: "2.0", Dropped: []core.MixedVersion{
		{Version: "1.0", Files: []core.MixedVersionFile{{ID: "root-main-1", FileName: "root-1.0.zip"}}},
	}}
}

// profileRef is the profile's reference to src:root.
func profileRef(t *testing.T, svc *core.Service, profile string) *domain.ModReference {
	t.Helper()
	p, err := svc.NewProfileManager().Get(context.Background(), "g1", profile)
	require.NoError(t, err)
	ref := p.FindRef("src", "root")
	require.NotNil(t, ref)
	return ref
}

func TestProfileApply_HealsAMixedVersionRef(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupInterplayService(t, false)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "root", Version: "2.0", FileIDs: mixedRootIDs}))

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, []core.StoredFilesHeal{rootHeal()}, plan.Heals, "the plan names the version kept and the file dropped")
	assert.Empty(t, plan.CuratorMixed)
	assert.Equal(t, "2.0", plan.ToInstall[0].Version)

	result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Failed)

	ref := profileRef(t, svc, "default")
	assert.Equal(t, []string{"root-main-2", "root-opt-1"}, ref.FileIDs, "the apply records the healed ids")
	assert.Equal(t, "2.0", ref.Version)
	row, err := svc.GetInstalledMod(ctx, "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"root-main-2", "root-opt-1"}, row.FileIDs)

	again, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, again.NoChanges, "a healed reference is not drift: %+v", again.ToInstall)
	assert.Empty(t, again.Heals)
}

func TestProfileApply_CollectionRefKeepsTheCuratorsFiles(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupInterplayService(t, false)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "root", Version: "1.0", FileIDs: mixedRootIDs, FromCollection: true}))

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	assert.Empty(t, plan.Heals, "the curator chose these files")
	assert.Equal(t, []core.CuratorMixedFiles{{SourceID: "src", ModID: "root", Name: "Root", Versions: []core.MixedVersion{
		{Version: "2.0", Files: []core.MixedVersionFile{{ID: "root-main-2", FileName: "root-2.0.zip"}}},
		{Version: "1.0", Files: []core.MixedVersionFile{{ID: "root-main-1", FileName: "root-1.0.zip"}}},
	}}}, plan.CuratorMixed)

	result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Failed)

	ref := profileRef(t, svc, "default")
	assert.ElementsMatch(t, mixedRootIDs, ref.FileIDs, "every one of the curator's files")
	assert.Equal(t, "1.0", ref.Version)
	assert.True(t, ref.FromCollection)

	again, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, again.NoChanges, "%+v", again.ToInstall)
}

func TestProfileSwitch_HealsAMixedVersionRef(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupInterplayService(t, false)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	_, err = pm.Create(ctx, game.ID, "stable")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "root", Version: "2.0", FileIDs: mixedRootIDs}))

	plan, err := svc.PlanProfileSwitch(ctx, game, "stable")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, []core.StoredFilesHeal{rootHeal()}, plan.Heals, "the switch plan names the heal too")

	result, err := svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Installed)
	assert.Empty(t, result.Warnings, "a heal the plan previewed is not warned about again")

	ref := profileRef(t, svc, "stable")
	assert.Equal(t, []string{"root-main-2", "root-opt-1"}, ref.FileIDs)
	assert.Equal(t, "2.0", ref.Version)

	again, err := svc.PlanProfileApply(ctx, game, "stable")
	require.NoError(t, err)
	assert.True(t, again.NoChanges, "%+v", again.ToInstall)
}

func TestProfileSwitch_CollectionRefKeepsTheCuratorsFiles(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	_, err = pm.Create(ctx, game.ID, "stable")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "root", Version: "1.0", FileIDs: mixedRootIDs, FromCollection: true}))

	plan, err := svc.PlanProfileSwitch(ctx, game, "stable")
	require.NoError(t, err)
	assert.Empty(t, plan.Heals, "a collection's mod is not looked up at plan time, and never healed")
	assert.Zero(t, mock.fileFetches.Load(), "the plan asked the source nothing")

	result, err := svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Root: the collection's files mix main files of versions 2.0 (root-2.0.zip) and 1.0 (root-1.0.zip); installed as the curator chose them"}, result.Warnings,
		"the switch says so when it reaches the mod")
	ref := profileRef(t, svc, "stable")
	assert.ElementsMatch(t, mixedRootIDs, ref.FileIDs)

	again, err := svc.PlanProfileApply(ctx, game, "stable")
	require.NoError(t, err)
	assert.True(t, again.NoChanges, "%+v", again.ToInstall)
}

func TestProfileImport_HealsAMixedVersionRef(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupInterplayService(t, false)

	profile := &domain.Profile{Name: "target", GameID: "g1",
		Mods: []domain.ModReference{{SourceID: "src", ModID: "root", Version: "2.0", FileIDs: mixedRootIDs}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(ctx, game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 1)
	assert.Equal(t, []core.StoredFilesHeal{rootHeal()}, plan.Heals)

	result, err := svc.ApplyImport(ctx, game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Warnings, "a heal the plan previewed is not warned about again")

	ref := profileRef(t, svc, "target")
	assert.Equal(t, []string{"root-main-2", "root-opt-1"}, ref.FileIDs)
	assert.Equal(t, "2.0", ref.Version)

	again, err := svc.PlanProfileApply(ctx, game, "target")
	require.NoError(t, err)
	assert.True(t, again.NoChanges, "%+v", again.ToInstall)
}

// TestApplyUpdate_HealsAMixedVersionRowBeforeUpdating: an installed set
// mixing the 2.0 and 1.0 main files updates to 3.0 without carrying the
// stale 1.0 main file over as an "unchanged extra" - the optional patch is
// carried, the heal is warned about (an update's plan resolves no files).
func TestApplyUpdate_HealsAMixedVersionRowBeforeUpdating(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	mock.files["root"] = append(interplayRootFiles(), domain.DownloadableFile{ID: "root-main-3", Name: "Root 3.0", FileName: "root-3.0.zip", Version: "3.0", Category: "MAIN"})
	stageInterplayDownload(t, mock, "root-main-3", "root-main-3.esp")

	old := seedUpdatableMod(t, svc, game, "src", "root", "Root", "2.0", mixedRootIDs, map[string][]byte{"root.esp": []byte("old")})
	upd := domain.Update{InstalledMod: *old, NewVersion: "3.0"}
	plan, err := svc.NewUpdatePlanForApplyTest(ctx, game.ID, "default", upd)
	require.NoError(t, err)
	result, err := svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	assert.Equal(t, "Root: its stored main files are of more than one version; keeping version 2.0 and dropping root-1.0.zip (1.0) (reinstall the mod at a dropped file's version to use it instead)", result.Warnings[0])

	row, err := svc.GetInstalledMod(ctx, "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"root-main-3", "root-opt-1"}, row.FileIDs, "the stale 1.0 main file is not carried over")
	assert.Equal(t, "3.0", row.Version)
}

// TestDeployProfile_HealsAMixedVersionRowItRebuilds: a deploy that must
// rebuild a row's cache entry heals its mixed main files, records the
// healed ids on the row and the profile, and warns (it has no plan that
// could have said so).
func TestDeployProfile_HealsAMixedVersionRowItRebuilds(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupInterplayService(t, false)
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "2.0", GameID: "g1"},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: mixedRootIDs,
	}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "root", Version: "2.0", FileIDs: mixedRootIDs}))

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Deployed, "%+v", result.Skipped)
	assert.Contains(t, result.Warnings, "Root: its stored main files are of more than one version; keeping version 2.0 and dropping root-1.0.zip (1.0) (reinstall the mod at a dropped file's version to use it instead)")

	row, err := svc.GetInstalledMod(ctx, "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"root-main-2", "root-opt-1"}, row.FileIDs)
	assert.ElementsMatch(t, []string{"root-main-2", "root-opt-1"}, profileRef(t, svc, "default").FileIDs)
}

// addComponentsMod serves "comp" on mock: Core at 1.2 (current) and 1.1
// (archived), and Textures at 1.0 - a separate component the author
// versions on its own - plus extra listed files, each downloadable.
func addComponentsMod(t *testing.T, mock *perModMultiFileSource, extra ...domain.DownloadableFile) {
	t.Helper()
	files := append([]domain.DownloadableFile{
		{ID: "core-12", Name: "Core", FileName: "Core-1.2.zip", Version: "1.2", Category: "MAIN", IsPrimary: true},
		{ID: "core-11", Name: "Core", FileName: "Core-1.1.zip", Version: "1.1", Category: "OLD_VERSION"},
		{ID: "tex-10", Name: "Textures", FileName: "Textures-1.0.zip", Version: "1.0", Category: "MAIN"},
	}, extra...)
	mock.files["comp"] = files
	for _, f := range files {
		stageInterplayDownload(t, mock, f.ID, f.ID+".esp")
	}
	mock.AddMod("g1", &domain.Mod{ID: "comp", SourceID: "src", Name: "Components", Version: "1.2", GameID: "g1"})
}

// TestProfileApply_KeepsASeparateComponentAtAnotherVersion: of Core's two
// copies the 1.2 one stays; Textures 1.0 is a separate component, kept
// and said so - never dropped, and no drift on the next apply.
func TestProfileApply_KeepsASeparateComponentAtAnotherVersion(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	addComponentsMod(t, mock)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "comp", Version: "1.2", FileIDs: []string{"core-11", "core-12", "tex-10"}}))

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.Equal(t, []core.StoredFilesHeal{{SourceID: "src", ModID: "comp", Name: "Components", Version: "1.2",
		Dropped: []core.MixedVersion{{Version: "1.1", Files: []core.MixedVersionFile{{ID: "core-11", FileName: "Core-1.1.zip"}}}},
		Kept:    []core.MixedVersion{{Version: "1.0", Files: []core.MixedVersionFile{{ID: "tex-10", FileName: "Textures-1.0.zip"}}}},
	}}, plan.Heals)

	_, err = svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	p, err := pm.Get(ctx, game.ID, "default")
	require.NoError(t, err)
	ref := p.FindRef("src", "comp")
	require.NotNil(t, ref)
	assert.Equal(t, []string{"core-12", "tex-10"}, ref.FileIDs, "Textures survives")
	assert.Equal(t, "1.2", ref.Version)

	again, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, again.NoChanges, "%+v", again.ToInstall)
}

// TestProfileApply_SeparateComponentsAloneAreNoHeal: Core 1.2 and Textures
// 1.0, no copies - nothing dropped, so nothing to heal or note; both
// install.
func TestProfileApply_SeparateComponentsAloneAreNoHeal(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	addComponentsMod(t, mock)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "comp", Version: "1.2", FileIDs: []string{"tex-10", "core-12"}}))

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.Empty(t, plan.Heals)

	result, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	row, err := svc.GetInstalledMod(ctx, "src", "comp", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"core-12", "tex-10"}, row.FileIDs)
	assert.Equal(t, "1.2", row.Version)
	again, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	assert.True(t, again.NoChanges, "%+v", again.ToInstall)
}

// TestAutoUpdate_KeepsASeparateComponent is the review's must-fix: the
// unattended automatic update of Core 1.2 -> 1.3 must not drop Textures
// 1.0, a separate component at another version.
func TestAutoUpdate_KeepsASeparateComponent(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	addComponentsMod(t, mock, domain.DownloadableFile{ID: "core-13", Name: "Core", FileName: "Core-1.3.zip", Version: "1.3", Category: "MAIN"})
	seedUpdatableMod(t, svc, game, "src", "comp", "Components", "1.2", []string{"core-12", "tex-10"}, map[string][]byte{"core-12.esp": []byte("c"), "tex-10.esp": []byte("t")})
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, "default"))
	_, err := svc.SetModUpdatePolicy(ctx, "src", "comp", game.ID, "default", domain.UpdateAuto)
	require.NoError(t, err)
	installed, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, installed, 1)

	plan, err := svc.PlanAutoUpdate(ctx, game, "default", installed, []domain.Update{{InstalledMod: installed[0], NewVersion: "1.3"}})
	require.NoError(t, err)
	require.NotNil(t, plan)
	run, err := svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.NoError(t, err)
	result, err := run.Apply(ctx, nil)
	require.NoError(t, err)
	require.Len(t, result.Applied, 1, "%+v", result)

	row, err := svc.GetInstalledMod(ctx, "src", "comp", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"core-13", "tex-10"}, row.FileIDs, "Textures survives the automatic update")
	assert.Equal(t, "1.3", row.Version)
}

// TestDeployProfile_RebuildsARowStampedByTheOlderRule: a row recorded
// before #662 at its patch's version (the patch listed first, the main
// file not primary) rebuilds the cache entry it names - same files, same
// version - instead of re-resolving to another.
func TestDeployProfile_RebuildsARowStampedByTheOlderRule(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	mock.files["legacy"] = []domain.DownloadableFile{
		{ID: "lp", Name: "Patch", FileName: "legacy-patch-0.5.zip", Version: "0.5", Category: "OPTIONAL"},
		{ID: "lm", Name: "Main", FileName: "legacy-1.0.zip", Version: "1.0", Category: "MAIN"},
	}
	stageInterplayDownload(t, mock, "lp", "lp.esp")
	stageInterplayDownload(t, mock, "lm", "lm.esp")
	mock.AddMod("g1", &domain.Mod{ID: "legacy", SourceID: "src", Name: "Legacy", Version: "1.0", GameID: "g1"})
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "legacy", SourceID: "src", Name: "Legacy", Version: "0.5", GameID: "g1"},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"lp", "lm"},
	}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "legacy", Version: "0.5", FileIDs: []string{"lp", "lm"}}))

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Deployed, "%+v", result.Skipped)
	assert.Empty(t, result.Warnings)
	row, err := svc.GetInstalledMod(ctx, "src", "legacy", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "0.5", row.Version)
	assert.ElementsMatch(t, []string{"lp", "lm"}, row.FileIDs, "both files, as recorded")
}

// TestDeployProfile_SkipsAHealThatWouldMoveTheVersion: a row at 0.3 whose
// stored main files are copies at 1.0 and 2.0 cannot be rebuilt as
// recorded; the deploy skips it, saying what to do, and writes nothing.
func TestDeployProfile_SkipsAHealThatWouldMoveTheVersion(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupInterplayService(t, false)
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "0.3", GameID: "g1"},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"root-main-1", "root-main-2"},
	}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "root", Version: "0.3", FileIDs: []string{"root-main-1", "root-main-2"}}))

	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0].Reason, "other than its recorded version 0.3 - reinstall the mod at the version you want")
	row, err := svc.GetInstalledMod(ctx, "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "0.3", row.Version)
	assert.Equal(t, []string{"root-main-1", "root-main-2"}, row.FileIDs)
}

// TestPlanProfileSwitch_BoundsItsLookups: a switch plan looks up at most 25
// mods with several stored files, and says how many it left to the run.
func TestPlanProfileSwitch_BoundsItsLookups(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	_, err = pm.Create(ctx, game.ID, "stable")
	require.NoError(t, err)
	for i := range 27 {
		id := fmt.Sprintf("m%02d", i)
		mock.files[id] = []domain.DownloadableFile{
			{ID: id + "a", FileName: "a.zip", Version: "1.0", Category: "MAIN"},
			{ID: id + "b", FileName: "b.zip", Version: "1.0", Category: "MAIN"},
		}
		mock.AddMod("g1", &domain.Mod{ID: id, SourceID: "src", Name: id, Version: "1.0", GameID: "g1"})
		require.NoError(t, pm.AddMod(ctx, game.ID, "stable", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0", FileIDs: []string{id + "a", id + "b"}}))
	}
	plan, err := svc.PlanProfileSwitch(ctx, game, "stable")
	require.NoError(t, err)
	assert.EqualValues(t, 25, mock.fileFetches.Load())
	require.Len(t, plan.Warnings, 1)
	assert.Contains(t, plan.Warnings[0], "2 more mod(s) with several stored files were not looked up")
}

// texturesFiles is a mod whose two main components differ in name only by
// digits: Textures 2K at 1.0, and Textures 4K at 1.1 and (when newer) 1.2.
func texturesFiles(newer bool) []domain.DownloadableFile {
	files := []domain.DownloadableFile{
		{ID: "t2k", Name: "Textures 2K", FileName: "Textures_2K-1.0.zip", Version: "1.0", Category: "MAIN"},
		{ID: "t4k", Name: "Textures 4K", FileName: "Textures_4K-1.1.zip", Version: "1.1", Category: "MAIN", IsPrimary: true},
	}
	if newer {
		files[1].IsPrimary = false
		files = append(files, domain.DownloadableFile{ID: "t4k12", Name: "Textures 4K", FileName: "Textures_4K-1.2.zip", Version: "1.2", Category: "MAIN", IsPrimary: true})
	}
	return files
}

// addTexturesMod serves "tex" on mock with texturesFiles.
func addTexturesMod(t *testing.T, mock *perModMultiFileSource, newer bool) {
	t.Helper()
	mock.files["tex"] = texturesFiles(newer)
	for _, f := range mock.files["tex"] {
		stageInterplayDownload(t, mock, f.ID, f.ID+".esp")
	}
	mock.AddMod("g1", &domain.Mod{ID: "tex", SourceID: "src", Name: "Textures", Version: "1.1", GameID: "g1"})
}

// TestApplyInstall_ComponentsTellingApartByDigitsInstallTogether (#662
// re-review): Textures 2K (1.0) and Textures 4K (1.1) are separate main
// components, not copies, so installing both is no mixed-version refusal.
func TestApplyInstall_ComponentsTellingApartByDigitsInstallTogether(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	addTexturesMod(t, mock, false)
	plan, err := svc.PlanInstall(ctx, game, "default", "src", "tex", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{TargetFileIDs: []string{"t2k", "t4k"}}, nil)
	require.NoError(t, err)
	row, err := svc.GetInstalledMod(ctx, "src", "tex", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"t2k", "t4k"}, row.FileIDs)
	assert.Equal(t, "1.1", row.Version)
}

// TestAutoUpdate_KeepsComponentsTellingApartByDigits (#662 re-review): the
// unattended automatic update of Textures 4K 1.1 -> 1.2 keeps Textures 2K
// 1.0, whose name differs only in digits.
func TestAutoUpdate_KeepsComponentsTellingApartByDigits(t *testing.T) {
	ctx := context.Background()
	svc, game, mock := setupInterplayService(t, false)
	addTexturesMod(t, mock, true)
	seedUpdatableMod(t, svc, game, "src", "tex", "Textures", "1.1", []string{"t2k", "t4k"}, map[string][]byte{"t2k.esp": []byte("2"), "t4k.esp": []byte("4")})
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, game.ID, "default"))
	_, err := svc.SetModUpdatePolicy(ctx, "src", "tex", game.ID, "default", domain.UpdateAuto)
	require.NoError(t, err)
	installed, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, installed, 1)

	plan, err := svc.PlanAutoUpdate(ctx, game, "default", installed, []domain.Update{{InstalledMod: installed[0], NewVersion: "1.2"}})
	require.NoError(t, err)
	require.NotNil(t, plan)
	run, err := svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	require.NoError(t, err)
	result, err := run.Apply(ctx, nil)
	require.NoError(t, err)
	require.Len(t, result.Applied, 1, "%+v", result)

	row, err := svc.GetInstalledMod(ctx, "src", "tex", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"t2k", "t4k12"}, row.FileIDs, "Textures 2K survives the automatic update")
	assert.Equal(t, "1.2", row.Version)
}
