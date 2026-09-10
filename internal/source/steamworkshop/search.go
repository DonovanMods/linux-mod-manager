// Package steamworkshop: this file is Tier 2's search half (#269 W2) -
// Valve's IPublishedFileService/QueryFiles endpoint behind the user's own
// Steam Web API key, mapped onto lmm's source.SearchQuery/SearchResult.
//
// Search is the ONE call in this package that needs a credential.
// Everything else - item metadata, collections, the ACF scan - is keyless,
// which is why Tier 1 shipped without any auth at all.
package steamworkshop

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	// rankedByTextSearch and rankedByTrend are Valve's query_type values.
	// A search with words in it ranks by relevance; an empty one is a
	// BROWSE, and the useful default order for browsing a workshop is
	// what is currently popular.
	rankedByTextSearch = 12
	rankedByTrend      = 3

	// defaultPageSize / maxPageSize bound numperpage. 100 is Valve's own
	// ceiling for this endpoint; the default matches what a `lmm search`
	// caller gets when it names no page size.
	defaultPageSize = 20
	maxPageSize     = 100

	// searchTTL is how long one query's results stay fresh in memory.
	//
	// Five minutes is the design's number and it is short on purpose: a
	// workshop's ranking moves, and the value of the cache here is the
	// user pressing the same search twice, not lmm serving stale rows for
	// an hour. Unlike the metadata cache it is memory-only - a search is a
	// question, not a fact about an installed item, and a process that
	// exits has nothing worth keeping.
	searchTTL = 5 * time.Minute
)

// queryFilesResponse is QueryFiles' envelope. The row type is the same
// itemDetails GetPublishedFileDetails answers with - see its
// FileDescription field for the one naming difference between them.
type queryFilesResponse struct {
	Response struct {
		Total   int           `json:"total"`
		Details []itemDetails `json:"publishedfiledetails"`
	} `json:"response"`
}

// searchCache is the 5-minute memory of one process's searches, keyed on
// the fully normalised query (see searchKey).
//
// There is deliberately NO local quota counter beside it. Valve's Web API
// allows 100k calls a day per key, and that budget is the KEY's, not
// lmm's: the same key is very likely also in the user's Playnite, their
// shell profile and a script or two, and lmm can see none of that usage.
// A counter here would therefore be a confident wrong number, and the
// remedy for actually exhausting the quota - back off and try later - is
// already what the retry transport does when Valve says 429.
type searchCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]searchCacheEntry
}

type searchCacheEntry struct {
	result  source.SearchResult
	expires time.Time
}

func newSearchCache(now func() time.Time) *searchCache {
	if now == nil {
		now = time.Now
	}
	return &searchCache{now: now, entries: make(map[string]searchCacheEntry)}
}

// get returns a cached result that has not expired.
func (c *searchCache) get(key string) (source.SearchResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return source.SearchResult{}, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return source.SearchResult{}, false
	}
	return entry.result, true
}

// put records one result for searchTTL, evicting anything already expired
// so a long-lived `lmm serve` does not accumulate every search ever run.
func (c *searchCache) put(key string, result source.SearchResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = searchCacheEntry{result: result, expires: now.Add(searchTTL)}
}

// searchKey is the cache key: every parameter that changes the REQUEST,
// normalised so trivially different spellings of the same search share one
// entry. Tags are sorted because their order does not reach Valve as
// meaning - they are all required - and case/space folding of the free-text
// term matches how the endpoint itself treats it.
func searchKey(q source.SearchQuery, keyID string) string {
	tags := append([]string(nil), q.Tags...)
	sort.Strings(tags)
	// The key's FINGERPRINT is part of the identity: two keys are two
	// accounts, and a re-key must not serve the previous credential's
	// answers. The key itself never enters a map key (#79's rule: the
	// plaintext lives in exactly one place, the client that sends it).
	return strings.Join([]string{
		keyID,
		q.GameID,
		strings.ToLower(strings.TrimSpace(q.Query)),
		strings.ToLower(strings.TrimSpace(q.Category)),
		strings.ToLower(strings.Join(tags, "\x00")),
		strconv.Itoa(q.Page),
		strconv.Itoa(pageSizeOf(q)),
	}, "\x1f")
}

// pageSizeOf resolves the requested page size against Valve's ceiling.
func pageSizeOf(q source.SearchQuery) int {
	switch {
	case q.PageSize <= 0:
		return defaultPageSize
	case q.PageSize > maxPageSize:
		return maxPageSize
	default:
		return q.PageSize
	}
}

// Search implements source.ModSource: one page of Workshop items for the
// game's Steam app id.
//
// Without a key it returns domain.ErrAuthRequired without spending a
// request: Valve's answer to a keyless QueryFiles is a guaranteed 403, and
// asking anyway would be both pointless and rude. A key Valve REFUSES
// takes the same route, through the client's 403 mapping.
func (s *Source) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	if query.GameID == "" {
		return source.SearchResult{}, fmt.Errorf("source %q: searching: no Steam app id for this game (map one with `lmm game edit --source steamworkshop=<appid>`)", sourceID)
	}
	if !s.client.http.IsAuthenticated() {
		return source.SearchResult{}, fmt.Errorf("%w: Steam Web API key required (run `lmm auth login steamworkshop`)", domain.ErrAuthRequired)
	}

	cacheKey := searchKey(query, s.client.keyID)
	if cached, ok := s.client.search.get(cacheKey); ok {
		return cached, nil
	}

	var resp queryFilesResponse
	if err := s.client.http.DoJSON(ctx, "GET", queryFilesPath+"?"+searchParams(query).Encode(), &resp); err != nil {
		return source.SearchResult{}, fmt.Errorf("source %q: searching: %w", sourceID, err)
	}

	result := source.SearchResult{
		TotalCount: resp.Response.Total,
		Page:       query.Page,
		PageSize:   pageSizeOf(query),
	}
	for _, d := range resp.Response.Details {
		if d.PublishedFileID == "" || !d.searchable() {
			continue
		}
		result.Mods = append(result.Mods, modFromDetails(d, query.GameID))
	}
	s.client.search.put(cacheKey, result)
	return result, nil
}

// searchParams maps source.SearchQuery onto QueryFiles' parameters, per the
// design's table (docs/plans/2026-09-09-steam-workshop-design.md §4).
//
// Category is one MORE required tag rather than its own parameter: the
// Workshop has no category concept distinct from tags, and SearchQuery's
// own contract already says the value is source-specific.
//
// The return_* flags are what make the response worth mapping at all - a
// bare QueryFiles answers ids and titles, with no description, no preview
// and no tags to put in Category.
func searchParams(q source.SearchQuery) url.Values {
	v := url.Values{}
	v.Set("appid", q.GameID)
	text := strings.TrimSpace(q.Query)
	if text != "" {
		v.Set("search_text", text)
		v.Set("query_type", strconv.Itoa(rankedByTextSearch))
	} else {
		v.Set("query_type", strconv.Itoa(rankedByTrend))
	}
	// Steam's page is 1-based; SearchQuery.Page is 0-based everywhere else
	// in lmm.
	v.Set("page", strconv.Itoa(q.Page+1))
	v.Set("numperpage", strconv.Itoa(pageSizeOf(q)))

	i := 0
	for _, tag := range q.Tags {
		if tag = strings.TrimSpace(tag); tag == "" {
			continue
		}
		v.Set("requiredtags["+strconv.Itoa(i)+"]", tag)
		i++
	}
	if cat := strings.TrimSpace(q.Category); cat != "" {
		v.Set("requiredtags["+strconv.Itoa(i)+"]", cat)
	}

	v.Set("return_metadata", "1")
	v.Set("return_tags", "1")
	v.Set("return_previews", "1")
	v.Set("return_details", "1")
	return v
}
