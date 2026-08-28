package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDownloadModStagesUnderDataDir drives a real HTTP download end to end and
// pins where the archive is staged. The unit tests around newStagingDir cover the
// helper; this covers the wiring, which is what actually regressed to $TMPDIR.
func TestDownloadModStagesUnderDataDir(t *testing.T) {
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
    files:
      - id: main
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        url: %s/files/cool-mod-1.2.0.zip
        sha256: %s
        primary: true
`, srv.URL, archiveSHA)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })

	src, err := custom.New(custom.SourceDefinition{
		ID:        "staging-repo",
		Name:      "Staging Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: true, // httptest serves plain http
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	ctx := context.Background()
	res, err := src.Search(ctx, source.SearchQuery{Query: "cool", GameID: "testgame", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]

	files, err := src.GetModFiles(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, files, 1)

	_, err = svc.DownloadMod(ctx, "staging-repo", game, &mod, &files[0], nil)
	require.NoError(t, err)

	downloads := filepath.Join(dataDir, "downloads")
	assert.DirExists(t, downloads, "downloads should be staged under the data dir, not $TMPDIR")

	// The per-download subdirectory is removed on success; only the root remains.
	entries, err := os.ReadDir(downloads)
	require.NoError(t, err)
	assert.Empty(t, entries, "staging directory should be cleaned up after a successful download")
}
