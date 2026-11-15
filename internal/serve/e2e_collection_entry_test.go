package serve_test

// #650: importing a collection is discoverable. The search box (the omnibar
// and the dedicated search page) recognises a pasted NexusMods collection
// link and an https .lmmc link, as it already recognised a Steam Workshop
// one, and offers the import; the profile picker menu and Mission Control's
// Profile card carry an "Import collection…" entry that opens a small modal
// holding the profiles modal's own import forms.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eNexusCollectionURL is the fixture collection's page address, for the
// game the NexusMods fixture maps.
const e2eNexusCollectionURL = "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef"

// e2eNexusRefSource is e2eNexusSource reading a slug out of a page
// address the way the real source does (the double it wraps echoes the
// reference as the collection's id, which a URL cannot be), and recording
// the reference each plan sent.
type e2eNexusRefSource struct {
	e2eNexusSource
	mu   *sync.Mutex
	refs *[]string
}

func (s e2eNexusRefSource) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	s.mu.Lock()
	*s.refs = append(*s.refs, ref)
	s.mu.Unlock()
	slug := ref
	if at := strings.LastIndex(slug, "@"); at >= 0 {
		slug = slug[:at]
	}
	slug = slug[strings.LastIndex(slug, "/")+1:]
	return s.e2eNexusSource.ResolveGameCollection(ctx, gameSourceID, slug)
}

// newE2ENexusRefFixture is newE2ENexusCollectionFixture with the
// address-reading double; refs returns what each plan sent.
func newE2ENexusRefFixture(t *testing.T) (e2eFixture, func() []string) {
	t.Helper()
	f := newE2ENexusCollectionFixture(t)
	var mu sync.Mutex
	var refs []string
	f.Svc.RegisterSource(e2eNexusRefSource{e2eNexusSource{newFakeSource("nexusmods")}, &mu, &refs})
	return f, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), refs...)
	}
}

// TestE2E_CollectionLink_ParsesWhatCoreAccepts holds collectionlink.js to
// the forms the importers accept: nexusmods.ParseCollectionRef's slug,
// slug@revision and page addresses, core.parseCollectionURL's https
// address (here: one ending .lmmc), and the Workshop link the search page
// already recognised. Anything else is a search term.
func TestE2E_CollectionLink_ParsesWhatCoreAccepts(t *testing.T) {
	f := newE2EFixture(t)

	var got map[string]any
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { collectionLink } = await import("/static/app/collectionlink.js");
			const cases = {
				nexusURL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/rqhcxy",
				nexusBareHost: "https://nexusmods.com/games/skyrimspecialedition/collections/rqhcxy/",
				nexusRevisionPath: "https://www.nexusmods.com/games/skyrimspecialedition/collections/rqhcxy/revisions/4?tab=mods",
				nexusAtRevision: "https://www.nexusmods.com/games/skyrimspecialedition/collections/rqhcxy@2",
				nexusNext: "https://next.nexusmods.com/skyrimspecialedition/collections/RQHCXY",
				nexusMod: "https://www.nexusmods.com/skyrimspecialedition/mods/12604",
				slug: "rqhcxy",
				slugAt: "rqhcxy@3",
				twoWords: "rqhcxy armour",
				lmmc: "https://example.com/lists/my%20list.LMMC?token=x",
				lmmcPlain: "http://example.com/list.lmmc",
				yaml: "https://example.com/list.yaml",
				workshop: "https://steamcommunity.com/sharedfiles/filedetails/?id=3512001122",
				empty: "   ",
			};
			const out = {};
			for (const [k, v] of Object.entries(cases)) out[k] = collectionLink(v);
			return out;
		})()`, &got, awaitPromise),
	)

	nexus := func(domain, slug string, revision float64, ref string, tentative bool) map[string]any {
		return map[string]any{"source": "nexusmods", "domain": domain, "slug": slug,
			"revision": revision, "ref": ref, "tentative": tentative}
	}
	const page = "https://www.nexusmods.com/games/skyrimspecialedition/collections/rqhcxy"
	assert.Equal(t, nexus("skyrimspecialedition", "rqhcxy", 0, page, false), got["nexusURL"])
	assert.Equal(t, nexus("skyrimspecialedition", "rqhcxy", 0, page, false), got["nexusBareHost"])
	assert.Equal(t, nexus("skyrimspecialedition", "rqhcxy", 4, page+"@4", false), got["nexusRevisionPath"],
		"a revision page imports that revision, in the slug@revision form the source accepts")
	assert.Equal(t, nexus("skyrimspecialedition", "rqhcxy", 2, page+"@2", false), got["nexusAtRevision"])
	assert.Equal(t, nexus("skyrimspecialedition", "rqhcxy", 0, page, false), got["nexusNext"])
	assert.Nil(t, got["nexusMod"], "a mod page is not a collection")
	assert.Equal(t, nexus("", "rqhcxy", 0, "rqhcxy", true), got["slug"],
		"a bare slug is tentative: it is also an ordinary search word")
	assert.Equal(t, nexus("", "rqhcxy", 3, "rqhcxy@3", false), got["slugAt"])
	assert.Nil(t, got["twoWords"])
	assert.Equal(t, map[string]any{"source": "url", "url": "https://example.com/lists/my%20list.LMMC?token=x", "https": true}, got["lmmc"])
	assert.Equal(t, map[string]any{"source": "url", "url": "http://example.com/list.lmmc", "https": false}, got["lmmcPlain"])
	assert.Nil(t, got["yaml"], "only a .lmmc address is unmistakably a collection")
	assert.Equal(t, map[string]any{"source": "steamworkshop", "ref": "3512001122"}, got["workshop"])
	assert.Nil(t, got["empty"])
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_CollectionLink_AgreesWithTheSourceParser holds collectionlink.js
// to internal/source/nexusmods/testdata/collection_refs.json, the fixture
// nexusmods.parseCollectionRef is held to as well (TestParseCollectionRef):
// the search box offers exactly the references `lmm profile import
// --nexus-collection` takes, naming the same game, slug and revision.
func TestE2E_CollectionLink_AgreesWithTheSourceParser(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "source", "nexusmods", "testdata", "collection_refs.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Ref      string `json:"ref"`
			Domain   string `json:"domain"`
			Slug     string `json:"slug"`
			Revision int    `json:"revision"`
			Invalid  bool   `json:"invalid"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)

	f := newE2EFixture(t)
	type parsed struct {
		Nexus    bool   `json:"nexus"`
		Domain   string `json:"domain"`
		Slug     string `json:"slug"`
		Revision int    `json:"revision"`
	}
	var got []parsed
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { collectionLink } = await import("/static/app/collectionlink.js");
			const fixture = `+string(data)+`;
			return fixture.cases.map((c) => {
				const r = collectionLink(c.ref);
				return r && r.source === "nexusmods"
					? { nexus: true, domain: r.domain, slug: r.slug, revision: r.revision }
					: { nexus: false, domain: "", slug: "", revision: 0 };
			});
		})()`, &got, awaitPromise),
	)

	require.Len(t, got, len(fixture.Cases))
	for i, c := range fixture.Cases {
		want := parsed{Nexus: !c.Invalid, Domain: c.Domain, Slug: c.Slug, Revision: c.Revision}
		assert.Equal(t, want, got[i], "%q", c.Ref)
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_CollectionLink_SearchPageOffersTheNexusImport is the issue's
// first journey: a NexusMods collection's address pasted into search
// offers the import, and the offer opens the import's plan.
func TestE2E_CollectionLink_SearchPageOffersTheNexusImport(t *testing.T) {
	f, refs := newE2ENexusRefFixture(t)

	var offer, modal string
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, e2eNexusCollectionURL)),
		chromedp.WaitVisible(`[data-testid="collection-offer"][data-source="nexusmods"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-offer"]`, &offer),
		clickWhenSettled(`[data-testid="collection-offer"] button`),
		chromedp.WaitVisible(`[data-testid="source-collection"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &modal),
	)

	assert.Contains(t, offer, "This is a NexusMods collection")
	assert.Contains(t, modal, "Three Mods", "the collection's own plan")
	assert.Contains(t, modal, "3 items at revision 3")
	assert.Equal(t, []string{e2eNexusCollectionURL}, refs(), "the page address reaches the source as the profiles modal sends it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_CollectionLink_OmnibarOffersTheImport: the omnibar is the search
// box a user actually pastes into, so its fan-out carries the same offer.
func TestE2E_CollectionLink_OmnibarOffersTheImport(t *testing.T) {
	f, refs := newE2ENexusRefFixture(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, e2eNexusCollectionURL+"\r", chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results [data-testid="collection-offer"][data-source="nexusmods"]`, chromedp.ByQuery),
		clickWhenSettled(`.omnibar-results [data-testid="collection-offer"] button`),
		chromedp.WaitVisible(`[data-testid="source-collection"]`, chromedp.ByQuery),
	)
	assert.Equal(t, []string{e2eNexusCollectionURL}, refs())
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_CollectionLink_AnotherGamesCollectionSaysSo: an address for a
// NexusMods game other than the selected one's is named as such, and offers
// no import the source would refuse.
func TestE2E_CollectionLink_AnotherGamesCollectionSaysSo(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)

	var offer string
	var buttons int
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, "https://www.nexusmods.com/games/fallout4/collections/abcdef")),
		chromedp.WaitVisible(`[data-testid="collection-offer"][data-source="nexusmods"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-offer"]`, &offer),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="collection-offer"] button').length`, &buttons),
	)

	assert.Contains(t, offer, "fallout4")
	assert.Contains(t, offer, "not "+f.Game.Name)
	assert.Zero(t, buttons, "the source refuses another game's collection")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_CollectionLink_BareSlugOffersOnlyWhenNothingMatched: a bare slug
// is also an ordinary search word, so it is offered as a collection only
// when the search found nothing for it.
func TestE2E_CollectionLink_BareSlugOffersOnlyWhenNothingMatched(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)

	var offer string
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, "abcdef")),
		chromedp.WaitVisible(`[data-testid="collection-offer"][data-source="nexusmods"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-offer"]`, &offer),
	)
	assert.Contains(t, offer, "If “abcdef” is a NexusMods collection")

	g := newE2EFixtureWithSearchableMods(t)
	g.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, g.Svc.SaveGame(t.Context(), g.Game))
	var offers int
	g.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(g.e2eFixture, "boots")),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="collection-offer"]').length`, &offers),
	)
	assert.Zero(t, offers, "a word that found mods is a search, not a collection")
	assert.Empty(t, f.BrowserErrors())
	assert.Empty(t, g.BrowserErrors())
}

// TestE2E_CollectionLink_LmmcAddressOffersTheURLImport: an https address
// of a .lmmc file offers the URL import; a plain-http one says lmm fetches
// over https only.
func TestE2E_CollectionLink_LmmcAddressOffersTheURLImport(t *testing.T) {
	f := newE2EFixture(t)

	var offer, plain string
	var plainButtons int
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, "https://example.invalid/list.lmmc")),
		chromedp.WaitVisible(`[data-testid="collection-offer"][data-source="url"] button`, chromedp.ByQuery),
		textContent(`[data-testid="collection-offer"]`, &offer),
		chromedp.Navigate(workshopSearchPath(f, "http://example.invalid/list.lmmc")),
		chromedp.WaitVisible(`[data-testid="collection-offer"][data-source="url"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-offer"]`, &plain),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="collection-offer"] button').length`, &plainButtons),
	)
	assert.Contains(t, offer, "This is a collection")
	assert.Contains(t, plain, "https only")
	assert.Zero(t, plainButtons)
	assert.Empty(t, f.BrowserErrors())
}

// openImportCollectionFromPicker opens the "Import collection…" modal from
// the profile picker's menu.
func openImportCollectionFromPicker() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`.profile-picker__menu [data-action="import-collection"]`),
		chromedp.WaitVisible(`.modal[data-kind="import-collection"] [data-testid="nexus-collection-ref"]`, chromedp.ByQuery),
	}
}

// TestE2E_ImportCollection_PickerMenuOpensTheImport: the picker's
// "Import collection…" opens a modal with the collection inputs and the
// file upload, and its NexusMods input plans the import.
func TestE2E_ImportCollection_PickerMenuOpensTheImport(t *testing.T) {
	f, refs := newE2ENexusRefFixture(t)

	var modal, plan string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionFromPicker(),
		textContent(`.modal[data-kind="import-collection"]`, &modal),
		chromedp.SetValue(`[data-testid="nexus-collection-ref"]`, e2eNexusCollectionURL, chromedp.ByQuery),
		clickWhenSettled(`[data-testid="nexus-collection-import"]`),
		chromedp.WaitVisible(`[data-testid="source-collection"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &plan),
	)

	assert.Contains(t, modal, "NexusMods collection")
	assert.Contains(t, modal, "From a URL")
	assert.Contains(t, modal, "Choose file")
	assert.Contains(t, plan, "Three Mods")
	assert.Equal(t, []string{e2eNexusCollectionURL}, refs())
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportCollection_EscapeReturnsFocusToThePicker: the modal's
// opener was a menu item that closed with the menu, so Escape gives focus
// back to the picker's trigger, as "Manage profiles…" does.
func TestE2E_ImportCollection_EscapeReturnsFocusToThePicker(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionFromPicker(),
		settleEffects(),
		chromedp.KeyEvent("\x1b"),
		waitGone(`.modal`),
		pollUntil(`document.activeElement?.classList.contains("profile-picker__trigger") === true`),
	)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportCollection_ProfileCardOpensTheImport: the Profile card
// carries the same entry.
func TestE2E_ImportCollection_ProfileCardOpensTheImport(t *testing.T) {
	f := newE2ENexusCollectionFixture(t)
	// A listed mod that is not installed puts the Profile card on screen.
	require.NoError(t, f.Svc.NewProfileManager().AddMod(context.Background(), f.Game.ID, "default",
		domain.ModReference{SourceID: "nexusmods", ModID: "999", Version: "1.0"}))

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--profile`, chromedp.ByQuery),
		clickWhenSettled(`.card--profile [data-action="import-collection"]`),
		chromedp.WaitVisible(`.modal[data-kind="import-collection"] [data-testid="import-url"]`, chromedp.ByQuery),
	)
	assert.Empty(t, f.BrowserErrors())
}
