package serve_test

// Browser scenarios for issue 269's Tier-2 web surfaces (#346): the
// bring-your-own-key auth field, source-scoped search rendering a Workshop
// hit, the collection-import affordance on the search page, and issue 365's
// library-row half.
//
// These are the only tests that EXECUTE the SPA, so they are the only ones
// that can see an instructions block that never renders, a search row that
// prints a 19-digit content id, or a row menu offering an action the mod
// page hides.
//
// Skipped when no Chrome/Chromium is on PATH, the 7z pattern the rest of
// this suite follows.

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	kb "github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The collection the Tier-2 fixture resolves, the link a user pastes into
// the search box, and the second catalog item both it and the search
// scenarios use.
const (
	e2eWorkshopCollectionID  = "2500900001"
	e2eWorkshopCollectionURL = "https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001"
	e2eWorkshopSecondFileID  = "3512001122"
	e2eWorkshopSecondContent = "1122334455667788991"
)

// e2eWorkshopTier2Source adds the two Tier-2 capabilities to the Tier-1
// fake: a Search that can be made to refuse for want of a key, and a
// collection resolver.
type e2eWorkshopTier2Source struct {
	*e2eWorkshopSource
	searchErr error
}

func (s *e2eWorkshopTier2Source) Search(ctx context.Context, q source.SearchQuery) (source.SearchResult, error) {
	if s.searchErr != nil {
		return source.SearchResult{}, s.searchErr
	}
	return s.e2eWorkshopSource.Search(ctx, q)
}

func (s *e2eWorkshopTier2Source) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Updates: true, Auth: true}
}

func (s *e2eWorkshopTier2Source) AuthInstructions() string {
	return "Get a free key at https://steamcommunity.com/dev/apikey. " +
		"The key is personal and confidential - never share it, and never paste someone else's."
}

func (s *e2eWorkshopTier2Source) EnvKey() string { return "STEAM_WEB_API_KEY" }

// Name is the display name every auth surface labels the row with. The
// Tier-1 fake inherits fakeSource's "Fake Source", which would make the
// Setup > Auth assertion below pass or fail for reasons about the harness
// rather than about the source.
func (s *e2eWorkshopTier2Source) Name() string { return "Steam Workshop" }

func (s *e2eWorkshopTier2Source) IsAuthenticated() bool { return false }

func (s *e2eWorkshopTier2Source) ResolveCollection(context.Context, string) (source.Collection, error) {
	return source.Collection{
		ID: e2eWorkshopCollectionID, Name: "Cargo Ships", URL: e2eWorkshopCollectionURL,
		ItemIDs: []string{e2eWorkshopFileID, e2eWorkshopSecondFileID},
	}, nil
}

// newE2EWorkshopTier2Fixture is the Tier-1 fixture with the Tier-2 source
// swapped in. searchErr, when non-nil, is what Search refuses with - the
// unkeyed case is domain.ErrAuthRequired.
func newE2EWorkshopTier2Fixture(t *testing.T, searchErr error) e2eFixture {
	t.Helper()
	f := newE2EWorkshopFixture(t)

	src, err := f.Svc.GetSource(e2eWorkshopSourceID)
	require.NoError(t, err)
	tier1, ok := src.(*e2eWorkshopSource)
	require.True(t, ok)

	// A SECOND catalog item, so a search returns a row for something not
	// already installed - the row whose version rendering matters.
	tier1.addMod(fakeSourceMod{Mod: domain.Mod{
		ID: e2eWorkshopSecondFileID, SourceID: e2eWorkshopSourceID,
		Name: "Sample Cargo Ship", Version: e2eWorkshopSecondContent,
		Author:    "76561198000000001",
		UpdatedAt: time.Unix(e2eWorkshopTimeUpdated, 0).UTC(),
	}})

	f.Svc.RegisterSource(&e2eWorkshopTier2Source{e2eWorkshopSource: tier1, searchErr: searchErr})
	return f
}

// seedWorkshopTier2ManagedMod is seedWorkshopManagedMod's twin for a fixture
// whose registry now holds the TIER-2 wrapper: an ordinary lmm-managed mod
// alongside the external one, so a scenario about how the two rows differ
// has one of each.
func seedWorkshopTier2ManagedMod(t *testing.T, f e2eFixture) {
	t.Helper()
	src, err := f.Svc.GetSource(e2eWorkshopSourceID)
	require.NoError(t, err)
	ws, ok := src.(*e2eWorkshopTier2Source)
	require.True(t, ok)
	ws.addMod(fakeSourceMod{Mod: domain.Mod{
		ID: "managed-1", SourceID: e2eWorkshopSourceID, Name: "Managed Mod", Version: "1.0",
	}})
	seedInstalledMod(t, f.Svc, f.Game, domain.Mod{
		ID: "managed-1", SourceID: e2eWorkshopSourceID, Name: "Managed Mod",
		Version: "1.0", GameID: f.Game.ID,
	}, true, map[string][]byte{"managed.pak": []byte("managed")})
}

// workshopSearchPath is the dedicated search page's deep-link route for a
// fixture that is not the search suite's own shape.
func workshopSearchPath(f e2eFixture, query string) string {
	return f.HomePath() + "/search?q=" + url.QueryEscape(query)
}

// TestE2E_WorkshopTier2_SetupAuthOffersTheKeyFieldAndItsInstructions is the
// BYO-key surface: the Steam Workshop row appears in Setup > Auth the moment
// the source declares Auth, and it carries the source's own setup steps -
// including the sentence a user has to read before pasting a key that is not
// theirs.
func TestE2E_WorkshopTier2_SetupAuthOffersTheKeyFieldAndItsInstructions(t *testing.T) {
	f := newE2EWorkshopTier2Fixture(t, nil)

	var row, instructions string
	var fields int
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("auth")),
		chromedp.WaitVisible(`[data-testid="setup-auth"] .setup-auth__row`, chromedp.ByQuery),
		textContent(`.setup-auth__row[data-source="steamworkshop"]`, &row),
		textContent(`[data-testid="auth-instructions-steamworkshop"]`, &instructions),
		chromedp.Evaluate(`document.querySelectorAll('.setup-auth__row[data-source="steamworkshop"] input[type="password"]').length;`, &fields),
	)

	assert.Equal(t, 1, fields, "a not-authenticated row offers exactly one key field")
	assert.Contains(t, row, "Steam Workshop")
	assert.Contains(t, row, "STEAM_WEB_API_KEY", "the env var is named beside the field")
	assert.Contains(t, instructions, "steamcommunity.com/dev/apikey")
	assert.Contains(t, instructions, "never share it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_WorkshopTier2_UnkeyedSearchSaysAuthenticationIsRequired: Search is
// declared unconditionally, so the source IS searched; without a key Valve
// refuses, core hands the failure back as a per-source warning, and the page
// renders it rather than an empty result that reads as "no such mod".
func TestE2E_WorkshopTier2_UnkeyedSearchSaysAuthenticationIsRequired(t *testing.T) {
	f := newE2EWorkshopTier2Fixture(t, domain.ErrAuthRequired)
	// A SECOND source on the same game, which searches fine. That is the
	// realistic shape (a Workshop game usually has NexusMods or CurseForge
	// mapped too) AND the one that exercises the rule this test is about:
	// core's aggregate path reports a per-source failure as a WARNING
	// beside the hits that did arrive, rather than failing the whole
	// search. A Workshop-only game takes the other branch, which
	// TestAPISearch_AnUnkeyedSourceIs401 covers without a browser.
	other := newFakeSource("other")
	other.addMod(fakeSourceMod{Mod: domain.Mod{
		ID: "o1", SourceID: "other", Name: "Cargo Crate", Version: "1.0",
	}})
	f.Svc.RegisterSource(other)
	f.Game.SourceIDs["other"] = ""
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	var page string
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, "cargo")),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".search-page").textContent;`, &page),
	)
	assert.Contains(t, page, "Cargo Crate", "the source that worked still answers")
	assert.Contains(t, page, "authentication required",
		"and the one that needs a key says so, rather than being silently absent")
	assert.Contains(t, page, "steamworkshop")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_WorkshopTier2_KeyedSearchRendersTheRevisionDateNotTheContentID is
// the version-DISPLAY rule reaching the one surface Tier 2 itself opened. A
// search hit is a CATALOG document with no installed row behind it, which is
// why core.SearchHit carries its own `external` flag.
func TestE2E_WorkshopTier2_KeyedSearchRendersTheRevisionDateNotTheContentID(t *testing.T) {
	f := newE2EWorkshopTier2Fixture(t, nil)

	var page, versions string
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, "Sample Cargo Ship")),
		chromedp.WaitVisible(`.search-result`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".search-page").textContent;`, &page),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".search-result__version")).map((s) => s.textContent.trim()).join("|");`, &versions),
	)

	assert.Contains(t, page, "Sample Cargo Ship")
	assert.Equal(t, e2eWorkshopRevisionDate, versions,
		"a Workshop hit shows its revision date where a version goes")
	assert.NotContains(t, page, e2eWorkshopSecondContent, "the content id is never printed as a version")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_WorkshopTier2_PastedCollectionURLOffersAnImport covers the search
// box's collection affordance and the plan it opens: a URL is not a search
// term, so the page offers what the user actually meant.
func TestE2E_WorkshopTier2_PastedCollectionURLOffersAnImport(t *testing.T) {
	f := newE2EWorkshopTier2Fixture(t, nil)

	var modal string
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, e2eWorkshopCollectionURL)),
		chromedp.WaitVisible(`[data-testid="collection-offer"]`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="collection-offer"] button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &modal),
	)

	assert.Contains(t, modal, "cargo-ships", "the profile the collection would create")
	assert.NotContains(t, modal, e2eWorkshopManifest,
		"no surface prints the content id as a version")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_WorkshopTier2_TheCollectionPlanRendersItsOwnDocument is W2 review
// Important 3. The plan ships `workshop_collection` over the wire and the
// SPA read none of it, so a collection import rendered as the generic
// ImportPlan buckets: an un-subscribed item was a bare
// "steamworkshop:3512001122 @ —" line under "Missing entirely", with no
// name, no link and no remedy — the per-item reporting design §4 asks for
// existed only in the CLI.
//
// The install controls are the other half. ApplyWorkshopCollectionImport
// forces Install=false / NoInstall=true, so "Download and install N pending
// mods" and the advanced "Never install" were both present and ignored.
func TestE2E_WorkshopTier2_TheCollectionPlanRendersItsOwnDocument(t *testing.T) {
	f := newE2EWorkshopTier2Fixture(t, nil)

	var modal, header string
	var installControls, advanced, links int
	f.runInBrowser(t,
		chromedp.Navigate(workshopSearchPath(f, e2eWorkshopCollectionURL)),
		chromedp.WaitVisible(`[data-testid="collection-offer"]`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="collection-offer"] button`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="collection-items"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &modal),
		textContent(`[data-testid="collection-header"]`, &header),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] .plan__control')).filter((l) => l.textContent.includes("Download and install")).length;`, &installControls),
		chromedp.Evaluate(`document.querySelectorAll('.modal[data-kind="profile_import"] .plan__advanced').length;`, &advanced),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="collection-items"] a[href*="steamcommunity.com"]').length;`, &links),
	)

	assert.Contains(t, modal, "1 item already tracked",
		`the summary says "tracked", not "installed": nothing is deployed into this profile`)
	assert.Contains(t, header, "Cargo Ships", "the collection's own title")
	assert.Contains(t, header, "Nothing is downloaded",
		"the reason the install controls are absent rather than merely unchecked")
	assert.Contains(t, modal, "Sample Workshop Item", "the item's NAME, not a bare ref")
	assert.Contains(t, modal, "subscribe in Steam",
		"design §4's per-item remedy reaches the browser too")
	assert.Contains(t, modal, "already tracked")
	assert.Equal(t, 0, installControls,
		"a control core would silently override must not be offered")
	assert.Equal(t, 0, advanced, `and neither may the advanced "Never install"`)
	assert.Positive(t, links, "each item links to its Workshop page")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_WorkshopTier2_ProfilesModalImportsACollection is W2 review Minor
// 9: the Profiles modal's collection input is named FIRST among the task
// spec's SPA surfaces and had no test of any kind — no E2E, no HTTP flow,
// no reference outside its own definition. Its search-box twin got the
// browser test; this is the same journey through the other door.
func TestE2E_WorkshopTier2_ProfilesModalImportsACollection(t *testing.T) {
	f := newE2EWorkshopTier2Fixture(t, nil)

	var modal string
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
		chromedp.WaitVisible(`[data-testid="collection-ref"]`, chromedp.ByQuery),
		chromedp.SetValue(`[data-testid="collection-ref"]`, e2eWorkshopCollectionID, chromedp.ByQuery),
		chromedp.SetValue(`[data-testid="collection-profile-name"]`, "my-ships", chromedp.ByQuery),
		chromedp.Click(`.profiles-import__collection button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="collection-items"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &modal),
	)

	assert.Contains(t, modal, "my-ships", "--as reaches the plan from this door too")
	assert.Contains(t, modal, "Cargo Ships")
	assert.Contains(t, modal, "subscribe in Steam")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_WorkshopTier2_LibraryRowMenuHidesRelinkForAnExternalRow is issue
// 365 (b): the full mod page has hidden Re-link since Tier 1 ("there is no
// link to move"), while the row's own menu still offered it - the same
// present-and-refused shape the Tier-1 review argued against, on two
// surfaces describing one row.
func TestE2E_WorkshopTier2_LibraryRowMenuHidesRelinkForAnExternalRow(t *testing.T) {
	f := newE2EWorkshopTier2Fixture(t, nil)
	seedWorkshopTier2ManagedMod(t, f)

	var externalItems, managedItems []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mod-row`, chromedp.ByQuery),
		chromedp.Evaluate(openRowMenuJS("Sample Workshop Item"), nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(rowMenuItemsJS, &externalItems),
		// The dismiss listener is attached by an EFFECT, which in a
		// headless browser can lag the menu's own DOM appearance by a few
		// animation frames - this suite's established pattern for the same
		// menu (TestE2E_RowMenuClosesOnOutsideClickAndEscape).
		settleEffects(),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(openRowMenuJS("Managed Mod"), nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(rowMenuItemsJS, &managedItems),
	)

	assert.NotContains(t, externalItems, "Re-link…", "there is no link to move")
	assert.Contains(t, managedItems, "Re-link…", "an ordinary row still offers it")
	assert.Empty(t, f.BrowserErrors())
}

// openRowMenuJS clicks the per-row menu button of the library row whose text
// contains name - the same shape e2e_test.go's own row-menu scenarios use.
func openRowMenuJS(name string) string {
	return `Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes(` +
		strconvQuote(name) + `)).querySelector("td.col--menu button").click();`
}

// rowMenuItemsJS reads the open row menu's item labels.
const rowMenuItemsJS = `Array.from(document.querySelectorAll(".row-menu__item")).map((b) => b.textContent.trim());`
