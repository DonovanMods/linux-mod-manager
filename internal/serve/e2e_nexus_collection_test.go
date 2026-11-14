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

func (e2eNexusSource) ResolveGameCollection(_ context.Context, gameSourceID, ref string) (source.Collection, error) {
	return e2eNexusCollection(gameSourceID, ref), nil
}

// e2eNexusCollection is collection ref (rev 3) for gameSourceID.
func e2eNexusCollection(gameSourceID, ref string) source.Collection {
	return source.Collection{ID: ref, Name: "Three Mods", Revision: 3, Author: "curator",
		URL: "https://www.nexusmods.com/games/" + gameSourceID + "/collections/abcdef", Handle: "/h",
		Items: []source.CollectionItem{
			{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten",
				URL: "https://www.nexusmods.com/" + gameSourceID + "/mods/10?tab=files&file_id=100"},
		}}
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

// e2eUnsupportedRecipe has a required item of a mod type no adapter here
// routes, so its import is refused.
const e2eUnsupportedRecipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact", "ModType": "SMAPI"}]}`

// seedE2ENexusRecipe puts slug's recipe (rev 3) in game's cache entry.
func seedE2ENexusRecipe(t *testing.T, f e2eFixture, slug, recipe string) {
	t.Helper()
	root := filepath.Join(f.Svc.GlobalCacheDir(), f.Game.ID)
	if f.Game.CachePath != "" {
		root = f.Game.CachePath
	}
	entry := filepath.Join(root, "collections", "nexusmods-"+slug, "3")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "collection.json"), []byte(recipe), 0o644))
}

func newE2ENexusCollectionFixture(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	f.Svc.RegisterSource(e2eNexusSource{newFakeSource("nexusmods")})
	f.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "abcdef", e2eNexusRecipe)
	seedE2ENexusRecipe(t, f, "unsup", e2eUnsupportedRecipe)
	return f
}

// openNexusImport opens the profiles modal and plans the NexusMods
// collection ref.
func openNexusImport(ref string) chromedp.Tasks {
	return chromedp.Tasks{
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
		chromedp.SetValue(`[data-testid="nexus-collection-ref"]`, ref, chromedp.ByQuery),
		clickWhenSettled(`[data-testid="nexus-collection-import"]`),
		chromedp.WaitVisible(`[data-testid="source-items"]`, chromedp.ByQuery),
	}
}

func TestE2E_NexusCollection_ProfilesModalPlansTogglesAndImports(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)

	var before, after string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("abcdef"),
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

// #607 C2b review, nits 10 and 11: a collection lmm cannot import shows the
// refusal before anything is written, naming each missing feature with the
// items that raise it.
func TestE2E_NexusCollection_UnsupportedBannerNamesTheItems(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)
	var banner string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("unsup"),
		chromedp.WaitVisible(`[data-testid="collection-unsupported"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-unsupported"]`, &banner),
	)
	assert.Contains(t, banner, "mod_type:SMAPI (nexusmods:10)")
	assert.Empty(t, f.BrowserErrors())
}

// e2ePremiumNexusSource is a NexusMods double over a Premium account whose
// files really download (the search harness's download server).
type e2ePremiumNexusSource struct{ *e2eSearchSource }

func (e2ePremiumNexusSource) ResolveGameCollection(_ context.Context, gameSourceID, ref string) (source.Collection, error) {
	return e2eNexusCollection(gameSourceID, ref), nil
}

func (e2ePremiumNexusSource) ManifestArchiveURL(context.Context, source.Collection) (string, error) {
	return "", source.ErrNotSupported
}

func (e2ePremiumNexusSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	return e2eNexusSource{}.ParseManifest(root)
}

func (e2ePremiumNexusSource) AccountTier(context.Context) (source.Tier, error) {
	return source.TierPremium, nil
}

// #607 C2b review, nit 11: on a Premium account the plan offers the
// install, and Confirm downloads and installs every item.
func TestE2E_NexusCollection_PremiumAccountInstalls(t *testing.T) {
	sandboxE2EEnv(t)
	src := newE2ESearchSource(t, "nexusmods")
	src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "10", SourceID: "nexusmods", Name: "Ten", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "100", Name: "Main", FileName: "ten.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"100": "ten.esp"},
	})
	src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "11", SourceID: "nexusmods", Name: "Eleven", Version: "1.1"},
		files:   []domain.DownloadableFile{{ID: "110", Name: "Main", FileName: "eleven.zip", Version: "1.1", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"110": "eleven.esp"},
	})
	f := newE2EFixture(t)
	f.Svc.RegisterSource(e2ePremiumNexusSource{src})
	f.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "abcdef", e2eNexusRecipe)

	var plan string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("abcdef"),
		textContent(`.modal[data-kind="profile_import"]`, &plan),
		chromedp.Click(`.modal .plan__control input[type="checkbox"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Contains(t, plan, "will be installed")
	assert.Contains(t, plan, "Download and install 2 pending mods")
	assert.NotContains(t, plan, "download by hand")

	require.Eventually(t, func() bool {
		rows, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, "Three Mods")
		return err == nil && len(rows) == 2
	}, 15*time.Second, 50*time.Millisecond, "both items downloaded and installed")
	row, err := f.Svc.GetInstalledMod(t.Context(), "nexusmods", "11", f.Game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, row.UpdatePolicy, "the latest item follows its newest file")
	assert.Empty(t, f.BrowserErrors())
}

// e2eSMAPIRecipe is a Stardew Valley collection whose SMAPI item states
// Vortex's "SMAPI" mod type (#635).
const e2eSMAPIRecipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "SMAPI - Stardew Modding API", "Version": "4.0", "SourceType": "nexus", "ModID": "2400", "FileID": "240", "UpdatePolicy": "exact", "ModType": "SMAPI"},
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"}]}`

// #635, owner ruling: the plan view lists a Stardew Valley collection's SMAPI
// as a step to take by hand - its page and SMAPI's own installer - and
// refuses nothing for it.
func TestE2E_NexusCollection_SMAPIIsAPrerequisiteStep(t *testing.T) {
	f := newE2EFixture(t)
	f.Svc.RegisterSource(e2eNexusSource{newFakeSource("nexusmods")})
	f.Game.SourceIDs["nexusmods"] = "stardewvalley"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "smapi", e2eSMAPIRecipe)

	var item, href string
	var unsupported bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("smapi"),
		textContent(`[data-item="2400:240"]`, &item),
		chromedp.Evaluate(`document.querySelector('[data-item="2400:240"] a')?.getAttribute("href") ?? ""`, &href),
		chromedp.Evaluate(`document.querySelector('[data-testid="collection-unsupported"]') !== null`, &unsupported),
	)
	assert.Contains(t, item, "get by hand")
	assert.Contains(t, item, "where to get it")
	assert.Contains(t, item, "own installer")
	assert.Contains(t, item, "install on Linux.sh")
	assert.NotContains(t, item, "type SMAPI", "the type is the prerequisite's, not a deploy route")
	assert.Equal(t, "https://www.nexusmods.com/stardewvalley/mods/2400", href)
	assert.False(t, unsupported, "nothing blocks the import")
	assert.Empty(t, f.BrowserErrors())
}
