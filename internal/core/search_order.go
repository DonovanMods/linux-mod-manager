package core

import (
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// The ordering contract of every search result list (#503), applied by
// Service.Search to whatever the sources returned - one source, the
// aggregate merge, the omnibar's capped fan-out - so every source and
// every frontend behaves the same regardless of how much of it a source
// does natively:
//
//  1. Hits whose name IS the query come first (nameMatchesQuery), in the
//     order they arrived.
//  2. The rest follow in the chosen sort: updated is UpdatedAt, newest
//     first, with an unknown date last; downloads is Downloads, most first;
//     popular is Endorsements, most first, with a source that reports no
//     rating (nil) after one that reports zero; relevance leaves the
//     sources' own order alone.
//  3. Ties keep the order they arrived in (every step is stable).
//
// Sources that can sort server-side do (source.SearchQuery.Sort), which is
// what makes page N the right page; this pass is what makes the page they
// return correct whether or not they did.

// searchNameKey folds a name to lowercase letters and digits only, so
// "Leatrix Plus", "leatrix-plus" and "Leatrix_Plus" are one key.
func searchNameKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// nameMatchesQuery reports whether name is the query: equal ignoring case
// and surrounding space, or equal once both are folded by searchNameKey.
// Equality, never containment - "sky" is not "SkyUI". An empty query
// matches nothing.
func nameMatchesQuery(name, query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(name), q) {
		return true
	}
	key := searchNameKey(q)
	return key != "" && searchNameKey(name) == key
}

// compareHits orders two hits by sort alone: negative when a belongs before
// b, positive when after, zero for a tie (and always zero for relevance).
func compareHits(sort domain.SearchSort, a, b *domain.Mod) int {
	switch sort {
	case domain.SortUpdated:
		switch {
		case a.UpdatedAt.IsZero() && b.UpdatedAt.IsZero():
			return 0
		case a.UpdatedAt.IsZero():
			return 1
		case b.UpdatedAt.IsZero():
			return -1
		default:
			return b.UpdatedAt.Compare(a.UpdatedAt)
		}
	case domain.SortDownloads:
		return compareDescending(a.Downloads, b.Downloads)
	case domain.SortPopular:
		switch {
		case a.Endorsements == nil && b.Endorsements == nil:
			return 0
		case a.Endorsements == nil:
			return 1
		case b.Endorsements == nil:
			return -1
		default:
			return compareDescending(*a.Endorsements, *b.Endorsements)
		}
	default:
		return 0
	}
}

func compareDescending(a, b int64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	default:
		return 0
	}
}

// orderSearchHits applies the contract above to mods in place.
func orderSearchHits(mods []domain.Mod, query string, sort domain.SearchSort) {
	if sort != domain.SortRelevance && sort != "" {
		slices.SortStableFunc(mods, func(a, b domain.Mod) int { return compareHits(sort, &a, &b) })
	}
	// Stable partition: exact matches, then everything else, each keeping
	// the order the sort (or the source) left it in.
	exact := make([]domain.Mod, 0, 2)
	rest := make([]domain.Mod, 0, len(mods))
	for _, m := range mods {
		if nameMatchesQuery(m.Name, query) {
			exact = append(exact, m)
		} else {
			rest = append(rest, m)
		}
	}
	copy(mods, append(exact, rest...))
}

// sortsOffered is the sorts a frontend may offer for results that came from
// the sources in caps: relevance, then every other sort some source lists
// as meaningful (source.Capabilities.Sorts), in domain.SearchSorts order.
func sortsOffered(caps []source.Capabilities) []domain.SearchSort {
	out := []domain.SearchSort{domain.SortRelevance}
	for _, s := range domain.SearchSorts() {
		if s == domain.SortRelevance {
			continue
		}
		for _, c := range caps {
			if c.SupportsSort(s) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// IsInvalidSearchSort reports whether err is a refused SearchOptions.Sort
// (it wraps domain.ErrInvalidSearchSort) - bad input rather than a failure,
// and the classifier a frontend that may import no more than core branches
// on.
func IsInvalidSearchSort(err error) bool { return errors.Is(err, domain.ErrInvalidSearchSort) }
