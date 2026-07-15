package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManifestSourceEndToEnd drives the full loop against a static file
// server: manifest fetch -> search -> files (sha256) -> download+verify ->
// cache, plus dependency resolution and update checks (issue #48 acceptance).
func TestManifestSourceEndToEnd(t *testing.T) {
	archive := []byte("mod payload bytes")
	sum := sha256.Sum256(archive)
	archiveSHA := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    summary: Makes things cooler
    dependencies: [dep-mod]
    files:
      - id: main
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        url: %s/files/cool-mod-1.2.0.zip
        sha256: %s
        primary: true
  - id: dep-mod
    name: Dep Mod
    version: 0.5.0
    files:
      - id: main
        filename: dep-mod.zip
        url: %s/files/dep-mod.zip
`, srv.URL, archiveSHA, srv.URL)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })

	src, err := custom.New(custom.SourceDefinition{
		ID:        "e2e-repo",
		Name:      "E2E Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: true, // httptest serves plain http
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))
	ctx := context.Background()

	// Search finds the mod and stamps identity.
	res, err := src.Search(ctx, source.SearchQuery{Query: "cool", GameID: "testgame", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]
	assert.Equal(t, "e2e-repo", mod.SourceID)
	assert.Equal(t, "testgame", mod.GameID)

	// Dependencies resolve within the source.
	deps, err := src.GetDependencies(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, domain.ModReference{SourceID: "e2e-repo", ModID: "dep-mod"}, deps[0])

	// Files carry the declared sha256; download verifies it and lands in cache.
	files, err := src.GetModFiles(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, archiveSHA, files[0].SHA256)

	result, err := svc.DownloadMod(ctx, "e2e-repo", game, &mod, &files[0], nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))

	// Update checks: installed 1.0.0 -> manifest 1.2.0 offers an update.
	installed := []domain.InstalledMod{{Mod: domain.Mod{ID: "cool-mod", SourceID: "e2e-repo", Version: "1.0.0"}}}
	updates, err := src.CheckUpdates(ctx, installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.2.0", updates[0].NewVersion)
}

// TestManifestSourceEndToEndCorruptDownload pins acceptance criterion 2 of
// the sha256 wiring: a server whose file bytes don't match the declared hash
// must fail the install and leave the cache empty.
func TestManifestSourceEndToEndCorruptDownload(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wrongSHA := strings.Repeat("de", 32) // 64 hex chars that won't match the served bytes
	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: bad-mod
    name: Bad Mod
    version: 1.0.0
    files:
      - id: main
        filename: bad-mod.zip
        url: %s/files/bad-mod.zip
        sha256: %s
`, srv.URL, wrongSHA)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tampered content")) })

	src, err := custom.New(custom.SourceDefinition{
		ID: "bad-repo", Name: "Bad Repo", Type: custom.TypeManifest, AllowHTTP: true,
		Manifest: &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))
	ctx := context.Background()

	mod, err := src.GetMod(ctx, "testgame", "bad-mod")
	require.NoError(t, err)
	files, err := src.GetModFiles(ctx, mod)
	require.NoError(t, err)

	_, err = svc.DownloadMod(ctx, "bad-repo", game, mod, &files[0], nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256 mismatch")
	gameCache := svc.GetGameCache(game)
	assert.False(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))
}
