package icarus

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path"
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

// A huge user-supplied Page overflows the page*pageSize multiplication
// (int wraps to a negative value), which previously produced a negative
// slice start and panicked instead of just clamping to an empty page past
// the end of the result set (#136 PR #181 review).
func TestIcarus_Search_HugePage_ClampsInsteadOfPanicking(t *testing.T) {
	srv := httptest.NewServer(modsListHandler([]map[string]any{
		{"id": "abc", "fields": map[string]any{"name": map[string]any{"stringValue": "Bear Mount"}}},
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	result, err := src.Search(context.Background(), source.SearchQuery{Page: math.MaxInt, PageSize: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Mods) != 0 {
		t.Errorf("Mods = %+v, want empty (page far beyond the 1-mod result set)", result.Mods)
	}
	if result.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1", result.TotalCount)
	}
}

// The same overflow class hits start+pageSize (computing "end") once start
// is non-zero: page*pageSize (1*MaxInt) doesn't itself overflow, and gets
// clamped down to the small, valid len(mods) — but THAT small start plus a
// huge PageSize wraps the addition negative, and the existing
// "end > len(mods)" clamp can't catch an end that's gone negative (Page: 0
// can't exercise this: 0+anything never overflows).
func TestIcarus_Search_HugePageSize_ClampsInsteadOfPanicking(t *testing.T) {
	srv := httptest.NewServer(modsListHandler([]map[string]any{
		{"id": "abc", "fields": map[string]any{"name": map[string]any{"stringValue": "Bear Mount"}}},
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	result, err := src.Search(context.Background(), source.SearchQuery{Page: 1, PageSize: math.MaxInt})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Mods) != 0 {
		t.Errorf("Mods = %+v, want empty (page 1 is past the 1-mod result set)", result.Mods)
	}
	if result.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1", result.TotalCount)
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

func TestIcarus_GetModFiles_BothVariants_ExmodzIsPrimary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name": "projects/p/databases/(default)/documents/mods/abc",
			"fields": map[string]any{
				"name": map[string]any{"stringValue": "Bear Mount"},
				"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{
					"pak":    map[string]any{"stringValue": "https://x/bear.pak"},
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
	if len(files) != 2 {
		t.Fatalf("files = %+v, want 2 entries", files)
	}
	byID := map[string]domain.DownloadableFile{}
	for _, f := range files {
		byID[f.ID] = f
	}
	if !byID["exmodz"].IsPrimary {
		t.Error("exmodz must be the default when both variants exist")
	}
	if byID["pak"].IsPrimary {
		t.Error("pak should not be primary when exmodz exists")
	}
	if byID["exmodz"].Description != "mergeable EXMOD - recommended" {
		t.Errorf("exmodz Description = %q, want %q", byID["exmodz"].Description, "mergeable EXMOD - recommended")
	}
	if byID["pak"].Description != "prebuilt PAK" {
		t.Errorf("pak Description = %q, want %q", byID["pak"].Description, "prebuilt PAK")
	}
}

func TestIcarus_GetModFiles_SingleVariant_StaysPrimary(t *testing.T) {
	tests := []struct {
		name             string
		fileKey          string
		fileURL          string
		expectedFileName string
		expectedDesc     string
	}{
		{
			name:             "pak-only",
			fileKey:          "pak",
			fileURL:          "https://x/bear.pak",
			expectedFileName: "bear.pak",
			expectedDesc:     "prebuilt PAK",
		},
		{
			name:             "exmodz-only",
			fileKey:          "exmodz",
			fileURL:          "https://x/bear.exmodz",
			expectedFileName: "bear.exmodz",
			expectedDesc:     "mergeable EXMOD - recommended",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"name": "projects/p/databases/(default)/documents/mods/abc",
					"fields": map[string]any{
						"name": map[string]any{"stringValue": "Bear Mount"},
						"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{
							tt.fileKey: map[string]any{"stringValue": tt.fileURL},
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
			if len(files) != 1 {
				t.Fatalf("files = %+v, want exactly 1 entry", files)
			}
			if files[0].FileName != tt.expectedFileName {
				t.Errorf("FileName = %q, want %q", files[0].FileName, tt.expectedFileName)
			}
			if !files[0].IsPrimary {
				t.Error("single file should be marked primary")
			}
			if files[0].Description != tt.expectedDesc {
				t.Errorf("Description = %q, want %q", files[0].Description, tt.expectedDesc)
			}
		})
	}
}

// TestIcarus_GetMod pins the happy path: a single Firestore document maps
// through mapDoc into the domain.Mod fields Search/GetModFiles callers
// expect. GetMod's queryGameID parameter is deliberately not exercised here
// (tracked/accepted elsewhere, per this sweep's scope).
func TestIcarus_GetMod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name": "projects/p/databases/(default)/documents/mods/abc",
			"fields": map[string]any{
				"name":    map[string]any{"stringValue": "Bear Mount"},
				"author":  map[string]any{"stringValue": "Jimk72"},
				"version": map[string]any{"stringValue": "3.3"},
			},
		})
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	mod, err := src.GetMod(context.Background(), "icarus", "abc")
	if err != nil {
		t.Fatalf("GetMod: %v", err)
	}
	if mod.ID != "abc" || mod.Name != "Bear Mount" || mod.Author != "Jimk72" || mod.Version != "3.3" {
		t.Errorf("GetMod = %+v, want ID=abc Name=Bear Mount Author=Jimk72 Version=3.3", mod)
	}
	if mod.GameID != "icarus" {
		t.Errorf("GameID = %q, want icarus", mod.GameID)
	}
}

// TestIcarus_GetDownloadURL table-drives both the success path (fileID
// present, either "pak" or "exmodz") and the not-found error path.
func TestIcarus_GetDownloadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name": "projects/p/databases/(default)/documents/mods/abc",
			"fields": map[string]any{
				"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{
					"pak":    map[string]any{"stringValue": "https://x/bear.pak"},
					"exmodz": map[string]any{"stringValue": "https://x/bear.exmodz"},
				}}},
			},
		})
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL
	mod := &domain.Mod{ID: "abc", GameID: "icarus"}

	tests := []struct {
		name    string
		fileID  string
		want    string
		wantErr bool
	}{
		{name: "pak file ID resolves", fileID: "pak", want: "https://x/bear.pak"},
		{name: "exmodz file ID resolves", fileID: "exmodz", want: "https://x/bear.exmodz"},
		{name: "unrecognized file ID errors", fileID: "nope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := src.GetDownloadURL(context.Background(), mod, tt.fileID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("GetDownloadURL(%q) = %q, nil; want an error", tt.fileID, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetDownloadURL(%q): %v", tt.fileID, err)
			}
			if got != tt.want {
				t.Errorf("GetDownloadURL(%q) = %q, want %q", tt.fileID, got, tt.want)
			}
		})
	}
}

// TestIcarus_CheckUpdates drives two installed mods against a per-ID
// catalog: one whose stored version is behind the catalog's (an update is
// expected) and one that already matches (no update).
func TestIcarus_CheckUpdates(t *testing.T) {
	catalog := map[string]map[string]any{
		"abc": {"name": map[string]any{"stringValue": "Bear Mount"}, "version": map[string]any{"stringValue": "3.3"}},
		"def": {"name": map[string]any{"stringValue": "Wolf Pack"}, "version": map[string]any{"stringValue": "1.0"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		fields, ok := catalog[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name":   "projects/p/databases/(default)/documents/mods/" + id,
			"fields": fields,
		})
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "abc", Version: "3.2"}}, // catalog has 3.3: newer, update expected
		{Mod: domain.Mod{ID: "def", Version: "1.0"}}, // catalog has 1.0: same, no update
	}

	updates, err := src.CheckUpdates(context.Background(), installed)
	if err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if len(updates) != 1 || updates[0].InstalledMod.ID != "abc" || updates[0].NewVersion != "3.3" {
		t.Fatalf("updates = %+v, want exactly one update for abc -> 3.3", updates)
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
