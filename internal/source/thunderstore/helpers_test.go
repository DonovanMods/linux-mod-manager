package thunderstore_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/require"
)

// testCommunity is the slug every test uses. It is a real community name
// only so the fixture reads plausibly; nothing here ever asks the site
// about it.
const testCommunity = "lethal-company"

// sandboxEnv redirects HOME and every XDG variable at a temp directory, so
// nothing in this package can read or write a real user's configuration,
// data or cache even by accident. Every Source these tests build is given
// an explicit CacheDir already; this is the belt to that's braces.
func sandboxEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS",
	} {
		t.Setenv(key, t.TempDir())
	}
}

// fixtureDocument is the hand-built community index, read from testdata.
func fixtureDocument(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "community_small.json"))
	require.NoError(t, err)
	return data
}

// indexServer stands in for Thunderstore's one listing endpoint: it serves
// a document with a Last-Modified, answers a matching If-Modified-Since
// with 304, and counts what it was asked for.
type indexServer struct {
	*httptest.Server

	mu           sync.Mutex
	body         []byte
	lastModified string
	gzip         bool

	requests     int
	conditionals int
	served200    int
	served304    int
	downloads    int

	// archives is the zip served for each /package/download/... path, keyed
	// by the exact path GetDownloadURL builds.
	archives map[string][]byte
}

// newIndexServer starts a server for body. Every test MUST use one: no test
// in this package may name the real host, and TestNoTestReachesTheProductionAPI
// enforces it.
func newIndexServer(t *testing.T, body []byte) *indexServer {
	t.Helper()
	s := &indexServer{body: body, lastModified: "Wed, 10 Sep 2026 12:00:00 GMT"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *indexServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if strings.HasPrefix(r.URL.Path, "/package/download/") {
		s.serveDownload(w, r)
		return
	}
	if want := "/c/" + testCommunity + "/api/v1/package/"; r.URL.Path != want {
		http.Error(w, "no such community", http.StatusNotFound)
		return
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		s.conditionals++
		if ims == s.lastModified {
			s.served304++
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	s.served200++
	w.Header().Set("Last-Modified", s.lastModified)
	w.Header().Set("Content-Type", "application/json")
	if s.gzip && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write(s.body)
		_ = zw.Close()
		return
	}
	_, _ = w.Write(s.body)
}

// serveDownload answers /package/download/<namespace>/<name>/<version>/ -
// the one other path this source builds a URL for, and the only way an
// end-to-end install test can exist without touching the real site. The
// zip it serves is whatever publishArchive last stored for that path, so a
// test decides what shape of archive comes back.
func (s *indexServer) serveDownload(w http.ResponseWriter, r *http.Request) {
	s.downloads++
	zip, ok := s.archives[r.URL.Path]
	if !ok {
		http.Error(w, "no such version", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.Itoa(len(zip)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(zip)
}

// publishArchive makes the download URL for one package version answer with
// zip. namespace/name/version are the three fields GetDownloadURL builds
// the path from.
func (s *indexServer) publishArchive(namespace, name, version string, zip []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archives == nil {
		s.archives = map[string][]byte{}
	}
	s.archives[fmt.Sprintf("/package/download/%s/%s/%s/", namespace, name, version)] = zip
}

// publish replaces the served document and moves its Last-Modified, which
// is what makes the next conditional GET answer 200 instead of 304.
func (s *indexServer) publish(body []byte, lastModified string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = body
	s.lastModified = lastModified
}

// counts reports what the server has been asked for so far.
func (s *indexServer) counts() (requests, conditionals, served200, served304 int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.conditionals, s.served200, s.served304
}

// serveGzip makes the server compress, so a test can prove the transport's
// transparent decoding is what the streaming decode reads through.
func (s *indexServer) serveGzip() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gzip = true
}

// withNewShipLootVersion is the fixture document with one extra version
// published on top of tinyhoot-ShipLoot, for the tests whose subject is
// what a refresh FINDS rather than how it is fetched.
func withNewShipLootVersion(t *testing.T) []byte {
	t.Helper()
	var packages []map[string]any
	require.NoError(t, json.Unmarshal(fixtureDocument(t), &packages))
	for _, pkg := range packages {
		if pkg["full_name"] != "tinyhoot-ShipLoot" {
			continue
		}
		versions, _ := pkg["versions"].([]any)
		newest := map[string]any{
			"name": "ShipLoot", "full_name": "tinyhoot-ShipLoot-1.2.0",
			"description":    "Shows the total value of scrap aboard the ship.",
			"version_number": "1.2.0", "dependencies": []any{"BepInEx-BepInExPack-5.4.2100"},
			"date_created": "2026-09-11T10:00:00.000000Z", "website_url": "",
			"file_size": float64(41000),
		}
		pkg["versions"] = append([]any{newest}, versions...)
		pkg["date_updated"] = "2026-09-11T10:00:00.000000Z"
	}
	out, err := json.Marshal(packages)
	require.NoError(t, err)
	return out
}

// testClock is the injectable clock every TTL assertion runs on, so a test
// asserts the POLICY without spending six hours.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *testClock {
	return &testClock{now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newSource builds a Source against srv with its own cache root and clock.
func newSource(t *testing.T, srv *indexServer) (*thunderstore.Source, string, *testClock) {
	t.Helper()
	sandboxEnv(t)
	cacheDir := t.TempDir()
	clock := newClock()
	src := thunderstore.New(thunderstore.Options{
		CacheDir: cacheDir,
		BaseURL:  srv.URL,
		Now:      clock.Now,
	})
	return src, cacheDir, clock
}

// indexDir is where the index for community lives under cacheDir.
func indexDir(cacheDir, community string) string {
	return filepath.Join(cacheDir, "_thunderstore", community)
}

// readIndexFile parses index.json as what it is on disk: a schema, a
// generation and an array of fixed-shape arrays. Parsed here by hand
// rather than through the package's own types, so the test pins the FORMAT
// and not just the round trip.
func readIndexFile(t *testing.T, cacheDir, community string) struct {
	Schema     int                 `json:"schema"`
	Rows       [][]json.RawMessage `json:"rows"`
	Generation string              `json:"generation"`
} {
	t.Helper()
	var file struct {
		Schema     int                 `json:"schema"`
		Rows       [][]json.RawMessage `json:"rows"`
		Generation string              `json:"generation"`
	}
	data, err := os.ReadFile(filepath.Join(indexDir(cacheDir, community), "index.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &file))
	return file
}

// readIndexRows is readIndexFile's row table, which is what most tests
// want.
func readIndexRows(t *testing.T, cacheDir, community string) [][]json.RawMessage {
	t.Helper()
	return readIndexFile(t, cacheDir, community).Rows
}

// rowString reads one field of an index row as a string.
func rowString(t *testing.T, row []json.RawMessage, field int) string {
	t.Helper()
	var s string
	require.NoError(t, json.Unmarshal(row[field], &s))
	return s
}

// rowInt reads one field of an index row as an int64.
func rowInt(t *testing.T, row []json.RawMessage, field int) int64 {
	t.Helper()
	var n int64
	require.NoError(t, json.Unmarshal(row[field], &n))
	return n
}

// readWatermark reads watermark.json as a plain map, so the test asserts
// the keys on disk rather than whatever the package's struct happens to be
// called.
func readWatermark(t *testing.T, cacheDir, community string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(indexDir(cacheDir, community), "watermark.json"))
	require.NoError(t, err)
	var wm map[string]any
	require.NoError(t, json.Unmarshal(data, &wm))
	return wm
}

// syntheticDocument is a whole community document as bytes, with a knob on
// how WIDE each record is. Two documents of the same package count and
// different widths are what a test needs to prove that "the last row's
// bytes fit inside the file" is not the same question as "these two files
// are the same build".
func syntheticDocument(packages, descriptionRepeats int) []byte {
	description := strings.Repeat("A description of a length these actually run to. ", descriptionRepeats)
	var buf bytes.Buffer
	buf.WriteString("[")
	for i := range packages {
		if i > 0 {
			buf.WriteString(",")
		}
		fmt.Fprintf(&buf,
			`{"name":"Mod%05d","full_name":"Owner%05d-Mod%05d","owner":"Owner%05d",`+
				`"date_updated":"2026-09-0%dT10:00:00Z","is_deprecated":false,`+
				`"categories":["Mods"],"versions":[{"version_number":"1.0.0",`+
				`"description":%q,"dependencies":[],"date_created":"2026-01-01T00:00:00Z",`+
				`"website_url":"","file_size":%d}]}`,
			i, i, i, i, (i%9)+1, description, 1000+i)
	}
	buf.WriteString("]")
	return buf.Bytes()
}
