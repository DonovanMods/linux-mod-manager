package serve_test

// #623 in a browser: a .lmmc carrying its author's own files - the plan
// view lists each bundled item, and the one holding code is held back
// until its box is ticked - and the profiles modal's "Export with my
// files" form, whose download carries the ticked mod's files.

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eBundledContainer packs a document naming two bundled items - patch
// (a text file) and tool (a DLL) - with their files.
func e2eBundledContainer(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, m := range []struct{ name, content string }{
		{"collection.yaml", "lmm_collection: 1\nid: c0ffee\nname: bundled\ntitle: Bundled Setup\ngame: {name: G}\nrequires: [bundled]\nmods:\n" +
			"  - {source_id: lmm-collection, mod_id: patch, name: My Patch}\n" +
			"  - {source_id: lmm-collection, mod_id: tool, name: My Tool}\n"},
		{"files/patch/patch.esp", "PATCH"},
		{"files/tool/helper.dll", "MZ\x90\x00"},
	} {
		hdr := &zip.FileHeader{Name: m.name, Method: zip.Deflate}
		hdr.SetMode(0o644)
		f, err := w.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = f.Write([]byte(m.content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// openProfilesModal opens the profiles modal from the profile picker.
func openProfilesModal() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		settleEffects(),
	}
}

func TestE2E_BundledCollection_PlanListsItemsAndHoldsCodeBack(t *testing.T) {
	f := newE2EFixture(t)
	importPath := filepath.Join(t.TempDir(), "bundled.lmmc")
	require.NoError(t, os.WriteFile(importPath, e2eBundledContainer(t), 0o644))

	var bundled, held string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesModal(),
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="collection-bundled"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-bundled"]`, &bundled),
		textContent(`[data-testid="import-restricted-bundled"]`, &held),
	)
	assert.Contains(t, bundled, "My Patch")
	assert.Contains(t, bundled, "files/patch/")
	assert.Contains(t, bundled, "My Tool")
	assert.Contains(t, held, "helper.dll")
	assert.NotContains(t, held, "patch.esp", "a text file is not held back")

	// Left unticked: the tool is held back, the patch imported.
	f.runInBrowser(t,
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		waitGone(`.modal[data-kind="profile_import"]`),
	)
	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "bundled")
		return err == nil && len(p.Mods) == 1 && p.Mods[0].ModID == "c0ffee.patch"
	}, 10*time.Second, 50*time.Millisecond, "the item holding code is held back, the other imported")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_BundledCollection_TickedCodeIsImported(t *testing.T) {
	f := newE2EFixture(t)
	importPath := filepath.Join(t.TempDir(), "bundled.lmmc")
	require.NoError(t, os.WriteFile(importPath, e2eBundledContainer(t), 0o644))
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesModal(),
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="import-bundled-code"]`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="import-bundled-code"]`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		waitGone(`.modal[data-kind="profile_import"]`),
	)
	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "bundled")
		return err == nil && len(p.Mods) == 2
	}, 10*time.Second, 50*time.Millisecond, "the ticked box imports the item holding code")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_BundledCollection_ExportWithMyFiles(t *testing.T) {
	f := newE2EFixture(t)
	ctx := t.Context()
	key := cache.Key{GameID: f.Game.ID, SourceID: domain.SourceLocal, ModID: "u-1", Version: "1.0"}
	require.NoError(t, f.Svc.GetGameCache(f.Game).Store(key, "tweaks/a.ini", []byte("[T]\n")))
	require.NoError(t, f.Svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "u-1", SourceID: domain.SourceLocal, Name: "My Tweaks", Version: "1.0", GameID: f.Game.ID},
		ProfileName: f.Profile, Enabled: true,
	}))
	require.NoError(t, f.Svc.NewProfileManager().AddMod(ctx, f.Game.ID, f.Profile,
		domain.ModReference{SourceID: domain.SourceLocal, ModID: "u-1", Version: "1.0"}))

	var href string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesModal(),
		chromedp.Click(fmt.Sprintf(`[data-action="export-bundle"][data-profile=%q]`, f.Profile), chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="bundle-candidate"]`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="bundle-candidate"][data-mod="local:u-1"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.AttributeValue(`[data-action="export-bundle-download"]`, "href", &href, nil, chromedp.ByQuery),
		chromedp.Click(`[data-action="export-bundle-download"]`, chromedp.ByQuery),
	)
	assert.Contains(t, href, "bundle=local%3Au-1")
	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(ctx, f.Game.ID, f.Profile)
		return err == nil && p.Collection != nil && p.Collection.ID != ""
	}, 5*time.Second, 20*time.Millisecond, "the export records the collection identity before downloading")

	var exportedB64 string
	f.runInBrowser(t, chromedp.Evaluate(fmt.Sprintf(`fetch(%q).then((r) => r.arrayBuffer()).then((b) => {
		const u = new Uint8Array(b); let s = "";
		for (let i = 0; i < u.length; i++) s += String.fromCharCode(u[i]);
		return btoa(s);
	})`, href), &exportedB64,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	container, err := base64.StdEncoding.DecodeString(exportedB64)
	require.NoError(t, err)
	require.True(t, core.IsCollectionContainer(container))
	zr, err := zip.NewReader(bytes.NewReader(container), int64(len(container)))
	require.NoError(t, err)
	var names []string
	for _, zf := range zr.File {
		names = append(names, zf.Name)
	}
	assert.Contains(t, names, "files/my-tweaks/tweaks/a.ini", "the ticked mod's files travel in the container")
	assert.Empty(t, f.BrowserErrors())
}
