package custom

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func i64(n int64) *int64 { return &n }

func modIDs(res source.SearchResult) []string {
	ids := make([]string, 0, len(res.Mods))
	for _, m := range res.Mods {
		ids = append(ids, m.ID)
	}
	return ids
}

// sortCatalogue is a local catalogue whose names (alphabetical: a..f) are
// deliberately NOT in any sort's order, so a sort that did nothing shows.
func sortCatalogue() []domain.Mod {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	return []domain.Mod{
		{ID: "a", Name: "a", UpdatedAt: day(1), Downloads: 5, Endorsements: i64(1)},
		{ID: "b", Name: "b", Downloads: 50}, // undated
		{ID: "c", Name: "c", UpdatedAt: day(9), Downloads: 5, Endorsements: i64(9)},
		{ID: "d", Name: "d", UpdatedAt: day(5), Endorsements: i64(0)},
		{ID: "e", Name: "e", UpdatedAt: day(9), Downloads: 20}, // ties c on date
		{ID: "f", Name: "f", UpdatedAt: day(3), Downloads: 5, Endorsements: i64(4)},
	}
}

func TestSearchMods_SortsTheWholeMatchSetBeforePaging(t *testing.T) {
	cat := sortCatalogue()
	tests := []struct {
		sort domain.SearchSort
		want []string
	}{
		{"", []string{"a", "b", "c", "d", "e", "f"}},
		{domain.SortRelevance, []string{"a", "b", "c", "d", "e", "f"}},
		// Updated desc, undated last, the tie (c, e) in the alphabetical base order.
		{domain.SortUpdated, []string{"c", "e", "d", "f", "a", "b"}},
		// Downloads desc; the three-way tie at 5 (a, c, f) and the zero (d) stay alphabetical.
		{domain.SortDownloads, []string{"b", "e", "a", "c", "f", "d"}},
		// Endorsements desc, nil last (a recorded 0 is still data and beats nil).
		{domain.SortPopular, []string{"c", "f", "a", "d", "b", "e"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.sort), func(t *testing.T) {
			res := searchMods(cat, source.SearchQuery{Sort: tt.sort})
			assert.Equal(t, tt.want, modIDs(res))
			assert.Equal(t, 6, res.TotalCount)

			// Page 2 of size 2 is the right slice of the SORTED set.
			p2 := searchMods(cat, source.SearchQuery{Sort: tt.sort, Page: 1, PageSize: 2})
			assert.Equal(t, tt.want[2:4], modIDs(p2))
		})
	}
}

func TestSearchMods_SortKeepsTheQueryFilter(t *testing.T) {
	cat := sortCatalogue()
	cat[0].Summary = "needle"
	cat[3].Summary = "needle"
	res := searchMods(cat, source.SearchQuery{Query: "needle", Sort: domain.SortUpdated})
	assert.Equal(t, []string{"d", "a"}, modIDs(res))
	assert.Equal(t, 2, res.TotalCount)
}

// A local catalogue offers the updated sort only once a search has loaded it
// and found a date on at least one entry (#503): Capabilities reports what the
// catalogue last searched actually carried, and core reads it after the search
// answered. Before any search nothing is known, so nothing is offered.
func TestDirectoryCapabilities_UpdatedOnlyWhenTheCatalogueIsDated(t *testing.T) {
	d := newTestDirectory(t)
	assert.Empty(t, d.Capabilities().Sorts, "nothing is known before the first search")

	_, err := d.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	assert.Equal(t, []domain.SearchSort{domain.SortUpdated}, d.Capabilities().Sorts,
		"an entry's mtime is the one field a directory fills")

	empty, err := NewDirectory(SourceDefinition{ID: "e", Name: "E", Type: TypeDirectory, Directory: &DirectoryConfig{Path: t.TempDir()}})
	require.NoError(t, err)
	_, err = empty.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	assert.Empty(t, empty.Capabilities().Sorts, "a directory with no mods has nothing to order by date")
}

func TestDirectorySearch_SortUpdatedOrdersByModificationTime(t *testing.T) {
	d := newTestDirectory(t)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, days := range map[string]int{"BiggerBackpack": 1, "archived-mod-2.0.zip": 30, "PlainMod-0.5": 10} {
		stamp := base.AddDate(0, 0, days)
		require.NoError(t, os.Chtimes(filepath.Join(d.path, name), stamp, stamp))
	}
	res, err := d.Search(context.Background(), source.SearchQuery{Sort: domain.SortUpdated})
	require.NoError(t, err)
	assert.Equal(t, []string{"archived-mod-2.0", "PlainMod-0.5", "BiggerBackpack"}, modIDs(res))

	first, err := d.Search(context.Background(), source.SearchQuery{Sort: domain.SortUpdated, PageSize: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"archived-mod-2.0"}, modIDs(first), "page 1 is the newest, not the first alphabetically")
}

const undatedManifestYAML = `
version: 1
mods:
  - id: one
    name: One
    version: 1.0.0
    files:
      - id: main
        filename: one.zip
        url: https://example.com/files/one.zip
  - id: two
    name: Two
    version: 2.0.0
    updated_at: not-a-date
    files:
      - id: main
        filename: two.zip
        url: https://example.com/files/two.zip
`

func manifestFromYAML(t *testing.T, doc string) *Manifest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))
	m, err := NewManifest(manifestDef(path))
	require.NoError(t, err)
	return m
}

func TestManifestCapabilities_UpdatedOnlyWhenAnEntryIsDated(t *testing.T) {
	dated := manifestFromYAML(t, validManifestYAML)
	assert.Empty(t, dated.Capabilities().Sorts, "nothing is known before the first search")
	_, err := dated.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	assert.Equal(t, []domain.SearchSort{domain.SortUpdated}, dated.Capabilities().Sorts,
		"one dated entry is enough; the schema has no downloads or endorsements")

	// No entry dated (one has no updated_at, one an unparseable one): the
	// sort would order nothing, so it is not offered.
	undated := manifestFromYAML(t, undatedManifestYAML)
	res, err := undated.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	require.Len(t, res.Mods, 2)
	assert.Empty(t, undated.Capabilities().Sorts)
}

func TestManifestCapabilities_FollowTheCatalogueAsItChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte(undatedManifestYAML), 0o644))
	m, err := NewManifest(manifestDef(path))
	require.NoError(t, err)

	_, err = m.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	assert.Empty(t, m.Capabilities().Sorts)

	require.NoError(t, os.WriteFile(path, []byte(validManifestYAML), 0o644)) // a local manifest is re-read every search
	_, err = m.Search(context.Background(), source.SearchQuery{})
	require.NoError(t, err)
	assert.Equal(t, []domain.SearchSort{domain.SortUpdated}, m.Capabilities().Sorts)
}

func TestManifestSearch_SortUpdatedPutsUndatedLast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte(validManifestYAML), 0o644))
	m, err := NewManifest(manifestDef(path))
	require.NoError(t, err)

	res, err := m.Search(context.Background(), source.SearchQuery{Sort: domain.SortUpdated})
	require.NoError(t, err)
	require.Len(t, res.Mods, 2)
	assert.Equal(t, "cool-mod", res.Mods[0].ID, "the dated entry leads")
	assert.True(t, res.Mods[1].UpdatedAt.IsZero())
}

// The api source delegates ordering to the user's endpoint and never claims
// to sort natively, but it lists the sorts its MAPPED fields can fill: core
// orders the page that comes back.
func TestAPICapabilities_SortsFollowTheModMapping(t *testing.T) {
	sorts := func(mod map[string]string) []domain.SearchSort {
		def := apiDef("https://x.test")
		def.API.Mappings.Mod = mod
		a, err := NewAPI(def)
		require.NoError(t, err)
		return a.Capabilities().Sorts
	}
	base := map[string]string{"id": "id", "name": "name"}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	assert.Empty(t, sorts(base))
	assert.Equal(t, []domain.SearchSort{domain.SortUpdated}, sorts(with(map[string]string{"updated_at": "updated"})))
	assert.Equal(t, []domain.SearchSort{domain.SortDownloads}, sorts(with(map[string]string{"downloads": "dl"})))
	assert.Equal(t,
		[]domain.SearchSort{domain.SortUpdated, domain.SortDownloads},
		sorts(with(map[string]string{"updated_at": "updated", "downloads": "dl"})))
}

func TestAPICapabilities_NoSearchEndpointStillReportsNoSearch(t *testing.T) {
	def := apiDef("https://x.test")
	def.API.Endpoints.Search = nil
	def.API.Mappings.Mod = map[string]string{"id": "id", "name": "name", "downloads": "dl"}
	a, err := NewAPI(def)
	require.NoError(t, err)
	assert.False(t, a.Capabilities().Search)
	assert.Empty(t, a.Capabilities().Sorts, "nothing to sort when nothing can be searched")
}
