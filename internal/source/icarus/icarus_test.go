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

func TestIcarus_Search_FiltersClientSide(t *testing.T) {
	srv := httptest.NewServer(modsListHandler([]map[string]any{
		{"id": "abc", "fields": map[string]any{
			"name": map[string]any{"stringValue": "Bear Mount"}, "author": map[string]any{"stringValue": "Jimk72"},
			"description": map[string]any{"stringValue": "Ride a bear"}, "version": map[string]any{"stringValue": "3.3"},
			"compatibility": map[string]any{"stringValue": "w57"},
			"files":         map[string]any{"mapValue": map[string]any{"fields": map[string]any{"exmodz": map[string]any{"stringValue": "https://x/bear.exmodz"}}}},
		}},
		{"id": "def", "fields": map[string]any{
			"name": map[string]any{"stringValue": "Wolf Pack"}, "author": map[string]any{"stringValue": "Someone"},
			"description": map[string]any{"stringValue": "Tame wolves"}, "version": map[string]any{"stringValue": "1.0"},
			"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{"pak": map[string]any{"stringValue": "https://x/wolf.pak"}}}},
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
