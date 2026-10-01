package icarus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// sortedSource serves four catalogue documents whose names (Aardvark..Dingo)
// run opposite to their updateTime, with one undated and one tied on date.
func sortedSource(t *testing.T) *Icarus {
	t.Helper()
	doc := func(id, name, updated string) map[string]any {
		d := map[string]any{
			"name":   "projects/p/databases/(default)/documents/mods/" + id,
			"fields": map[string]any{"name": map[string]any{"stringValue": name}},
		}
		if updated != "" {
			d["updateTime"] = updated
		}
		return d
	}
	docs := []map[string]any{
		doc("a", "Aardvark", "2025-01-01T00:00:00Z"),
		doc("b", "Badger", ""),
		doc("c", "Cougar", "2025-03-01T00:00:00Z"),
		doc("d", "Dingo", "2025-03-01T00:00:00Z"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"documents": docs}) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL
	return src
}

func searchIDs(t *testing.T, src *Icarus, q source.SearchQuery) []string {
	t.Helper()
	res, err := src.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	ids := make([]string, 0, len(res.Mods))
	for _, m := range res.Mods {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestIcarus_Capabilities_ListsUpdatedOnly(t *testing.T) {
	got := New(http.DefaultClient, "p").Capabilities().Sorts
	if !slices.Equal(got, []domain.SearchSort{domain.SortUpdated}) {
		t.Fatalf("Sorts = %v, want [updated]", got)
	}
}

func TestIcarus_Search_SortUpdatedOrdersBeforePaging(t *testing.T) {
	src := sortedSource(t)

	want := []string{"c", "d", "a", "b"} // newest first, tie by name, undated last
	if got := searchIDs(t, src, source.SearchQuery{Sort: domain.SortUpdated}); !slices.Equal(got, want) {
		t.Fatalf("updated order = %v, want %v", got, want)
	}
	if got := searchIDs(t, src, source.SearchQuery{Sort: domain.SortUpdated, PageSize: 2}); !slices.Equal(got, want[:2]) {
		t.Fatalf("page 1 = %v, want the two newest %v", got, want[:2])
	}
	if got := searchIDs(t, src, source.SearchQuery{Sort: domain.SortUpdated, PageSize: 2, Page: 1}); !slices.Equal(got, want[2:]) {
		t.Fatalf("page 2 = %v, want %v", got, want[2:])
	}
}

func TestIcarus_Search_OtherSortsKeepTheNameOrder(t *testing.T) {
	src := sortedSource(t)
	want := []string{"a", "b", "c", "d"}
	for _, sort := range []domain.SearchSort{"", domain.SortRelevance, domain.SortDownloads, domain.SortPopular} {
		if got := searchIDs(t, src, source.SearchQuery{Sort: sort}); !slices.Equal(got, want) {
			t.Errorf("sort %q: order = %v, want %v", sort, got, want)
		}
	}
}
