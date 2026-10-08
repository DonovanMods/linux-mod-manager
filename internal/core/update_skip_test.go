package core_test

// #542: "skip this version" holds off ONE pending update without pinning the
// mod. The skip lives on the installed row (skipped_version) and lasts until
// the source offers something newer; the bulk surfaces leave a skipped
// update out, and an explicit single-mod update still works and clears it.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipTestSource offers newer for every installed mod not already at it,
// and serves one new file for the apply.
type skipTestSource struct {
	*multiFileDownloadSource
	newer string
}

func (s *skipTestSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var out []domain.Update
	for _, im := range installed {
		if im.Version != s.newer {
			out = append(out, domain.Update{InstalledMod: im, NewVersion: s.newer})
		}
	}
	return out, nil
}

func newSkipTestService(t *testing.T) (*core.Service, *domain.Game, *skipTestSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")})
	seedUpdatableMod(t, svc, game, "src", "mod2", "Mod Two", "1.0", []string{"old-2"},
		map[string][]byte{"mod2-old.esp": []byte("old-content")})

	src := &skipTestSource{
		multiFileDownloadSource: &multiFileDownloadSource{
			mockSourceWithDownloads: newMockSourceWithDownloads("src"),
			files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "new.esp", IsPrimary: true}},
		},
		newer: "2.0",
	}
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	src.AddMod("g1", &domain.Mod{ID: "mod2", SourceID: "src", Name: "Mod Two", Version: "2.0", GameID: "g1"})
	src.AddDownload("new-1", []byte("new-content"))
	return svc, game, src
}

func skipReport(t *testing.T, svc *core.Service, game *domain.Game) *core.UpdateCheckReport {
	t.Helper()
	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	return report
}

func TestSkipModUpdate_DefaultsToTheOfferedVersion(t *testing.T) {
	svc, game, _ := newSkipTestService(t)

	result, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "")
	require.NoError(t, err)
	assert.Equal(t, "2.0", result.SkippedVersion, "no version given skips the one the source offers now")
	assert.Equal(t, "2.0", result.Mod.SkippedVersion)
	assert.Equal(t, domain.UpdateNotify, result.UpdatePolicy, "a skip is not a pin")

	row, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.SkippedVersion)
}

func TestSkipModUpdate_NoUpdateAndNoVersionIsRefused(t *testing.T) {
	svc, game, src := newSkipTestService(t)
	src.newer = "1.0" // everything is current

	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "")
	require.ErrorIs(t, err, core.ErrNoUpdateToSkip)
}

func TestCheckGameUpdateReport_SkippedVersionIsNotOffered(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)

	report := skipReport(t, svc, game)
	require.Len(t, report.Updates, 1, "the skipped update is not offered")
	assert.Equal(t, "mod2", report.Updates[0].InstalledMod.ID)
	require.Len(t, report.SkippedUpdates, 1, "but it is still findable")
	assert.Equal(t, "mod1", report.SkippedUpdates[0].InstalledMod.ID)
	assert.Equal(t, "2.0", report.SkippedUpdates[0].NewVersion)
	assert.Equal(t, "2.0", report.SkippedUpdates[0].InstalledMod.SkippedVersion)
	assert.Equal(t, 1, report.Skipped.Updates)
	assert.Equal(t, 0, report.Skipped.Total(), "a skipped update's mod WAS checked")

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "mod2", updates[0].InstalledMod.ID)
}

func TestCheckGameUpdateReport_NewerThanSkippedIsOfferedAgain(t *testing.T) {
	svc, game, src := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)

	src.newer = "2.1"
	report := skipReport(t, svc, game)
	assert.Len(t, report.Updates, 2, "a version newer than the skipped one is a new update")
	assert.Empty(t, report.SkippedUpdates)
	assert.Zero(t, report.Skipped.Updates)
}

func TestUnskipModUpdate_OffersTheUpdateAgain(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)

	result, err := svc.UnskipModUpdate(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, result.SkippedVersion)

	report := skipReport(t, svc, game)
	assert.Len(t, report.Updates, 2)
	assert.Empty(t, report.SkippedUpdates)
}

func TestPlanUpdateBatch_LeavesSkippedUpdatesOut(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)

	plan, err := svc.PlanUpdateBatch(context.Background(), game, "default", nil)
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1, "update all leaves the skipped update out")
	assert.Equal(t, "mod2", plan.Updates[0].InstalledMod.ID)
}

// PlanUpdateBatchFrom filters on its own, so a caller that hands it the
// whole check (skipped updates included) still cannot apply a skipped one.
func TestPlanUpdateBatchFrom_LeavesSkippedUpdatesOut(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)
	report := skipReport(t, svc, game)
	all := append(append([]domain.Update{}, report.Updates...), report.SkippedUpdates...)

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", all, nil)
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)
	assert.Equal(t, "mod2", plan.Updates[0].InstalledMod.ID)
}

func TestPlanUpdate_ExplicitUpdateIgnoresAndClearsTheSkip(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)

	plan, err := svc.PlanUpdate(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, plan.Update, "an explicit single-mod update still sees the skipped version")
	assert.Equal(t, "2.0", plan.Update.NewVersion)

	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	row, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Empty(t, row.SkippedVersion, "applying the update clears the skip")
}

// `lmm mod show` and the full mod page read the skip off ModDetail.
func TestModDetail_CarriesTheSkippedVersion(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)

	detail, err := svc.ModDetail(context.Background(), game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed)
	assert.Equal(t, "2.0", detail.Installed.SkippedVersion)
}

// The full mod page's "Update to vX" is a one-key batch selection: naming
// the mod is an explicit request, so the skipped update is planned, applied,
// and the skip cleared.
func TestPlanUpdateBatch_ASelectionNamingASkippedUpdateStillUpdates(t *testing.T) {
	svc, game, _ := newSkipTestService(t)
	_, err := svc.SkipModUpdate(context.Background(), game, "src", "mod1", "default", "2.0")
	require.NoError(t, err)

	plan, err := svc.PlanUpdateBatch(context.Background(), game, "default", []string{"src:mod1"})
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)
	assert.Empty(t, plan.NotFound)

	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Applied, 1)

	row, err := svc.GetInstalledMod(context.Background(), "src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Empty(t, row.SkippedVersion)
}
