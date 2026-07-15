package custom

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
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
        sha256: aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd
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

// TestNewManifestLocalPathNoScheme is a defensive proof that a schemeless
// local path still constructs: SourceDefinition.Validate rejects unsupported
// URL schemes (e.g. ftp://) before a definition ever reaches NewManifest, so
// NewManifest itself only needs to treat non-http(s) values as local paths.
func TestNewManifestLocalPathNoScheme(t *testing.T) {
	m, err := NewManifest(manifestDef("relative/mods.yaml"))
	require.NoError(t, err)
	assert.False(t, m.isRemote)
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

// TestManifestFetchRemoteErrorDoesNotLeakQueryAPIKey pins final-review
// finding 2: a query-auth manifest source hitting an unreachable server must
// not leak the API key baked into the authenticated request URL. httpClient.Do
// returns a *url.Error whose Error() string embeds the full request URL
// (including the query-string key); fetchRemote must unwrap that before
// wrapping, not report the raw error verbatim.
func TestManifestFetchRemoteErrorDoesNotLeakQueryAPIKey(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close()) // now unreachable: connections refused

	unauthURL := "http://" + addr + "/mods.yaml"
	def := manifestDef(unauthURL)
	def.AllowHTTP = true
	def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
	m, err := NewManifest(def)
	require.NoError(t, err)
	m.SetAPIKey("LEAKME123")

	_, err = m.fetch(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "LEAKME123", "error must not leak the query-auth API key")
	assert.NotContains(t, err.Error(), "api_key=", "error must not leak the query parameter at all")
	assert.Contains(t, err.Error(), "my-repo", "error must still name the source")
	assert.Contains(t, err.Error(), unauthURL, "error must still name the (unauthenticated) manifest URL")
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

func TestManifestIdentityAndCapabilities(t *testing.T) {
	m := newLocalManifest(t)
	assert.Equal(t, source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: false}, m.Capabilities())
	assert.Empty(t, m.AuthURL())

	_, err := m.ExchangeToken(context.Background(), "code")
	assert.True(t, errors.Is(err, source.ErrNotSupported))

	def := manifestDef("https://x.test/mods.yaml")
	def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
	authed, err := NewManifest(def)
	require.NoError(t, err)
	assert.True(t, authed.Capabilities().Auth)
}

func TestManifestSearch(t *testing.T) {
	m := newLocalManifest(t)
	ctx := context.Background()

	t.Run("empty query returns all mods for a matching game", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{GameID: "skyrim"})
		require.NoError(t, err)
		assert.Equal(t, 2, res.TotalCount) // cool-mod matches game_ids; other-mod has no game_ids (all games)
	})

	t.Run("game_ids filters non-matching games", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{GameID: "othergame"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1) // only other-mod (no game_ids = all games)
		assert.Equal(t, "other-mod", res.Mods[0].ID)
	})

	t.Run("empty GameID matches everything", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{})
		require.NoError(t, err)
		assert.Equal(t, 2, res.TotalCount)
	})

	t.Run("query matches and mod fields map", func(t *testing.T) {
		res, err := m.Search(ctx, source.SearchQuery{Query: "cool", GameID: "skyrim"})
		require.NoError(t, err)
		require.Len(t, res.Mods, 1)
		mod := res.Mods[0]
		assert.Equal(t, "cool-mod", mod.ID)
		assert.Equal(t, "my-repo", mod.SourceID)
		assert.Equal(t, "Cool Mod", mod.Name)
		assert.Equal(t, "1.2.0", mod.Version)
		assert.Equal(t, "someone", mod.Author)
		assert.Equal(t, "Makes things cooler", mod.Summary)
		assert.Equal(t, "skyrim", mod.GameID)
	})
}

func TestManifestGetMod(t *testing.T) {
	m := newLocalManifest(t)
	mod, err := m.GetMod(context.Background(), "skyrim", "cool-mod")
	require.NoError(t, err)
	assert.Equal(t, "Cool Mod", mod.Name)
	assert.Equal(t, "skyrim", mod.GameID)

	_, err = m.GetMod(context.Background(), "skyrim", "nope")
	assert.ErrorContains(t, err, "not found")
}

func TestManifestFilesAndDownloadURL(t *testing.T) {
	m := newLocalManifest(t)
	ctx := context.Background()

	mod, err := m.GetMod(ctx, "skyrim", "cool-mod")
	require.NoError(t, err)

	files, err := m.GetModFiles(ctx, mod)
	require.NoError(t, err)
	require.Len(t, files, 1)
	f := files[0]
	assert.Equal(t, "main", f.ID)
	assert.Equal(t, "cool-mod-1.2.0.zip", f.FileName)
	assert.Equal(t, "1.2.0", f.Version)
	assert.Equal(t, int64(4), f.Size)
	assert.Equal(t, "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd", f.SHA256)
	assert.True(t, f.IsPrimary)

	u, err := m.GetDownloadURL(ctx, mod, "main")
	require.NoError(t, err)
	assert.Equal(t, "https://files.test/cool-mod-1.2.0.zip", u)

	_, err = m.GetDownloadURL(ctx, mod, "nope")
	assert.ErrorContains(t, err, "not found")
}

func TestManifestDownloadURLQueryAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte(testManifest), 0644))
	def := manifestDef(path)
	def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
	m, err := NewManifest(def)
	require.NoError(t, err)
	m.SetAPIKey("sekrit")

	mod, err := m.GetMod(context.Background(), "skyrim", "cool-mod")
	require.NoError(t, err)
	u, err := m.GetDownloadURL(context.Background(), mod, "main")
	require.NoError(t, err)
	assert.Equal(t, "https://files.test/cool-mod-1.2.0.zip?api_key=sekrit", u)
}

func TestManifestGetDependencies(t *testing.T) {
	m := newLocalManifest(t)

	mod, err := m.GetMod(context.Background(), "skyrim", "cool-mod")
	require.NoError(t, err)
	deps, err := m.GetDependencies(context.Background(), mod)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, domain.ModReference{SourceID: "my-repo", ModID: "other-mod"}, deps[0])

	other, err := m.GetMod(context.Background(), "skyrim", "other-mod")
	require.NoError(t, err)
	deps, err = m.GetDependencies(context.Background(), other)
	require.NoError(t, err)
	assert.Empty(t, deps)
}

func TestManifestCheckUpdates(t *testing.T) {
	m := newLocalManifest(t) // cool-mod is at 1.2.0

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "cool-mod", SourceID: "my-repo", Version: "1.0.0"}},
		{Mod: domain.Mod{ID: "other-mod", SourceID: "my-repo", Version: "0.9.0"}},
		{Mod: domain.Mod{ID: "removed", SourceID: "my-repo", Version: "1.0"}},
	}

	updates, err := m.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "cool-mod", updates[0].InstalledMod.ID)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

func TestManifestFetchReturnsDefensiveCopy(t *testing.T) {
	m := newLocalManifest(t)
	ctx := context.Background()

	first, err := m.fetch(ctx)
	require.NoError(t, err)
	// Mutate everything a caller could plausibly touch.
	first.Mods[0].Name = "MUTATED"
	first.Mods[0].GameIDs[0] = "MUTATED"
	first.Mods[0].Files[0].URL = "MUTATED"
	first.Mods = nil

	second, err := m.fetch(ctx)
	require.NoError(t, err)
	require.Len(t, second.Mods, 2)
	assert.Equal(t, "Cool Mod", second.Mods[0].Name)
	assert.Equal(t, "skyrim", second.Mods[0].GameIDs[0])
	assert.NotEqual(t, "MUTATED", second.Mods[0].Files[0].URL)
}

// TestManifestFetchRemoteReturnsDefensiveCopy pins that callers cannot corrupt
// the remote cache (m.cached) via the returned pointer. Uses an httptest server,
// fetches once (populates cache), mutates the returned doc thoroughly (mirroring
// the local test's mutations), fetches again within TTL (cache hit), and asserts
// the second result is pristine. Counts server hits to prove the second fetch
// came from cache, not the network.
func TestManifestFetchRemoteReturnsDefensiveCopy(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(testManifest))
	}))
	defer srv.Close()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	def.Manifest.Refresh = "15m" // explicit TTL; default is fine
	m, err := NewManifest(def)
	require.NoError(t, err)

	ctx := context.Background()

	// First fetch: populates cache
	first, err := m.fetch(ctx)
	require.NoError(t, err)
	require.Len(t, first.Mods, 2)
	assert.Equal(t, 1, hits, "first fetch must hit the server")

	// Mutate everything a caller could plausibly touch.
	first.Mods[0].Name = "MUTATED"
	first.Mods[0].GameIDs[0] = "MUTATED"
	first.Mods[0].Files[0].URL = "MUTATED"
	first.Mods = nil

	// Second fetch: within TTL, must be from cache
	second, err := m.fetch(ctx)
	require.NoError(t, err)
	require.Len(t, second.Mods, 2)
	assert.Equal(t, "Cool Mod", second.Mods[0].Name)
	assert.Equal(t, "skyrim", second.Mods[0].GameIDs[0])
	assert.NotEqual(t, "MUTATED", second.Mods[0].Files[0].URL)
	assert.Equal(t, 1, hits, "second fetch within TTL must use cache, not hit server")
}

func TestManifestFetchConcurrent(t *testing.T) {
	// Race-detector safety net: concurrent fetches (cache hits and misses)
	// must be data-race free. Run with -race in CI/the suite.
	hits := 0
	var srvMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srvMu.Lock()
		hits++
		srvMu.Unlock()
		_, _ = w.Write([]byte(testManifest))
	}))
	defer srv.Close()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	m, err := NewManifest(def)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ferr := m.fetch(context.Background())
			assert.NoError(t, ferr)
		}()
	}
	wg.Wait()
	srvMu.Lock()
	defer srvMu.Unlock()
	assert.GreaterOrEqual(t, hits, 1)
}

func TestManifestFetchDoesNotBlockOnHungServer(t *testing.T) {
	// A hung server must be bounded by the client timeout, not hang forever.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until test cleanup
	}))
	defer func() { close(release); srv.Close() }()

	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	m, err := NewManifest(def)
	require.NoError(t, err)
	m.httpClient = &http.Client{Timeout: 200 * time.Millisecond}

	start := time.Now()
	_, err = m.fetch(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.NotContains(t, err.Error(), "api_key")
}

func TestNewManifestClientHasTimeout(t *testing.T) {
	m, err := NewManifest(manifestDef("https://x.test/mods.yaml"))
	require.NoError(t, err)
	require.NotNil(t, m.httpClient)
	assert.Equal(t, manifestFetchTimeout, m.httpClient.Timeout)
}

func TestManifestDownloadHeaders(t *testing.T) {
	t.Run("header auth with key", func(t *testing.T) {
		def := manifestDef("https://x.test/mods.yaml")
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "header", Name: "X-API-Key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Equal(t, map[string]string{"X-API-Key": "sekrit"}, m.DownloadHeaders())
	})

	t.Run("query auth or no key yields nil", func(t *testing.T) {
		def := manifestDef("https://x.test/mods.yaml")
		def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: "query", Name: "api_key"}}
		m, err := NewManifest(def)
		require.NoError(t, err)
		m.SetAPIKey("sekrit")
		assert.Nil(t, m.DownloadHeaders())

		noKey, err := NewManifest(manifestDef("https://x.test/mods.yaml"))
		require.NoError(t, err)
		assert.Nil(t, noKey.DownloadHeaders())
	})
}
