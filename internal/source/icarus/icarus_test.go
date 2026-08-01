package icarus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

func modsListHandler(mods []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docs := make([]map[string]any, len(mods))
		for i, m := range mods {
			docs[i] = map[string]any{
				"name":   "projects/p/databases/(default)/documents/mods/" + m["id"].(string),
				"fields": m["fields"],
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"documents": docs}) //nolint:errcheck
	}
}

// The mock server deliberately returns documents in non-alphabetical order
// (Wolf, Bear, Aardvark) — Firestore's listCollection order isn't guaranteed
// stable across runs, so Search must sort before paginating rather than
// trusting (or accidentally reproducing) whatever order the server used.
func TestIcarus_Search_FiltersClientSide(t *testing.T) {
	srv := httptest.NewServer(modsListHandler([]map[string]any{
		{"id": "def", "fields": map[string]any{
			"name": map[string]any{"stringValue": "Wolf Pack"}, "author": map[string]any{"stringValue": "Someone"},
			"description": map[string]any{"stringValue": "Tame wolves"}, "version": map[string]any{"stringValue": "1.0"},
			"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{"pak": map[string]any{"stringValue": "https://x/wolf.pak"}}}},
		}},
		{"id": "abc", "fields": map[string]any{
			"name": map[string]any{"stringValue": "Bear Mount"}, "author": map[string]any{"stringValue": "Jimk72"},
			"description": map[string]any{"stringValue": "Ride a bear"}, "version": map[string]any{"stringValue": "3.3"},
			"compatibility": map[string]any{"stringValue": "w57"},
			"files":         map[string]any{"mapValue": map[string]any{"fields": map[string]any{"exmodz": map[string]any{"stringValue": "https://x/bear.exmodz"}}}},
		}},
		{"id": "ghi", "fields": map[string]any{
			"name": map[string]any{"stringValue": "Aardvark Delight"}, "author": map[string]any{"stringValue": "Someone"},
			"description": map[string]any{"stringValue": "Burrowing companion"}, "version": map[string]any{"stringValue": "1.0"},
			"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{"pak": map[string]any{"stringValue": "https://x/aardvark.pak"}}}},
		}},
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	result, err := src.Search(context.Background(), source.SearchQuery{Query: "bear"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Mods) != 1 || result.Mods[0].Name != "Bear Mount" {
		t.Fatalf("Search(%q) = %+v, want exactly Bear Mount", "bear", result.Mods)
	}
	if result.Mods[0].GameID != "icarus" {
		t.Errorf("GameID = %q, want icarus", result.Mods[0].GameID)
	}

	all, err := src.Search(context.Background(), source.SearchQuery{Query: ""})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	wantOrder := []string{"Aardvark Delight", "Bear Mount", "Wolf Pack"}
	if len(all.Mods) != len(wantOrder) {
		t.Fatalf("Search(\"\") returned %d mods, want %d", len(all.Mods), len(wantOrder))
	}
	for i, want := range wantOrder {
		if all.Mods[i].Name != want {
			t.Errorf("Search(\"\") order[%d] = %q, want %q (deterministic, alphabetical by Name)", i, all.Mods[i].Name, want)
		}
	}
}

func TestIcarus_GetModFiles_ReturnsExmodzAndPak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name": "projects/p/databases/(default)/documents/mods/abc",
			"fields": map[string]any{
				"name": map[string]any{"stringValue": "Bear Mount"},
				"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{
					"exmodz": map[string]any{"stringValue": "https://x/bear.exmodz"},
				}}},
			},
		})
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	files, err := src.GetModFiles(context.Background(), &domain.Mod{ID: "abc", GameID: "icarus"})
	if err != nil {
		t.Fatalf("GetModFiles: %v", err)
	}
	if len(files) != 1 || files[0].FileName != "bear.exmodz" {
		t.Fatalf("files = %+v, want one bear.exmodz entry", files)
	}
	if !files[0].IsPrimary {
		t.Error("single file should be marked primary")
	}
}

// The fallback must always be a dotted name (never a bare "exmodz"/"pak"),
// or isExmodzFile's ".exmodz" suffix check and compiledFileName's
// filepath.Ext-based rename in Service would both silently misroute the
// download instead of failing loudly (#136 review round 2).
func TestFileNameFromURL(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		fallbackExt string
		want        string
	}{
		{"basename with extension is used as-is", "https://x/mods/Bear_Mount.exmodz", "exmodz", "Bear_Mount.exmodz"},
		{"basename without an extension gets fallbackExt appended", "https://x/mods/Bear_Mount", "exmodz", "Bear_Mount.exmodz"},
		{"root path falls back to a dotted name", "https://x/", "pak", "mod.pak"},
		{"empty path falls back to a dotted name", "https://x", "pak", "mod.pak"},
		{"a path of exactly '..' falls back to a dotted name", "https://x/mods/..", "exmodz", "mod.exmodz"},
		{"an unparseable URL falls back to a dotted name", "://not a url", "pak", "mod.pak"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fileNameFromURL(tt.rawURL, tt.fallbackExt)
			if got != tt.want {
				t.Errorf("fileNameFromURL(%q, %q) = %q, want %q", tt.rawURL, tt.fallbackExt, got, tt.want)
			}
		})
	}
}
