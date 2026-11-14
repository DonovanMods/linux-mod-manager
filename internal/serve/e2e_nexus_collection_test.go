package serve_test

// #607: importing a NexusMods collection from the browser - the profiles
// modal's NexusMods input, the plan view listing every item with what the
// import does with it, a per-item toggle that opts an optional item in (by
// re-planning, so the preview is what Confirm submits), and the import. The
// source is a double over a free account, and the collection's recipe is
// already in its cache entry, so no 7z and no network are needed.

import (
	"context"
	"encoding/json/v2"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eNexusSource is a NexusMods double with one collection, abcdef rev 3.
type e2eNexusSource struct{ *fakeSource }

func (e2eNexusSource) ResolveGameCollection(_ context.Context, gameSourceID, _ string) (source.Collection, error) {
	return source.Collection{ID: "abcdef", Name: "Three Mods", Revision: 3, Author: "curator",
		URL: "https://www.nexusmods.com/games/" + gameSourceID + "/collections/abcdef", Handle: "/h",
		Items: []source.CollectionItem{
			{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten",
				URL: "https://www.nexusmods.com/" + gameSourceID + "/mods/10?tab=files&file_id=100"},
		}}, nil
}

func (e2eNexusSource) ManifestArchiveURL(context.Context, source.Collection) (string, error) {
	return "", source.ErrNotSupported // cached: never fetched
}

func (e2eNexusSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	data, err := fs.ReadFile(root, "collection.json")
	if err != nil {
		return source.CollectionManifest{}, err
	}
	var m source.CollectionManifest
	return m, json.Unmarshal(data, &m)
}

func (e2eNexusSource) AccountTier(context.Context) (source.Tier, error) { return source.TierFree, nil }

const e2eNexusRecipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"},
	{"Name": "Eleven", "Version": "1.1", "SourceType": "nexus", "ModID": "11", "FileID": "110", "UpdatePolicy": "latest"},
	{"Name": "Twelve", "Version": "2.0", "SourceType": "nexus", "ModID": "12", "FileID": "120", "UpdatePolicy": "exact", "Optional": true}],
	"Tools": [{"Name": "SSEEdit", "Exe": "Data\\SSEEdit.exe"}]}`

func newE2ENexusCollectionFixture(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	f.Svc.RegisterSource(e2eNexusSource{newFakeSource("nexusmods")})
	f.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	root := filepath.Join(f.Svc.GlobalCacheDir(), f.Game.ID)
	if f.Game.CachePath != "" {
		root = f.Game.CachePath
	}
	entry := filepath.Join(root, "collections", "nexusmods-abcdef", "3")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "collection.json"), []byte(e2eNexusRecipe), 0o644))
	return f
}

func TestE2E_NexusCollection_ProfilesModalPlansTogglesAndImports(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)

	var before, after string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="nexus-collection-ref"]`, chromedp.ByQuery),
		// The profile list fills in after the modal opens and moves the form
		// down (#584): wait for it before pressing anything.
		chromedp.WaitVisible(`.profiles-row`, chromedp.ByQuery),
		chromedp.SetValue(`[data-testid="nexus-collection-ref"]`, "abcdef", chromedp.ByQuery),
		clickWhenSettled(`[data-testid="nexus-collection-import"]`),
		chromedp.WaitVisible(`[data-testid="source-items"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &before),
		// The optional item's toggle re-plans with it opted in.
		clickWhenSettled(`[data-item="12:120"] [data-testid="source-optional-toggle"]`),
		pollUntil(`document.querySelector('[data-item="12:120"]')?.dataset.state === "manual_download"`),
		textContent(`.modal[data-kind="profile_import"]`, &after),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)

	assert.Contains(t, before, "3 items at revision 3")
	assert.Contains(t, before, "download by hand", "a free account's items are manual downloads")
	assert.Contains(t, before, "optional - not included", "optional items are off by default")
	assert.Contains(t, before, "file page")
	assert.Contains(t, before, "curator asks for the latest file")
	assert.Contains(t, before, "SSEEdit", "the curator's tools are listed")
	assert.Contains(t, before, "are manual downloads", "core's note says why, once")
	assert.NotContains(t, before, "Download and install", "a free account is offered no install it cannot do")
	assert.NotContains(t, after, "optional - not included", "the toggle opted the item in")

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Three Mods")
		return err == nil && len(p.Mods) == 3
	}, 15*time.Second, 50*time.Millisecond, "the import saves the profile with every item, the opted-in one included")
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abcdef", Revision: 3,
		URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Author: "curator"}, p.Collection.Origin)
	assert.Empty(t, f.BrowserErrors())
}
