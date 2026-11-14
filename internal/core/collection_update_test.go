package core_test

// #613 C8: updating a collection profile from its origin - a NexusMods
// collection's newer revision through the source double of
// collection_source_import_test.go, and a re-fetched https address through
// an httptest TLS server. Nothing reaches the network.

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revisionSource is collectionSource with a version per file, so a newer
// revision can pin a newer file.
type revisionSource struct {
	*collectionSource
	versions map[string]string // file id -> version
}

func (s *revisionSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	m, err := s.collectionSource.ParseManifest(root)
	for i := range m.Mods {
		if v := s.versions[m.Mods[i].FileID]; v != "" {
			m.Mods[i].Version = v
		}
	}
	return m, err
}

func (s *revisionSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	var out []domain.DownloadableFile
	for _, id := range s.filesByMod[mod.ID] {
		v := s.versions[id]
		if v == "" {
			v = "1"
		}
		out = append(out, domain.DownloadableFile{ID: id, Name: "File " + id, FileName: mod.ID + "-" + id + ".zip", Version: v})
	}
	return out, nil
}

type updateFixture struct {
	svc       *core.Service
	game      *domain.Game
	src       *revisionSource
	downloads *mockSourceWithDownloads
	archive   atomic.Pointer[[]byte]
	document  atomic.Pointer[[]byte]
	server    *httptest.Server
}

// newUpdateFixture is a game with a NexusMods double whose collection
// "abcdef" is at revision 3 (mods 10, 11 and the optional 12), served with
// mod files 100, 101 (a newer file of mod 10), 110, 120 and 130 (mod 13,
// which revision 4 adds). One TLS server serves the recipe archive and an
// lmm collection document, both swappable.
func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	f := &updateFixture{}
	archive := recipeArchive(t, threeModRecipe)
	f.archive.Store(&archive)
	empty := []byte{}
	f.document.Store(&empty)
	mux := http.NewServeMux()
	mux.HandleFunc("/recipe.7z", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(*f.archive.Load()) })
	mux.HandleFunc("/c.yaml", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(*f.document.Load()) })
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: f.server.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	f.svc = svc

	f.downloads = newMockSourceWithDownloads("nexusmods")
	t.Cleanup(f.downloads.Close)
	f.src = &revisionSource{
		collectionSource: &collectionSource{
			mockSourceWithDownloads: f.downloads,
			recipe: &recipeSource{mockSource: f.downloads.mockSource, archiveURL: f.server.URL + "/recipe.7z",
				collection: source.Collection{ID: "abcdef", Name: "Three Mods", Revision: 3, Author: "curator",
					URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Handle: "/h",
					Items: []source.CollectionItem{
						{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten"},
						{Kind: source.CollectionItemModFile, ModID: "11", FileID: "110", Name: "Eleven"},
						{Kind: source.CollectionItemModFile, ModID: "12", FileID: "120", Name: "Twelve"},
					}}},
			tier:       source.TierPremium,
			filesByMod: map[string][]string{"10": {"100", "101"}, "11": {"110"}, "12": {"120"}, "13": {"130"}},
		},
		versions: map[string]string{"100": "1", "101": "2", "110": "1", "120": "1", "130": "1"},
	}
	for _, id := range []string{"10", "11", "12", "13"} {
		f.downloads.AddMod("skyrimspecialedition", &domain.Mod{ID: id, SourceID: "nexusmods", Name: "Mod " + id, Version: "1", GameID: "skyrimspecialedition"})
	}
	for _, file := range []string{"100", "101", "110", "120", "130"} {
		f.downloads.AddDownload(file, createTestZipBytes(t, map[string]string{"file" + file + ".txt": "content " + file}))
	}
	svc.RegisterSource(f.src)
	f.game = &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), f.game))
	return f
}

// importNexus imports the collection at its current revision, installing it.
func (f *updateFixture) importNexus(t *testing.T) {
	t.Helper()
	plan, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "abcdef", ProfileName: "coll",
	})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
}

// bumpNexus publishes revision 4: mod 10 moves to file 101 (version 2),
// mod 11 is dropped, mod 13 is added.
const revisionFourRecipe = `{"name": "Three Mods", "mods": [
	{"name": "Ten", "modId": "10", "fileId": "101"},
	{"name": "Twelve", "modId": "12", "fileId": "120"},
	{"name": "Thirteen", "modId": "13", "fileId": "130"}]}`

func (f *updateFixture) bumpNexus(t *testing.T) {
	t.Helper()
	archive := recipeArchive(t, revisionFourRecipe)
	f.archive.Store(&archive)
	c := &f.src.recipe.collection
	c.Revision = 4
	c.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: "101", Name: "Ten"},
		{Kind: source.CollectionItemModFile, ModID: "12", FileID: "120", Name: "Twelve"},
		{Kind: source.CollectionItemModFile, ModID: "13", FileID: "130", Name: "Thirteen"},
	}
}

func (f *updateFixture) planUpdate(t *testing.T) *core.CollectionUpdatePlan {
	t.Helper()
	plan, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "coll"})
	require.NoError(t, err)
	return plan
}

func (f *updateFixture) profile(t *testing.T) *domain.Profile {
	t.Helper()
	p, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "coll")
	require.NoError(t, err)
	return p
}

func keys(items []core.CollectionUpdateItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Key())
	}
	return out
}

func TestCollectionImport_MarksItsEntries(t *testing.T) {
	f := newUpdateFixture(t)
	f.importNexus(t)
	for _, ref := range f.profile(t).Mods {
		assert.True(t, ref.FromCollection, "%s is the collection's", ref.ModID)
	}
	// A lock the user sets makes the entry theirs.
	_, err := f.svc.SetModLock(context.Background(), "nexusmods", "11", "skyrim", "coll", "")
	require.NoError(t, err)
	assert.False(t, f.profile(t).FindRef("nexusmods", "11").FromCollection)
	_, err = f.svc.ClearModLock(context.Background(), "nexusmods", "10", "skyrim", "coll")
	require.NoError(t, err)
	assert.False(t, f.profile(t).FindRef("nexusmods", "10").FromCollection, "so does unlocking one")
}

func TestCollectionUpdate_NexusRevisionBump(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importNexus(t)
	oldRecipe := filepath.Join(f.svc.CacheDir(), "skyrim", "collections", "nexusmods-abcdef", "3")
	require.DirExists(t, oldRecipe)
	f.bumpNexus(t)

	plan := f.planUpdate(t)
	assert.Equal(t, core.CollectionFromSource, plan.Origin)
	assert.Equal(t, 3, plan.Version)
	assert.Equal(t, 4, plan.NewVersion)
	assert.False(t, plan.UpToDate)
	assert.Equal(t, []string{"nexusmods:13"}, keys(plan.Added))
	require.Equal(t, []string{"nexusmods:10"}, keys(plan.Changed))
	assert.Equal(t, []string{"version", "files"}, plan.Changed[0].Changes)
	assert.Equal(t, "1", plan.Changed[0].Version)
	assert.Equal(t, "2", plan.Changed[0].NewVersion)
	assert.Equal(t, []string{"nexusmods:11"}, keys(plan.Removed), "the dropped item is offered for removal")
	assert.Empty(t, plan.Conflicts)
	require.NotNil(t, plan.Source)
	assert.Equal(t, 4, plan.Source.Revision)

	result, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Added)
	assert.Equal(t, 1, result.Changed)
	assert.Equal(t, 1, result.Kept, "the obsolete member is kept unless asked")
	assert.Empty(t, result.Removed)

	saved := f.profile(t)
	assert.Equal(t, 4, saved.Collection.Version)
	assert.Equal(t, 4, saved.Collection.Origin.Revision)
	ten := saved.FindRef("nexusmods", "10")
	require.NotNil(t, ten)
	assert.Equal(t, "2", ten.Version)
	assert.Equal(t, []string{"101"}, ten.FileIDs)
	assert.True(t, ten.Locked, "the collection's exact pin stays locked")
	assert.True(t, ten.FromCollection)
	eleven := saved.FindRef("nexusmods", "11")
	require.NotNil(t, eleven, "a dropped item is never removed silently")
	assert.False(t, eleven.FromCollection, "kept, it is the user's now")
	thirteen := saved.FindRef("nexusmods", "13")
	require.NotNil(t, thirteen)
	assert.True(t, thirteen.FromCollection)

	row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "10", "skyrim", "coll")
	require.NoError(t, err)
	assert.Equal(t, "2", row.Version, "the locked pin moved through the sync-only bypass")
	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "13", "skyrim", "coll")
	require.NoError(t, err, "the added item is installed")
	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "coll")
	require.NoError(t, err, "the kept item stays installed")

	_, err = os.Stat(oldRecipe)
	assert.ErrorIs(t, err, os.ErrNotExist, "the old revision's recipe is dropped once no profile names it")

	// The same revision again is nothing to do.
	again := f.planUpdate(t)
	assert.True(t, again.UpToDate)
	assert.True(t, again.NoChanges)
	result, err = f.svc.ApplyCollectionUpdate(ctx, f.game, again, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, result.UpToDate)
}

func TestCollectionUpdate_RemovesTheObsoleteMembersAsked(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importNexus(t)
	f.bumpNexus(t)
	plan := f.planUpdate(t)

	_, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{Remove: []string{"nexusmods:10"}}, nil)
	require.ErrorIs(t, err, core.ErrBadUpdateChoice, "only an obsolete member can be removed")

	result, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{Remove: []string{"nexusmods:11"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"nexusmods:11"}, result.Removed)
	assert.Zero(t, result.Kept)
	assert.Nil(t, f.profile(t).FindRef("nexusmods", "11"))
	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "coll")
	assert.Error(t, err, "uninstalled")
}

func TestCollectionUpdate_UserLockWinsUnlessOverridden(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "kept", true: "overridden"}[override], func(t *testing.T) {
			f := newUpdateFixture(t)
			ctx := context.Background()
			f.importNexus(t)
			_, err := f.svc.SetModLock(ctx, "nexusmods", "10", "skyrim", "coll", "")
			require.NoError(t, err)
			f.bumpNexus(t)

			plan := f.planUpdate(t)
			assert.Empty(t, plan.Changed)
			require.Equal(t, []string{"nexusmods:10"}, keys(plan.Conflicts))
			assert.True(t, plan.Conflicts[0].Locked)
			assert.Equal(t, []string{"version", "files"}, plan.Conflicts[0].Changes)

			opts := core.CollectionUpdateOptions{}
			if override {
				opts.Override = []string{"nexusmods:10"}
			}
			result, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, opts, nil)
			require.NoError(t, err)
			ten := f.profile(t).FindRef("nexusmods", "10")
			row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "10", "skyrim", "coll")
			require.NoError(t, err)
			if override {
				assert.Equal(t, 1, result.Overridden)
				assert.Equal(t, "2", ten.Version)
				assert.True(t, ten.FromCollection, "taken the collection's way, it follows the collection again")
				assert.Equal(t, "2", row.Version)
				return
			}
			assert.Equal(t, 1, result.Conflicts)
			assert.Equal(t, "1", ten.Version, "the user's lock wins")
			assert.True(t, ten.Locked)
			assert.False(t, ten.FromCollection)
			assert.Equal(t, "1", row.Version)
		})
	}
}

func TestCollectionUpdate_RefusesAStalePlan(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importNexus(t)
	f.bumpNexus(t)
	plan := f.planUpdate(t)
	_, err := f.svc.SetModLock(ctx, "nexusmods", "10", "skyrim", "coll", "")
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
	assert.Equal(t, 3, f.profile(t).Collection.Version, "nothing written")
}

func TestCollectionUpdate_ProfileWithoutOrigin(t *testing.T) {
	f := newUpdateFixture(t)
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "coll", GameID: "skyrim"}))
	_, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "coll"})
	require.ErrorIs(t, err, core.ErrCollectionNotUpdatable)
	assert.True(t, core.IsCollectionNotUpdatable(err))
}

// urlDocument is an lmm collection document of the given id and version
// for the fixture's game, its mods pinned by file.
func urlDocument(t *testing.T, id string, version int, mutate func(doc *domain.CollectionDocument)) []byte {
	t.Helper()
	doc := &domain.CollectionDocument{
		FormatVersion: domain.CollectionFormatVersion, Name: "coll",
		Game: domain.CollectionGame{Name: "Skyrim", Sources: map[string]string{"nexusmods": "skyrimspecialedition"}},
		Info: domain.CollectionInfo{ID: id, Version: version, Title: "Shared"},
		Items: []domain.CollectionItem{
			{Name: "Ten", Ref: domain.ModReference{SourceID: "nexusmods", ModID: "10", Version: "1", FileIDs: []string{"100"}}},
			{Name: "Eleven", Ref: domain.ModReference{SourceID: "nexusmods", ModID: "11", Version: "1", FileIDs: []string{"110"}}},
		},
	}
	if mutate != nil {
		mutate(doc)
	}
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	return data
}

// importURL imports the fixture's document from its address.
func (f *updateFixture) importURL(t *testing.T, data []byte) {
	t.Helper()
	f.document.Store(&data)
	plan, err := f.svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: f.server.URL + "/c.yaml?token=s3cret"})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
}

func (f *updateFixture) serve(data []byte) { f.document.Store(&data) }

// versionTwo moves mod 10 to file 101 and replaces mod 11 with mod 13.
func versionTwo(doc *domain.CollectionDocument) {
	doc.Items = []domain.CollectionItem{
		{Name: "Ten", Ref: domain.ModReference{SourceID: "nexusmods", ModID: "10", Version: "2", FileIDs: []string{"101"}}},
		{Name: "Thirteen", Ref: domain.ModReference{SourceID: "nexusmods", ModID: "13", Version: "1", FileIDs: []string{"130"}}},
	}
}

func TestCollectionUpdate_URLVersionBump(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	// The user adds a mod of their own, between the collection's two.
	p := f.profile(t)
	p.Mods = []domain.ModReference{p.Mods[0], {SourceID: "nexusmods", ModID: "12", Version: "1"}, p.Mods[1]}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))

	f.serve(urlDocument(t, "coll-1", 2, versionTwo))
	plan := f.planUpdate(t)
	assert.Equal(t, core.CollectionFromURL, plan.Origin)
	assert.Equal(t, f.server.URL+"/c.yaml?…", plan.URL, "shown without its query")
	assert.Equal(t, 1, plan.Version)
	assert.Equal(t, 2, plan.NewVersion)
	assert.Equal(t, []string{"nexusmods:13"}, keys(plan.Added))
	assert.Equal(t, []string{"nexusmods:10"}, keys(plan.Changed))
	assert.Equal(t, []string{"nexusmods:11"}, keys(plan.Removed))
	assert.Empty(t, plan.Conflicts, "the user's own mod is not the collection's to change")

	_, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{RemoveObsolete: true}, nil)
	require.NoError(t, err)
	saved := f.profile(t)
	assert.Equal(t, 2, saved.Collection.Version)
	assert.Equal(t, f.server.URL+"/c.yaml?token=s3cret", saved.Collection.Origin.ImportURL, "the subscription is kept")
	var order []string
	for _, ref := range saved.Mods {
		order = append(order, ref.ModID)
	}
	assert.Equal(t, []string{"10", "12", "13"}, order, "the user's mod keeps its place after the entry it followed")
	assert.False(t, saved.FindRef("nexusmods", "12").FromCollection)
	assert.Equal(t, "2", saved.FindRef("nexusmods", "10").Version)
}

func TestCollectionUpdate_URLSameVersionIsNothingToDo(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	f.serve(urlDocument(t, "coll-1", 1, versionTwo))
	plan := f.planUpdate(t)
	assert.True(t, plan.UpToDate)
	assert.Empty(t, plan.Added)
	result, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, result.UpToDate)
	assert.Equal(t, "1", f.profile(t).FindRef("nexusmods", "10").Version, "nothing changed")
}

func TestCollectionUpdate_URLRefusesAnotherCollection(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))

	f.serve(urlDocument(t, "coll-2", 2, versionTwo))
	_, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "coll"})
	var mismatch *core.CollectionUpdateMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, core.CollectionUpdateDifferentCollection, mismatch.Code)
	assert.Equal(t, "coll-2", mismatch.FetchedID)

	// An older version is refused too.
	f.serve(urlDocument(t, "coll-1", 1, nil))
	p := f.profile(t)
	p.Collection.Version = 5
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))
	_, err = f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "coll"})
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, core.CollectionUpdateOlderVersion, mismatch.Code)
}

func TestCollectionUpdate_GatesTheNewDocument(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	f.serve(urlDocument(t, "coll-1", 2, func(doc *domain.CollectionDocument) {
		versionTwo(doc)
		doc.Requires = []string{"patches"}
		doc.Hooks = domain.GameHooks{Install: domain.HookConfig{AfterAll: "/usr/bin/false"}}
		doc.HooksExplicit = domain.GameHooksExplicit{Install: domain.HookExplicitFlags{AfterAll: true}}
		doc.Overrides = map[string]string{"doorstop_config.ini": "[General]\nenabled=true\n", "Config/Game.ini": "[A]\nb=1\n"}
	}))

	plan := f.planUpdate(t)
	require.Len(t, plan.Unsupported, 1)
	assert.Equal(t, "patches", plan.Unsupported[0].Feature)
	require.Len(t, plan.Hooks, 1, "the new document's hooks are listed")
	assert.Equal(t, []string{"Config/Game.ini", "doorstop_config.ini"}, plan.OverridesAdded)
	require.Len(t, plan.RestrictedOverrides, 1)
	assert.Equal(t, "doorstop_config.ini", plan.RestrictedOverrides[0].Path, "a loader configuration is held back")

	_, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.True(t, core.IsCollectionUnsupported(err), "the feature gate refuses: %v", err)
	assert.Equal(t, 1, f.profile(t).Collection.Version, "nothing written")

	// Without the unsupported feature, the hooks and the loader config are
	// still held back.
	f.serve(urlDocument(t, "coll-1", 2, func(doc *domain.CollectionDocument) {
		versionTwo(doc)
		doc.Hooks = domain.GameHooks{Install: domain.HookConfig{AfterAll: "/usr/bin/false"}}
		doc.HooksExplicit = domain.GameHooksExplicit{Install: domain.HookExplicitFlags{AfterAll: true}}
		doc.Overrides = map[string]string{"doorstop_config.ini": "[General]\nenabled=true\n", "Config/Game.ini": "[A]\nb=1\n"}
	}))
	plan = f.planUpdate(t)
	result, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Import)
	require.Len(t, result.Import.OverridesSkipped, 1)
	saved := f.profile(t)
	assert.Equal(t, domain.GameHooksExplicit{}, saved.HooksExplicit, "a collection's hooks are never applied by an update")
	assert.Contains(t, saved.Overrides, "Config/Game.ini")
	assert.NotContains(t, saved.Overrides, "doorstop_config.ini")
}

// TestCollectionUpdate_WorkshopEntriesShowNoContentID: a Steam Workshop
// item's version is a content id, which no surface may print (#458): its
// entry carries the display stamp.
func TestCollectionUpdate_WorkshopEntriesShowNoContentID(t *testing.T) {
	f := newUpdateFixture(t)
	f.svc.RegisterSource(newWorkshopTestSource())
	f.game.SourceIDs["steamworkshop"] = "489830"
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	item := func(version string) func(doc *domain.CollectionDocument) {
		return func(doc *domain.CollectionDocument) {
			doc.Items = []domain.CollectionItem{{Name: "Ship", Ref: domain.ModReference{SourceID: "steamworkshop", ModID: "9001", Version: version, Disabled: true}}}
		}
	}
	f.serve(urlDocument(t, "coll-1", 1, item("1111111111111111111")))
	plan, err := f.svc.PlanCollectionImportURL(context.Background(), core.CollectionURLImportRequest{URL: f.server.URL + "/c.yaml"})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)

	f.serve(urlDocument(t, "coll-1", 2, item("2222222222222222222")))
	update := f.planUpdate(t)
	require.Len(t, update.Changed, 1)
	assert.Equal(t, core.NoRevisionDate, update.Changed[0].DisplayVersion)
	assert.Equal(t, core.NoRevisionDate, update.Changed[0].NewDisplayVersion)
}

// A duplicate of a NexusMods collection profile is a new collection (no
// id, no version) that keeps its origin's revision: it updates from it,
// and is up to date at the same revision.
func TestCollectionUpdate_DuplicateOfASourceCollection(t *testing.T) {
	f := newUpdateFixture(t)
	f.importNexus(t)
	_, err := f.svc.DuplicateProfile(context.Background(), "skyrim", "coll", "copy")
	require.NoError(t, err)
	plan, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "copy"})
	require.NoError(t, err)
	assert.True(t, plan.UpToDate)
	f.bumpNexus(t)
	plan, err = f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "copy"})
	require.NoError(t, err)
	assert.Equal(t, 3, plan.Version)
	assert.Equal(t, 4, plan.NewVersion)
	assert.Equal(t, []string{"nexusmods:10"}, keys(plan.Changed), "the duplicate keeps the collection's markers")
}

// A pin whose files change at the same version is recorded, and the plan
// says it applies when the mod is next reinstalled (#639 tracks
// reinstalling it at once).
func TestCollectionUpdate_SameVersionFilesCarryANote(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	f.serve(urlDocument(t, "coll-1", 2, func(doc *domain.CollectionDocument) {
		doc.Items[0].Ref.FileIDs = []string{"100", "101"}
	}))
	plan := f.planUpdate(t)
	require.Len(t, plan.Changed, 1)
	assert.Equal(t, []string{"files"}, plan.Changed[0].Changes)
	assert.Contains(t, plan.Changed[0].Note, "next reinstalled")
}
