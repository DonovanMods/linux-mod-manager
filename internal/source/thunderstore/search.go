// Package thunderstore: this file is search - the resident copy of the
// index, the scoring, and the mapping onto SearchQuery/SearchResult.
//
// There is no inverted index and there should not be one: a full scan of
// the largest community on the site is 1.5-3.6 ms (measured), so anything
// cleverer is a data structure to keep correct for a 3 ms saving.
package thunderstore

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	// defaultPageSize matches steamworkshop's, and every frontend's
	// expectation of a page.
	defaultPageSize = 20
	// maxPageSize caps what one request can ask for.
	maxPageSize = 100
	// maxPage caps the page NUMBER, so that (page-1)*pageSize cannot
	// overflow int for any accepted pageSize. A billion pages of the
	// smallest page size is 10^9 results, four orders of magnitude past
	// the largest community on the site; every page above it is empty
	// anyway, and now says so instead of panicking (T1 review #1).
	maxPage = 1_000_000_000
)

// Scoring weights, summed PER TERM. The shape of the list is the ranking
// policy: what the user typed matching a package's NAME outranks it
// appearing in an owner or a description, and a category match is a weak
// signal because a category matches thousands of packages.
const (
	scoreExactName   = 100
	scoreNamePrefix  = 40
	scoreNameContain = 20
	scoreOwner       = 10
	scoreCategory    = 8
	scoreDescription = 3
	// deprecatedPenalty scales a deprecated package's score. It never
	// decides an ordering on its own - a deprecated hit sorts below every
	// live one regardless - but it keeps the two groups internally
	// comparable.
	deprecatedPenalty = 0.25
)

// residentIndex is the loaded, search-ready copy of one community's
// index.json: the rows themselves plus the case-folded projections every
// query needs, computed once at load rather than per query.
type residentIndex struct {
	fetchedAt int64
	rows      []indexRow
	terms     []rowTerms
	// byName addresses a row by its full_name, which is the mod id every
	// package read arrives with (#409 §3.2). Built with the rest of the
	// resident copy rather than scanned per call: an update check asks
	// about every installed mod in turn, and thirty linear scans of the
	// largest community on the site is a cost with no reason to exist.
	byName map[string]int
}

// row returns the index row for a package's full_name.
func (idx *residentIndex) row(fullName string) (indexRow, bool) {
	i, ok := idx.byName[fullName]
	if !ok {
		return indexRow{}, false
	}
	return idx.rows[i], true
}

// rowTerms is one row's precomputed lowercase form. haystack is the
// membership test ("does every term appear at all"); the separate fields
// are what scoring weighs.
//
// description is a SLICE of haystack rather than its own string: it is the
// largest field by far, and it already sits inside haystack at a known
// offset (T1 review nit 12).
type rowTerms struct {
	haystack    string
	fullName    string
	name        string
	owner       string
	description string
	categories  []string
	updated     time.Time
	deprecated  bool
	nsfw        bool
}

// The synthetic categories (#410): facts Thunderstore flags on a package
// rather than lists among its categories. They are SHOWN as categories -
// every renderer already prints those - and FILTER like categories, but
// they are not searchable text and never score: typing "deprecated" is a
// query about package names, not a filter.
const (
	categoryDeprecated = "Deprecated"
	categoryNSFW       = "NSFW"
)

// Search implements source.ModSource over the local index (#360). It
// refreshes the index on its own TTL first - correctness must not depend on
// a frontend remembering to prime it - and builds it outright when there is
// none, which is the one-time cost §2.7's readout exists to explain.
//
// Everything here is a READ as far as the Service's query/mutation contract
// is concerned: no mutation slot, no cross-process lock, and the build is
// cancellable through ctx.
func (s *Source) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	community := query.GameID
	if err := validateCommunity(community); err != nil {
		return source.SearchResult{}, err
	}
	wm, rows, present, refreshErr := s.ensureIndex(ctx, community, false, nil)
	if !present {
		return source.SearchResult{}, refreshErr
	}
	idx, err := s.residentFor(community, wm, rows)
	if err != nil {
		return source.SearchResult{}, indexUnavailable(community, err)
	}
	// refreshErr here is a refresh that failed over an index still worth
	// serving: the stale copy answers the query rather than the user seeing
	// nothing, and the result SAYS so (#360 §2.4) rather than passing an old
	// answer off as a current one.
	var warnings []error
	if refreshErr != nil {
		warnings = []error{staleIndexWarning(community, wm, refreshErr)}
	}

	matches, hidden := idx.match(query)
	if len(matches) == 0 && hidden > 0 {
		warnings = append(warnings, hiddenNSFWWarning(hidden))
	}
	page, pageSize := clampPaging(query.Page, query.PageSize)
	total := len(matches)

	start := pageStart(page, pageSize, total)
	end := min(start+pageSize, total)

	mods := make([]domain.Mod, 0, end-start)
	for _, m := range matches[start:end] {
		mods = append(mods, modFromRow(community, idx.rows[m.row]))
	}
	return source.SearchResult{Mods: mods, TotalCount: total, Page: page, PageSize: pageSize, Warnings: warnings}, nil
}

// staleIndexWarning words a refresh that failed over a copy still being
// served: which community, how old the answer is, and why it could not be
// replaced. err keeps its chain, so the warning still classifies as
// source.ErrIndexUnavailable.
func staleIndexWarning(community string, wm watermark, err error) error {
	// The reader's local time, as every other time lmm prints (T3 review
	// F12).
	fetched := time.Unix(wm.FetchedAt, 0).Local().Format("2006-01-02 15:04")
	return fmt.Errorf("results come from the %s index fetched %s, because refreshing it failed: %w", community, fetched, err)
}

// clampPaging applies the page defaults. A page past the end is not an
// error - it is an empty page with the same TotalCount, which is what a
// paginated frontend expects when the index shrank under it.
//
// BOTH bounds are real bounds, not just defaults (T1 review #1). A page
// number reaches this source straight off the wire - `lmm serve` forwards
// ?page= verbatim - and this is the first source that does its own
// arithmetic with one instead of handing it to a remote API, so a value
// large enough to overflow `(page-1)*pageSize` used to slice a negative
// index and panic the request. maxPage is what keeps that arithmetic
// inside int; anything above it is a page past the end, which is already
// an empty page rather than an error.
func clampPaging(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if page > maxPage {
		page = maxPage
	}
	switch {
	case pageSize <= 0:
		pageSize = defaultPageSize
	case pageSize > maxPageSize:
		pageSize = maxPageSize
	}
	return page, pageSize
}

// pageStart is the first index of page, or total when the page lies past
// the end. Written as a division rather than as a multiplication and a
// clamp: (page-1)*pageSize is the product that overflows, and no clamp
// applied AFTER it can tell an overflowed product from a real one.
func pageStart(page, pageSize, total int) int {
	if page-1 > total/pageSize {
		return total
	}
	return min((page-1)*pageSize, total)
}

// scored is one matching row and what it scored.
type scored struct {
	row   int
	score float64
}

// match filters and ranks the whole index for one query.
//
// Filtering runs BEFORE ranking, so TotalCount counts filtered hits.
// Category and Tags both filter Thunderstore's categories - it has no
// second concept - exactly and case-folded, ANDed together, and the
// synthetic "Deprecated" and "NSFW" filter the same way.
//
// An NSFW package is left out unless the query asks for the NSFW category
// (#410, T1 review #7). That is the default Thunderstore's own clients
// ship, and the opt-in is the filter every frontend already has - so a
// browse of the community cannot put one in front of a user who did not
// ask, and a user who did ask sees only those.
//
// hidden counts the rows that matched everything but were left out for
// being NSFW, which the caller turns into a hint when nothing else matched.
func (idx *residentIndex) match(query source.SearchQuery) (matches []scored, hidden int) {
	terms := strings.Fields(strings.ToLower(query.Query))
	required := requiredCategories(query)
	wantNSFW := slices.Contains(required, strings.ToLower(categoryNSFW))

	matches = make([]scored, 0, 64)
	for i := range idx.terms {
		row := &idx.terms[i]
		if !row.hasEvery(required) {
			continue
		}
		if !row.contains(terms) {
			continue
		}
		if row.nsfw && !wantNSFW {
			hidden++
			continue
		}
		matches = append(matches, scored{row: i, score: row.score(terms)})
	}
	idx.rank(matches)
	return matches, hidden
}

// hiddenNSFWWarning is the hint a search that found nothing gets when the
// NSFW filter is why (T3 review F12).
func hiddenNSFWWarning(hidden int) error {
	if hidden == 1 {
		return fmt.Errorf("1 package marked NSFW matches this search and is hidden; ask for the %s category, or name the package by its id, to see it", categoryNSFW)
	}
	return fmt.Errorf("%d packages marked NSFW match this search and are hidden; ask for the %s category, or name a package by its id, to see them", hidden, categoryNSFW)
}

// requiredCategories collects the case-folded categories a row must carry.
func requiredCategories(query source.SearchQuery) []string {
	required := make([]string, 0, len(query.Tags)+1)
	if query.Category != "" {
		required = append(required, strings.ToLower(query.Category))
	}
	for _, tag := range query.Tags {
		if tag != "" {
			required = append(required, strings.ToLower(tag))
		}
	}
	return required
}

// rank orders matches: live packages before deprecated ones, then score,
// then most recently updated, then name. The last two keys are what make
// paging STABLE - two queries for page 1 and page 2 must not interleave.
func (idx *residentIndex) rank(matches []scored) {
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := &idx.terms[matches[i].row], &idx.terms[matches[j].row]
		if da, db := idx.rows[matches[i].row].Deprecated, idx.rows[matches[j].row].Deprecated; da != db {
			return !da
		}
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if !a.updated.Equal(b.updated) {
			return a.updated.After(b.updated)
		}
		return a.fullName < b.fullName
	})
}

// hasEvery reports whether the row carries every required category,
// counting the synthetic ones its flags stand for.
func (r *rowTerms) hasEvery(required []string) bool {
	for _, want := range required {
		switch want {
		case strings.ToLower(categoryDeprecated):
			if !r.deprecated {
				return false
			}
			continue
		case strings.ToLower(categoryNSFW):
			if !r.nsfw {
				return false
			}
			continue
		}
		found := false
		for _, have := range r.categories {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// contains reports whether EVERY term appears somewhere in the row. An
// empty query matches everything, which is the browse case.
func (r *rowTerms) contains(terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(r.haystack, term) {
			return false
		}
	}
	return true
}

// score sums each term's contribution. An empty query scores 0 for every
// row, leaving the ranking to date_updated - browse order.
func (r *rowTerms) score(terms []string) float64 {
	total := 0.0
	for _, term := range terms {
		switch {
		case r.fullName == term, r.name == term:
			total += scoreExactName
		case strings.HasPrefix(r.name, term):
			total += scoreNamePrefix
		case strings.Contains(r.name, term):
			total += scoreNameContain
		}
		if strings.Contains(r.owner, term) {
			total += scoreOwner
		}
		for _, cat := range r.categories {
			if cat == term {
				total += scoreCategory
				break
			}
		}
		if strings.Contains(r.description, term) {
			total += scoreDescription
		}
	}
	if r.deprecated {
		total *= deprecatedPenalty
	}
	return total
}

// residentFor returns the loaded index for community, loading it if this is
// the first search in this process or if a refresh has moved the watermark
// since the last one.
//
// rows is index.json ALREADY PARSED, when the caller had to parse it to
// decide the index was usable at all (T1 review #4). Reading the file
// again to answer a question already answered one frame up was a third of
// a warm search's wall clock on the largest community. nil means nothing
// was parsed - a resident copy already matched, or a rebuild replaced the
// bytes - and the file is read here.
func (s *Source) residentFor(community string, wm watermark, rows []indexRow) (*residentIndex, error) {
	s.mu.Lock()
	if idx, ok := s.resident[community]; ok && idx.fetchedAt == wm.FetchedAt {
		s.mu.Unlock()
		return idx, nil
	}
	s.mu.Unlock()

	if rows == nil {
		loaded, err := s.store.loadIndex(community)
		if err != nil {
			return nil, err
		}
		rows = loaded.Rows
	}
	idx := newResidentIndex(wm.FetchedAt, rows)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.resident[community] = idx
	return idx, nil
}

// residentMatches reports whether the loaded copy was built from the index
// generation fetchedAt names.
func (s *Source) residentMatches(community string, fetchedAt int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.resident[community]
	return ok && idx.fetchedAt == fetchedAt
}

// dropResident forgets the loaded copy, so the next search reloads. Called
// after a rebuild.
func (s *Source) dropResident(community string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resident, community)
}

// newResidentIndex precomputes every row's case-folded projection. This is
// the whole cost of a load - 87 ms and 42 MB for the largest community on
// the site - paid once per process per index generation.
func newResidentIndex(fetchedAt int64, rows []indexRow) *residentIndex {
	terms := make([]rowTerms, len(rows))
	byName := make(map[string]int, len(rows))
	for i, row := range rows {
		// First wins, so two rows claiming one full_name (which the site
		// cannot serve, but a corrupt index could) resolve to the row a
		// search would rank first rather than to whichever came last.
		if _, dup := byName[row.FullName]; !dup {
			byName[row.FullName] = i
		}
		_, name, _ := SplitPackage(row.FullName)
		owner := strings.TrimSuffix(row.FullName, "-"+name)
		cats := make([]string, len(row.Categories))
		for j, c := range row.Categories {
			cats[j] = strings.ToLower(c)
		}
		lowerFull := strings.ToLower(row.FullName)
		lowerDesc := strings.ToLower(row.Description)
		// One haystack per row, NUL-joined so a term cannot match across a
		// field boundary.
		haystack := lowerFull + "\x00" + lowerDesc + "\x00" + strings.Join(cats, "\x00")
		// description is a SLICE of the haystack, not a second copy of it
		// (T1 review nit 12). Descriptions are the largest field here by a
		// wide margin, and a Go string slice shares its backing array, so
		// this drops roughly a third of the resident footprint for the
		// cost of two offsets that the line above just determined.
		descStart := len(lowerFull) + 1
		terms[i] = rowTerms{
			haystack:    haystack,
			fullName:    lowerFull,
			name:        strings.ToLower(name),
			owner:       strings.ToLower(owner),
			description: haystack[descStart : descStart+len(lowerDesc)],
			categories:  cats,
			updated:     parseTimestamp(row.DateUpdated),
			deprecated:  row.Deprecated,
			nsfw:        row.NSFW,
		}
	}
	return &residentIndex{fetchedAt: fetchedAt, rows: rows, terms: terms, byName: byName}
}

// parseTimestamp reads one of Thunderstore's RFC 3339 timestamps. An
// unparseable value sorts as the zero time rather than failing a whole
// index load over one bad row.
func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// modFromRow maps one index row onto domain.Mod.
//
// Everything a search result shows is in the row, so a page of twenty
// results reads packages.jsonl zero times. URL and image URL are DERIVED
// rather than stored: both are pure functions of community/owner/name/
// version, which is why the index does not carry them.
func modFromRow(community string, row indexRow) domain.Mod {
	ns, name, _ := SplitPackage(row.FullName)
	return domain.Mod{
		ID:          row.FullName,
		SourceID:    sourceID,
		Name:        strings.ReplaceAll(name, "_", " "),
		Version:     row.LatestVersion,
		Author:      ns,
		Description: row.Description,
		GameID:      community,
		Category:    strings.Join(displayCategories(row), ", "),
		PictureURL:  IconURL(row.FullName, row.LatestVersion),
		SourceURL:   PackageURL(community, ns, name),
		UpdatedAt:   parseTimestamp(row.DateUpdated),
	}
}

// displayCategories is the row's categories, with the synthetic
// "Deprecated" and "NSFW" entries appended when the package carries those
// flags. Neither gets a domain.Mod field of its own: they ride in the
// categories every existing renderer already prints.
func displayCategories(row indexRow) []string {
	if !row.Deprecated && !row.NSFW {
		return row.Categories
	}
	cats := append([]string{}, row.Categories...)
	if row.Deprecated {
		cats = append(cats, categoryDeprecated)
	}
	if row.NSFW {
		cats = append(cats, categoryNSFW)
	}
	return cats
}

// PackageURL is a package's page on Thunderstore.
func PackageURL(community, namespace, name string) string {
	return fmt.Sprintf("%s/c/%s/p/%s/%s/", DefaultBaseURL, community, namespace, name)
}

// IconURL is a specific version's icon on Thunderstore's CDN. Derived, not
// stored: the index keeps neither the icon nor the download URL, because
// both are this same string substitution.
func IconURL(fullName, version string) string {
	if version == "" {
		return ""
	}
	return fmt.Sprintf("https://gcdn.thunderstore.io/live/repository/icons/%s-%s.png", fullName, version)
}

// SplitPackage splits a package's full_name into its namespace and name.
//
// Verified across all 50,707 packages of the largest community: owner and
// name are strictly [A-Za-z0-9_]+ and full_name is always exactly
// owner + "-" + name, so the split on the FIRST "-" is unambiguous. ok is
// false for anything that is not that shape, which callers treat as a
// package they cannot address rather than guessing.
func SplitPackage(fullName string) (namespace, name string, ok bool) {
	ns, rest, found := strings.Cut(fullName, "-")
	if !found || ns == "" || rest == "" {
		return "", "", false
	}
	return ns, rest, true
}
