package core_test

// #538: an EXTERNAL mod's installer (the Steam client, for a Workshop item)
// updates it without lmm, so the version stamped at adopt time goes stale
// on every Steam-side update. These tests pin the three halves of the fix:
// the update check reads the installed revision from the installer's own
// bookkeeping, ReconcileExternalMods writes it back through a single-row
// write, and verify reports (and --fix repairs) a stale record.

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	revAdopted = "1111111111111111111" // the manifest lmm recorded at adopt
	revSteam   = "2222222222222222222" // the manifest Steam has installed since
	revNewer   = "3333333333333333333" // a revision Steam has not installed yet
)

var (
	timeAdopted = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	timeSteam   = time.Date(2026, 10, 5, 15, 4, 0, 0, time.UTC)
)

// revisionCheckSource is the workshop fake with a real update check: it
// compares each row's Version against the revision "the API" publishes,
// exactly as steamworkshop's compareRevision does on its primary signal.
// seen records the rows the check was handed.
type revisionCheckSource struct {
	*workshopTestSource
	remote map[string]string
	seen   []domain.InstalledMod
}

func (s *revisionCheckSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	s.seen = append(s.seen, installed...)
	var out []domain.Update
	for _, im := range installed {
		if remote, ok := s.remote[im.ID]; ok && remote != im.Version {
			out = append(out, domain.Update{InstalledMod: im, NewVersion: remote})
		}
	}
	return out, nil
}

// newRevisionService wires a Service, a game mapped to app 1133870, the
// revision-checking fake, and one adopted external item whose recorded
// revision is revAdopted.
func newRevisionService(t *testing.T) (*core.Service, *domain.Game, *revisionCheckSource, domain.WorkshopItem) {
	t.Helper()
	svc := newFlowsTestService(t)
	src := &revisionCheckSource{workshopTestSource: newWorkshopTestSource(), remote: map[string]string{}}
	svc.RegisterSource(src)
	game := externalTestGame(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))

	root := t.TempDir()
	item := steamItem(t, root, "1133870", "3617086610", revSteam, timeSteam.Unix())
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: item.FileID, SourceID: "steamworkshop", Name: "Workshop Item",
			Version: revAdopted, GameID: game.ID, UpdatedAt: timeAdopted,
		},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
		Enabled: true, Deployed: true, External: true, ExternalPath: item.Path,
	}))
	seedProfileWithMod(t, svc, game.ID, "default", "steamworkshop", item.FileID, revAdopted)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{item}}
	return svc, game, src, item
}

func installedFor(t *testing.T, svc *core.Service, game *domain.Game) []domain.InstalledMod {
	t.Helper()
	installed, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return installed
}

func TestCheckGameUpdates_ExternalComparesSteamsInstalledRevision(t *testing.T) {
	svc, game, src, item := newRevisionService(t)
	src.remote[item.FileID] = revSteam // what Steam already installed

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	assert.Empty(t, updates, "Steam already installed the revision the API offers - that is not an update")

	require.Len(t, src.seen, 1)
	assert.Equal(t, revSteam, src.seen[0].Version, "the source is handed the revision Steam's manifest names")
	assert.True(t, timeSteam.Equal(src.seen[0].UpdatedAt), "with Steam's timeupdated as the secondary signal")

	row, err := svc.GetInstalledMod(context.Background(), "steamworkshop", item.FileID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, revAdopted, row.Version, "CheckGameUpdates is a query - it never writes the record")
}

func TestCheckGameUpdates_ExternalGenuinelyNewerIsStillAnUpdate(t *testing.T) {
	svc, game, src, item := newRevisionService(t)
	src.remote[item.FileID] = revNewer

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, revNewer, updates[0].NewVersion)
	assert.Equal(t, revSteam, updates[0].InstalledMod.Version, "the update reads FROM what Steam has installed, not the stale record")
}

// A Tier-3 row - a Workshop item lmm downloaded itself - is lmm's own
// install: its record is the truth, whatever Steam's manifest says.
func TestCheckGameUpdates_ManagedWorkshopRowKeepsItsRecord(t *testing.T) {
	svc, game, src, item := newRevisionService(t)
	row, err := svc.GetInstalledMod(context.Background(), "steamworkshop", item.FileID, game.ID, "default")
	require.NoError(t, err)
	row.External, row.ExternalPath = false, ""
	require.NoError(t, svc.SaveInstalledMod(context.Background(), row))
	src.remote[item.FileID] = revSteam

	updates, err := svc.CheckGameUpdates(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, updates, 1, "lmm's own copy is still at the revision it downloaded")
	assert.Equal(t, revAdopted, updates[0].InstalledMod.Version)
}

func TestCheckGameUpdateReport_MissingFromSteamsManifestIsItsOwnState(t *testing.T) {
	svc, game, src, item := newRevisionService(t)
	src.remote[item.FileID] = revNewer
	// Steam's manifest parsed fine and no longer lists the item.
	src.scan.Items = nil

	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	assert.Empty(t, report.Updates, "an item Steam no longer has is never reported as an update")
	assert.Empty(t, src.seen, "and is not even asked about")
	require.Len(t, report.ExternalMissing, 1)
	assert.Equal(t, core.ExternalModRef{SourceID: "steamworkshop", ModID: item.FileID, Name: "Workshop Item"}, report.ExternalMissing[0])
	assert.Empty(t, report.Warnings)
}

// When Steam's bookkeeping cannot be read at all, nothing can be called
// missing: the check falls back to lmm's recorded revision and says why.
func TestCheckGameUpdateReport_UnreadableManifestFallsBackToTheRecord(t *testing.T) {
	for name, scan := range map[string]source.WorkshopScan{
		"no library holds a manifest": {},
		"a manifest is damaged":       {Roots: []string{"/lib"}, Warnings: []string{"/lib/appworkshop_1133870.acf: unbalanced braces"}},
	} {
		t.Run(name, func(t *testing.T) {
			svc, game, src, item := newRevisionService(t)
			src.scan = scan
			src.remote[item.FileID] = revSteam

			report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
			require.NoError(t, err)
			require.Len(t, report.Updates, 1, "compared against the recorded revision, as before #538")
			assert.Equal(t, revAdopted, report.Updates[0].InstalledMod.Version)
			assert.Empty(t, report.ExternalMissing, "an unreadable manifest proves nothing is missing")
			assert.NotEmpty(t, report.Warnings, "the fallback is said out loud")
		})
	}
}

func TestCheckGameUpdateReport_CarriesTheSkipAndExternalCounts(t *testing.T) {
	svc, game, src, item := newRevisionService(t)
	src.remote[item.FileID] = revNewer

	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	assert.Equal(t, game.ID, report.GameID)
	assert.Equal(t, "default", report.Profile)
	require.Len(t, report.Updates, 1)
	assert.Equal(t, 1, report.External)
	assert.Empty(t, report.ErrorMessage)
}

func TestReconcileExternalMods_RecordsSteamsRevision(t *testing.T) {
	svc, game, _, item := newRevisionService(t)
	seeded, err := svc.GetInstalledMod(context.Background(), "steamworkshop", item.FileID, game.ID, "default")
	require.NoError(t, err)
	seeded.FileIDs = []string{"f1"}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), seeded))
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "steamworkshop", item.FileID, game.ID, "default", "f1", "deadbeef"))

	result, err := svc.ReconcileExternalMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, result.Reconciled, 1)
	change := result.Reconciled[0]
	assert.Equal(t, item.FileID, change.ModID)
	assert.Equal(t, revAdopted, change.FromVersion)
	assert.Equal(t, revSteam, change.ToVersion)

	row, err := svc.GetInstalledMod(context.Background(), "steamworkshop", item.FileID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, revSteam, row.Version)
	assert.True(t, timeSteam.Equal(row.UpdatedAt), "updated_at follows Steam's timeupdated, got %v", row.UpdatedAt)
	assert.Empty(t, row.PreviousVersion, "nothing to roll back to - Steam owns the files")

	files, err := svc.GetFilesWithChecksums(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "deadbeef", files[0].Checksum, "a single-row write, never a full-row save (#514)")

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, prof.Mods, 1)
	assert.Equal(t, revSteam, prof.Mods[0].Version, "the profile ref follows the record (Ruling 16 pairing)")

	again, err := svc.ReconcileExternalMods(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Empty(t, again.Reconciled, "an up-to-date record is left alone")
}

func TestReconcileExternalMods_LockedRefKeepsItsTarget(t *testing.T) {
	svc, game, _, item := newRevisionService(t)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "steamworkshop", item.FileID, ""))

	result, err := svc.ReconcileExternalMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, result.Reconciled, 1, "the DB row still records what is installed")

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, revAdopted, prof.Mods[0].Version, "a locked ref's Version is the lock's target - never rewritten")
	assert.True(t, prof.Mods[0].Locked)
}

func TestReconcileExternalMods_LeavesMissingAndUnreadableAlone(t *testing.T) {
	svc, game, src, item := newRevisionService(t)
	src.scan.Items = nil

	result, err := svc.ReconcileExternalMods(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Empty(t, result.Reconciled)

	src.scan = source.WorkshopScan{}
	result, err = svc.ReconcileExternalMods(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Empty(t, result.Reconciled)
	assert.NotEmpty(t, result.Warnings)

	row, err := svc.GetInstalledMod(context.Background(), "steamworkshop", item.FileID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, revAdopted, row.Version)
}

func TestVerify_ExternalStaleRecordIsFixable(t *testing.T) {
	svc, game, _, item := newRevisionService(t)

	plain := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal, Force: true})
	f := externalFindingFor(t, plain, item.FileID)
	assert.Equal(t, "external_stale", f.Status)
	assert.True(t, f.External)
	assert.True(t, f.Fixable, "--fix records Steam's revision")
	assert.Equal(t, revAdopted, f.Recorded)
	assert.Equal(t, revSteam, f.Effective)
	assert.Positive(t, plain.Warnings)

	fixed := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal, Fix: true})
	f = externalFindingFor(t, fixed, item.FileID)
	assert.Equal(t, "fixed_external_stale", f.Status)
	assert.False(t, f.Fixable)

	row, err := svc.GetInstalledMod(context.Background(), "steamworkshop", item.FileID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, revSteam, row.Version)

	after := verifyOnce(t, svc, game, core.VerifyOptions{Tier: core.VerifyLocal, Force: true})
	assert.Equal(t, "ok", externalFindingFor(t, after, item.FileID).Status)
}

func externalFindingFor(t *testing.T, result *core.VerifyResult, modID string) core.VerifyFinding {
	t.Helper()
	for _, f := range result.Findings {
		if f.ModID == modID {
			return f
		}
	}
	t.Fatalf("no finding for mod %q in %+v", modID, result.Findings)
	return core.VerifyFinding{}
}
