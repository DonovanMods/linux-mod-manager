package icarus

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestFirestoreClient_GetDocument_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"name":   "projects/p/databases/(default)/documents/mods/abc",
			"fields": map[string]any{"name": map[string]any{"stringValue": "Bear Mount"}},
		}
		json.NewEncoder(w).Encode(doc) //nolint:errcheck
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	c.baseURL = srv.URL

	doc, err := c.getDocument(context.Background(), "mods", "abc")
	if err != nil {
		t.Fatalf("getDocument: %v", err)
	}
	if doc.ID != "abc" {
		t.Errorf("doc.ID = %q, want abc", doc.ID)
	}
	if doc.Fields["name"] != "Bear Mount" {
		t.Errorf("doc.Fields[name] = %v, want Bear Mount", doc.Fields["name"])
	}
}

// newFirestoreClient(id, nil) must fall back to http.DefaultClient rather
// than leaving httpClient nil (which would panic the first time getJSON
// called c.httpClient.Do).
func TestNewFirestoreClient_NilHTTPClient_FallsBackToDefault(t *testing.T) {
	c := newFirestoreClient("test-project", nil)
	if c.httpClient != http.DefaultClient {
		t.Errorf("httpClient = %v, want http.DefaultClient", c.httpClient)
	}
}

// trackingBody wraps a response body to record whether it was read all the
// way to io.EOF (drained) and whether Close was called, independent of real
// TCP connection pooling — the property getJSON's error path needs to
// guarantee is "drain, then close," not any particular transport behavior.
type trackingBody struct {
	rc        io.ReadCloser
	readToEOF bool
	closed    bool
}

func (b *trackingBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err == io.EOF {
		b.readToEOF = true
	}
	return n, err
}

func (b *trackingBody) Close() error {
	b.closed = true
	return b.rc.Close()
}

// drainTrackingTransport substitutes every response's body with a
// trackingBody, recording the last one seen so a test can inspect it after
// the request completes.
type drainTrackingTransport struct {
	base    http.RoundTripper
	tracked *trackingBody
}

func (t *drainTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	t.tracked = &trackingBody{rc: resp.Body}
	resp.Body = t.tracked
	return resp, nil
}

// getJSON's non-200 error path must drain the response body to EOF before
// closing it — an early return without draining leaves bytes unread on the
// wire, which forces net/http's transport to close the underlying
// connection instead of pooling it for reuse (connection-reuse hygiene).
func TestFirestoreClient_GetJSON_DrainsBodyBeforeCloseOnErrorPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"nope, and some padding so there is real body to drain"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	transport := &drainTrackingTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}
	c := newFirestoreClient("test-project", client)
	c.baseURL = srv.URL

	err := c.getJSON(context.Background(), srv.URL, &struct{}{})
	if err == nil {
		t.Fatal("expected an error for a non-200 response, got nil")
	}
	if transport.tracked == nil {
		t.Fatal("transport never saw a request")
	}
	if !transport.tracked.readToEOF {
		t.Error("response body was not drained to EOF before Close on the error path")
	}
	if !transport.tracked.closed {
		t.Error("response body was not closed")
	}
}

// TestFirestoreClient_GetJSON_ErrorNamesURLAndBodySnippet guards a Copilot
// release-review finding on #203: getJSON's non-200 error was a bare "HTTP
// %d", naming neither which request failed nor what the server actually
// said - a 403 was otherwise undiagnosable (a Firestore permission error, a
// quota message, and a malformed-request response all look identical). The
// error must name the request URL and a snippet of the response body.
func TestFirestoreClient_GetJSON_ErrorNamesURLAndBodySnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"PERMISSION_DENIED: quota exceeded"}}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	err := c.getJSON(context.Background(), srv.URL+"/some/path", &struct{}{})
	if err == nil {
		t.Fatal("expected an error for a 403 response, got nil")
	}
	if !strings.Contains(err.Error(), srv.URL+"/some/path") {
		t.Errorf("error %q does not name the request URL", err.Error())
	}
	if !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Errorf("error %q does not include the response body snippet", err.Error())
	}
}

// TestFirestoreClient_GetJSON_ErrorBodySnippetIsCapped guards the "cap it"
// half of the same finding: a large/pathological error body (a stray HTML
// error page, a runaway response, ...) must not be echoed in full into the
// error message.
func TestFirestoreClient_GetJSON_ErrorBodySnippetIsCapped(t *testing.T) {
	const bodySize = 64 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write(bytes.Repeat([]byte("x"), bodySize)) //nolint:errcheck
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	err := c.getJSON(context.Background(), srv.URL, &struct{}{})
	if err == nil {
		t.Fatal("expected an error for a 403 response, got nil")
	}
	if len(err.Error()) >= bodySize {
		t.Errorf("error message is %d bytes - the response body snippet must be capped, not echoed in full", len(err.Error()))
	}
}
