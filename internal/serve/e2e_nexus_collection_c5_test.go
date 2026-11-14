package serve_test

// #610 C5 in a browser: a NexusMods collection with an exact file list, a
// patch to a DLL and a bundled item. The plan view lists each - the
// bundled item's state, the items' tags, every file list and patch with
// its baseline - and the patch to code needs its box ticked: Confirm
// without it is refused with nothing written, with it the profile is
// saved recording the file list and the patch.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const e2eC5Recipe = `{"Name": "Replicated", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact",
	 "FileList": [{"Path": "Ten\\a.json", "MD5": "0cc175b9c0f1b6a831c399e269772661"}, {"Path": "Ten/b.json", "MD5": "92eb5ffee6ae2fec3ad71c777531578f"}]},
	{"Name": "Eleven", "Version": "1.1", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "exact",
	 "Patches": {"Eleven\\Eleven.dll": "CABDDFC4"}},
	{"Name": "Marker", "Version": "1.0.0", "SourceType": "bundle", "UpdatePolicy": "exact", "FileExpression": "Bundled - Marker (v1.0.0)"}]}`

// seedE2EC5Recipe is seedE2ENexusRecipe with the recipe's bundled/ and
// patches/ trees, extracted as fetchCollectionRecipe leaves them.
func seedE2EC5Recipe(t *testing.T, f e2eFixture) {
	t.Helper()
	seedE2ENexusRecipe(t, f, "c5", e2eC5Recipe)
	root := filepath.Join(f.Svc.GlobalCacheDir(), f.Game.ID)
	if f.Game.CachePath != "" {
		root = f.Game.CachePath
	}
	entry := filepath.Join(root, "collections", "nexusmods-c5", "3")
	for rel, data := range map[string][]byte{
		"bundled/Bundled - Marker (v1.0.0)/[CP] Marker/manifest.json": []byte(`{"Name": "Marker"}`),
		"patches/Eleven/Eleven/Eleven.dll.diff":                       append([]byte("BSDIFF40"), make([]byte, 24)...),
		".lmm-recipe-files":                                           nil,
	} {
		p := filepath.Join(entry, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, data, 0o644))
	}
}

func TestE2E_NexusCollection_FileListsPatchesAndBundles(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)
	seedE2EC5Recipe(t, f)

	var items, inputs, gate, marker string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("c5"),
		chromedp.WaitVisible(`[data-testid="collection-tree-inputs"]`, chromedp.ByQuery),
		textContent(`[data-testid="source-items"]`, &items),
		textContent(`[data-testid="collection-tree-inputs"]`, &inputs),
		textContent(`[data-testid="import-restricted-patches"]`, &gate),
		chromedp.AttributeValue(`[data-testid="source-items"] [data-state="bundled"]`, "data-item", &marker, nil, chromedp.ByQuery),
	)
	assert.Contains(t, items, "bundled in the collection")
	assert.Contains(t, items, "exact file list of 2")
	assert.Contains(t, items, "1 patch")
	assert.Equal(t, "item:2", marker)
	assert.Contains(t, inputs, "installs exactly 2 listed files")
	assert.Contains(t, inputs, `patches Eleven\Eleven.dll (baseline CRC32 CABDDFC4)`)
	assert.Contains(t, gate, "code the system runs (.dll)")
	assert.Contains(t, gate, "the import is refused")

	// Unticked: refused, nothing written.
	f.runInBrowser(t,
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		waitGone(`.modal[data-kind="profile_import"]`),
		bellCountIs("1"),
	)
	_, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Replicated")
	require.Error(t, err, "a patch to code refuses the import without its box")

	// Ticked: the profile records the file list, the patch and the bundled
	// item.
	f.runInBrowser(t,
		// A fresh page: the refusal's toast would cover Confirm.
		chromedp.Navigate(f.HomePath()),
		openNexusImport("c5"),
		chromedp.WaitVisible(`[data-testid="import-patch-code"]`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="import-patch-code"]`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		waitGone(`.modal[data-kind="profile_import"]`),
	)
	require.Eventually(t, func() bool {
		_, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Replicated")
		return err == nil
	}, 15*time.Second, 50*time.Millisecond)
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Replicated")
	require.NoError(t, err)
	ten := p.FindRef("nexusmods", "10")
	require.NotNil(t, ten)
	assert.Len(t, ten.FileList["100"], 2)
	eleven := p.FindRef("nexusmods", "11")
	require.NotNil(t, eleven)
	assert.Len(t, eleven.Patches["110"], 1)
	assert.NotNil(t, p.FindRef(domain.SourceCollection, "nexusmods-c5.marker-v1-0-0"))
	assert.Empty(t, f.BrowserErrors())
}
