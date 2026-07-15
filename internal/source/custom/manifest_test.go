package custom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testManifest = `
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    author: someone
    summary: Makes things cooler
    game_ids: [skyrim]
    dependencies: [other-mod]
    files:
      - id: main
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        size: 4
        url: https://files.test/cool-mod-1.2.0.zip
        sha256: aabbcc
        primary: true
  - id: other-mod
    name: Other Mod
    version: 0.9.0
    summary: A dependency
    files:
      - id: main
        filename: other-mod.zip
        url: https://files.test/other-mod.zip
`

func manifestDef(url string) SourceDefinition {
	return SourceDefinition{
		ID:       "my-repo",
		Name:     "My Repo",
		Type:     TypeManifest,
		Manifest: &ManifestConfig{URL: url},
	}
}

// newLocalManifest writes testManifest to a temp file and builds a source over it.
func newLocalManifest(t *testing.T) *Manifest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte(testManifest), 0644))
	m, err := NewManifest(manifestDef(path))
	require.NoError(t, err)
	return m
}

func TestNewManifestIsPure(t *testing.T) {
	// Construction must succeed even when the manifest is unreachable —
	// fetch errors are operation-time, not registration-time.
	m, err := NewManifest(manifestDef("https://unreachable.invalid/mods.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "my-repo", m.ID())
	assert.Equal(t, "My Repo", m.Name())
}

func TestManifestFetchLocal(t *testing.T) {
	m := newLocalManifest(t)
	doc, err := m.fetch(context.Background())
	require.NoError(t, err)
	assert.Len(t, doc.Mods, 2)
}

func TestManifestFetchLocalMissingFileNamesPath(t *testing.T) {
	def := manifestDef(filepath.Join(t.TempDir(), "gone.yaml"))
	m, err := NewManifest(def)
	require.NoError(t, err)
	_, err = m.fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gone.yaml")
	assert.Contains(t, err.Error(), "my-repo")
}

func TestManifestFetchRemoteTTL(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(testManifest))
	}))
	defer srv.Close()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true // httptest is plain http
	def.Manifest.Refresh = "15m"
	m, err := NewManifest(def)
	require.NoError(t, err)

	current := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return current }

	ctx := context.Background()
	_, err = m.fetch(ctx)
	require.NoError(t, err)
	_, err = m.fetch(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, hits, "second fetch within TTL must hit the cache")

	current = current.Add(16 * time.Minute)
	_, err = m.fetch(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, hits, "fetch after TTL expiry must re-download")
}

func TestManifestFetchAttachesAuth(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		var gotHeader string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("X-API-Key")
			_, _ = w.Write([]byte(testManifest))
		}))
		defer srv.Close()

		def := manifestDef(srv.URL + "/mods.yaml")
		def.AllowHTTP = true
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")

		_, err = m.fetch(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotHeader)
	})

	t.Run("query", func(t *testing.T) {
		var gotQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query().Get("api_key")
			_, _ = w.Write([]byte(testManifest))
		}))
		defer srv.Close()

		def := manifestDef(srv.URL + "/mods.yaml")
		def.AllowHTTP = true
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")

		_, err = m.fetch(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "sekrit", gotQuery)
	})
}

func TestManifestFetchRemoteErrorNamesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	m, err := NewManifest(def)
	require.NoError(t, err)

	_, err = m.fetch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), srv.URL)
	assert.Contains(t, err.Error(), "my-repo")
}

func TestManifestIsAuthenticated(t *testing.T) {
	def := manifestDef("https://x.test/mods.yaml")
	def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
	m, err := NewManifest(def)
	require.NoError(t, err)
	assert.False(t, m.IsAuthenticated())
	m.SetAPIKey("k")
	assert.True(t, m.IsAuthenticated())
}
