package core_test

// Tests for #627: a mod recorded as manual-only (#543) is cleared once its
// source says, explicitly, that it serves the mod's files through its API
// again (domain.Mod.Distributable) - by the update check, which already
// fetches every installed mod, and wherever else core reads the source's
// metadata. An absent classification, or a refusal, leaves the record.

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// classifyingSource is fromFileSource reporting each installed mod it
// fetches during an update check, as CurseForge does (source.
// ReportFetchedMod), with the classification set in distributable - a mod
// with no entry is not reported at all. Its downloads are always refused,
// and counted.
type classifyingSource struct {
	*fromFileSource
	distributable map[string]*bool
	urlCalls      atomic.Int32
}

func (s *classifyingSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, inst := range installed {
		if d, ok := s.distributable[inst.ID]; ok {
			source.ReportFetchedMod(ctx, domain.Mod{ID: inst.ID, SourceID: s.ID(), GameID: inst.GameID, Distributable: d})
		}
	}
	return s.fromFileSource.CheckUpdates(ctx, installed)
}

func (s *classifyingSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	s.urlCalls.Add(1)
	return s.fromFileSource.GetDownloadURL(ctx, mod, fileID)
}

// newManualOnlyFixture is the from-file fixture after lmm has recorded
// Auctionator as manual-only (an explicit update met the source's refusal),
// with its source swapped for a classifyingSource.
func newManualOnlyFixture(t *testing.T) (*fromFileFixture, *classifyingSource) {
	t.Helper()
	f := newFromFileFixture(t)
	f.failSingleUpdate(t)
	require.True(t, f.row(t).ManualOnly, "precondition: the refusal is recorded")
	src := &classifyingSource{fromFileSource: f.src, distributable: map[string]*bool{}}
	f.svc.RegisterSource(src)
	return f, src
}

// check runs the bulk update check over the fixture's installed mods.
func (f *fromFileFixture) check(t *testing.T) *core.UpdateCheckReport {
	t.Helper()
	installed, err := f.svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	report, err := f.svc.CheckGameUpdateReport(context.Background(), f.game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	return report
}

// The acceptance case: the source says the mod is distributable, so the
// check clears the record, says so once, and offers the update as an
// ordinary one.
func TestCheckGameUpdateReport_SourceSaysDistributable_ClearsManualOnlyOnce(t *testing.T) {
	f, src := newManualOnlyFixture(t)
	src.distributable["42"] = boolPtr(true)

	report := f.check(t)

	assert.False(t, f.row(t).ManualOnly, "the record is cleared")
	require.Len(t, report.ManualOnlyCleared, 1, "the check says so")
	cleared := report.ManualOnlyCleared[0]
	assert.Equal(t, "msrc", cleared.SourceID)
	assert.Equal(t, "42", cleared.ModID)
	assert.Equal(t, "Auctionator", cleared.Name)
	assert.Contains(t, cleared.Message, "Auctionator: automatic downloads are available again")
	require.Len(t, report.Updates, 1)
	assert.False(t, report.Updates[0].InstalledMod.ManualOnly, "the update is an ordinary one again")

	again := f.check(t)
	assert.Empty(t, again.ManualOnlyCleared, "the notice is given once: nothing is recorded to clear any more")
	require.Len(t, again.Updates, 1)
	assert.False(t, again.Updates[0].InstalledMod.ManualOnly)
}

// An explicit "not distributable" keeps the record.
func TestCheckGameUpdateReport_SourceSaysNotDistributable_KeepsManualOnly(t *testing.T) {
	f, src := newManualOnlyFixture(t)
	src.distributable["42"] = boolPtr(false)

	report := f.check(t)

	assert.True(t, f.row(t).ManualOnly)
	assert.Empty(t, report.ManualOnlyCleared)
	require.Len(t, report.Updates, 1)
	assert.True(t, report.Updates[0].InstalledMod.ManualOnly)
}

// A source that says nothing - not reporting the mod, or reporting it with
// no classification - keeps the record.
func TestCheckGameUpdateReport_SourceSaysNothing_KeepsManualOnly(t *testing.T) {
	for name, report := range map[string]bool{"not reported": false, "reported unclassified": true} {
		t.Run(name, func(t *testing.T) {
			f, src := newManualOnlyFixture(t)
			if report {
				src.distributable["42"] = nil
			}

			got := f.check(t)

			assert.True(t, f.row(t).ManualOnly)
			assert.Empty(t, got.ManualOnlyCleared)
			require.Len(t, got.Updates, 1)
			assert.True(t, got.Updates[0].InstalledMod.ManualOnly)
		})
	}
}

// A mod with no pending update is cleared all the same: the check fetched
// it, and that is all the clear needs.
func TestCheckGameUpdateReport_NoPendingUpdate_StillClearsManualOnly(t *testing.T) {
	f, src := newManualOnlyFixture(t)
	src.updates = nil
	src.distributable["42"] = boolPtr(true)

	report := f.check(t)

	assert.Empty(t, report.Updates)
	assert.False(t, f.row(t).ManualOnly)
	require.Len(t, report.ManualOnlyCleared, 1)
	assert.Equal(t, "42", report.ManualOnlyCleared[0].ModID)
}

// CheckGameUpdates - the check without the report, which `lmm update <id>`
// and the update batch's plan run - clears too.
func TestCheckGameUpdates_SourceSaysDistributable_ClearsManualOnly(t *testing.T) {
	f, src := newManualOnlyFixture(t)
	src.distributable["42"] = boolPtr(true)
	installed, err := f.svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	updates, err := f.svc.CheckGameUpdates(context.Background(), f.game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)

	assert.False(t, f.row(t).ManualOnly)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].InstalledMod.ManualOnly)
}

// A distributable mod lmm never recorded is not news: no notice.
func TestCheckGameUpdateReport_DistributableModNeverRecorded_NoNotice(t *testing.T) {
	f := newFromFileFixture(t)
	src := &classifyingSource{fromFileSource: f.src, distributable: map[string]*bool{"42": boolPtr(true)}}
	f.svc.RegisterSource(src)

	report := f.check(t)

	assert.Empty(t, report.ManualOnlyCleared)
	assert.False(t, f.row(t).ManualOnly)
}

// Once cleared, the mod is in the update batch again - it is attempted, not
// skipped - and a source that still refuses records it again, as any
// refusal does.
func TestCheckGameUpdateReport_ClearedMod_BatchAttemptsIt_ARefusalRecordsItAgain(t *testing.T) {
	f, src := newManualOnlyFixture(t)
	src.distributable["42"] = boolPtr(true)
	report := f.check(t)
	require.False(t, f.row(t).ManualOnly)
	src.urlCalls.Store(0)

	plan, err := f.svc.PlanUpdateBatchFrom(context.Background(), f.game, "default", report.Updates, nil)
	require.NoError(t, err)
	result, err := f.svc.ApplyUpdateBatch(context.Background(), f.game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)

	assert.Empty(t, result.Skipped, "not skipped as manual-only any more")
	require.Len(t, result.Failed, 1, "attempted, and this source still refuses")
	assert.True(t, result.Failed[0].ManualDownload)
	assert.NotZero(t, src.urlCalls.Load(), "the batch asked the source for the download")
	assert.True(t, f.row(t).ManualOnly, "the refusal records it again")
}

// Once cleared, an auto-policy mod is an automatic update again.
func TestCheckGameUpdateReport_ClearedAutoPolicyMod_IsAnAutomaticUpdateAgain(t *testing.T) {
	f, src := newManualOnlyFixture(t)
	_, err := f.svc.SetModUpdatePolicy(context.Background(), "msrc", "42", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)
	require.Equal(t, domain.UpdateAuto, f.row(t).UpdatePolicy)
	key := domain.ModKey("msrc", "42")

	src.distributable["42"] = boolPtr(false)
	assert.NotContains(t, core.AutoUpdateKeys(f.check(t).Updates), key, "precondition: manual-only is never automatic")

	src.distributable["42"] = boolPtr(true)
	assert.True(t, slices.Contains(core.AutoUpdateKeys(f.check(t).Updates), key))
}

// The detail view reads the source's metadata too: an explicit
// "distributable" clears the record there, and the mod is not shown as
// manual-only.
func TestModDetail_SourceSaysDistributable_ClearsManualOnly(t *testing.T) {
	f, _ := newManualOnlyFixture(t)
	f.src.mods["42"].Distributable = boolPtr(true)

	detail, err := f.svc.ModDetail(context.Background(), f.game, "default", "msrc", "42")
	require.NoError(t, err)

	assert.False(t, detail.Mod.ManualOnly)
	assert.False(t, f.row(t).ManualOnly)
}

// ... and an absent classification leaves it.
func TestModDetail_SourceSaysNothing_KeepsManualOnly(t *testing.T) {
	f, _ := newManualOnlyFixture(t)

	detail, err := f.svc.ModDetail(context.Background(), f.game, "default", "msrc", "42")
	require.NoError(t, err)

	assert.True(t, detail.Mod.ManualOnly)
	assert.True(t, f.row(t).ManualOnly)
}

// An update from a hand-downloaded file reads the source's metadata as
// well: an explicit "distributable" clears the record.
func TestUpdateFromArchive_SourceSaysDistributable_ClearsManualOnly(t *testing.T) {
	f, _ := newManualOnlyFixture(t)
	f.src.mods["42"].Distributable = boolPtr(true)

	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)

	assert.False(t, f.row(t).ManualOnly)
}

// So does an archive import named to a source-backed mod
// (noteSourceClassification).
func TestImportArchive_SourceSaysDistributable_ClearsManualOnly(t *testing.T) {
	f := newInstallFromFileFixture(t)
	dl := f.failedInstall(t)
	f.src.mods["43"].Distributable = boolPtr(true)

	plan := f.plan(t, f.archive(t, "MapUtils-1.2.zip"), core.ImportArchiveOptions{ExpectedFileID: dl.FileID})
	_, err := f.apply(t, plan, core.ImportArchiveOptions{})
	require.NoError(t, err)

	assert.False(t, f.row(t).ManualOnly, "the source's explicit word outranks the refusal recorded before it")
}

// An adoption matched to a source that says it serves the mod clears a
// manual-only record the mod already had (noteSourceClassification).
func TestApplyAdopt_SourceSaysDistributable_ClearsManualOnly(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1", Distributable: boolPtr(true)}}
	src.files = []domain.DownloadableFile{{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")
	svc.RecordManualOnlyForTest(context.Background(), "g1", "acme-source", "42")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "42", "g1", "default")
	require.NoError(t, err)
	assert.False(t, installed.ManualOnly)
}

// A re-link to an identity whose source says it serves the mod clears a
// manual-only record that identity had; one whose source says nothing
// keeps it.
func TestApplyRelinkMod_NewIdentitySaysDistributable_ClearsManualOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		distributable *bool
		want          bool
	}{
		"distributable": {boolPtr(true), false},
		"says nothing":  {nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			svc, game, src := newModDetailTestService(t)
			game.SourceIDs["src"] = game.ID
			seedCategorised(t, svc, game, "a", "")
			src.AddMod(game.ID, &domain.Mod{ID: "b", SourceID: "src", Name: "Mod b", Version: "1.0", GameID: game.ID, Distributable: tc.distributable})
			ctx := context.Background()
			svc.RecordManualOnlyForTest(ctx, game.ID, "src", "b")

			plan, err := svc.PlanRelinkMod(ctx, game, "default", "src", "a", "src", "b")
			require.NoError(t, err)
			_, err = svc.ApplyRelinkMod(ctx, game, plan, core.RelinkOptions{}, nil)
			require.NoError(t, err)

			row, err := svc.GetInstalledMod(ctx, "src", "b", game.ID, "default")
			require.NoError(t, err)
			assert.Equal(t, tc.want, row.ManualOnly)
		})
	}
}

// `lmm update <id>`'s plan carries what its check cleared, so the single
// update can say so too.
func TestPlanUpdate_CarriesWhatItsCheckCleared(t *testing.T) {
	f, src := newManualOnlyFixture(t)
	src.distributable["42"] = boolPtr(true)

	plan, err := f.svc.PlanUpdate(context.Background(), f.game, "default", "msrc", "42")
	require.NoError(t, err)

	require.Len(t, plan.ManualOnlyCleared, 1)
	assert.Equal(t, "42", plan.ManualOnlyCleared[0].ModID)
	assert.False(t, plan.Mod.ManualOnly, "the plan's row is read after the clear")
	assert.False(t, f.row(t).ManualOnly)
}
