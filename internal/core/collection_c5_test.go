package core_test

// #610 C5: a NexusMods collection's exact file lists (`hashes`), binary
// patches and bundled files, converted, planned and installed through the
// source-collection import - with a source double serving the recipe and
// the pinned files, and a recipe archive holding bundled/ and patches/ as
// the survey's real ones do.

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
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

// The patch fixture internal/core's own tests build (bsdiff_internal_test.go):
// a version bump in a mod's manifest, as a curator's edit is.
const (
	c5ManifestV40 = `{"Name": "Additional Farm Cave", "Version": "4.0"}`
	c5ManifestV41 = `{"Name": "Additional Farm Cave", "Version": "4.1"}`
)

func c5MD5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func c5CRC(s string) string { return fmt.Sprintf("%08X", crc32.ChecksumIEEE([]byte(s))) }

func c5Patch(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "bsdiff", "same-length-edit.patch"))
	require.NoError(t, err)
	return data
}

// c5Recipe is a recipe with an item installing by an exact file list
// (mod 20), an item with a patch (mod 21), a bundled config item, and a
// bundled item holding a DLL. patchCRC is the baseline the recipe records
// for mod 21's manifest; patchTarget the patched path.
func c5Recipe(patchTarget, patchCRC string) string {
	return fmt.Sprintf(`{"name": "Replicated", "mods": [
	{"name": "Twenty", "modId": "20", "fileId": "200", "hashes": [
		{"path": "r6\\scripts\\A.reds", "md5": %q},
		{"path": "r6/scripts/b.reds", "md5": %q}]},
	{"name": "Twenty-One", "modId": "21", "fileId": "210", "patches": {%q: %q}},
	{"name": "Marker", "sourceType": "bundle", "fileExpression": "Bundled - Marker (v1.0.0)"},
	{"name": "Helper", "sourceType": "bundle", "fileExpression": "Bundled - Helper (v1)", "optional": true}]}`,
		c5MD5("a"), c5MD5("b"), patchTarget, patchCRC)
}

type c5Fixture struct {
	svc      *core.Service
	game     *domain.Game
	src      *collectionSource
	cacheDir string
}

// newC5Fixture serves recipe, with bundled/ and patches/ trees as fill
// writes them beside it.
func newC5Fixture(t *testing.T, recipe string, fill func(dir string)) *c5Fixture {
	t.Helper()
	archive := recipeArchiveOf(t, func(dir string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), []byte(recipe), 0o644))
		fill(dir)
	})
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(cdn.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	cacheDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: cacheDir, DownloadClient: cdn.Client()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	downloads := newMockSourceWithDownloads("nexusmods")
	t.Cleanup(downloads.Close)
	src := &collectionSource{
		mockSourceWithDownloads: downloads,
		recipe: &recipeSource{mockSource: downloads.mockSource, archiveURL: cdn.URL + "/recipe.7z",
			collection: source.Collection{ID: "abcdef", Name: "Replicated", Revision: 3, Author: "curator", Handle: "/h",
				Items: []source.CollectionItem{
					{Kind: source.CollectionItemModFile, ModID: "20", FileID: "200", Name: "Twenty"},
					{Kind: source.CollectionItemModFile, ModID: "21", FileID: "210", Name: "Twenty-One"},
				}}},
		tier:       source.TierPremium,
		filesByMod: map[string][]string{"20": {"200"}, "21": {"210"}},
	}
	for id, files := range map[string]map[string]string{
		"20": {"r6/scripts/A.reds": "a", "r6/scripts/b.reds": "b", "r6/extra/unlisted.txt": "x"},
		"21": {"Mod/manifest.json": c5ManifestV40, "Mod/other.json": "{}"},
	} {
		downloads.AddMod("skyrimspecialedition", &domain.Mod{ID: id, SourceID: "nexusmods", Name: "Mod " + id, Version: "1", GameID: "skyrimspecialedition"})
		downloads.AddDownload(map[string]string{"20": "200", "21": "210"}[id], createTestZipBytes(t, files))
	}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return &c5Fixture{svc: svc, game: game, src: src, cacheDir: cacheDir}
}

// c5Trees writes the recipe's bundled/ and patches/ trees: the marker, the
// helper with its DLL, and mod 21's patch under patches/<mod name>/.
func c5Trees(t *testing.T, patch []byte) func(dir string) {
	return func(dir string) {
		write := func(rel string, data []byte) {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, data, 0o644))
		}
		write("bundled/Bundled - Marker (v1.0.0)/Collection Marker/manifest.json", []byte(`{"marker": true}`))
		write("bundled/Bundled - Helper (v1)/bin/helper.dll", []byte("MZ not really"))
		write("patches/Twenty-One/Mod/manifest.json.diff", patch)
	}
}

func (f *c5Fixture) plan(t *testing.T, req core.SourceCollectionImportRequest) *core.ImportPlan {
	t.Helper()
	req.SourceID, req.Ref = "nexusmods", "abcdef"
	plan, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, req)
	require.NoError(t, err)
	return plan
}

func (f *c5Fixture) deployed(t *testing.T, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.game.ModPath, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func importItem(c *core.CollectionImport, modID string) *core.CollectionImportItem {
	for i := range c.Items {
		if c.Items[i].ModID == modID {
			return &c.Items[i]
		}
	}
	return nil
}

// TestSourceCollection_FileListPatchesAndBundles is the whole C5 path: the
// plan shows each, and the install extracts only the listed files, applies
// the patch onto its baseline, installs the bundled config, holds the
// bundled DLL back, records the inputs on the profile and keeps the patch.
func TestSourceCollection_FileListPatchesAndBundles(t *testing.T) {
	patch := c5Patch(t)
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, patch))
	plan := f.plan(t, core.SourceCollectionImportRequest{WithOptional: true})
	c := plan.Collection

	require.Empty(t, c.Unsupported, "file_list, patches and bundled are all supported: %+v", c.Unsupported)
	for _, feature := range []string{core.CollectionFeatureFileList, core.CollectionFeaturePatches, core.CollectionFeatureBundled} {
		assert.Contains(t, c.Requires, feature)
		assert.Contains(t, c.Source.Features, feature)
	}
	assert.Equal(t, 2, importItem(c, "20").FileList)
	require.Len(t, importItem(c, "21").Patches, 1)
	assert.Equal(t, core.CollectionItemPatch{FileID: "210", Path: `Mod\manifest.json`, CRC32: c5CRC(c5ManifestV40)}, importItem(c, "21").Patches[0])
	assert.Zero(t, c.RestrictedPatches)
	require.Len(t, c.Bundled, 2)
	assert.Equal(t, "marker-v1-0-0", c.Bundled[0].Item)
	assert.Equal(t, "nexusmods-abcdef.marker-v1-0-0", c.Bundled[0].ModID)
	assert.Empty(t, c.Bundled[0].Restricted)
	assert.NotEmpty(t, c.Bundled[1].Restricted, "the helper's DLL is code")
	states := map[string]core.CollectionItemState{}
	for _, it := range c.Source.Items {
		states[it.Name] = it.State
	}
	assert.Equal(t, core.CollectionItemBundled, states["Marker"])
	assert.False(t, c.Container, "a source collection is not a container")

	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"helper-v1"}, result.BundledSkipped, "a bundle holding code is held back, as an .lmmc's is")

	for rel, want := range map[string]string{
		"r6/scripts/A.reds": "a", "r6/scripts/b.reds": "b",
		"Mod/manifest.json": c5ManifestV41, "Mod/other.json": "{}",
		"Collection Marker/manifest.json": `{"marker": true}`,
	} {
		got, ok := f.deployed(t, rel)
		assert.True(t, ok, "%s is deployed", rel)
		assert.Equal(t, want, got, rel)
	}
	_, ok := f.deployed(t, "r6/extra/unlisted.txt")
	assert.False(t, ok, "a file the list does not name is never installed")
	_, ok = f.deployed(t, "bin/helper.dll")
	assert.False(t, ok)

	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Replicated")
	require.NoError(t, err)
	twenty := saved.FindRef("nexusmods", "20")
	require.NotNil(t, twenty)
	assert.Len(t, twenty.FileList["200"], 2)
	assert.Equal(t, `r6\scripts\A.reds`, twenty.FileList["200"][0].Path, "recorded as the curator wrote it")
	sum := sha256.Sum256(patch)
	sha := hex.EncodeToString(sum[:])
	twentyOne := saved.FindRef("nexusmods", "21")
	require.NotNil(t, twentyOne)
	assert.Equal(t, []domain.FilePatch{{Path: `Mod\manifest.json`, CRC32: c5CRC(c5ManifestV40), SHA256: sha}}, twentyOne.Patches["210"])
	assert.NotNil(t, saved.FindRef(domain.SourceCollection, "nexusmods-abcdef.marker-v1-0-0"))
	assert.Nil(t, saved.FindRef(domain.SourceCollection, "nexusmods-abcdef.helper-v1"), "held back")

	collections := filepath.Join(f.cacheDir, "skyrim", "collections")
	assert.FileExists(t, filepath.Join(collections, "patches", sha+".diff"), "the patch is kept, by its sha256")
	assert.FileExists(t, filepath.Join(collections, "nexusmods-abcdef", "3.json"), "the bundled items' index, beside the recipe")

	for _, id := range []string{"20", "21"} {
		row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", id, "skyrim", "Replicated")
		require.NoError(t, err)
		assert.Len(t, row.Variant, 12, "mod %s's tree is a variant", id)
	}

	// A second import of the same collection finds every tree cached: the
	// variant a plan expects is the one the install made.
	again := f.plan(t, core.SourceCollectionImportRequest{ProfileName: "Again"})
	cached := map[string]bool{}
	for _, ref := range again.AlreadyCached {
		cached[ref.ModID] = true
	}
	assert.True(t, cached["20"] && cached["21"], "already cached: %+v, missing: %+v", again.AlreadyCached, again.Missing)
}

// TestSourceCollection_DeployRebuildsTheTreeFromTheProfile: with the
// mods' cache entries gone, a deploy rebuilds each from the profile's
// recorded file list and patches - deploy's self-heal, which commits only
// into the row's own variant, so a rebuild that named another tree would
// refuse.
func TestSourceCollection_DeployRebuildsTheTreeFromTheProfile(t *testing.T) {
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, c5Patch(t)))
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "21", "skyrim", "Replicated")
	require.NoError(t, err)
	variant := row.Variant

	// Every mod's entry - the bundled item's too, which the lmm-collection
	// source rebuilds from the recipe's kept bundled/ tree.
	entries, err := os.ReadDir(filepath.Join(f.cacheDir, "skyrim"))
	require.NoError(t, err)
	removed := 0
	for _, e := range entries {
		if e.Name() != "collections" {
			require.NoError(t, os.RemoveAll(filepath.Join(f.cacheDir, "skyrim", e.Name())))
			removed++
		}
	}
	require.Equal(t, 3, removed, "%v", entries)
	require.NoError(t, os.RemoveAll(f.game.ModPath))
	require.NoError(t, os.MkdirAll(f.game.ModPath, 0o755))
	_, err = f.svc.DeployProfile(context.Background(), f.game, "Replicated", core.DeployOptions{All: true}, nil)
	require.NoError(t, err)
	row, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "21", "skyrim", "Replicated")
	require.NoError(t, err)
	assert.Equal(t, variant, row.Variant)
	got, _ := f.deployed(t, "Mod/manifest.json")
	assert.Equal(t, c5ManifestV41, got, "patched again")
	got, _ = f.deployed(t, "r6/scripts/b.reds")
	assert.Equal(t, "b", got)
	_, ok := f.deployed(t, "r6/extra/unlisted.txt")
	assert.False(t, ok, "filtered again")
	got, _ = f.deployed(t, "Collection Marker/manifest.json")
	assert.Equal(t, `{"marker": true}`, got, "the bundled item, from the recipe's kept tree")
}

// TestSourceCollection_PatchBaselineMismatchRefusesTheImport: a patch whose
// target is not the file it was made against refuses the whole collection
// - a deliberate divergence from Vortex, which installs it unpatched - and
// nothing is written.
func TestSourceCollection_PatchBaselineMismatchRefusesTheImport(t *testing.T) {
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC("another baseline")), c5Trees(t, c5Patch(t)))
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	require.Empty(t, plan.Collection.Unsupported, "the plan cannot see the archive")
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTreeInput)
	assert.Contains(t, err.Error(), "CRC32")
	_, err = config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Replicated")
	assert.Error(t, err, "nothing was written, the profile included")
	_, ok := f.deployed(t, "r6/scripts/A.reds")
	assert.False(t, ok)
}

// TestSourceCollection_PatchToCodeNeedsAllowAnyOverrides: a patch to a DLL
// is listed as restricted, and the import refuses without
// --allow-any-overrides rather than install the mod unpatched.
func TestSourceCollection_PatchToCodeNeedsAllowAnyOverrides(t *testing.T) {
	patch := c5Patch(t)
	g := newC5Fixture(t, c5Recipe(`Mod\Helper.dll`, c5CRC(c5ManifestV40)), func(dir string) {
		c5Trees(t, patch)(dir)
		p := filepath.Join(dir, "patches", "Twenty-One", "Mod", "Helper.dll.diff")
		require.NoError(t, os.WriteFile(p, patch, 0o644))
	})
	g.src.AddDownload("210", createTestZipBytes(t, map[string]string{"Mod/Helper.dll": c5ManifestV40}))
	plan := g.plan(t, core.SourceCollectionImportRequest{})
	require.Equal(t, 1, plan.Collection.RestrictedPatches)
	assert.NotEmpty(t, importItem(plan.Collection, "21").Patches[0].Restricted)

	_, err := g.svc.ApplyCollectionImport(context.Background(), g.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.ErrorIs(t, err, core.ErrCodePatch)
	assert.True(t, core.IsInvalidCollection(err), "the caller's to answer")
	_, err = config.LoadProfile(g.svc.ConfigDir(), "skyrim", "Replicated")
	assert.Error(t, err, "nothing was written")

	plan = g.plan(t, core.SourceCollectionImportRequest{})
	_, err = g.svc.ApplyCollectionImport(context.Background(), g.game, plan, core.ProfileImportOptions{Install: true, AllowAnyOverrides: true}, nil)
	require.NoError(t, err)
	got, _ := g.deployed(t, "Mod/Helper.dll")
	assert.Equal(t, c5ManifestV41, got)
}

// TestSourceCollection_MissingOrForeignPatchIsRefusedAtPlan: a patch the
// recipe does not carry, or carries in another format, refuses its item
// in the plan.
func TestSourceCollection_MissingOrForeignPatchIsRefusedAtPlan(t *testing.T) {
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), func(dir string) {
		c5Trees(t, []byte("HDIFF13&not a bsdiff"))(dir)
	})
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	require.Len(t, plan.Collection.Unsupported, 1)
	assert.Equal(t, core.CollectionFeaturePatchFormat, plan.Collection.Unsupported[0].Feature)

	g := newC5Fixture(t, c5Recipe(`Mod\elsewhere.json`, c5CRC(c5ManifestV40)), c5Trees(t, c5Patch(t)))
	plan = g.plan(t, core.SourceCollectionImportRequest{})
	require.Len(t, plan.Collection.Unsupported, 1)
	assert.Equal(t, core.CollectionFeatureManifestKeyPrefix+"mods.patches", plan.Collection.Unsupported[0].Feature)
	for _, it := range plan.Collection.Source.Items {
		if it.Name == "Twenty-One" {
			assert.Contains(t, it.Note, "holds no patch")
		}
	}
}

// TestCollectionExport_CarriesPatchesInTheContainer: a profile whose mods
// install with patches exports them in the .lmmc's patches/, and the
// container imports on another machine with every patch kept there.
func TestCollectionExport_CarriesPatchesInTheContainer(t *testing.T) {
	patch := c5Patch(t)
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, patch))
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	export, err := f.svc.ExportCollection(context.Background(), "skyrim", "Replicated", core.CollectionExportOptions{})
	require.NoError(t, err)
	assert.Contains(t, string(export.Document), "file_list:")
	assert.Contains(t, string(export.Document), "patches:")
	assert.True(t, strings.Contains(strings.Join(export.Notes, "\n"), "binary patch"), "%v", export.Notes)
	container, err := export.Container()
	require.NoError(t, err)
	sum := sha256.Sum256(patch)
	assert.Contains(t, string(container), "patches/"+hex.EncodeToString(sum[:])+".diff")

	// The recipient has the game and the source, and no patch.
	recv := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, patch))
	rplan, err := recv.svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: container, GameID: "skyrim", ProfileName: "Received"})
	require.NoError(t, err)
	require.Empty(t, rplan.Collection.Unsupported)
	_, err = recv.svc.ApplyCollectionImport(context.Background(), recv.game, rplan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	got, _ := recv.deployed(t, "Mod/manifest.json")
	assert.Equal(t, c5ManifestV41, got)

	// A plain YAML document carries no patch: a machine without it
	// refuses, and one that keeps it (the recipient, now) imports.
	plain := fmt.Sprintf("lmm_collection: 1\nname: plain\ngame: {name: Skyrim}\nrequires: [patches]\nmods:\n"+
		"  - source_id: nexusmods\n    mod_id: \"21\"\n    file_ids: [\"210\"]\n    patches:\n      \"210\": [{path: 'Mod\\manifest.json', crc32: %s, sha256: %s}]\n",
		c5CRC(c5ManifestV40), hex.EncodeToString(sum[:]))
	other := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, patch))
	_, err = other.svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(plain), GameID: "skyrim"})
	require.ErrorIs(t, err, core.ErrInvalidCollection)
	assert.Contains(t, err.Error(), "carries no patch")
	_, err = recv.svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(plain), GameID: "skyrim"})
	require.NoError(t, err, "the store keeps it")
}

// TestSourceCollection_FreeAccountInstallsBundledItems: a free account's
// import saves every item to download pending, and still installs the
// bundled items, which the recipe carries.
func TestSourceCollection_FreeAccountInstallsBundledItems(t *testing.T) {
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, c5Patch(t)))
	f.src.tier = source.TierFree
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	require.True(t, plan.Collection.Source.ManualDownload)
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	got, ok := f.deployed(t, "Collection Marker/manifest.json")
	assert.True(t, ok)
	assert.Equal(t, `{"marker": true}`, got)
	_, ok = f.deployed(t, "Mod/manifest.json")
	assert.False(t, ok, "a manual download is pending")
	assert.Equal(t, 2, result.Skipped, "the two items to download by hand")
}

// TestSourceCollection_InstallFromFileAppliesTheFileListAndPatch: a free
// account's pending item installed from the file the user fetched (`lmm
// install --id … --from-file`, the archive import) installs by the file's
// list and patch, as a download would, into the variant the profile names.
func TestSourceCollection_InstallFromFileAppliesTheFileListAndPatch(t *testing.T) {
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, c5Patch(t)))
	f.src.tier = source.TierFree
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	for id, files := range map[string]map[string]string{
		"20": {"r6/scripts/A.reds": "a", "r6/scripts/b.reds": "b", "r6/extra/unlisted.txt": "x"},
		"21": {"Mod/manifest.json": c5ManifestV40, "Mod/other.json": "{}"},
	} {
		fileID := map[string]string{"20": "200", "21": "210"}[id]
		// Named as the source names the file, so it matches it.
		archive := filepath.Join(t.TempDir(), id+"-"+fileID+".zip")
		require.NoError(t, os.WriteFile(archive, createTestZipBytes(t, files), 0o644))
		opts := core.ImportArchiveOptions{SourceID: "nexusmods", ModID: id, InstallFromFile: true, ExpectedFileID: fileID}
		iplan, err := f.svc.PlanImportArchive(context.Background(), f.game, "Replicated", archive, opts)
		require.NoError(t, err)
		_, err = f.svc.ApplyImportArchive(context.Background(), f.game, "Replicated", iplan, opts, nil)
		require.NoError(t, err)
	}
	got, _ := f.deployed(t, "Mod/manifest.json")
	assert.Equal(t, c5ManifestV41, got, "patched")
	_, ok := f.deployed(t, "r6/extra/unlisted.txt")
	assert.False(t, ok, "filtered")

	// The rows name the trees the profile records: nothing to converge.
	applyPlan, err := f.svc.PlanProfileApply(context.Background(), f.game, "Replicated")
	require.NoError(t, err)
	assert.True(t, applyPlan.NoChanges, "%+v", applyPlan)
}

// TestCollectionUpdate_CarriesFileListsPatchesAndBundles (#610 x #613): a
// revision update of a collection with a file list, a patch and a bundled
// item plans from the new revision's recipe - its bundled trees and
// patches, not a container's - and applies, keeping the new revision's
// bundled items and patches as an import does.
func TestCollectionUpdate_CarriesFileListsPatchesAndBundles(t *testing.T) {
	f := newC5Fixture(t, c5Recipe(`Mod\manifest.json`, c5CRC(c5ManifestV40)), c5Trees(t, c5Patch(t)))
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	f.src.recipe.collection.Revision = 4
	update, err := f.svc.PlanCollectionUpdate(context.Background(), f.game, core.CollectionUpdateRequest{Profile: "Replicated"})
	require.NoError(t, err)
	require.Empty(t, update.Unsupported)
	assert.Equal(t, 4, update.NewVersion)
	assert.Zero(t, update.RestrictedPatches)
	_, err = f.svc.ApplyCollectionUpdate(context.Background(), f.game, update, core.CollectionUpdateOptions{}, nil)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.cacheDir, "skyrim", "collections", "nexusmods-abcdef", "4.json"), "the new revision's bundled items are kept")
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Replicated")
	require.NoError(t, err)
	assert.NotNil(t, saved.FindRef(domain.SourceCollection, "nexusmods-abcdef.marker-v1-0-0"))
	assert.Len(t, saved.FindRef("nexusmods", "21").Patches["210"], 1)
	got, _ := f.deployed(t, "Mod/manifest.json")
	assert.Equal(t, c5ManifestV41, got)
}
