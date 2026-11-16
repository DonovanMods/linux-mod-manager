package serve_test

// #651: searching a game's NexusMods collections from the browser. The
// search page has a Mods / Collections tab pair for a game with a NexusMods
// source; a collection hit's Import opens the "Import a collection" dialog
// with its URL in the NexusMods field, and that dialog plans the import the
// profiles modal's own form does. The omnibar links to the Collections tab,
// and the import dialog links to it with no query (the game's collections).

import (
	"context"
	"net/url"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eCollectionSearchSource is e2eNexusRefSource whose collections can be
// searched: one page holding the fixture collection (abcdef, rev 3) and an
// adult one, recording each query.
type e2eCollectionSearchSource struct {
	e2eNexusRefSource
	qmu     *sync.Mutex
	queries *[]source.CollectionSearchQuery
}

func (s e2eCollectionSearchSource) SearchGameCollections(_ context.Context, gameSourceID string, q source.CollectionSearchQuery) (source.CollectionSearchResult, error) {
	s.qmu.Lock()
	*s.queries = append(*s.queries, q)
	s.qmu.Unlock()
	return source.CollectionSearchResult{Total: 2, Collections: []source.CollectionSummary{
		{ID: "abcdef", URL: "https://www.nexusmods.com/games/" + gameSourceID + "/collections/abcdef", Name: "Three Mods",
			Author: "curator", Summary: "Three mods, curated.", Endorsements: 1234, Downloads: 56789, Revision: 3,
			ModCount: 3, SizeBytes: 3 << 20},
		{ID: "ghijkl", URL: "https://www.nexusmods.com/games/" + gameSourceID + "/collections/ghijkl", Name: "Grown Up",
			Author: "other", Revision: 7, ModCount: 40, Adult: true},
	}}, nil
}

// newE2ECollectionSearchFixture is newE2ENexusRefFixture with the
// searchable double; queries returns what each collection search asked.
func newE2ECollectionSearchFixture(t *testing.T) (e2eFixture, func() []string, func() []source.CollectionSearchQuery) {
	t.Helper()
	f := newE2ENexusCollectionFixture(t)
	var mu, qmu sync.Mutex
	var refs []string
	var queries []source.CollectionSearchQuery
	f.Svc.RegisterSource(e2eCollectionSearchSource{
		e2eNexusRefSource{e2eNexusSource{newFakeSource("nexusmods")}, &mu, &refs}, &qmu, &queries,
	})
	return f, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), refs...)
		}, func() []source.CollectionSearchQuery {
			qmu.Lock()
			defer qmu.Unlock()
			return append([]source.CollectionSearchQuery(nil), queries...)
		}
}

// collectionsSearchPath is the search page's Collections tab for query.
func collectionsSearchPath(f e2eFixture, query string) string {
	path := f.HomePath() + "/search?type=collections"
	if query != "" {
		path += "&q=" + url.QueryEscape(query)
	}
	return path
}

const abcdefRow = `[data-collection="nexusmods/abcdef"]`

// TestE2E_CollectionSearch_ImportPlansTheCollection is the issue's
// journey: search the game's collections, press Import on a hit, and the
// import dialog plans that collection - the same plan the NexusMods field
// opens when the address is typed into it.
func TestE2E_CollectionSearch_ImportPlansTheCollection(t *testing.T) {
	f, refs, queries := newE2ECollectionSearchFixture(t)

	var page, ref, modal, selected string
	f.runInBrowser(t,
		chromedp.Navigate(collectionsSearchPath(f, "three")),
		chromedp.WaitVisible(abcdefRow, chromedp.ByQuery),
		textContent(`.search-page`, &page),
		chromedp.AttributeValue(`.search-page__tabs [data-type="collections"]`, "aria-selected", &selected, nil, chromedp.ByQuery),
		clickWhenSettled(abcdefRow+` [data-testid="collection-import"]`),
		chromedp.WaitVisible(`.modal[data-kind="import-collection"] [data-testid="nexus-collection-ref"]`, chromedp.ByQuery),
		chromedp.Value(`[data-testid="nexus-collection-ref"]`, &ref, chromedp.ByQuery),
		clickWhenSettled(`[data-testid="nexus-collection-import"]`),
		chromedp.WaitVisible(`[data-testid="source-collection"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &modal),
	)

	assert.Equal(t, "true", selected, "the Collections tab is the one shown")
	for _, want := range []string{"Collections matching “three”", "2 in all", "Three Mods", "rev 3", "curator",
		"3 mods", "1,234 endorsements", "56,789 downloads", "Three mods, curated.", "Grown Up", "Adult"} {
		assert.Contains(t, page, want)
	}
	require.NotEmpty(t, queries())
	assert.Equal(t, "three", queries()[0].Query)
	assert.Equal(t, e2eNexusCollectionURL, ref, "the hit's URL is in the NexusMods field")
	assert.Contains(t, modal, "Three Mods", "the collection's own plan")
	assert.Contains(t, modal, "3 items at revision 3")
	assert.Equal(t, []string{e2eNexusCollectionURL}, refs(), "the plan resolves the address the search returned")
	assert.Empty(t, f.BrowserErrors())
}

// The tabs switch the same query between mods and collections, through the
// URL; the omnibar's "Search collections" lands on the Collections tab.
func TestE2E_CollectionSearch_TabsAndTheOmnibarLink(t *testing.T) {
	f, _, queries := newE2ECollectionSearchFixture(t)

	var location, modsSelected string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "three\r", chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results [data-testid="search-collections"]`, chromedp.ByQuery),
		clickWhenSettled(`.omnibar-results [data-testid="search-collections"]`),
		chromedp.WaitVisible(abcdefRow, chromedp.ByQuery),
		chromedp.Location(&location),
		clickWhenSettled(`.search-page__tabs [data-type="mods"]`),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"][data-search-type="mods"]`, chromedp.ByQuery),
		chromedp.AttributeValue(`.search-page__tabs [data-type="mods"]`, "aria-selected", &modsSelected, nil, chromedp.ByQuery),
	)

	assert.Contains(t, location, "type=collections")
	assert.Contains(t, location, "q=three")
	assert.Equal(t, "true", modsSelected)
	require.Len(t, queries(), 1, "one collection search; the Mods tab searches mods")
	assert.Empty(t, f.BrowserErrors())
}

// With no collection in mind, the import dialog's "Browse" link lists the
// game's collections: the Collections tab with no query, as `lmm search
// --collections` with none.
func TestE2E_CollectionSearch_BrowseFromTheImportDialog(t *testing.T) {
	f, _, queries := newE2ECollectionSearchFixture(t)

	var page string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionFromPicker(),
		chromedp.WaitVisible(`[data-testid="browse-collections"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-testid="browse-collections"]`),
		chromedp.WaitVisible(abcdefRow, chromedp.ByQuery),
		textContent(`.search-page`, &page),
	)

	assert.Contains(t, page, "Collections — Page 1")
	require.Len(t, queries(), 1)
	assert.Empty(t, queries()[0].Query, "no query lists the game's collections")
	assert.Empty(t, f.BrowserErrors())
}
