package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateSearchTextGoldens re-records testdata/search_golden/.
var updateSearchTextGoldens = flag.Bool("update-search-text", false, "re-record the lmm search text goldens")

// datedSearchSource answers every search with a fixed hit list.
type datedSearchSource struct {
	pageSizeSpySource
	hits []domain.Mod
}

func (s *datedSearchSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{Mods: s.hits, TotalCount: len(s.hits)}, nil
}

func assertSearchTextGolden(t *testing.T, name, actual string) {
	t.Helper()
	path := filepath.Join("testdata", "search_golden", name+".txt")
	if *updateSearchTextGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(actual), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden %s missing - record it with -update-search-text", path)
	assert.Equal(t, string(want), actual)
}

// TestDoSearch_UpdatedColumn is #433's CLI half: a search whose hits carry
// a date gets an UPDATED column - an age for a recent hit, the date for an
// old one, "-" for the hit whose source reported none (never 0001-01-01) -
// and a search where no hit carries one gets no column at all.
func TestDoSearch_UpdatedColumn(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	orig := cliNow
	cliNow = func() time.Time { return now }
	t.Cleanup(func() { cliNow = orig })

	for _, tc := range []struct {
		name string
		hits []domain.Mod
	}{
		{"dated_and_undated", []domain.Mod{
			{ID: "1", Name: "Fresh", Author: "a", Version: "2.0", UpdatedAt: now.Add(-3 * time.Hour)},
			{ID: "2", Name: "Old", Author: "b", Version: "1.0", UpdatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)},
			{ID: "3", Name: "Undated", Author: "c", Version: "0.1"},
		}},
		{"all_undated", []domain.Mod{
			{ID: "3", Name: "Undated", Author: "c", Version: "0.1"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &datedSearchSource{pageSizeSpySource: pageSizeSpySource{id: "dated"}}
			for _, h := range tc.hits {
				h.SourceID = src.id
				src.hits = append(src.hits, h)
			}
			svc, game := newPageSizeSpyService(t, &src.pageSizeSpySource)
			svc.RegisterSource(src) // replaces the spy under the same id
			withSearchFlags(t, "", 10)

			out := captureStdout(t, func() error {
				return doSearch(context.Background(), svc, game, []string{"q"})
			})
			assert.NotContains(t, out, "0001-01-01")
			assertSearchTextGolden(t, tc.name, out)
		})
	}
}
