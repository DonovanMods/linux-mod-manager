package custom

import (
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// searchMods implements the client-side search semantics shared by directory
// and manifest sources (design §5): case-insensitive substring match on
// name/ID/summary, name matches ranked before summary-only matches, then
// alphabetical; local pagination with default page size 20. GameID is stamped
// onto every returned mod so downstream installs are attributed correctly.
//
// query.Sort (#503) reorders the WHOLE match set before it is paged, so page N
// is the right page: the stable sort over the order above means ties keep the
// name-match-then-alphabetical order. Empty or relevance leaves that order
// untouched.
func searchMods(mods []domain.Mod, query source.SearchQuery) source.SearchResult {
	q := strings.ToLower(query.Query)
	type ranked struct {
		mod       domain.Mod
		nameMatch bool
	}
	var matches []ranked
	for _, m := range mods {
		nameMatch := q == "" || strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.ID), q)
		summaryMatch := strings.Contains(strings.ToLower(m.Summary), q)
		if !nameMatch && !summaryMatch {
			continue
		}
		matches = append(matches, ranked{mod: m, nameMatch: nameMatch})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].nameMatch != matches[j].nameMatch {
			return matches[i].nameMatch
		}
		return matches[i].mod.Name < matches[j].mod.Name
	})

	if less := sortLess(query.Sort); less != nil {
		sort.SliceStable(matches, func(i, j int) bool { return less(&matches[i].mod, &matches[j].mod) })
	}

	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	start := query.Page * pageSize
	if start < 0 {
		start = 0
	}
	end := min(start+pageSize, len(matches))
	if start > len(matches) {
		start = len(matches)
	}

	out := make([]domain.Mod, 0, end-start)
	for _, m := range matches[start:end] {
		mod := m.mod
		mod.GameID = query.GameID
		out = append(out, mod)
	}

	return source.SearchResult{
		Mods:       out,
		TotalCount: len(matches),
		Page:       query.Page,
		PageSize:   pageSize,
	}
}

// sortLess is the ordering a search sort asks for, or nil for relevance and
// anything unrecognised (the caller's own order stands). Same rules as core's
// post-sort: updated newest first with an undated mod last, downloads most
// first, popular most endorsed first with an unknown count last.
func sortLess(by domain.SearchSort) func(a, b *domain.Mod) bool {
	switch by {
	case domain.SortUpdated:
		return func(a, b *domain.Mod) bool {
			if a.UpdatedAt.IsZero() != b.UpdatedAt.IsZero() {
				return !a.UpdatedAt.IsZero()
			}
			return a.UpdatedAt.After(b.UpdatedAt)
		}
	case domain.SortDownloads:
		return func(a, b *domain.Mod) bool { return a.Downloads > b.Downloads }
	case domain.SortPopular:
		return func(a, b *domain.Mod) bool {
			if (a.Endorsements == nil) != (b.Endorsements == nil) {
				return a.Endorsements != nil
			}
			return a.Endorsements != nil && *a.Endorsements > *b.Endorsements
		}
	}
	return nil
}
