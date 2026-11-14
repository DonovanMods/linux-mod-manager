package core_test

// #607 C2: importing a source's own collection - converted, planned and
// applied through L1's pipeline - with a source double that resolves the
// list, serves the recipe, reports an account tier and serves the pinned
// files, so nothing of any real source's format is involved (internal/app
// drives the real NexusMods recipes).

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectionSource is a full source with revisioned collections.
type collectionSource struct {
	*mockSourceWithDownloads
	recipe     *recipeSource
	tier       source.Tier
	tierErr    error
	filesByMod map[string][]string // mod id -> file ids it serves
	urlErr     error
	refreshes  int
}

func (s *collectionSource) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	return s.recipe.ResolveGameCollection(ctx, gameSourceID, ref)
}

func (s *collectionSource) ManifestArchiveURL(ctx context.Context, c source.Collection) (string, error) {
	return s.recipe.ManifestArchiveURL(ctx, c)
}

func (s *collectionSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	m, err := s.recipe.ParseManifest(root)
	for i := range m.Mods {
		switch m.Mods[i].ModID {
		case "11":
			m.Mods[i].UpdatePolicy = "latest"
		case "12":
			m.Mods[i].Optional = true
		}
	}
	return m, err
}

func (s *collectionSource) AccountTier(context.Context) (source.Tier, error) {
	return s.tier, s.tierErr
}

// RefreshAccountTier counts the plan's fresh asks (source.AccountTierRefresher).
func (s *collectionSource) RefreshAccountTier(ctx context.Context) (source.Tier, error) {
	s.refreshes++
	return s.AccountTier(ctx)
}

func (s *collectionSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	if s.urlErr != nil {
		return "", s.urlErr
	}
	return s.mockSourceWithDownloads.GetDownloadURL(ctx, mod, fileID)
}

func (s *collectionSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	var out []domain.DownloadableFile
	for _, id := range s.filesByMod[mod.ID] {
		out = append(out, domain.DownloadableFile{ID: id, Name: "File " + id, FileName: mod.ID + "-" + id + ".zip", Version: "1"})
	}
	return out, nil
}

const threeModRecipe = `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "100"},
	{"name": "Eleven", "modId": "11", "fileId": "110"},
	{"name": "Twelve", "modId": "12", "fileId": "120"}]}`

type sourceImportFixture struct {
	svc  *core.Service
	game *domain.Game
	src  *collectionSource
}

func newSourceImportFixture(t *testing.T, tier source.Tier) *sourceImportFixture {
	t.Helper()
	archive := recipeArchive(t, threeModRecipe)
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(cdn.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: cdn.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	downloads := newMockSourceWithDownloads("nexusmods")
	t.Cleanup(downloads.Close)
	src := &collectionSource{
		mockSourceWithDownloads: downloads,
		recipe: &recipeSource{mockSource: downloads.mockSource, archiveURL: cdn.URL + "/recipe.7z",
			collection: source.Collection{ID: "abcdef", Name: "Three Mods", Revision: 3, Author: "curator",
				URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Handle: "/h",
				Items: []source.CollectionItem{
					{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten", URL: "https://www.nexusmods.com/skyrimspecialedition/mods/10?tab=files&file_id=100"},
					{Kind: source.CollectionItemModFile, ModID: "11", FileID: "110", Name: "Eleven"},
					{Kind: source.CollectionItemModFile, ModID: "12", FileID: "120", Name: "Twelve"},
				}}},
		tier:       tier,
		filesByMod: map[string][]string{"10": {"100"}, "11": {"110"}, "12": {"120"}},
	}
	for _, id := range []string{"10", "11", "12"} {
		downloads.AddMod("skyrimspecialedition", &domain.Mod{ID: id, SourceID: "nexusmods", Name: "Mod " + id, Version: "1", GameID: "skyrimspecialedition"})
		fileID := map[string]string{"10": "100", "11": "110", "12": "120"}[id]
		downloads.AddDownload(fileID, createTestZipBytes(t, map[string]string{"mod" + id + ".esp": "plugin " + id}))
	}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return &sourceImportFixture{svc: svc, game: game, src: src}
}

func (f *sourceImportFixture) plan(t *testing.T, req core.SourceCollectionImportRequest) *core.ImportPlan {
	t.Helper()
	if req.SourceID == "" {
		req.SourceID = "nexusmods"
	}
	if req.Ref == "" {
		req.Ref = "abcdef"
	}
	plan, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, req)
	require.NoError(t, err)
	return plan
}

func states(c *core.ConvertedCollection) map[string]core.CollectionItemState {
	out := map[string]core.CollectionItemState{}
	for _, it := range c.Items {
		out[it.Name] = it.State
	}
	return out
}

func TestPlanSourceCollectionImport_PlansTheConvertedCollection(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	plan := f.plan(t, core.SourceCollectionImportRequest{})

	c := plan.Collection
	require.NotNil(t, c.Source)
	assert.Equal(t, core.CollectionFromSource, c.Input)
	assert.False(t, c.HooksImportable)
	assert.Equal(t, "Three Mods", plan.Profile.Name)
	assert.Equal(t, "skyrim", plan.Profile.GameID)
	assert.Equal(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abcdef", Revision: 3,
		URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Author: "curator"}, c.Origin)
	assert.Equal(t, "premium", c.Source.AccountTier)
	assert.False(t, c.Source.ManualDownload)
	assert.Equal(t, map[string]core.CollectionItemState{
		"Ten": core.CollectionItemWillInstall, "Eleven": core.CollectionItemWillInstall, "Twelve": core.CollectionItemOptionalSkipped,
	}, states(c.Source))
	assert.Equal(t, map[core.CollectionItemState]int{core.CollectionItemWillInstall: 2, core.CollectionItemOptionalSkipped: 1}, c.Source.Counts)
	require.Len(t, plan.Profile.Mods, 2, "the optional item is off by default")
	assert.Len(t, plan.Missing, 2)

	plan = f.plan(t, core.SourceCollectionImportRequest{Optional: []string{"12:120"}})
	assert.Len(t, plan.Profile.Mods, 3, "opted in by id")
	assert.Equal(t, []string{"12:120"}, plan.Collection.Source.Optional)
}

func TestApplySourceCollectionImport_InstallsAllAndSetsLatestToAuto(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	plan := f.plan(t, core.SourceCollectionImportRequest{WithOptional: true})
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, result.Installed)

	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "11", "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, row.UpdatePolicy, "a latest item follows its newest file")
	row, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "10", "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy)

	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, "nexusmods:abcdef", saved.Collection.ID)
	assert.True(t, saved.Mods[0].Locked, "exact pins are locked")
	assert.False(t, saved.Mods[1].Locked, "latest is not")
}

func TestApplySourceCollectionImport_IsAllOrNothing(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	f.src.filesByMod["11"] = nil // its pinned file is gone
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.Error(t, err, "one item that cannot be acquired refuses the whole collection")
	_, err = config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	assert.Error(t, err, "nothing was written, the profile included")
	_, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "10", "skyrim", "Three Mods")
	assert.Error(t, err)
}

func TestSourceCollectionImport_FreeAccountPlansManualDownloads(t *testing.T) {
	f := newSourceImportFixture(t, source.TierFree)
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	c := plan.Collection.Source
	assert.True(t, c.ManualDownload)
	assert.Equal(t, "free", c.AccountTier)
	assert.Equal(t, core.CollectionItemManualDownload, states(c)["Ten"])
	assert.Equal(t, 2, c.Counts[core.CollectionItemManualDownload])
	for _, it := range c.Items {
		if it.Name == "Ten" {
			assert.Contains(t, it.URL, "file_id=100", "the file's page is linked")
		}
	}

	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err, "the profile is saved with every item pending")
	assert.Equal(t, 0, result.Installed)
	assert.Equal(t, 0, f.src.DownloadCount(), "nothing is downloaded")
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Len(t, saved.Mods, 2)
}

func TestSourceCollectionImport_UnknownTierSaysNothingUpFront(t *testing.T) {
	f := newSourceImportFixture(t, "")
	f.src.tierErr = domain.ErrAuthRequired
	c := f.plan(t, core.SourceCollectionImportRequest{}).Collection.Source
	assert.Empty(t, c.AccountTier)
	assert.False(t, c.ManualDownload)
}

func TestSourceCollectionImport_AlreadyInstalledItems(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	again := f.plan(t, core.SourceCollectionImportRequest{})
	assert.Equal(t, core.CollectionItemInstalled, states(again.Collection.Source)["Ten"])
}

// #607 C2b review, should-fix 3: the auto policy is written with the row,
// inside the import - for a mod borrowed from another profile too.
func TestApplySourceCollectionImport_AutoPolicyOnABorrowedRow(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	again := f.plan(t, core.SourceCollectionImportRequest{ProfileName: "again"})
	require.Len(t, again.AlreadyCached, 2, "both mods are borrowed from the first profile")
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, again, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "11", "skyrim", "again")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, row.UpdatePolicy)
	row, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "10", "skyrim", "again")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy)
}

// #607 C2b review, nit 6: a free account installs what needs no download
// - a mod another profile already has - and records the rest pending.
func TestSourceCollectionImport_FreeAccountInstallsWhatIsAlreadyCached(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game,
		f.plan(t, core.SourceCollectionImportRequest{Optional: nil}), core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	downloads := f.src.DownloadCount()

	f.src.tier = source.TierFree
	plan := f.plan(t, core.SourceCollectionImportRequest{ProfileName: "free", WithOptional: true})
	got := states(plan.Collection.Source)
	assert.Equal(t, core.CollectionItemWillInstall, got["Ten"], "cached elsewhere: no download needed")
	assert.Equal(t, core.CollectionItemManualDownload, got["Twelve"], "never downloaded: by hand")

	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Installed+result.Recorded, "both cached mods get this profile's rows (recorded: it is not the active profile)")
	assert.Equal(t, downloads, f.src.DownloadCount(), "nothing downloaded")
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "free")
	require.NoError(t, err)
	assert.Len(t, saved.Mods, 3, "the manual item is recorded, pending")
}

// #607 C2b review, should-fix 4: a free account's "latest" items cannot be
// set to auto before they are installed, so the note names them.
func TestSourceCollectionImport_FreeAccountNamesItsPendingLatestItems(t *testing.T) {
	f := newSourceImportFixture(t, source.TierFree)
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game,
		f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	var named bool
	for _, n := range result.Notes {
		if strings.Contains(n, "newest file: Eleven") && strings.Contains(n, "--auto") {
			named = true
		}
	}
	assert.True(t, named, "%v", result.Notes)
}

// #607 C2b review, nit 7: a plan asks the source for the tier afresh, so
// an account upgraded minutes ago is planned as it is now.
func TestPlanSourceCollectionImport_AsksForAFreshTier(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	f.plan(t, core.SourceCollectionImportRequest{})
	f.plan(t, core.SourceCollectionImportRequest{})
	assert.Equal(t, 2, f.src.refreshes)
}

// #607 C2b review, nit 8: an account planned as Premium that lapses before
// Apply gets ONE refusal saying to plan again, not one per mod.
func TestApplySourceCollectionImport_ALapsedAccountIsOneRefusal(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	f.src.urlErr = &source.AccountRestrictedError{Reason: "Premium only"}
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.ErrorIs(t, err, source.ErrAccountRestricted)
	assert.Contains(t, err.Error(), "plan the import again")
	_, err = config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	assert.Error(t, err, "nothing was written")
}
