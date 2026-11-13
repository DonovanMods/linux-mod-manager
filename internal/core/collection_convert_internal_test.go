package core

import (
	"errors"
	"fmt"
	"net/url"
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

	c := convertCollection(convertInputFor(m))
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
	c := convertCollection(convertInputFor(&source.CollectionManifest{Name: "C",
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
	c := convertCollection(convertInputFor(&source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{mod, odd}}))

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
	c := convertCollection(in)
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

	c := convertCollection(convertInputFor(m))
	assert.Equal(t, []string{"1"}, refOrder(c.Document), "optional mods are off by default")
	assert.Equal(t, CollectionItemOptionalSkipped, itemByName(t, c, "Optional").State)
	assert.Equal(t, "2:22", itemByName(t, c, "Optional").ID)
	assert.Contains(t, c.Features, CollectionFeatureOptional)
	assert.Empty(t, c.Unsupported)

	in := convertInputFor(m)
	in.optional = map[string]bool{"3:33": true}
	c = convertCollection(in)
	assert.Equal(t, []string{"1", "3"}, refOrder(c.Document), "one item opted in by id")

	in = convertInputFor(m)
	in.withOptional = true
	c = convertCollection(in)
	assert.Equal(t, []string{"1", "2", "3"}, refOrder(c.Document))
	assert.Equal(t, CollectionItemWillInstall, itemByName(t, c, "Optional").State)
}

func TestConvert_ExternalItems(t *testing.T) {
	browse := source.ManifestMod{Name: "F4SE", SourceType: "browse", URL: "https://f4se.silverlock.org", Instructions: "get it", UpdatePolicy: "latest"}
	direct := source.ManifestMod{Name: "Optional Thing", SourceType: "direct", URL: "https://x.example/a.zip", Optional: true}
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa"), browse, direct}}
	c := convertCollection(convertInputFor(m))
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
	c := convertCollection(in)

	assert.Empty(t, c.Unsupported, "dinput routes; the optional SMAPI item is skipped")
	assert.Equal(t, "dinput", itemByName(t, c, "SKSE64").ModType)
	assert.Equal(t, []string{"mod_type:dinput"}, c.Document.Requires, "a v1 document that relies on a mod type says so")
	assert.Equal(t, CollectionItemUnsupported, itemByName(t, c, "SMAPI thing").State)

	in.modTypeSupported = nil
	c = convertCollection(in)
	assert.Equal(t, []string{"mod_type:dinput"}, featureNames(c.Unsupported), "no adapter route, no import")
}

func TestConvert_FilesOfOneModMerge(t *testing.T) {
	main := nexusMod("Mod - Main", "1", "11", "aa")
	extra := nexusMod("Mod - Extra", "1", "12", "ab")
	extra.UpdatePolicy = "prefer"
	other := nexusMod("Other", "2", "21", "bb")
	m := &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{main, other, extra}}
	c := convertCollection(convertInputFor(m))
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
	c = convertCollection(in)
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
	c := convertCollection(convertInputFor(m))
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
	c := convertCollection(convertInputFor(m))
	assert.Equal(t, []string{"1", "2"}, refOrder(c.Document))
	assert.True(t, containsSubstring(c.Notes, "cycle"), "%v", c.Notes)
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
	c := convertCollection(convertInputFor(m))
	assert.Equal(t, []string{"file_load_order:cyberpunk2077-archive", "manifest:mergedData", "manifest:mods.future", CollectionFeaturePluginOrder},
		featureNames(c.Unsupported))
	assert.Nil(t, c.Unsupported[1].Items, "a collection-level key names no item")
	assert.Equal(t, []string{"nexusmods:1"}, c.Unsupported[2].Items)
	assert.Equal(t, []CollectionTool{{Name: "SSEEdit", Exe: `Data\SSEEdit.exe`}}, c.Tools)
	assert.True(t, containsSubstring(c.Notes, "tool"), "tools are informational: %v", c.Notes)

	m = &source.CollectionManifest{Name: "C", Mods: []source.ManifestMod{nexusMod("A", "1", "11", "aa")},
		PluginRules: source.ManifestPluginRules{Groups: []source.ManifestPluginGroup{{Name: "g"}}}}
	assert.Equal(t, []string{CollectionFeaturePluginOrder}, featureNames(convertCollection(convertInputFor(m)).Unsupported))
}

func TestConvert_ManifestUnavailableStillRendersTheList(t *testing.T) {
	in := convertInputFor(nil)
	in.manifestErr = "lmm needs the system 7z"
	in.list.Items = []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "1", FileID: "11", Version: "1.0", Name: "A", UpdatePolicy: "exact", FileCategory: "ARCHIVED", URL: "https://x"},
		{Kind: source.CollectionItemModFile, ModID: "2", FileID: "21", Version: "2.0", Name: "B", Optional: true},
		{Kind: source.CollectionItemExternal, Name: "Ext", URL: "https://e"},
	}
	c := convertCollection(in)
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
	c := convertCollection(in)
	a := itemByName(t, c, "A")
	assert.Equal(t, "OLD_VERSION", a.FileCategory, "an old file installs by its exact id")
	assert.Equal(t, "https://page", a.URL)
	assert.True(t, containsSubstring(c.Notes, "disagree"), "%v", c.Notes)
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
