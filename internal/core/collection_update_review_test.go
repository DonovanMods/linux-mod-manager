package core_test

// #613 review: removals that fail or are cancelled keep the profile and
// the installed mods in step, edits made between plan and apply survive,
// unversioned documents, game mismatches, the user's duplicates, optional
// toggles, the shared cache root, FOMOD round trips, and the plugin-list and
// override three-way merge.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionUpdate_AFailedRemovalKeepsTheMemberInstalledAndListed(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.importNexus(t)
	fail := createTestScript(t, t.TempDir(), "before_all.sh", "#!/bin/bash\nexit 1\n")
	seedHooks(t, f.svc, f.game, "coll", domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: fail}})
	f.bumpNexus(t)
	plan := f.planUpdate(t)

	result, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{Remove: []string{"nexusmods:11"}}, nil)
	require.NoError(t, err, "the update itself landed")
	assert.Empty(t, result.Removed)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "could not remove nexusmods:11")
	assert.Contains(t, result.Warnings[0], "stays in the profile, installed")

	eleven := f.profile(t).FindRef("nexusmods", "11")
	require.NotNil(t, eleven, "still listed: never a mod installed but missing from the profile")
	assert.False(t, eleven.FromCollection, "and the user's now")
	row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "coll")
	require.NoError(t, err)
	assert.True(t, row.Deployed)
}

func TestCollectionUpdate_ACancelledRemovalReportsNothingRemoved(t *testing.T) {
	f := newUpdateFixture(t)
	f.importNexus(t)
	f.bumpNexus(t)
	plan := f.planUpdate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := func(e core.Event) {
		if se, ok := e.(core.StepEvent); ok && se.Phase == core.ImportSaved {
			cancel()
		}
	}
	result, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{Remove: []string{"nexusmods:11"}}, sink)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
	assert.Empty(t, result.Removed, "nothing is reported removed")
	// The member is in the profile and installed, or in neither.
	_, rowErr := f.svc.GetInstalledMod(context.Background(), "nexusmods", "11", "skyrim", "coll")
	listed := f.profile(t).FindRef("nexusmods", "11") != nil
	assert.Equal(t, rowErr == nil, listed, "the profile and the installed rows agree")
}

func TestCollectionUpdate_KeepsEditsMadeBetweenPlanAndApply(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	f.serve(urlDocument(t, "coll-1", 2, versionTwo))
	plan := f.planUpdate(t)

	// A hook and a link method set after the plan.
	p := f.profile(t)
	p.LinkMethod, p.LinkMethodExplicit = domain.LinkHardlink, true
	p.Hooks.Install.AfterAll, p.HooksExplicit.Install.AfterAll = "/bin/true", true
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))

	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	saved := f.profile(t)
	assert.Equal(t, 2, saved.Collection.Version)
	assert.True(t, saved.LinkMethodExplicit)
	assert.Equal(t, domain.LinkHardlink, saved.LinkMethod)
	assert.Equal(t, "/bin/true", saved.Hooks.Install.AfterAll)
}

func TestCollectionUpdate_UnversionedDocuments(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	f.serve(urlDocument(t, "coll-1", 0, versionTwo))
	_, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "coll"})
	var mismatch *core.CollectionUpdateMismatchError
	require.ErrorAs(t, err, &mismatch, "a document with no version is never newer than version 1")
	assert.Equal(t, core.CollectionUpdateOlderVersion, mismatch.Code)

	p := f.profile(t)
	p.Collection.Version = 0
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))
	plan := f.planUpdate(t)
	assert.False(t, plan.UpToDate)
	assert.Contains(t, plan.Warnings[len(plan.Warnings)-1], "neither the profile nor the document names a collection version")
}

func TestCollectionUpdate_WarnsOnAGameMismatch(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	f.serve(urlDocument(t, "coll-1", 2, func(doc *domain.CollectionDocument) {
		versionTwo(doc)
		doc.Game = domain.CollectionGame{Name: "Another", Sources: map[string]string{"nexusmods": "fallout4"}}
	}))
	plan := f.planUpdate(t)
	assert.Contains(t, plan.Warnings, "this collection is for Another (nexusmods fallout4), but importing into skyrim as asked")
}

func TestCollectionUpdate_KeepsTheUsersDuplicates(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	p := f.profile(t)
	dup := domain.ModReference{SourceID: "nexusmods", ModID: "12", Version: "1"}
	p.Mods = append(p.Mods, dup, dup, domain.ModReference{SourceID: "nexusmods", ModID: "10", Version: "9"})
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))
	f.serve(urlDocument(t, "coll-1", 2, func(doc *domain.CollectionDocument) {
		doc.Items = doc.Items[:1] // mod 10 only, unchanged
	}))
	plan := f.planUpdate(t)
	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	var order []string
	for _, ref := range f.profile(t).Mods {
		order = append(order, ref.ModID+"@"+ref.Version)
	}
	assert.Equal(t, []string{"10@1", "11@1", "12@1", "12@1", "10@9"}, order, "every repeated reference is kept as written")
}

func TestCollectionUpdate_AnUntickedOptionalItemIsNotReAdded(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	plan, err := f.svc.PlanSourceCollectionImport(ctx, f.game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "abcdef", ProfileName: "coll", WithOptional: true})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	f.bumpNexus(t)

	kept, err := f.svc.PlanCollectionUpdate(ctx, f.game, core.CollectionUpdateRequest{Profile: "coll"})
	require.NoError(t, err)
	assert.NotContains(t, keys(kept.Removed), "nexusmods:12", "the optional item the profile has stays in")
	assert.Contains(t, kept.Source.Optional, "12:120")

	unticked, err := f.svc.PlanCollectionUpdate(ctx, f.game, core.CollectionUpdateRequest{Profile: "coll", OptionalChosen: true, Optional: []string{}})
	require.NoError(t, err)
	assert.Contains(t, keys(unticked.Removed), "nexusmods:12", "unticked, it is not re-added")
}

func TestCollectionUpdate_ARevisionAnotherGameSharesIsKept(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	shared := t.TempDir()
	f.game.CachePath = shared
	require.NoError(t, f.svc.SaveGame(ctx, f.game))
	f.importNexus(t)
	other := &domain.Game{ID: "skyrim2", Name: "Skyrim copy", ModPath: t.TempDir(), CachePath: shared,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, f.svc.SaveGame(ctx, other))
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "theirs", GameID: "skyrim2",
		Collection: &domain.CollectionInfo{ID: "nexusmods:abcdef", Version: 3, Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abcdef", Revision: 3}}}))
	recipe := filepath.Join(shared, "collections", "nexusmods-abcdef", "3")
	require.DirExists(t, recipe)

	f.bumpNexus(t)
	_, err := f.svc.ApplyCollectionUpdate(ctx, f.game, f.planUpdate(t), core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	_, err = os.Stat(recipe)
	assert.NoError(t, err, "another game's profile still names revision 3 in the shared cache")
}

// fomodUpdateFixture is updateFixture whose mod 13 (added by version 2) is
// a FOMOD installer the document records no answers for.
func TestCollectionUpdate_AnsweringAnInstallersQuestions(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.downloads.AddDownload("130", fomodZip(t, "4K", "v1"))
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	f.serve(urlDocument(t, "coll-1", 2, versionTwo))
	plan := f.planUpdate(t)

	_, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "the installer asks: %v", err)
	assert.Equal(t, 1, f.profile(t).Collection.Version, "nothing written")

	result, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{
		Fomod: map[string]domain.FomodChoices{"nexusmods:13": answer("2K")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Added)
	saved := f.profile(t)
	assert.Equal(t, 2, saved.Collection.Version)
	assert.Equal(t, "2K", saved.FindRef("nexusmods", "13").Fomod.Steps[0].Groups[0].Choices[0].Name, "the answers are recorded")
}

// pluginDoc is a version of the collection carrying a plugin list and
// overrides.
func pluginDoc(t *testing.T, version int, order []domain.PluginEntry, overrides map[string]string) []byte {
	return urlDocument(t, "coll-1", version, func(doc *domain.CollectionDocument) {
		doc.Plugins = domain.ProfilePlugins{Order: order}
		doc.Overrides = overrides
	})
}

func TestCollectionUpdate_PluginListAndOverridesMergeThreeWays(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	on := func(name string) domain.PluginEntry { return domain.PluginEntry{Name: name, Enabled: true} }
	off := func(name string) domain.PluginEntry { return domain.PluginEntry{Name: name} }
	f.importURL(t, pluginDoc(t, 1, []domain.PluginEntry{on("A.esp"), on("B.esp"), on("C.esp")},
		map[string]string{"a.ini": "1", "b.ini": "1", "c.ini": "1"}))
	require.NotNil(t, f.profile(t).CollectionBaseline, "the import records what the collection applied")

	// The user switches B off, adds D after A, and edits a.ini.
	p := f.profile(t)
	p.Plugins.Order = []domain.PluginEntry{on("A.esp"), on("D.esp"), off("B.esp"), on("C.esp")}
	p.Overrides["a.ini"] = []byte("mine")
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))

	// Version 2 reorders, switches C off, adds E; rewrites a.ini (a
	// conflict) and b.ini, drops c.ini and adds d.ini.
	f.serve(pluginDoc(t, 2, []domain.PluginEntry{off("C.esp"), on("A.esp"), on("B.esp"), on("E.esp")},
		map[string]string{"a.ini": "2", "b.ini": "2", "d.ini": "2"}))
	plan := f.planUpdate(t)
	require.NotNil(t, plan.Plugins)
	assert.Equal(t, []string{"E.esp"}, plan.Plugins.Added)
	assert.Empty(t, plan.Plugins.Conflicts, "B: only the user changed it; C: only the collection")
	assert.Empty(t, plan.Plugins.Moved, "only the collection reordered")
	assert.Equal(t, []string{"d.ini"}, plan.OverridesAdded)
	assert.Equal(t, []string{"b.ini"}, plan.OverridesChanged)
	assert.Equal(t, []string{"c.ini"}, plan.OverridesRemoved)
	assert.Equal(t, []string{"a.ini"}, plan.OverrideConflicts)
	assert.Contains(t, plan.ConflictKeys(), "override:a.ini")

	_, err := f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	saved := f.profile(t)
	assert.Equal(t, []domain.PluginEntry{off("C.esp"), on("A.esp"), on("D.esp"), off("B.esp"), on("E.esp")}, saved.Plugins.Order,
		"the collection's order, the user's D kept after A, the user's B off, the collection's C off")
	assert.Equal(t, "mine", string(saved.Overrides["a.ini"]), "the user's side of a conflict wins by default")
	assert.Equal(t, "2", string(saved.Overrides["b.ini"]))
	assert.Equal(t, "2", string(saved.Overrides["d.ini"]))
	assert.NotContains(t, saved.Overrides, "c.ini")

	// Version 3: both reorder - the user moved E first, the collection
	// moves A last - and both disagree about a.ini again.
	saved.Plugins.Order = []domain.PluginEntry{on("E.esp"), off("C.esp"), on("A.esp"), on("D.esp"), off("B.esp")}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), saved))
	f.serve(pluginDoc(t, 3, []domain.PluginEntry{off("C.esp"), on("B.esp"), on("E.esp"), on("A.esp")},
		map[string]string{"a.ini": "3", "b.ini": "2", "d.ini": "2"}))
	plan = f.planUpdate(t)
	require.NotNil(t, plan.Plugins)
	assert.NotEmpty(t, plan.Plugins.Moved, "the plan names the plugins both moved")
	assert.Contains(t, plan.ConflictKeys(), "plugins:order")

	_, err = f.svc.ApplyCollectionUpdate(ctx, f.game, plan, core.CollectionUpdateOptions{Override: []string{"plugins:order", "override:a.ini"}}, nil)
	require.NoError(t, err)
	saved = f.profile(t)
	var names []string
	for _, e := range saved.Plugins.Order {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"C.esp", "B.esp", "E.esp", "A.esp", "D.esp"}, names, "overridden: the collection's order, D still after A")
	assert.Equal(t, "3", string(saved.Overrides["a.ini"]), "overridden: the collection's content")
}

// #613 re-review: a collection with no plugins and no overrides records an
// empty baseline, so an override the user adds later is theirs and
// survives the update.
func TestCollectionUpdate_AnEmptyBaselineKeepsTheUsersOverride(t *testing.T) {
	f := newUpdateFixture(t)
	f.importURL(t, urlDocument(t, "coll-1", 1, nil))
	require.NotNil(t, f.profile(t).CollectionBaseline, "an empty baseline is recorded")
	p := f.profile(t)
	p.Overrides = map[string][]byte{"mine.ini": []byte("user")}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))
	f.serve(urlDocument(t, "coll-1", 2, versionTwo))
	plan := f.planUpdate(t)
	assert.Empty(t, plan.OverridesRemoved)
	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "user", string(f.profile(t).Overrides["mine.ini"]), "the user's own override is kept")
}

// #613 re-review: with no baseline at all (a snapshot restore, an older
// lmm rewriting the file), nothing is the collection's to remove: the
// user's additions stay, and a difference on a shared item is a conflict
// the user wins by default.
func TestCollectionUpdate_NoBaselineKeepsTheUsersAdditions(t *testing.T) {
	f := newUpdateFixture(t)
	on := func(name string) domain.PluginEntry { return domain.PluginEntry{Name: name, Enabled: true} }
	off := func(name string) domain.PluginEntry { return domain.PluginEntry{Name: name} }
	f.importURL(t, pluginDoc(t, 1, []domain.PluginEntry{on("A.esp"), on("B.esp")}, map[string]string{"a.ini": "1"}))
	p := f.profile(t)
	p.Plugins.Order = []domain.PluginEntry{on("A.esp"), off("B.esp"), on("Mine.esp")}
	p.Overrides["mine.ini"] = []byte("user")
	p.CollectionBaseline = nil
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))
	require.Nil(t, f.profile(t).CollectionBaseline)

	f.serve(pluginDoc(t, 2, []domain.PluginEntry{on("A.esp"), on("B.esp"), on("C.esp")}, map[string]string{"a.ini": "2"}))
	plan := f.planUpdate(t)
	require.NotNil(t, plan.Plugins)
	assert.Equal(t, []string{"B.esp"}, plan.Plugins.Conflicts, "a shared difference is a conflict")
	assert.Equal(t, []string{"a.ini"}, plan.OverrideConflicts)
	assert.Empty(t, plan.OverridesRemoved)
	assert.Empty(t, plan.Plugins.Removed)

	_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	saved := f.profile(t)
	var names []string
	for _, e := range saved.Plugins.Order {
		names = append(names, e.Name)
	}
	assert.Contains(t, names, "Mine.esp", "the user's plugin is kept")
	assert.Contains(t, names, "C.esp", "the collection's new one is added")
	assert.Equal(t, "user", string(saved.Overrides["mine.ini"]), "the user's override is kept")
	assert.Equal(t, "1", string(saved.Overrides["a.ini"]), "the user wins the conflict by default")
	for _, e := range saved.Plugins.Order {
		if e.Name == "B.esp" {
			assert.False(t, e.Enabled, "B stays as the user left it")
		}
	}
	require.NotNil(t, saved.CollectionBaseline, "the update records a baseline again")
}

// #613 re-review (#609 pin): a URL update's new item replays its recorded
// installer choices only against the archive whose md5 the document
// records; another archive asks instead.
func TestCollectionUpdate_CarriesTheMD5Pin(t *testing.T) {
	for name, wrong := range map[string]bool{"wrong md5": true, "right md5": false} {
		t.Run(name, func(t *testing.T) {
			f := newUpdateFixture(t)
			archive := fomodZip(t, "4K", "v1")
			f.downloads.AddDownload("130", archive)
			sum := md5.Sum(archive)
			recorded := hex.EncodeToString(sum[:])
			if wrong {
				recorded = strings.Repeat("0", 32)
			}
			f.importURL(t, urlDocument(t, "coll-1", 1, nil))
			f.serve(urlDocument(t, "coll-1", 2, func(doc *domain.CollectionDocument) {
				versionTwo(doc)
				doc.Requires = append(doc.Requires, "fomod")
				doc.Items[1].Ref.Fomod = answer("4K")
				doc.Items[1].Files = []domain.CollectionFileRef{{ID: "130", MD5: recorded}}
			}))
			plan := f.planUpdate(t)
			_, err := f.svc.ApplyCollectionUpdate(context.Background(), f.game, plan, core.CollectionUpdateOptions{}, nil)
			if !wrong {
				require.NoError(t, err)
				assert.Equal(t, 2, f.profile(t).Collection.Version)
				return
			}
			var q *core.FomodChoicesRequiredError
			require.True(t, errors.As(err, &q), "got %v", err)
			require.Len(t, q.Mods, 1)
			assert.Equal(t, core.FomodReasonArchiveMismatch, q.Mods[0].Reason)
			assert.Equal(t, 1, f.profile(t).Collection.Version, "nothing written")
		})
	}
}
