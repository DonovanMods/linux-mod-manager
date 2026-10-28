package serve_test

// #503 (server-side search sort) and #506 (the post-mutation search refresh
// dropped the tag filter), driven end to end in a real browser.
//
// The search page's sort select used to reorder one already-fetched page in
// the browser. It is now a SERVER-side sort: choosing one re-queries page 0
// with ?sort=, and the select offers only the sorts the report says are
// worth offering (sorts_available) - never an option that would sort every
// hit by a field no source fills in.
//
// Every assertion about what the browser SENT reads the browser's own
// network log (recordSearchRequests), not a server-side double: the claim
// is about the request the SPA built, and a double that happened to ignore
// a missing parameter would hide exactly the bugs these tests exist for.

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// e2eSortableSource is the search fixture's downloading source with a Search
// that behaves like a real catalog: it honours category, tags, sort and
// paging, in that order, so page N of a sorted search is the right page of
// the WHOLE catalog rather than a reordered slice of the id-ordered one.
type e2eSortableSource struct {
	*e2eSearchSource
	sorts []domain.SearchSort
	tags  map[string][]string
}

func (s *e2eSortableSource) Capabilities() source.Capabilities {
	return source.Capabilities{
		Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true,
		Sorts: s.sorts,
	}
}

func (s *e2eSortableSource) Search(_ context.Context, q source.SearchQuery) (source.SearchResult, error) {
	var mods []domain.Mod
	needle := strings.ToLower(q.Query)
	for _, entry := range s.mods {
		m := entry.mod
		if q.Category != "" && m.Category != q.Category {
			continue
		}
		if !e2eHasEveryTag(s.tags[m.ID], q.Tags) {
			continue
		}
		if needle == "" || strings.Contains(strings.ToLower(m.Name), needle) {
			mods = append(mods, m)
		}
	}
	e2eOrderBySort(mods, q.Sort)
	total := len(mods)
	if q.PageSize > 0 {
		start := min(q.Page*q.PageSize, total)
		end := min(start+q.PageSize, total)
		mods = mods[start:end]
	}
	return source.SearchResult{Mods: mods, TotalCount: total}, nil
}

func e2eHasEveryTag(have, want []string) bool {
	for _, tag := range want {
		if !slices.Contains(have, tag) {
			return false
		}
	}
	return true
}

// e2eSortKey is the number a sort orders by, descending. Relevance has none:
// the source's own order here is by id.
func e2eSortKey(sort domain.SearchSort, m domain.Mod) int64 {
	switch sort {
	case domain.SortUpdated:
		return m.UpdatedAt.Unix()
	case domain.SortDownloads:
		return m.Downloads
	case domain.SortPopular:
		if m.Endorsements != nil {
			return *m.Endorsements
		}
	}
	return 0
}

// e2eOrderBySort orders mods by id, then (for a real sort) by that sort's
// key descending. Every key in the fixture is unique, so there are no ties.
func e2eOrderBySort(mods []domain.Mod, sort domain.SearchSort) {
	slices.SortFunc(mods, func(a, b domain.Mod) int { return strings.Compare(a.ID, b.ID) })
	if sort == "" || sort == domain.SortRelevance {
		return
	}
	slices.SortStableFunc(mods, func(a, b domain.Mod) int {
		return int(e2eSortKey(sort, b) - e2eSortKey(sort, a))
	})
}

// e2eSortCatalog is what the sortable fixture's sources hold, kept so a test
// can compute the page the SERVER should have answered.
type e2eSortCatalog struct {
	mods map[string][]domain.Mod // by source id
	tags map[string]map[string][]string
}

// rows is the names the search page should render: every named source asked
// for its own page of the filtered, sorted catalog, the pages merged, and the
// merge ordered the way core orders it (the sort's key descending; downloads
// descending, then name, for relevance).
func (c e2eSortCatalog) rows(sources []string, category, tag string, sort domain.SearchSort, page int) []string {
	var merged []domain.Mod
	for _, id := range sources {
		var filtered []domain.Mod
		for _, m := range c.mods[id] {
			if category != "" && m.Category != category {
				continue
			}
			if tag != "" && !slices.Contains(c.tags[id][m.ID], tag) {
				continue
			}
			if !strings.Contains(strings.ToLower(m.Name), "item") {
				continue
			}
			filtered = append(filtered, m)
		}
		e2eOrderBySort(filtered, sort)
		start := min(page*e2eManyResultsPageSize, len(filtered))
		end := min(start+e2eManyResultsPageSize, len(filtered))
		merged = append(merged, filtered[start:end]...)
	}
	key := sort
	if key == "" || key == domain.SortRelevance {
		key = domain.SortDownloads
	}
	slices.SortStableFunc(merged, func(a, b domain.Mod) int {
		if d := e2eSortKey(key, b) - e2eSortKey(key, a); d != 0 {
			return int(d)
		}
		return strings.Compare(a.Name, b.Name)
	})
	names := make([]string, len(merged))
	for i, m := range merged {
		names[i] = m.Name
	}
	return names
}

type e2eSortFixture struct {
	e2eFixture
	Catalog e2eSortCatalog
	Nexus   *e2eSortableSource
}

// The fixture's two sources. Both honour tags (searchpage.js's
// TAG_CAPABLE_SOURCES), so the tag filter is on offer.
const (
	e2eSortBigSource   = "nexusmods"
	e2eSortSmallSource = "thunderstore"
	e2eSortBigMods     = 45
)

// newE2EFixtureWithSortableSearch seeds a game with two sources. The big one
// holds 45 "Item NN" mods (more than two pages), the small one four. Every
// ordering key (downloads, last update, endorsements) is a distinct value per
// mod and orders the catalog DIFFERENTLY, so a sort that took effect is
// visible in which rows are shown and in what order. sorts is what both
// sources list as meaningful - nil offers none, which is what the select must
// not render for.
func newE2EFixtureWithSortableSearch(t *testing.T, sorts []domain.SearchSort) e2eSortFixture {
	t.Helper()
	sandboxE2EEnv(t)

	catalog := e2eSortCatalog{
		mods: map[string][]domain.Mod{},
		tags: map[string]map[string][]string{},
	}
	build := func(sourceID string, prefix, nameFmt string, count, firstK int, armour func(i int) bool) *e2eSortableSource {
		src := &e2eSortableSource{
			e2eSearchSource: newE2ESearchSource(t, sourceID),
			sorts:           sorts,
			tags:            map[string][]string{},
		}
		catalog.tags[sourceID] = src.tags
		base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		for i := 1; i <= count; i++ {
			k := int64(firstK + i)
			category := "Weapons"
			if i%2 == 1 {
				category = "Armor"
			}
			endorsements := (k * 53 % 101) + 1
			id := fmt.Sprintf("%s%02d", prefix, i)
			mod := domain.Mod{
				ID: id, SourceID: sourceID, Name: fmt.Sprintf(nameFmt, i), Version: "1.0",
				Category: category, Summary: "Summary for " + id,
				Downloads: (k*37%101 + 1) * 1000, Endorsements: &endorsements,
				UpdatedAt: base.AddDate(0, 0, int(k*29%101)),
			}
			src.addMod(e2eSearchSourceMod{
				mod: mod,
				files: []domain.DownloadableFile{
					{ID: "f1", Name: "Main", FileName: id + ".zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 64},
				},
				members: map[string]string{"f1": "Mods/" + id + ".pak"},
			})
			if armour(i) {
				src.tags[id] = []string{"armour"}
			}
			catalog.mods[sourceID] = append(catalog.mods[sourceID], mod)
		}
		return src
	}
	big := build(e2eSortBigSource, "item", "Item %02d", e2eSortBigMods, 0, func(i int) bool { return i%4 != 0 })
	small := build(e2eSortSmallSource, "ts", "Item TS %d", 4, e2eSortBigMods, func(i int) bool { return i%2 == 1 })

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(big)
	svc.RegisterSource(small)

	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{big.ID(): "", small.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eSortFixture{
		e2eFixture: e2eFixture{
			Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
			BrowserErrors: browserErrors,
		},
		Catalog: catalog,
		Nexus:   big,
	}
}

// SearchPagePath is the search page's deep link; extra is appended verbatim.
func (f e2eSortFixture) SearchPagePath(query, extra string) string {
	return f.HomePath() + "/search?q=" + url.QueryEscape(query) + extra
}

// e2eSearchRequests is every GET /api/v1/search the BROWSER sent, in order,
// read from its network log.
type e2eSearchRequests struct {
	mu   sync.Mutex
	seen []url.Values
	// ids are the network ids of the requests in seen, and finished the ones
	// whose response has been fully received (or failed): what lets a test
	// wait for every request the page sent to have LANDED, rather than guess
	// how long a late one takes.
	ids      map[network.RequestID]bool
	finished int
}

// recordSearchRequests starts logging f's browser's search requests. Call it
// before the first navigation.
func recordSearchRequests(t *testing.T, f e2eFixture) *e2eSearchRequests {
	t.Helper()
	rec := &e2eSearchRequests{ids: map[network.RequestID]bool{}}
	chromedp.ListenTarget(f.Ctx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			u, err := url.Parse(e.Request.URL)
			if err != nil || u.Path != "/api/v1/search" {
				return
			}
			rec.mu.Lock()
			rec.seen = append(rec.seen, u.Query())
			rec.ids[e.RequestID] = true
			rec.mu.Unlock()
		case *network.EventLoadingFinished:
			rec.finish(e.RequestID)
		case *network.EventLoadingFailed:
			rec.finish(e.RequestID)
		}
	})
	f.runInBrowser(t, network.Enable())
	return rec
}

// finish counts id's response as received, if id is a search request.
func (r *e2eSearchRequests) finish(id network.RequestID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids[id] {
		delete(r.ids, id)
		r.finished++
	}
}

// inFlight is how many search requests have been sent and not yet answered.
func (r *e2eSearchRequests) inFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen) - r.finished
}

func (r *e2eSearchRequests) all() []url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

func (r *e2eSearchRequests) count() int { return len(r.all()) }

func (r *e2eSearchRequests) last() url.Values {
	all := r.all()
	if len(all) == 0 {
		return url.Values{}
	}
	return all[len(all)-1]
}

// after is every request sent once the first n had been.
func (r *e2eSearchRequests) after(n int) []url.Values {
	all := r.all()
	if n >= len(all) {
		return nil
	}
	return all[n:]
}

// settled waits for the search page to have answered request number want and
// be showing its results: the request has been sent AND the page is out of
// its loading state. A request is only sent after the loading state is
// rendered, so "sent, then hydrated" cannot be satisfied by the page that was
// showing before the action.
func (r *e2eSearchRequests) settled(want int) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for r.count() < want {
			select {
			case <-ctx.Done():
				return fmt.Errorf("waiting for search request %d: only %d sent: %w", want, r.count(), ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		return chromedp.Poll(`document.querySelector('.search-page[data-hydrated="true"]') !== null
			&& document.querySelector('.search-page .app-booting') === null`, nil,
			chromedp.WithPollingInterval(25*time.Millisecond)).Do(ctx)
	})
}

// landed waits for at least want search requests to have been sent, EVERY one
// of them to have been answered, and the page to be showing its results. It
// is settled for a page that re-reads more than once (an install ending
// re-reads the route and refreshes the search page, two requests): the last
// answer to land is the one the page keeps, so reading the page any earlier
// races it.
func (r *e2eSearchRequests) landed(want int) chromedp.Action {
	return chromedp.Tasks{
		r.settled(want),
		chromedp.ActionFunc(func(ctx context.Context) error {
			for r.inFlight() > 0 {
				select {
				case <-ctx.Done():
					return fmt.Errorf("waiting for %d search request(s) to be answered: %w", r.inFlight(), ctx.Err())
				case <-time.After(25 * time.Millisecond):
				}
			}
			return nil
		}),
		r.settled(want),
	}
}

// sortOptions reads the sort select's options as "value=label", or nil when
// the page renders no sort select at all.
func sortOptions() (string, *[]string) {
	var opts []string
	return `(() => {
		const s = document.querySelector('.search-page select[name="sort"]');
		return s ? Array.from(s.options).map(o => o.value + "=" + o.textContent.trim()) : [];
	})()`, &opts
}

func searchRowNames(out *[]string) chromedp.Action {
	return chromedp.Evaluate(
		`Array.from(document.querySelectorAll(".search-result__name")).map(e => e.textContent.trim())`, out)
}

// firstBigSourceMod is the first of names that the big source holds.
func firstBigSourceMod(t *testing.T, f e2eSortFixture, names []string) domain.Mod {
	t.Helper()
	for _, name := range names {
		for _, m := range f.Catalog.mods[e2eSortBigSource] {
			if m.Name == name {
				return m
			}
		}
	}
	require.FailNow(t, "no big-source mod among the rows", "%v", names)
	return domain.Mod{}
}

var e2eAllSorts = []domain.SearchSort{domain.SortUpdated, domain.SortDownloads, domain.SortPopular}

// TestE2E_SearchSortSelectOffersOnlySortsTheReportListsAsAvailable is the
// "never a dead option" rule: relevance is always there, and each other sort
// only when some source that answered can order by it.
func TestE2E_SearchSortSelectOffersOnlySortsTheReportListsAsAvailable(t *testing.T) {
	cases := []struct {
		name   string
		sorts  []domain.SearchSort
		expect []string // nil: no select at all
	}{
		{"every sort", e2eAllSorts, []string{"relevance=Relevance", "updated=Last updated", "downloads=Most downloaded", "popular=Popular"}},
		{"downloads only", []domain.SearchSort{domain.SortDownloads}, []string{"relevance=Relevance", "downloads=Most downloaded"}},
		{"updated and popular", []domain.SearchSort{domain.SortPopular, domain.SortUpdated}, []string{"relevance=Relevance", "updated=Last updated", "popular=Popular"}},
		{"none: relevance alone is no choice, so no select", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newE2EFixtureWithSortableSearch(t, tc.sorts)

			expr, opts := sortOptions()
			var selected string
			f.runInBrowser(t,
				chromedp.Navigate(f.SearchPagePath("item", "")),
				chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
				chromedp.Evaluate(expr, opts),
				chromedp.Evaluate(`document.querySelector('.search-page select[name="sort"]')?.value ?? ""`, &selected),
			)
			assert.Equal(t, tc.expect, nilIfEmpty(*opts))
			if tc.expect != nil {
				assert.Equal(t, "relevance", selected, "a search with no ?sort= shows relevance selected")
			}
			assert.Empty(t, f.BrowserErrors())
		})
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// TestE2E_SearchSortSelectIsLabelledAndHasNoPageLocalWording pins the a11y
// shape (the select is inside a label that names it, like the other filters)
// and that the "(on this page)" caveat - true only of the old client-side sort
// - is gone.
func TestE2E_SearchSortSelectIsLabelledAndHasNoPageLocalWording(t *testing.T) {
	f := newE2EFixtureWithSortableSearch(t, e2eAllSorts)

	var label string
	var labelled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("item", "")),
		chromedp.WaitVisible(`.search-page select[name="sort"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const s = document.querySelector('.search-page select[name="sort"]');
			return s.labels.length === 1 && s.closest("label").classList.contains("library__control");
		})()`, &labelled),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="sort"]').labels[0].firstChild.textContent.trim()`, &label),
	)
	assert.True(t, labelled, "the sort select must be labelled, like the category and source selects")
	assert.Equal(t, "Sort", label)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchSortRequeriesPageZeroServerSide is the heart of #503: choosing
// a sort sends ?sort= for page 0 and the page shows what the SERVER answered -
// the top of the WHOLE catalog under that ordering, which is not the page the
// unsorted search showed.
func TestE2E_SearchSortRequeriesPageZeroServerSide(t *testing.T) {
	f := newE2EFixtureWithSortableSearch(t, e2eAllSorts)
	reqs := recordSearchRequests(t, f.e2eFixture)
	both := []string{e2eSortBigSource, e2eSortSmallSource}

	var initial []string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("item", "")),
		reqs.settled(1),
		searchRowNames(&initial),
	)
	assert.Equal(t, f.Catalog.rows(both, "", "", domain.SortRelevance, 0), initial)
	assert.Empty(t, reqs.last().Get("sort"), "the first search sends no sort at all: absent means relevance")

	// Page 1 first, so the re-query is visibly back at page 0.
	f.runInBrowser(t,
		chromedp.Click(`.search-page__pager button:last-child`, chromedp.ByQuery),
		reqs.settled(2),
	)
	require.Equal(t, "1", reqs.last().Get("page"))

	for i, sort := range []domain.SearchSort{domain.SortUpdated, domain.SortPopular, domain.SortDownloads} {
		var names []string
		f.runInBrowser(t,
			chromedp.SetValue(`.search-page select[name="sort"]`, string(sort), chromedp.ByQuery),
			reqs.settled(3+i),
			searchRowNames(&names),
		)
		req := reqs.last()
		assert.Equal(t, string(sort), req.Get("sort"), "the request the browser sent")
		assert.Equal(t, "0", req.Get("page"), "a sort change goes back to page 0, like the other filters")
		assert.Equal(t, "item", req.Get("q"))
		assert.Equal(t, f.Catalog.rows(both, "", "", sort, 0), names,
			"rendered rows must be the server's page for %q, in the server's order", sort)
	}

	// And back to relevance: the parameter goes away rather than being sent as
	// "relevance", and the original rows are back.
	var names []string
	f.runInBrowser(t,
		chromedp.SetValue(`.search-page select[name="sort"]`, "relevance", chromedp.ByQuery),
		reqs.settled(6),
		searchRowNames(&names),
	)
	assert.False(t, reqs.last().Has("sort"), "relevance is the absence of ?sort=")
	assert.Equal(t, initial, names)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchSortSurvivesPaginationFiltersAndTheRefreshAfterAnInstall is
// the carry-through rule: sort, category, source and tags are all part of the
// search's state, so every path that re-runs or pages it (Next/Prev, each
// other filter, the re-read after an install completes) sends all of them.
func TestE2E_SearchSortSurvivesPaginationFiltersAndTheRefreshAfterAnInstall(t *testing.T) {
	f := newE2EFixtureWithSortableSearch(t, e2eAllSorts)
	reqs := recordSearchRequests(t, f.e2eFixture)
	big := []string{e2eSortBigSource}

	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("item", "")),
		reqs.settled(1),
		chromedp.WaitVisible(`.search-page select[name="source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.search-page select[name="category"]`, chromedp.ByQuery),
	)

	n := 1
	step := func(action chromedp.Action) {
		t.Helper()
		n++
		f.runInBrowser(t, action, reqs.settled(n))
	}
	step(chromedp.SetValue(`.search-page select[name="sort"]`, "updated", chromedp.ByQuery))
	step(chromedp.SetValue(`.search-page select[name="source"]`, e2eSortBigSource, chromedp.ByQuery))
	step(chromedp.SetValue(`.search-page select[name="category"]`, "Armor", chromedp.ByQuery))
	step(chromedp.SetValue(`.search-page input[name="tag"]`, "armour", chromedp.ByQuery))

	want := func(page string) url.Values {
		return url.Values{
			"q": {"item"}, "sort": {"updated"}, "source": {e2eSortBigSource},
			"category": {"Armor"}, "tag": {"armour"}, "page": {page},
			"page_size": {fmt.Sprint(e2eManyResultsPageSize)},
		}
	}
	// Only the keys a filter changes are compared: the scope params the SPA
	// adds (game/profile) are not what is being asserted here.
	sent := func(v url.Values) url.Values {
		out := url.Values{}
		for _, k := range []string{"q", "sort", "source", "category", "tag", "page", "page_size"} {
			if v.Has(k) {
				out[k] = v[k]
			}
		}
		return out
	}
	require.Equal(t, want("0"), sent(reqs.last()),
		"each filter change must carry every other filter, the sort included")

	// Next, then Prev: the sort rides along both ways.
	var page2 []string
	step(chromedp.Click(`.search-page__pager button:last-child`, chromedp.ByQuery))
	assert.Equal(t, want("1"), sent(reqs.last()))
	f.runInBrowser(t, searchRowNames(&page2))
	assert.Equal(t, f.Catalog.rows(big, "Armor", "armour", domain.SortUpdated, 1), page2,
		"page 2 of the filtered, sorted catalog")

	// Installing from page 2 re-reads the search once the job ends. Whatever
	// that re-read sends, it must send the whole state - and the page must
	// still be showing it afterwards.
	target := firstBigSourceMod(t, f, page2)
	row := searchResultRow(e2eSortBigSource, target.ID)
	before := reqs.count()
	f.runInBrowser(t,
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		chromedp.WaitVisible(row+` .badge--good`, chromedp.ByQuery),
		// An install ending re-reads twice: the route (hydrate) and the
		// search page (refreshSearchResults). Both are asserted below.
		reqs.landed(before+2),
	)

	followUps := reqs.after(before)
	require.NotEmpty(t, followUps, "an install must re-read the search so its row reads Installed")
	for _, v := range followUps {
		assert.Equal(t, want("1"), sent(v), "every re-read after the install must carry the whole search state")
	}

	var afterNames []string
	var selectedSort, selectedCategory, selectedSource, tagValue string
	f.runInBrowser(t,
		searchRowNames(&afterNames),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="sort"]').value`, &selectedSort),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="category"]').value`, &selectedCategory),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="source"]').value`, &selectedSource),
		chromedp.Evaluate(`document.querySelector('.search-page input[name="tag"]').value`, &tagValue),
	)
	assert.Equal(t, page2, afterNames, "the page the user was on, in the same order")
	assert.Equal(t, "updated", selectedSort)
	assert.Equal(t, "Armor", selectedCategory)
	assert.Equal(t, e2eSortBigSource, selectedSource)
	assert.Equal(t, "armour", tagValue)

	// Prev still carries it, from the refreshed state.
	step(chromedp.Click(`.search-page__pager button:first-child`, chromedp.ByQuery))
	assert.Equal(t, want("0"), sent(reqs.last()))
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchRefreshAfterAnInstallKeepsTheTagFilter is #506: the re-read
// that runs when an install finishes dropped the active tag filter (every
// other re-run of the search passes it), so the results silently widened to
// untagged mods the moment a row was installed.
func TestE2E_SearchRefreshAfterAnInstallKeepsTheTagFilter(t *testing.T) {
	f := newE2EFixtureWithSortableSearch(t, e2eAllSorts)
	reqs := recordSearchRequests(t, f.e2eFixture)

	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("item", "")),
		reqs.settled(1),
		chromedp.WaitVisible(`.search-page input[name="tag"]`, chromedp.ByQuery),
		chromedp.SetValue(`.search-page input[name="tag"]`, "armour", chromedp.ByQuery),
		reqs.settled(2),
	)
	require.Equal(t, []string{"armour"}, reqs.last()["tag"])

	var names []string
	f.runInBrowser(t, searchRowNames(&names))
	require.NotEmpty(t, names)
	target := firstBigSourceMod(t, f, names)
	row := searchResultRow(e2eSortBigSource, target.ID)

	before := reqs.count()
	f.runInBrowser(t,
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		chromedp.WaitVisible(row+` .badge--good`, chromedp.ByQuery),
		reqs.landed(before+2), // the route's re-read and the search page's refresh
	)

	followUps := reqs.after(before)
	require.NotEmpty(t, followUps)
	for _, v := range followUps {
		assert.Equal(t, []string{"armour"}, v["tag"], "a re-read after the install must keep the tag filter: %v", v)
	}
	var tagValue string
	var untagged bool
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector('.search-page input[name="tag"]').value`, &tagValue),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".search-result__name")).some(e => /Item (04|08|12)$/.test(e.textContent.trim()))`, &untagged),
	)
	assert.Equal(t, "armour", tagValue)
	assert.False(t, untagged, "mods without the tag must not reappear after the re-read")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchSortIsPartOfTheURL: a link, a reload and a deep link keep the
// chosen sort, and one this search cannot offer is ignored rather than
// breaking the page.
func TestE2E_SearchSortIsPartOfTheURL(t *testing.T) {
	f := newE2EFixtureWithSortableSearch(t, e2eAllSorts)
	reqs := recordSearchRequests(t, f.e2eFixture)
	both := []string{e2eSortBigSource, e2eSortSmallSource}

	var names []string
	var selected string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("item", "&sort=updated")),
		reqs.settled(1),
		searchRowNames(&names),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="sort"]').value`, &selected),
	)
	assert.Equal(t, "updated", reqs.last().Get("sort"), "a deep link's sort is what the first search sends")
	assert.Equal(t, "updated", selected)
	assert.Equal(t, f.Catalog.rows(both, "", "", domain.SortUpdated, 0), names)

	// Choosing a sort writes it into the URL without adding a history entry.
	var search string
	f.runInBrowser(t,
		chromedp.SetValue(`.search-page select[name="sort"]`, "popular", chromedp.ByQuery),
		reqs.settled(2),
		chromedp.Evaluate(`window.location.search`, &search),
	)
	q, err := url.ParseQuery(strings.TrimPrefix(search, "?"))
	require.NoError(t, err)
	assert.Equal(t, "popular", q.Get("sort"))
	assert.Equal(t, "item", q.Get("q"), "the query stays")

	// A reload comes back in the same sort.
	f.runInBrowser(t,
		chromedp.Reload(),
		reqs.settled(3),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="sort"]').value`, &selected),
	)
	assert.Equal(t, "popular", reqs.last().Get("sort"))
	assert.Equal(t, "popular", selected)

	// Back to relevance drops it from the URL.
	f.runInBrowser(t,
		chromedp.SetValue(`.search-page select[name="sort"]`, "relevance", chromedp.ByQuery),
		reqs.settled(4),
		chromedp.Evaluate(`window.location.search`, &search),
	)
	q, err = url.ParseQuery(strings.TrimPrefix(search, "?"))
	require.NoError(t, err)
	assert.False(t, q.Has("sort"))

	// A sort nobody offers is not sent (it would be a 400), and the page is fine.
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("item", "&sort=bogus")),
		reqs.settled(5),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="sort"]').value`, &selected),
	)
	assert.False(t, reqs.last().Has("sort"), "an unknown sort is dropped, not forwarded: %v", reqs.last())
	assert.Equal(t, "relevance", selected)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchSortNotOfferedByTheReportFallsBackToRelevance: a sort the
// URL asks for that the search's own report does not list (no source that
// answered can order by it) shows relevance selected and is not sent again.
func TestE2E_SearchSortNotOfferedByTheReportFallsBackToRelevance(t *testing.T) {
	f := newE2EFixtureWithSortableSearch(t, []domain.SearchSort{domain.SortDownloads})
	reqs := recordSearchRequests(t, f.e2eFixture)

	var selected string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("item", "&sort=popular")),
		reqs.settled(1),
		chromedp.Evaluate(`document.querySelector('.search-page select[name="sort"]').value`, &selected),
	)
	assert.Equal(t, "popular", reqs.last().Get("sort"), "the deep link's sort is asked for")
	assert.Equal(t, "relevance", selected,
		"popular is not in sorts_available, so the select must not claim it")

	f.runInBrowser(t,
		chromedp.Click(`.search-page__pager button:last-child`, chromedp.ByQuery),
		reqs.settled(2),
	)
	assert.False(t, reqs.last().Has("sort"), "a sort the report does not offer is not passed on: %v", reqs.last())
	assert.Empty(t, f.BrowserErrors())
}
