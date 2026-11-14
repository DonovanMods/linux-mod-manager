package core

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The converter (#607 C2a + #609 C4): a source's collection list and recipe
// in, lmm's version-1 collection document out, with the §3 gate's verdict.
// Pure: these tests build manifests by hand, one behaviour each; the real
// recorded recipes run through it end to end in internal/app.

func nexusMod(name, modID, fileID, md5 string) source.ManifestMod {
	return source.ManifestMod{Name: name, Version: "1.0", SourceType: "nexus", ModID: modID, FileID: fileID,
		MD5: md5, SizeBytes: 100, LogicalFilename: name, UpdatePolicy: "exact"}
}

func convertInputFor(m *source.CollectionManifest) convertInput {
	return convertInput{
		sourceID: "nexusmods",
		gameID:   "skyrim",
		game:     domain.CollectionGame{Name: "Skyrim", Sources: map[string]string{"nexusmods": "skyrimspecialedition"}},
		list: source.Collection{ID: "abcdef", Name: "List name", Revision: 7, Author: "curator",
			URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef"},
		manifest: m,
	}
}

func refOrder(doc *domain.CollectionDocument) []string {
	out := make([]string, len(doc.Items))
	for i, it := range doc.Items {
		out[i] = it.Ref.ModID
	}
	return out
}

func featureNames(fs []UnsupportedCollectionFeature) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Feature
	}
	return out
}

func itemByName(t *testing.T, c *ConvertedCollection, name string) ConvertedCollectionItem {
	t.Helper()
	for _, it := range c.Items {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("no item %q", name)
	return ConvertedCollectionItem{}
}

func TestConvert_IdentityOriginAndPins(t *testing.T) {
	exact := nexusMod("Exact", "1", "11", "aa")
	prefer := nexusMod("Prefer", "2", "22", "bb")
	prefer.UpdatePolicy = "prefer"
	latest := nexusMod("Latest", "3", "33", "cc")
	latest.UpdatePolicy = "latest"
	m := &source.CollectionManifest{Name: "My Collection", Author: "Curator", Description: "d", InstallInstructions: "i",
		GameSourceID: "skyrimspecialedition", GameVersions: []string{"1.6"},
		Mods: []source.ManifestMod{exact, prefer, latest}}

	c := mustConvert(t, convertInputFor(m))
	doc := c.Document
	require.NotNil(t, doc)
	assert.Equal(t, domain.CollectionFormatVersion, doc.FormatVersion)
	assert.Equal(t, "My Collection", doc.Name)
	assert.Equal(t, "skyrim", doc.GameID)
	assert.Equal(t, "nexusmods:abcdef", doc.Info.ID)
	assert.Equal(t, 7, doc.Info.Version)
	assert.Equal(t, "My Collection", doc.Info.Title)
	assert.Equal(t, "Curator", doc.Info.Author)
	assert.Equal(t, "i", doc.Info.InstallInstructions)
	assert.Equal(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abcdef", Revision: 7,
		URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Author: "Curator"}, doc.Info.Origin)
	assert.Equal(t, []string{"1.6"}, doc.Game.Versions)
	assert.Equal(t, "skyrimspecialedition", doc.Game.Sources["nexusmods"])

	require.Len(t, doc.Items, 3)
	assert.Equal(t, domain.ModReference{SourceID: "nexusmods", ModID: "1", Version: "1.0", FileIDs: []string{"11"}, Locked: true}, doc.Items[0].Ref,
		"exact: pinned and locked")
	assert.Equal(t, []domain.CollectionFileRef{{ID: "11", MD5: "aa", Size: 100}}, doc.Items[0].Files)
	assert.Equal(t, "Exact", doc.Items[0].Name)
	assert.Equal(t, domain.ModReference{SourceID: "nexusmods", ModID: "2", Version: "1.0", FileIDs: []string{"22"}}, doc.Items[1].Ref,
		"prefer: pinned, not locked")
	assert.Equal(t, domain.ModReference{SourceID: "nexusmods", ModID: "3", Version: "1.0", FileIDs: []string{"33"}}, doc.Items[2].Ref,
		"latest: the curator's file, unlocked")
	assert.Equal(t, []string{"nexusmods:3"}, c.AutoUpdate, "and auto-updating from then on")
	assert.Empty(t, c.Unsupported)
	assert.Empty(t, doc.Requires)
	for _, it := range c.Items {
		assert.Equal(t, CollectionItemWillInstall, it.State)
	}
}

// Owner ruling 2026-11-13 (#607 Q3): a "latest" item installs the
// curator's tested file now, unlocked, and its update policy becomes auto,
// so the next update check moves it to the newest file. No API call at plan
// time.
func TestConvert_LatestInstallsTheTestedFileThenAutoUpdates(t *testing.T) {
	latest := nexusMod("Latest", "3", "33", "cc")
	latest.UpdatePolicy = "latest"
	mixedExact := nexusMod("Mixed Main", "4", "41", "dd")
	mixedLatest := nexusMod("Mixed Extra", "4", "42", "de")
	mixedLatest.UpdatePolicy = "latest"
	exact := nexusMod("Exact", "5", "51", "ee")
	c := mustConvert(t, convertInputFor(&source.CollectionManifest{Name: "C",
		Mods: []source.ManifestMod{latest, mixedExact, mixedLatest, exact}}))

	require.Len(t, c.Document.Items, 3)
	assert.Equal(t, domain.ModReference{SourceID: "nexusmods", ModID: "3", Version: "1.0", FileIDs: []string{"33"}}, c.Document.Items[0].Ref,
		"the curator's exact file id, unlocked")
	assert.Equal(t, []domain.CollectionFileRef{{ID: "33", MD5: "cc", Size: 100}}, c.Document.Items[0].Files)
	assert.Equal(t, []string{"nexusmods:3"}, c.AutoUpdate, "only an entry whose every file is latest auto-updates")
	assert.True(t, c.Document.Items[1].Ref.Locked, "a mod with an exact file stays locked")

	l := itemByName(t, c, "Latest")
	assert.True(t, l.AutoUpdate)
	assert.Equal(t, "curator asks for the latest file; installs the tested file now, then auto-updates", l.Note)
	m := itemByName(t, c, "Mixed Extra")
	assert.False(t, m.AutoUpdate)
	assert.Contains(t, m.Note, "pinned exactly")
	assert.Empty(t, itemByName(t, c, "Exact").Note)
	assert.False(t, itemByName(t, c, "Exact").AutoUpdate)
}

func TestConvert_FomodChoicesBecomeRecordedChoices(t *testing.T) {
	mod := nexusMod("Installer", "1", "11", "aa")
	mod.InstallerType = "fomod"
	mod.Choices = []domain.FomodStepChoice{
		{Name: "", Groups: []domain.FomodGroupChoice{{Name: "Patches", Choices: []domain.FomodOptionChoice{}}}},
		{Name: "Main", Groups: []domain.FomodGroupChoice{{Name: "Pick", Choices: []domain.FomodOptionChoice{{Name: " Spaced", Idx: 1}}}}},
	}
	odd := nexusMod("Odd", "2", "22", "bb")
	odd.InstallerType = "bain"
	c := mustConvert(t, convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{mod, odd}}))

	ref := c.Document.Items[0].Ref
	assert.Equal(t, mod.Choices, ref.Fomod.Steps, "the choices are recorded as written: no trimming, an empty pick list kept")
	assert.Empty(t, ref.Fomod.ConfigSHA256, "Vortex records no config hash")
	assert.Equal(t, []string{CollectionFeatureFomod}, c.Document.Requires)
	assert.True(t, itemByName(t, c, "Installer").Installer)
	assert.Equal(t, []string{CollectionFeatureFomodUnknownInstaller}, featureNames(c.Unsupported))
	assert.Equal(t, []string{"nexusmods:2"}, c.Unsupported[0].Items)
	assert.Contains(t, c.Features, CollectionFeatureFomod)
}

func TestConvert_ItemFeaturesBlockRequiredAndSkipOptional(t *testing.T) {
	list := nexusMod("Listed", "1", "11", "aa")
	list.FileList = []source.ManifestFileHash{{Path: `r6\a`, MD5: "x"}}
	patched := nexusMod("Patched", "2", "22", "bb")
	patched.Optional = true
	patched.Patches = map[string]string{"a.txt": "DEADBEEF"}
	bundle := source.ManifestMod{Name: "Bundle", SourceType: "bundle", UpdatePolicy: "exact"}
	overrides := nexusMod("Overrides", "4", "44", "dd")
	overrides.FileOverrides = []string{"a.txt"}
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{list, patched, bundle, overrides}}

	in := convertInputFor(m)
	in.withOptional = true
	c := mustConvert(t, in)
	assert.Equal(t, []string{CollectionFeatureBundled, CollectionFeatureFileList, CollectionFeatureFileOverrides}, featureNames(c.Unsupported),
		"a required item's unsupported feature blocks; the optional item's does not")
	assert.Equal(t, CollectionItemUnsupported, itemByName(t, c, "Patched").State, "even opted in, it is skipped")
	assert.Equal(t, []string{CollectionFeaturePatches}, itemByName(t, c, "Patched").Features)
	assert.Equal(t, CollectionItemUnsupported, itemByName(t, c, "Listed").State)
	assert.Contains(t, c.Features, CollectionFeaturePatches)
	assert.NotContains(t, refOrder(c.Document), "2")
	assert.True(t, containsSubstring(c.Notes, "Patched"), "%v", c.Notes)
}

func TestConvert_OptionalItemsAreOptIn(t *testing.T) {
	req := nexusMod("Required", "1", "11", "aa")
	opt := nexusMod("Optional", "2", "22", "bb")
	opt.Optional = true
	opt2 := nexusMod("Optional Two", "3", "33", "cc")
	opt2.Optional = true
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{req, opt, opt2}}

	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{"1"}, refOrder(c.Document), "optional mods are off by default")
	assert.Equal(t, CollectionItemOptionalSkipped, itemByName(t, c, "Optional").State)
	assert.Equal(t, "2:22", itemByName(t, c, "Optional").ID)
	assert.Contains(t, c.Features, CollectionFeatureOptional)
	assert.Empty(t, c.Unsupported)

	in := convertInputFor(m)
	in.optional = map[string]bool{"3:33": true}
	c = mustConvert(t, in)
	assert.Equal(t, []string{"1", "3"}, refOrder(c.Document), "one item opted in by id")

	in = convertInputFor(m)
	in.withOptional = true
	c = mustConvert(t, in)
	assert.Equal(t, []string{"1", "2", "3"}, refOrder(c.Document))
	assert.Equal(t, CollectionItemWillInstall, itemByName(t, c, "Optional").State)
}

func TestConvert_ExternalItems(t *testing.T) {
	browse := source.ManifestMod{Name: "F4SE", SourceType: "browse", URL: "https://f4se.silverlock.org", Instructions: "get it", UpdatePolicy: "latest"}
	direct := source.ManifestMod{Name: "Optional Thing", SourceType: "direct", URL: "https://x.example/a.zip", Optional: true}
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa"), browse, direct}}
	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []domain.CollectionExternalItem{{Name: "F4SE", URL: "https://f4se.silverlock.org", Instructions: "get it"}}, c.Document.External)
	assert.Equal(t, []string{CollectionFeatureExternal}, c.Document.Requires)
	assert.Equal(t, CollectionItemExternal, itemByName(t, c, "F4SE").State)
	assert.Equal(t, CollectionItemOptionalSkipped, itemByName(t, c, "Optional Thing").State)
	assert.Empty(t, c.Unsupported)
}

func TestConvert_ModTypes(t *testing.T) {
	skse := nexusMod("SKSE64", "30379", "1", "aa")
	skse.ModType = "dinput"
	smapi := nexusMod("SMAPI thing", "2", "2", "bb")
	smapi.ModType = "SMAPI"
	smapi.Optional = true
	in := convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{skse, smapi}})
	in.withOptional = true
	in.modTypeSupported = func(t string) bool { return t == "dinput" }
	c := mustConvert(t, in)

	assert.Empty(t, c.Unsupported, "dinput routes; the optional SMAPI item is skipped")
	assert.Equal(t, "dinput", itemByName(t, c, "SKSE64").ModType)
	assert.Equal(t, []string{"mod_type:dinput"}, c.Document.Requires, "a v1 document that relies on a mod type says so")
	assert.Equal(t, CollectionItemUnsupported, itemByName(t, c, "SMAPI thing").State)

	in.modTypeSupported = nil
	c = mustConvert(t, in)
	assert.Equal(t, []string{"mod_type:dinput"}, featureNames(c.Unsupported), "no adapter route, no import")
}

func TestConvert_FilesOfOneModMerge(t *testing.T) {
	main := nexusMod("Mod - Main", "1", "11", "aa")
	extra := nexusMod("Mod - Extra", "1", "12", "ab")
	extra.UpdatePolicy = "prefer"
	other := nexusMod("Other", "2", "21", "bb")
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{main, other, extra}}
	c := mustConvert(t, convertInputFor(m))
	require.Len(t, c.Document.Items, 2, "one profile entry per mod")
	first := c.Document.Items[0]
	assert.Equal(t, []string{"11", "12"}, first.Ref.FileIDs)
	assert.True(t, first.Ref.Locked, "the strictest policy wins")
	assert.Len(t, first.Files, 2)
	assert.Empty(t, c.Unsupported)

	// Two installers in one mod, or two mod types, cannot share one entry.
	a := nexusMod("Two A", "5", "51", "x1")
	a.InstallerType = "fomod"
	a.Choices = []domain.FomodStepChoice{{Name: "s"}}
	b := nexusMod("Two B", "5", "52", "x2")
	b.InstallerType = "fomod"
	b.Choices = []domain.FomodStepChoice{{Name: "s"}}
	d := nexusMod("Root", "6", "61", "y1")
	d.ModType = "dinput"
	e := nexusMod("Data", "6", "62", "y2")
	in := convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{a, b, d, e}})
	in.modTypeSupported = func(string) bool { return true }
	c = mustConvert(t, in)
	assert.Equal(t, []string{CollectionFeatureFomodMultiFile, CollectionFeatureModTypeMixed}, featureNames(c.Unsupported))
}

func TestConvert_PhaseAndRulesOrderTheProfile(t *testing.T) {
	a := nexusMod("A", "1", "11", "md5a")
	b := nexusMod("B", "2", "21", "md5b")
	cc := nexusMod("C", "3", "31", "md5c")
	cc.Phase = 1
	d := nexusMod("D", "4", "41", "md5d")
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{cc, a, b, d},
		ModRules: []source.ManifestModRule{
			// "A after B": B loads first, A overwrites it.
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{FileMD5: "md5b"}},
			// by logical name when the md5 does not match (an older file)
			{Type: "before", Source: source.ManifestModRef{FileMD5: "nope", LogicalFileName: "D", VersionMatch: "*"}, Reference: source.ManifestModRef{FileMD5: "md5b"}},
			// a rule against a mod outside the collection orders nothing
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{FileMD5: "outsider", LogicalFileName: "Elsewhere"}},
			{Type: "conflicts", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{LogicalFileName: "X"}},
		}}
	c := mustConvert(t, convertInputFor(m))
	// Phase first (C is phase 1, so last), then the rules: D before B, B before A.
	assert.Equal(t, []string{"4", "2", "1", "3"}, refOrder(c.Document))
	assert.Empty(t, c.Unsupported, "an unresolved rule raises no feature (ruled 2026-11-13)")
	assert.Contains(t, c.Features, CollectionFeatureModRules)
	assert.Contains(t, c.Features, CollectionFeaturePhase)
	assert.True(t, containsSubstring(c.Notes, "not in this collection"), "%v", c.Notes)
	assert.True(t, containsSubstring(c.Notes, "conflicts"), "%v", c.Notes)
}

func TestConvert_RuleCycleKeepsTheBaseOrderWithANote(t *testing.T) {
	a := nexusMod("A", "1", "11", "md5a")
	b := nexusMod("B", "2", "21", "md5b")
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{a, b},
		ModRules: []source.ManifestModRule{
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{FileMD5: "md5b"}},
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5b"}, Reference: source.ManifestModRef{FileMD5: "md5a"}},
		}}
	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{"1", "2"}, refOrder(c.Document))
	assert.True(t, containsSubstring(c.Notes, "cycle"), "%v", c.Notes)
}

// The reviewer's repro (#607 C2a review, 1): a node DOWNSTREAM of a cycle
// keeps its rule. C before A; B and C each before the other. The cycle is
// {B, C}; A only waits on it. Breaking at the lowest-numbered unfinished
// node (A) would have put A first, against the satisfiable C-before-A, and
// named the wrong cycle.
func TestConvert_RuleCycleIsBrokenInsideTheCycle(t *testing.T) {
	a := nexusMod("A", "1", "11", "md5a")
	b := nexusMod("B", "2", "21", "md5b")
	c := nexusMod("C", "3", "31", "md5c")
	ref := func(md5 string) source.ManifestModRef { return source.ManifestModRef{FileMD5: md5} }
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{a, b, c},
		ModRules: []source.ManifestModRule{
			{Type: "before", Source: ref("md5c"), Reference: ref("md5a")},
			{Type: "before", Source: ref("md5b"), Reference: ref("md5c")},
			{Type: "before", Source: ref("md5c"), Reference: ref("md5b")},
		}}
	conv := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{"2", "3", "1"}, refOrder(conv.Document), "B, C, then A: C-before-A holds")
	var note string
	for _, n := range conv.Notes {
		if strings.Contains(n, "cycle") {
			note = n
		}
	}
	assert.Contains(t, note, "B, C", "the whole cycle is named")
	assert.NotContains(t, note, "A,", "and only the cycle")
}

func TestStableTopoSort_CyclesAreReportedOnce(t *testing.T) {
	// 0 -> 1 -> 2 -> 0, plus a chord 2 -> 1, and 3 downstream of 2.
	edges := [][]int{{1}, {2}, {0, 1, 3}, {}}
	indegree := []int{1, 2, 1, 1}
	order, cycles := stableTopoSort(4, edges, indegree)
	assert.Equal(t, []int{0, 1, 2, 3}, order)
	assert.Equal(t, [][]int{{0, 1, 2}}, cycles)
}

func TestConvert_CollectionLevelFeatures(t *testing.T) {
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")},
		Plugins:        []source.ManifestPlugin{{Name: "A.esp", Enabled: true}},
		FileLoadOrders: []source.ManifestFileLoadOrder{{ID: "cyberpunk2077-archive", Entries: 3}},
		Tools:          []source.ManifestTool{{Name: "SSEEdit", Exe: `Data\SSEEdit.exe`}},
		Unknown: []source.ManifestUnknownKey{
			{ModIndex: -1, Key: "mergedData"},
			{ModIndex: 0, Item: "A", Key: "mods.future"},
		}}
	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{"file_load_order:cyberpunk2077-archive", "manifest:mergedData", "manifest:mods.future", CollectionFeaturePluginOrder},
		featureNames(c.Unsupported))
	assert.Nil(t, c.Unsupported[1].Items, "a collection-level key names no item")
	assert.Equal(t, []string{"nexusmods:1"}, c.Unsupported[2].Items)
	assert.Equal(t, []CollectionTool{{Name: "SSEEdit", Exe: `Data\SSEEdit.exe`}}, c.Tools)
	assert.True(t, containsSubstring(c.Notes, "tool"), "tools are informational: %v", c.Notes)

	m = &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")},
		PluginRules: source.ManifestPluginRules{Groups: []source.ManifestPluginGroup{{Name: "g"}}}}
	assert.Equal(t, []string{CollectionFeaturePluginOrder}, featureNames(mustConvert(t, convertInputFor(m)).Unsupported))
}

func TestConvert_ManifestUnavailableStillRendersTheList(t *testing.T) {
	in := convertInputFor(nil)
	in.manifestErr = "lmm needs the system 7z"
	in.list.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "1", FileID: "11", Version: "1.0", Name: "A", UpdatePolicy: "exact", FileCategory: "ARCHIVED", URL: "https://x"},
		{Kind: source.CollectionItemModFile, ModID: "2", FileID: "21", Version: "2.0", Name: "B", Optional: true},
		{Kind: source.CollectionItemExternal, Name: "Ext", URL: "https://e"},
	}
	c := mustConvert(t, in)
	assert.Equal(t, []string{CollectionFeatureManifestUnavailable}, featureNames(c.Unsupported))
	assert.Equal(t, "lmm needs the system 7z", c.ManifestError)
	assert.Equal(t, "List name", c.Document.Name)
	assert.Equal(t, []string{"1"}, refOrder(c.Document))
	assert.True(t, c.Document.Items[0].Ref.Locked)
	a := itemByName(t, c, "A")
	assert.Equal(t, "ARCHIVED", a.FileCategory)
	assert.Equal(t, "https://x", a.URL)
	assert.Equal(t, CollectionItemOptionalSkipped, itemByName(t, c, "B").State)
	assert.Equal(t, CollectionItemExternal, itemByName(t, c, "Ext").State)
}

func TestConvert_ListFactsJoinTheRecipe(t *testing.T) {
	in := convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")}})
	in.list.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "1", FileID: "11", FileCategory: "OLD_VERSION", URL: "https://page", FileName: "A file"},
		{Kind: source.CollectionItemModFile, ModID: "9", FileID: "99"},
	}
	c := mustConvert(t, in)
	a := itemByName(t, c, "A")
	assert.Equal(t, "OLD_VERSION", a.FileCategory, "an old file installs by its exact id")
	assert.Equal(t, "https://page", a.URL)
	assert.True(t, containsSubstring(c.Notes, "published list are not in its recipe"), "%v", c.Notes)

	// And the other way: a recipe file the published list does not have.
	in = convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa"), nexusMod("Extra", "2", "22", "bb")}})
	in.list.Items = []source.CollectionItem{{Kind: source.CollectionItemModFile, ModID: "1", FileID: "11"}}
	c = mustConvert(t, in)
	assert.True(t, containsSubstring(c.Notes, "not in the collection's published list (Extra)"), "%v", c.Notes)
}

// mustConvert is convertCollection for an input it accepts.
func mustConvert(t *testing.T, in convertInput) *ConvertedCollection {
	t.Helper()
	c, err := convertCollection(in)
	require.NoError(t, err)
	return c
}

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestRedactLink_CutsTheSignedLinkOutOfAnyError(t *testing.T) {
	link := "https://cdn.example/recipe.7z?md5=abc123&expires=99&user_id=7"
	inner := &url.Error{Op: "Get", URL: link, Err: errors.New("dial tcp: connection refused")}
	err := redactLink(fmt.Errorf("downloading the recipe: %w", inner), link)
	assert.NotContains(t, err.Error(), "abc123")
	assert.NotContains(t, err.Error(), "md5=")
	assert.Contains(t, err.Error(), "connection refused")
	var ue *url.Error
	assert.True(t, errors.As(err, &ue), "the cause is still there to classify")
	plain := errors.New("HTTP error: 403 Forbidden")
	assert.Same(t, plain, redactLink(plain, link))
}

// #607 review 5: nothing the curator or the source wrote reaches a plan
// with a control, escape or bidi character in it, or as a non-web link.
func TestConvert_SanitisesEveryCuratorString(t *testing.T) {
	const esc, bidi = "\x1b]8;;https://evil.example\x07", "\u202e"
	mod := nexusMod("Mod"+esc+"Name", "1", "11", "aa")
	mod.ModType = "din" + bidi + "put"
	mod.Instructions = "do" + esc + " this"
	ext := source.ManifestMod{Name: "Ext" + bidi, SourceType: "browse", URL: "javascript:alert(1)"}
	m := &source.CollectionManifest{Name: "Coll" + esc, Author: "Cur" + bidi + "ator", Description: "d" + esc,
		Mods:           []source.ManifestMod{mod, ext},
		Tools:          []source.ManifestTool{{Name: "Tool" + esc, Exe: `x` + bidi + `.exe`}},
		FileLoadOrders: []source.ManifestFileLoadOrder{{ID: "lo" + esc, Entries: 1}},
		Unknown:        []source.ManifestUnknownKey{{ModIndex: -1, Key: "key" + bidi}},
	}
	in := convertInputFor(m)
	in.list.Name = "List" + esc
	in.list.URL = "javascript:alert(2)"
	in.list.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "1", FileID: "11", URL: "javascript:alert(3)", FileCategory: "MAIN" + esc},
		{Kind: source.CollectionItemExternal, Name: "Listed ext" + esc, URL: "data:text/html,x"},
	}
	c := mustConvert(t, in)

	var all []string
	all = append(all, c.Name, c.Author, c.URL)
	all = append(all, c.Notes...)
	all = append(all, c.Features...)
	for _, f := range c.Unsupported {
		all = append(all, f.Feature)
		all = append(all, f.Items...)
	}
	for _, it := range c.Items {
		all = append(all, it.Name, it.URL, it.ModType, it.Instructions, it.FileCategory, it.Note)
		all = append(all, it.Features...)
	}
	for _, tl := range c.Tools {
		all = append(all, tl.Name, tl.Exe)
	}
	d := c.Document
	all = append(all, d.Name, d.Info.Title, d.Info.Author, d.Info.Description)
	all = append(all, d.Requires...)
	for _, e := range d.External {
		all = append(all, e.Name, e.URL)
	}
	for _, s := range all {
		assert.False(t, hasUnsafeRunes(s), "unsafe characters survived in %q", s)
		assert.NotContains(t, s, "javascript:")
		assert.NotContains(t, s, "data:text")
	}
	assert.Contains(t, c.Features, "mod_type:"+strconv.QuoteToASCII("din"+bidi+"put"), "an unsafe type is escaped, never shown raw")
	assert.Contains(t, c.Features, "file_load_order:"+strconv.QuoteToASCII("lo"+esc))
}

func TestConvert_RefusesWhatL1Refuses(t *testing.T) {
	mod := nexusMod("M", "1", "11", "aa")
	mod.InstallerType = "fomod"
	mod.Choices = []domain.FomodStepChoice{{Name: "step\u202e", Groups: []domain.FomodGroupChoice{{Name: "g", Choices: []domain.FomodOptionChoice{}}}}}
	_, err := convertCollection(convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{mod}}))
	assert.ErrorIs(t, err, ErrInvalidCollection, "a recorded choice that cannot be shown as it would replay is refused, as L1 refuses it")

	long := nexusMod(strings.Repeat("n", maxCollectionLine+1), "1", "11", "aa")
	_, err = convertCollection(convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{long}}))
	assert.ErrorIs(t, err, ErrInvalidCollection, "over the line cap")
}

// #607 review 6: an item - or a whole recipe - of another game is refused,
// never looked up in this one.
func TestConvert_AnotherGamesItemIsRefused(t *testing.T) {
	other := nexusMod("Other Game's", "1", "11", "aa")
	other.DomainName = "fallout4"
	same := nexusMod("Same Game's", "2", "22", "bb")
	same.DomainName = "skyrimspecialedition"
	c := mustConvert(t, convertInputFor(&source.CollectionManifest{Name: "C", GameSourceID: "skyrimspecialedition",
		Mods: []source.ManifestMod{other, same}}))
	assert.Equal(t, []string{"manifest:mods.domainName"}, featureNames(c.Unsupported))
	assert.Equal(t, []string{"nexusmods:1"}, c.Unsupported[0].Items)

	c = mustConvert(t, convertInputFor(&source.CollectionManifest{Name: "C", GameSourceID: "fallout4",
		Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")}}))
	assert.Equal(t, []string{"manifest:info.domainName"}, featureNames(c.Unsupported))
}

// #607 review 7: the published list's external resources reach the
// document beside the recipe's own, the same link once.
func TestConvert_ListExternalResourcesJoinTheRecipe(t *testing.T) {
	browse := source.ManifestMod{Name: "F4SE", SourceType: "browse", URL: "https://f4se.silverlock.org"}
	in := convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa"), browse}})
	in.list.Items = []source.CollectionItem{
		{Kind: source.CollectionItemExternal, Name: "F4SE (listed)", URL: "https://f4se.silverlock.org"},
		{Kind: source.CollectionItemExternal, Name: "ENB", URL: "https://enbdev.example"},
		{Kind: source.CollectionItemExternal, Name: "Optional ENB preset", URL: "https://preset.example", Optional: true},
	}
	c := mustConvert(t, in)
	assert.Equal(t, []domain.CollectionExternalItem{
		{Name: "F4SE", URL: "https://f4se.silverlock.org"},
		{Name: "ENB", URL: "https://enbdev.example"},
	}, c.Document.External)
	assert.Contains(t, c.Features, CollectionFeatureExternal)
	assert.Equal(t, CollectionItemOptionalSkipped, itemByName(t, c, "Optional ENB preset").State)
}

func TestConvert_NotesTheCasesItCannotSettle(t *testing.T) {
	empty := nexusMod("Empty Answers", "1", "11", "aa")
	empty.InstallerType = "fomod"
	noVersion := nexusMod("No Version", "2", "21", "bb")
	noVersion.Version = ""
	preferMain := nexusMod("Mixed Main", "3", "31", "cc")
	preferMain.UpdatePolicy = "prefer"
	latestExtra := nexusMod("Mixed Extra", "3", "32", "cd")
	latestExtra.UpdatePolicy = "latest"
	c := mustConvert(t, convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{empty, noVersion, preferMain, latestExtra}}))
	assert.Contains(t, itemByName(t, c, "Empty Answers").Note, "installer will ask")
	assert.True(t, containsSubstring(c.Notes, "No Version is pinned to the curator's file but names no version"), "%v", c.Notes)
	assert.Contains(t, itemByName(t, c, "Mixed Extra").Note, "prefers its tested file")
	assert.Empty(t, c.AutoUpdate)
}

func TestConvert_RuleResolutionIsLabelledAccurately(t *testing.T) {
	a := nexusMod("A", "1", "11", "md5a")
	b := nexusMod("B", "2", "21", "md5b")
	b.Version = "2.0"
	twinOne := nexusMod("Twin", "3", "31", "md5t1")
	twinTwo := nexusMod("Twin", "4", "41", "md5t2")
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{a, b, twinOne, twinTwo},
		ModRules: []source.ManifestModRule{
			// another version of a member: not guessed
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{LogicalFileName: "B", VersionMatch: "1.0"}},
			// a range: not evaluated
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{LogicalFileName: "B", VersionMatch: ">=1.0"}},
			// a name two items share
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{LogicalFileName: "Twin"}},
			// the exact version resolves
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{LogicalFileName: "B", VersionMatch: "2.0"},
				Unknown: []string{"reference.oddKey"}},
			// a dropped rule's unknown key changes nothing
			{Type: "after", Source: source.ManifestModRef{FileMD5: "md5a"}, Reference: source.ManifestModRef{FileMD5: "outsider"}, Unknown: []string{"weird"}},
		}}
	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{"2", "1", "3", "4"}, refOrder(c.Document), "only the exact-version rule applied: B before A")
	assert.True(t, containsSubstring(c.Notes, "3 ordering rule(s) name a mod lmm cannot match to one item"), "%v", c.Notes)
	assert.True(t, containsSubstring(c.Notes, "1 ordering rule(s) name a mod that is not in this collection"), "%v", c.Notes)
	assert.Equal(t, []string{"manifest:modRules.reference.oddKey"}, featureNames(c.Unsupported),
		"an applied rule's unknown key is the collection's; the dropped rule's is not")
}

func TestConvert_ModRulesFeatureOnlyForOrderingRules(t *testing.T) {
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")},
		ModRules: []source.ManifestModRule{{Type: "conflicts", Source: source.ManifestModRef{FileMD5: "aa"}, Reference: source.ManifestModRef{LogicalFileName: "X"}}}}
	c := mustConvert(t, convertInputFor(m))
	assert.NotContains(t, c.Features, CollectionFeatureModRules)
}

func TestConvert_NotesARuleThatCrossesPhases(t *testing.T) {
	early := nexusMod("Early", "1", "11", "md5e")
	late := nexusMod("Late", "2", "21", "md5l")
	late.Phase = 2
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{early, late},
		ModRules: []source.ManifestModRule{{Type: "before", Source: source.ManifestModRef{FileMD5: "md5l"}, Reference: source.ManifestModRef{FileMD5: "md5e"}}}}
	c := mustConvert(t, convertInputFor(m))
	assert.Equal(t, []string{"2", "1"}, refOrder(c.Document))
	assert.True(t, containsSubstring(c.Notes, "put Late before a mod of an earlier install phase"), "%v", c.Notes)
}
