package custom

import (
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// searchMods implements the client-side search semantics shared by directory
// and manifest sources (design §5): case-insensitive substring match on
// name/ID/summary, name matches ranked before summary-only matches, then
// alphabetical; local pagination with default page size 20. GameID is stamped
// onto every returned mod so downstream installs are attributed correctly.
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
