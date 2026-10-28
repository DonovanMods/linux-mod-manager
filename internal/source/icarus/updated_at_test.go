package icarus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// TestIcarus_ReportsTheCatalogEntrysUpdateTime pins #433 for this source:
// Firestore stamps every document with updateTime, and that is the date a
// search hit, a mod page and the install picker show. A document without
// one carries the zero time, which every surface renders as no date at all.
func TestIcarus_ReportsTheCatalogEntrysUpdateTime(t *testing.T) {
	fields := map[string]any{
		"name":  map[string]any{"stringValue": "Bear Mount"},
		"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{"exmodz": map[string]any{"stringValue": "https://example.com/bear.EXMODZ"}}}},
	}
	for _, tc := range []struct {
		name       string
		updateTime string
		want       time.Time
	}{
		{"dated", "2025-03-04T05:06:07.123456Z", time.Date(2025, 3, 4, 5, 6, 7, 123456000, time.UTC)},
		{"undated", "", time.Time{}},
		{"unparsable", "yesterday", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]any{"name": "projects/p/databases/(default)/documents/mods/abc", "fields": fields}
			if tc.updateTime != "" {
				doc["updateTime"] = tc.updateTime
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/projects/test-project/databases/(default)/documents/mods" {
					json.NewEncoder(w).Encode(map[string]any{"documents": []any{doc}}) //nolint:errcheck
					return
				}
				json.NewEncoder(w).Encode(doc) //nolint:errcheck
			}))
			defer srv.Close()
			src := New(srv.Client(), "test-project")
			src.firestore.baseURL = srv.URL
			ctx := context.Background()

			found, err := src.Search(ctx, source.SearchQuery{GameID: "icarus", Query: "bear"})
			if err != nil || len(found.Mods) != 1 {
				t.Fatalf("Search = %v, %v; want one hit", found.Mods, err)
			}
			if !found.Mods[0].UpdatedAt.Equal(tc.want) {
				t.Errorf("Search UpdatedAt = %v, want %v", found.Mods[0].UpdatedAt, tc.want)
			}

			mod, err := src.GetMod(ctx, "icarus", "abc")
			if err != nil {
				t.Fatalf("GetMod: %v", err)
			}
			if !mod.UpdatedAt.Equal(tc.want) {
				t.Errorf("GetMod UpdatedAt = %v, want %v", mod.UpdatedAt, tc.want)
			}

			files, err := src.GetModFiles(ctx, mod)
			if err != nil || len(files) != 1 {
				t.Fatalf("GetModFiles = %v, %v; want one file", files, err)
			}
			if !files[0].UploadedAt.Equal(tc.want) {
				t.Errorf("file UploadedAt = %v, want %v", files[0].UploadedAt, tc.want)
			}
		})
	}
}
