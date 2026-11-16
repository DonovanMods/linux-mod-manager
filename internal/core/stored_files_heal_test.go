package core_test

// #662: a flow that replays a profile's stored file ids never refuses a set
// whose main files mix versions. A user's reference heals to one version -
// previewed by the plan, recorded by the apply, and quiet on the next run;
// a collection's reference installs the curator's files as they are, with a
// note.

import (
	"context"
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
	svc, game, _ := setupInterplayService(t, false)
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	_, err = pm.Create(ctx, game.ID, "stable")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "stable", domain.ModReference{SourceID: "src", ModID: "root", Version: "1.0", FileIDs: mixedRootIDs, FromCollection: true}))

	plan, err := svc.PlanProfileSwitch(ctx, game, "stable")
	require.NoError(t, err)
	assert.Empty(t, plan.Heals)
	require.Len(t, plan.CuratorMixed, 1)
	assert.Equal(t, "Root", plan.CuratorMixed[0].Name)

	_, err = svc.ApplyProfileSwitch(ctx, game, plan, nil)
	require.NoError(t, err)
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
	assert.Equal(t, "Root: its stored main files are of more than one version; keeping version 2.0 and dropping root-1.0.zip (1.0)", result.Warnings[0])

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
	assert.Contains(t, result.Warnings, "Root: its stored main files are of more than one version; keeping version 2.0 and dropping root-1.0.zip (1.0)")

	row, err := svc.GetInstalledMod(ctx, "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"root-main-2", "root-opt-1"}, row.FileIDs)
	assert.ElementsMatch(t, []string{"root-main-2", "root-opt-1"}, profileRef(t, svc, "default").FileIDs)
}
