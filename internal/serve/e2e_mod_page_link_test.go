package serve_test

// #513: a link to a mod's page on its source, wherever the mod is shown and
// on a failed download. Driven in a real browser, because the claim is about
// the DOM a user clicks - the href, the target, the rel - and about what is
// NOT there when the source's URL is not a plain web address.

import (
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	e2eLinkedPage   = "https://example.test/mods/linked"
	e2eFoundPage    = "https://example.test/mods/found"
	e2eBrokenPage   = "https://example.test/mods/broken"
	e2eManualPage   = "https://example.test/mods/manual"
	e2eUnsafeLinked = "javascript:alert('installed')"
	e2eUnsafeFound  = "javascript:alert('found')"

	// e2eSourceName is e2eSearchSource.Name(), the "source name" the link names.
	e2eSourceName = "E2E Search Source"
)

// newE2EFixtureWithModPages is the searchable fixture plus mods that differ
// only in what their source says their page is: two installed (a safe URL, a
// javascript: one), two searchable (the same pair) and one whose download
// 404s, so its install job fails.
func newE2EFixtureWithModPages(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)

	installed := func(id, name, page string) {
		mod := domain.Mod{ID: id, SourceID: "fake", Name: name, Version: "1.0", SourceURL: page}
		// In the catalog too: the slide-over's changelog and the full page
		// are live reads, and a 404 there would fail the BrowserErrors bar.
		f.Src.addMod(e2eSearchSourceMod{mod: mod})
		mod.GameID = f.Game.ID
		seedInstalledMod(t, f.Svc, f.Game, mod, true, nil)
		require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
			domain.ModReference{SourceID: "fake", ModID: id, Version: "1.0"}))
	}
	installed("linked", "Linked Mod", e2eLinkedPage)
	installed("evil", "Evil Mod", e2eUnsafeLinked)

	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "found", SourceID: "fake", Name: "Found Mod", Version: "1.0", SourceURL: e2eFoundPage},
		files:   []domain.DownloadableFile{{ID: "x1", Name: "Main", FileName: "found.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"x1": "Mods/found.pak"},
	})
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "evilfound", SourceID: "fake", Name: "Evil Found Mod", Version: "1.0", SourceURL: e2eUnsafeFound},
		files:   []domain.DownloadableFile{{ID: "y1", Name: "Main", FileName: "evilfound.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"y1": "Mods/evilfound.pak"},
	})
	f.Src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "broken", SourceID: "fake", Name: "Broken Download", Version: "1.0", SourceURL: e2eBrokenPage},
		// A file with no archive behind it: the download answers 404.
		files: []domain.DownloadableFile{{ID: "z1", Name: "Main", FileName: "broken.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
	})
	f.Src.addMod(e2eSearchSourceMod{
		mod:   domain.Mod{ID: "manual", SourceID: "fake", Name: "Manual Download", Version: "1.0", SourceURL: e2eManualPage},
		files: []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "manual.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
	})
	// The author has opted out of third-party downloads.
	f.Src.urlErrs = map[string]error{"manual": &source.ManualDownloadError{Reason: "the author has turned off API downloads"}}
	return f
}

type e2eLink struct {
	Href   string `json:"href"`
	Target string `json:"target"`
	Rel    string `json:"rel"`
	Text   string `json:"text"`
	Label  string `json:"label"`
}

// linkIn reads the mod-page link inside scope, or leaves *out nil when there is none.
func linkIn(scope string, out **e2eLink) chromedp.Action {
	return chromedp.Evaluate(`(() => {
		const a = document.querySelector(`+jsString(scope+` a.mod-page-link`)+`);
		if (!a) return null;
		return {
			href: a.getAttribute("href"), target: a.getAttribute("target"), rel: a.getAttribute("rel"),
			text: a.textContent.trim(), label: a.getAttribute("aria-label") ?? "",
		};
	})()`, out)
}

// awaitPromise makes chromedp.Evaluate wait for the async IIFE it was given.
func awaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

func assertModPageLink(t *testing.T, got *e2eLink, href, modName string) {
	t.Helper()
	require.NotNil(t, got, "the mod's page link must be rendered")
	assert.Equal(t, href, got.Href)
	assert.Equal(t, "_blank", got.Target, "opens in a new tab")
	assert.Equal(t, "noopener noreferrer", got.Rel)
	assert.Equal(t, "Open on "+e2eSourceName, got.Text)
	assert.Contains(t, got.Label, modName, "the accessible name says which mod")
	assert.Contains(t, got.Label, "Open on "+e2eSourceName, "the accessible name contains the visible text")
}

func TestE2E_ModPageLink_SlideOverOfAnInstalledMod(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	var link *e2eLink
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "linked")),
		chromedp.WaitVisible(`.slide-over__panel`, chromedp.ByQuery),
		pollUntil(`document.querySelector(".slide-over a.mod-page-link")?.textContent.includes("E2E Search Source")`),
		linkIn(".slide-over", &link),
	)
	assertModPageLink(t, link, e2eLinkedPage, "Linked Mod")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ModPageLink_SlideOverOfASearchResult(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	var link *e2eLink
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "found mod", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(searchResultRow("fake", "found"), chromedp.ByQuery),
		chromedp.Click(searchResultRow("fake", "found")+" .search-result__name", chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over__panel`, chromedp.ByQuery),
		pollUntil(`document.querySelector(".slide-over a.mod-page-link")?.textContent.includes("E2E Search Source")`),
		linkIn(".slide-over", &link),
	)
	assertModPageLink(t, link, e2eFoundPage, "Found Mod")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ModPageLink_FullModPage(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	var link *e2eLink
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "linked")),
		chromedp.WaitVisible(`.mod-page__title`, chromedp.ByQuery),
		pollUntil(`document.querySelector(".mod-page a.mod-page-link")?.textContent.includes("E2E Search Source")`),
		linkIn(".mod-page", &link),
	)
	assertModPageLink(t, link, e2eLinkedPage, "Linked Mod")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ModPageLink_SearchPageAndOmnibarRows(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	var omnibar, page *e2eLink
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "found mod", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(`.omnibar-results `+searchResultRow("fake", "found"), chromedp.ByQuery),
		pollUntil(`document.querySelector('.omnibar-results .search-result[data-mod="fake/found"] a.mod-page-link')?.textContent.includes("E2E Search Source")`),
		linkIn(`.omnibar-results `+searchResultRow("fake", "found"), &omnibar),
		chromedp.Navigate(f.SearchPagePath("found mod")),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"] `+searchResultRow("fake", "found"), chromedp.ByQuery),
		pollUntil(`document.querySelector('.search-page .search-result[data-mod="fake/found"] a.mod-page-link')?.textContent.includes("E2E Search Source")`),
		linkIn(`.search-page `+searchResultRow("fake", "found"), &page),
	)
	assertModPageLink(t, omnibar, e2eFoundPage, "Found Mod")
	assertModPageLink(t, page, e2eFoundPage, "Found Mod")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ModPageLink_LibraryRowMenu(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	var link *e2eLink
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`.library__table tr[data-mod="fake:linked"] [data-action="row-menu"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		pollUntil(`document.querySelector(".row-menu a.mod-page-link")?.textContent.includes("E2E Search Source")`),
		linkIn(".row-menu", &link),
	)
	assertModPageLink(t, link, e2eLinkedPage, "Linked Mod")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModPageLink_UnsafeURLsRenderNoLink: a source's URL is data. A
// javascript: one must not become a link anywhere a mod is shown, and must
// not become an href at all.
func TestE2E_ModPageLink_UnsafeURLsRenderNoLink(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	var slideOver, fullPage, omnibarRow, libraryMenu, anyUnsafeHref int
	var slideOverTitle string
	count := func(sel string, out *int) chromedp.Action {
		return chromedp.Evaluate(`document.querySelectorAll(`+jsString(sel)+`).length`, out)
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "evil")),
		chromedp.WaitVisible(`.slide-over__panel`, chromedp.ByQuery),
		// Settled: the changelog section has finished loading.
		pollUntil(`!document.querySelector(".slide-over")?.textContent.includes("Loading changelog")`),
		textContent(`.slide-over .section-header`, &slideOverTitle),
		count(".slide-over a.mod-page-link", &slideOver),
		chromedp.Navigate(f.ModPagePath("fake", "evil")),
		chromedp.WaitVisible(`.mod-page__title`, chromedp.ByQuery),
		pollUntil(`!document.querySelector(".mod-page")?.textContent.includes("Loading")`),
		count(".mod-page a.mod-page-link", &fullPage),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`.library__table tr[data-mod="fake:evil"] [data-action="row-menu"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		count(".row-menu a.mod-page-link", &libraryMenu),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "evil found", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(searchResultRow("fake", "evilfound"), chromedp.ByQuery),
		count(searchResultRow("fake", "evilfound")+" a.mod-page-link", &omnibarRow),
		chromedp.Click(searchResultRow("fake", "evilfound")+" .search-result__name", chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over__panel`, chromedp.ByQuery),
		count(`a[href^="javascript:" i], a[href^="data:" i], a[href^="file:" i]`, &anyUnsafeHref),
	)
	assert.Equal(t, "Evil Mod", slideOverTitle)
	assert.Zero(t, slideOver, "slide-over")
	assert.Zero(t, fullPage, "full page")
	assert.Zero(t, libraryMenu, "library row menu")
	assert.Zero(t, omnibarRow, "search row")
	assert.Zero(t, anyUnsafeHref, "no unsafe href anywhere, including the search result's slide-over")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModPageLink_SafeWebURLRule pins the browser half of the rule that
// domain.SafeWebURL states in Go.
func TestE2E_ModPageLink_SafeWebURLRule(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	var got []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { safeWebUrl } = await import("/static/app/weburl.js");
			return [
				"https://www.nexusmods.com/skyrim/mods/1",
				"http://example.test/mod",
				"  https://example.test/mod \n",
				"javascript:alert(1)",
				"JaVaScRiPt:alert(1)",
				"data:text/html,<script>alert(1)</script>",
				"file:///etc/passwd",
				"ftp://example.test/mod",
				"/relative",
				"//example.test/mod",
				"example.test/mod",
				"https:///mod",
				"",
				"   ",
				null,
				undefined,
				42,
			].map((u) => safeWebUrl(u));
		})()`, &got, awaitPromise),
	)
	assert.Equal(t, []string{
		"https://www.nexusmods.com/skyrim/mods/1",
		"http://example.test/mod",
		"https://example.test/mod",
		"", "", "", "", "", "", "", "", "", "", "", "", "", "",
	}, got)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModPageLink_FailedInstallJobShowsThePage: a download that fails
// puts the mod's page next to the failure - in place on the row and in the
// tray - from the typed details of the job's error, not from its text.
func TestE2E_ModPageLink_FailedInstallJobShowsThePage(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	row := searchResultRow("fake", "broken")
	var inline, tray *e2eLink
	var message string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "broken", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="failed"]`, chromedp.ByQuery),
		textContent(row+` .job-progress__text`, &message),
		pollUntil(`document.querySelector('.search-result[data-mod="fake/broken"] .job-progress a.mod-page-link')?.textContent.includes("E2E Search Source")`),
		linkIn(row+" .job-progress", &inline),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="failed"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.tray__failure a.mod-page-link')?.textContent.includes("E2E Search Source")`),
		linkIn(".tray__failure", &tray),
	)
	assert.Contains(t, message, "download failed", "the failure keeps its own sentence")
	assertModPageLink(t, inline, e2eBrokenPage, "Broken Download")
	assertModPageLink(t, tray, e2eBrokenPage, "Broken Download")
}

// TestE2E_ModPageLink_ManualDownloadSaysWhereTheFileComesFrom: a source that
// refuses automated downloads (CurseForge's third-party opt-out) fails the
// install with the typed manual_download flag, and the failure says to get the
// file from the mod's page and import it.
func TestE2E_ModPageLink_ManualDownloadSaysWhereTheFileComesFrom(t *testing.T) {
	f := newE2EFixtureWithModPages(t)

	row := searchResultRow("fake", "manual")
	var link *e2eLink
	var hint, message string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "manual download", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="failed"]`, chromedp.ByQuery),
		textContent(row+` .job-progress__text`, &message),
		pollUntil(`document.querySelector('.search-result[data-mod="fake/manual"] .download-page a.mod-page-link') !== null`),
		textContent(row+` .download-page__hint`, &hint),
		linkIn(row+" .download-page", &link),
	)
	assert.Contains(t, message, "download unavailable via API")
	assert.Contains(t, message, "the author has turned off API downloads", "the source's own reason is kept")
	assert.Contains(t, hint, "Import an archive")
	assertModPageLink(t, link, e2eManualPage, "Manual Download")
}
