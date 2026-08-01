package icarus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFirestoreClient_ListCollection_Paginates(t *testing.T) {
	pages := []map[string]any{
		{
			"documents": []map[string]any{
				{"name": "projects/p/databases/(default)/documents/mods/abc", "fields": map[string]any{"name": map[string]any{"stringValue": "Bear Mount"}}},
			},
			"nextPageToken": "page2",
		},
		{
			"documents": []map[string]any{
				{"name": "projects/p/databases/(default)/documents/mods/def", "fields": map[string]any{"name": map[string]any{"stringValue": "Wolf Mount"}}},
			},
		},
	}
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := pages[callCount]
		callCount++
		json.NewEncoder(w).Encode(page) //nolint:errcheck
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	c.baseURL = srv.URL // test seam, see Step 3

	docs, err := c.listCollection(context.Background(), "mods")
	if err != nil {
		t.Fatalf("listCollection: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2 (pagination should have followed nextPageToken)", len(docs))
	}
	if docs[0].ID != "abc" || docs[1].ID != "def" {
		t.Errorf("doc IDs = %q, %q, want abc, def", docs[0].ID, docs[1].ID)
	}
	if docs[0].Fields["name"] != "Bear Mount" {
		t.Errorf("docs[0].Fields[name] = %v, want Bear Mount", docs[0].Fields["name"])
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (one per page)", callCount)
	}
}

// nextPageToken is opaque server-issued data, not guaranteed URL-safe as-is;
// listCollection must query-escape it before appending it to the request URL
// rather than concatenating it raw. Round-trips a token containing
// characters ("&", "=", "+", "/", "?") that would corrupt the query string
// if not escaped, and asserts the mock server decodes it back to the exact
// original value.
func TestFirestoreClient_ListCollection_EscapesPageToken(t *testing.T) {
	const rawToken = "page&two=x+y/z?w"
	pages := []map[string]any{
		{
			"documents": []map[string]any{
				{"name": "projects/p/databases/(default)/documents/mods/abc", "fields": map[string]any{}},
			},
			"nextPageToken": rawToken,
		},
		{
			"documents": []map[string]any{},
		},
	}
	callCount := 0
	var gotPageToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if callCount == 1 {
			gotPageToken = r.URL.Query().Get("pageToken")
		}
		page := pages[callCount]
		callCount++
		json.NewEncoder(w).Encode(page) //nolint:errcheck
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	c.baseURL = srv.URL

	if _, err := c.listCollection(context.Background(), "mods"); err != nil {
		t.Fatalf("listCollection: %v", err)
	}
	if gotPageToken != rawToken {
		t.Errorf("server decoded pageToken = %q, want %q (round-trip through query-escaping)", gotPageToken, rawToken)
	}
}

func TestFirestoreClient_GetDocument_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	c.baseURL = srv.URL

	_, err := c.getDocument(context.Background(), "mods", "missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}
