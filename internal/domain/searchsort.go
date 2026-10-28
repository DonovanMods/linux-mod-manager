package domain

import (
	"errors"
	"fmt"
	"strings"
)

// SearchSort is how a search orders its hits: the closed set `lmm search
// --sort` and `/api/v1/search?sort=` accept (#503). It is a string on the
// wire and in the CLI, so the zero value ("") means the default, relevance.
type SearchSort string

// The four sorts. Relevance is the sources' own order; the other three order
// by a field every domain.Mod carries (UpdatedAt, Downloads, Endorsements)
// and are always descending - newest, most downloaded and most endorsed
// first - because ascending is never what a search for a mod wants.
const (
	SortRelevance SearchSort = "relevance"
	SortUpdated   SearchSort = "updated"
	SortDownloads SearchSort = "downloads"
	SortPopular   SearchSort = "popular"
)

// ErrInvalidSearchSort is what ParseSearchSort wraps for a value outside the
// set: bad input, never a source failure.
var ErrInvalidSearchSort = errors.New("invalid search sort")

// SearchSorts returns every SearchSort in display order. A fresh slice each
// call, so a caller cannot reorder the set for everyone else.
func SearchSorts() []SearchSort {
	return []SearchSort{SortRelevance, SortUpdated, SortDownloads, SortPopular}
}

// ParseSearchSort reads a sort name, case-insensitively and ignoring
// surrounding space. Empty means SortRelevance. Anything else outside the
// set wraps ErrInvalidSearchSort and names the values that are valid.
func ParseSearchSort(s string) (SearchSort, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	if name == "" {
		return SortRelevance, nil
	}
	for _, v := range SearchSorts() {
		if string(v) == name {
			return v, nil
		}
	}
	names := make([]string, 0, 4)
	for _, v := range SearchSorts() {
		names = append(names, string(v))
	}
	return "", fmt.Errorf("%w %q: expected one of %s", ErrInvalidSearchSort, s, strings.Join(names, ", "))
}
