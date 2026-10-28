package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workshopTestSource is a fake ModSource implementing the two optional
// capabilities the adopt flow uses - the local scan and the batch metadata
// describe - so core's rules are tested without the real Steam source, and
// without any possibility of a network call or a read of a real library.
type workshopTestSource struct {
	*adoptTestSource
	scan     source.WorkshopScan
	scanErr  error
	describe []source.ModDescription
	descErr  error
	// descRefresh records the refresh flag the last DescribeMods saw.
	descRefresh bool
	descCalls   int
}

func newWorkshopTestSource() *workshopTestSource {
	return &workshopTestSource{adoptTestSource: newAdoptTestSource("steamworkshop")}
}

func (s *workshopTestSource) ScanWorkshopItems(ctx context.Context, appID string) (source.WorkshopScan, error) {
	if s.scanErr != nil {
		return source.WorkshopScan{}, s.scanErr
	}
	return s.scan, nil
}

func (s *workshopTestSource) DescribeMods(ctx context.Context, appID string, ids []string, refresh bool) ([]source.ModDescription, error) {
	s.descCalls++
	s.descRefresh = refresh
	if s.descErr != nil {
		return nil, s.descErr
	}
	return s.describe, nil
}

// steamItem builds one on-disk Workshop item under root, exactly as the
// Steam client lays it out, and returns the domain row a scan would report.
func steamItem(t *testing.T, root, appID, fileID, manifest string, timeUpdated int64) domain.WorkshopItem {
	t.Helper()
	dir := filepath.Join(root, "steamapps", "workshop", "content", appID, fileID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mod.pak"), []byte("steam owns this"), 0o644))
	return domain.WorkshopItem{
		FileID: fileID, Path: dir, SizeOnDisk: 15, Manifest: manifest, TimeUpdated: timeUpdated,
	}
}

// newWorkshopService wires a Service with the fake workshop source
// registered and a game mapped to app id 1133870.
func newWorkshopService(t *testing.T) (*core.Service, *domain.Game, *workshopTestSource, string) {
	t.Helper()
	svc := newFlowsTestService(t)
	src := newWorkshopTestSource()
	svc.RegisterSource(src)
	game := &domain.Game{
		ID: "space-engineers-2", Name: "Space Engineers 2", ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"steamworkshop": "1133870"},
	}
	// A real game always has its default profile on disk; the adopt flow
	// upserts refs into it.
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	steamRoot := t.TempDir()
	return svc, game, src, steamRoot
}

func TestScanWorkshop_SplitsTrackedFromUntracked(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	a := steamItem(t, root, "1133870", "3617086610", "7987119735124793734", 1764767935)
	b := steamItem(t, root, "1133870", "3512001122", "1122334455667788990", 1758000000)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{b, a}}

	// One of the two is already tracked.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "3617086610", SourceID: "steamworkshop", Name: "Known", GameID: game.ID},
		ProfileName: "default", Enabled: true, Deployed: true, External: true, ExternalPath: a.Path,
	}))

	scan, err := svc.ScanWorkshop(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Equal(t, "1133870", scan.AppID)
	assert.Equal(t, []string{root}, scan.Libraries)
	require.Len(t, scan.Items, 2)
	assert.Equal(t, 1, scan.Tracked)
	assert.Equal(t, 1, scan.Untracked)
}

func TestScanWorkshop_NoWorkshopSourceConfigured(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	_, err := svc.ScanWorkshop(context.Background(), game, "default")
	require.Error(t, err, "a game with no steamworkshop mapping has nothing to scan")
}

func TestPlanWorkshopAdopt_FoldsInSteamMetadata(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	item := steamItem(t, root, "1133870", "3617086610", "7987119735124793734", 1764767935)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{item}}
	src.describe = []source.ModDescription{{
		ModID: "3617086610",
		Mod: domain.Mod{
			ID: "3617086610", SourceID: "steamworkshop", Name: "Sample Workshop Item",
			Author: "76561198000000000", Category: "Blueprint",
			SourceURL: "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610",
		},
	}}

	plan, err := svc.PlanWorkshopAdopt(context.Background(), game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Entries, 1)
	assert.False(t, plan.NoChanges)

	entry := plan.Entries[0]
	assert.Equal(t, "3617086610", entry.FileID)
	assert.Equal(t, item.Path, entry.Path)
	assert.Equal(t, "7987119735124793734", entry.Manifest)
	require.NotNil(t, entry.Mod)
	assert.Equal(t, "Sample Workshop Item", entry.Mod.Name)
	assert.False(t, entry.Unavailable)
}

func TestPlanWorkshopAdopt_UnavailableItemStaysAdoptable(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	item := steamItem(t, root, "1133870", "2900001111", "9988776655443322110", 1700000000)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{item}}
	src.describe = []source.ModDescription{{
		ModID: "2900001111", Unavailable: true, Note: "Steam does not describe this item",
	}}

	plan, err := svc.PlanWorkshopAdopt(context.Background(), game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Entries, 1, "the item is on disk and the game loads it, so lmm can still track it")
	assert.True(t, plan.Entries[0].Unavailable)
	assert.NotEmpty(t, plan.Entries[0].Note)
	assert.Nil(t, plan.Entries[0].Mod)
}

func TestPlanWorkshopAdopt_MetadataFailureDegradesToAWarning(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	item := steamItem(t, root, "1133870", "3617086610", "7987119735124793734", 1764767935)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{item}}
	src.descErr = errors.New("steam is unreachable")

	plan, err := svc.PlanWorkshopAdopt(context.Background(), game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err, "Steam being down must not stop lmm tracking what is on disk right now")
	require.Len(t, plan.Entries, 1)
	assert.Nil(t, plan.Entries[0].Mod)
	require.Len(t, plan.Scan.Warnings, 1)
	assert.Contains(t, plan.Scan.Warnings[0], "still adoptable")
}

func TestPlanWorkshopAdopt_RefreshReachesTheSource(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{
		steamItem(t, root, "1133870", "3617086610", "m", 1),
	}}

	_, err := svc.PlanWorkshopAdopt(context.Background(), game, "default", core.WorkshopAdoptOptions{Refresh: true})
	require.NoError(t, err)
	assert.True(t, src.descRefresh)
}

func TestPlanWorkshopAdopt_NothingUntrackedIsNoChanges(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	src.scan = source.WorkshopScan{Roots: []string{root}}

	plan, err := svc.PlanWorkshopAdopt(context.Background(), game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)
	assert.True(t, plan.NoChanges)
	assert.Empty(t, plan.Entries)
	assert.Zero(t, src.descCalls, "nothing to describe means no request at all")
}

func TestApplyWorkshopAdopt_RecordsTrackingAndTouchesNoFiles(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	item := steamItem(t, root, "1133870", "3617086610", "7987119735124793734", 1764767935)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{item}}
	src.describe = []source.ModDescription{{
		ModID: "3617086610",
		Mod: domain.Mod{
			ID: "3617086610", SourceID: "steamworkshop", Name: "Sample Workshop Item",
			Author: "76561198000000000",
		},
	}}
	ctx := context.Background()

	plan, err := svc.PlanWorkshopAdopt(ctx, game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)

	var phases []core.DeployPhase
	result, err := svc.ApplyWorkshopAdopt(ctx, game, plan, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok {
			phases = append(phases, fe.FlowPhase())
		}
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Adopted)
	assert.Contains(t, phases, core.WorkshopScanned)
	assert.Contains(t, phases, core.WorkshopAdopted)

	mod, err := svc.GetInstalledMod(ctx, "steamworkshop", "3617086610", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.External)
	assert.Equal(t, item.Path, mod.ExternalPath)
	assert.Equal(t, "7987119735124793734", mod.Version, "the ACF manifest IS the version identity")
	assert.Equal(t, domain.UpdateNotify, mod.UpdatePolicy, "notify is forced at adopt")
	assert.True(t, mod.Deployed, "its files are where the game reads them")
	assert.True(t, mod.Enabled)
	assert.Equal(t, "Sample Workshop Item", mod.Name)
	assert.Equal(t, game.ID, mod.GameID, "the lmm game id, not the Steam app id")
	assert.Empty(t, mod.FileIDs, "lmm downloaded nothing")

	// The profile ref is upserted...
	profile, err := svc.NewProfileManager().Get(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NotNil(t, profile.FindRef("steamworkshop", "3617086610"))

	// ...and NOTHING was written under the game's mod directory or into the
	// cache: lmm tracks this item, it does not manage it.
	entries, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.False(t, svc.GetGameCache(game).Exists(game.ID, "steamworkshop", "3617086610", "7987119735124793734"))
	assert.FileExists(t, filepath.Join(item.Path, "mod.pak"), "Steam's own file is untouched")
}

func TestApplyWorkshopAdopt_SkipsAnItemAlreadyTracked(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	item := steamItem(t, root, "1133870", "3617086610", "m1", 1)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{item}}
	ctx := context.Background()

	plan, err := svc.PlanWorkshopAdopt(ctx, game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyWorkshopAdopt(ctx, game, plan, nil)
	require.NoError(t, err)

	// The same plan applied twice: the second run finds it tracked. (It is
	// refused as stale first - Ruling 5 - so re-plan, as a frontend would.)
	plan2, err := svc.PlanWorkshopAdopt(ctx, game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)
	assert.True(t, plan2.NoChanges)
	result, err := svc.ApplyWorkshopAdopt(ctx, game, plan2, nil)
	require.NoError(t, err)
	assert.Zero(t, result.Adopted)
}

func TestApplyWorkshopAdopt_StalePlanIsRefused(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{
		steamItem(t, root, "1133870", "3617086610", "m1", 1),
	}}
	ctx := context.Background()

	plan, err := svc.PlanWorkshopAdopt(ctx, game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)

	// Something else installs a mod into the profile between plan and apply.
	seedNamedInstalledMod(t, svc, game, "src", "9", "Other Mod", "1.0", true, map[string][]byte{"x.esp": []byte("x")})

	_, err = svc.ApplyWorkshopAdopt(ctx, game, plan, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

func TestApplyWorkshopAdopt_ItemWithNoMetadataAdoptsUnderAHonestName(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{
		steamItem(t, root, "1133870", "2900001111", "", 1700000000),
	}}
	src.describe = []source.ModDescription{{ModID: "2900001111", Unavailable: true, Note: "delisted"}}
	ctx := context.Background()

	plan, err := svc.PlanWorkshopAdopt(ctx, game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)
	result, err := svc.ApplyWorkshopAdopt(ctx, game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Adopted)

	mod, err := svc.GetInstalledMod(ctx, "steamworkshop", "2900001111", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Workshop item 2900001111", mod.Name,
		"a blank row would be worse than saying lmm does not know the name")
	assert.Empty(t, mod.Version, "no manifest in the ACF means no content identity to record")
}
