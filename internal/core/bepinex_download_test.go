package core_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bepinexZipBytes builds a zip archive in memory from a member -> content
// map, so a download fixture can serve the same shapes the archive-import
// tests build on disk.
func bepinexZipBytes(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(members[name]))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// bepinexDownloadFixture is a real HTTP download of a BepInEx archive into a
// service: a manifest custom source serving one mod whose single file is the
// zip built from members, plus the count of times the archive itself was
// actually fetched.
//
// It exists because the DOWNLOAD ingest (extractIntoStaging) is a different
// code path from the archive-import ingest (importWithIdentity), and #358's
// contract is that both reach the cache in the same layout. The request
// counter is what lets a test assert the stronger property #359 needs: a
// second run after the loader declaration must not pull the bytes again.
type bepinexDownloadFixture struct {
	svc       *core.Service
	game      *domain.Game
	mod       domain.Mod
	file      domain.DownloadableFile
	downloads *atomic.Int64
}

// newBepInExDownloadFixture wires the fixture. declared controls whether the
// game carries #359's `loader: kind: bepinex` block.
func newBepInExDownloadFixture(t *testing.T, members map[string]string, declared bool) *bepinexDownloadFixture {
	t.Helper()

	archive := bepinexZipBytes(t, members)
	sum := sha256.Sum256(archive)
	archiveSHA := hex.EncodeToString(sum[:])

	var downloads atomic.Int64
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: thing
    name: Thing
    version: 1.0.0
    files:
      - id: main
        filename: Thing-1.0.0.zip
        version: 1.0.0
        url: %s/files/Thing-1.0.0.zip
        sha256: %s
        primary: true
`, srv.URL, archiveSHA)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(archive)
	})

	src, err := custom.New(custom.SourceDefinition{
		ID:        "bepinex-repo",
		Name:      "BepInEx Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: true, // httptest serves plain http
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	svc := newFlowsTestService(t)
	svc.RegisterSource(src)

	root := t.TempDir()
	game := &domain.Game{
		ID: "valheim", Name: "Valheim",
		InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"bepinex-repo": "valheim"},
	}
	if declared {
		game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	ctx := context.Background()
	res, err := src.Search(ctx, source.SearchQuery{Query: "thing", GameID: game.ID, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	files, err := src.GetModFiles(ctx, &res.Mods[0])
	require.NoError(t, err)
	require.Len(t, files, 1)

	return &bepinexDownloadFixture{svc: svc, game: game, mod: res.Mods[0], file: files[0], downloads: &downloads}
}

// download runs the download ingest once.
func (f *bepinexDownloadFixture) download(t *testing.T) error {
	t.Helper()
	_, err := f.svc.DownloadModForTest(context.Background(), "bepinex-repo", f.game, &f.mod, &f.file, nil)
	return err
}

// TestDownloadIngest_BepInEx_WrappedPackageDropsTheMetadataInsideTheWrapper is
// F1 through the OTHER ingest: a downloaded wrapped package must reach the
// cache with the same members the archive import produces, metadata included -
// which for a BepInEx game means the four files never reach the game root.
func TestDownloadIngest_BepInEx_WrappedPackageDropsTheMetadataInsideTheWrapper(t *testing.T) {
	fixture := newBepInExDownloadFixture(t, map[string]string{
		"SomePack/BepInEx/plugins/Thing.dll": "assembly",
		"SomePack/manifest.json":             `{"name":"SomePack"}`,
		"SomePack/icon.png":                  "png",
		"SomePack/README.md":                 "# SomePack",
		"SomePack/CHANGELOG.md":              "## 1.0.0",
	}, true)
	require.NoError(t, fixture.download(t))

	cached, err := fixture.svc.GetGameCache(fixture.game).ListFiles(
		fixture.game.ID, fixture.mod.SourceID, fixture.mod.ID, fixture.mod.Version)
	require.NoError(t, err)
	slashed := make([]string, 0, len(cached))
	for _, c := range cached {
		slashed = append(slashed, filepath.ToSlash(c))
	}
	sort.Strings(slashed)
	assert.Equal(t, []string{"BepInEx/plugins/Thing.dll"}, slashed,
		"the cache entry IS the game directory's layout: no package metadata in it")

	_, err = os.Lstat(filepath.Join(fixture.game.InstallPath, "manifest.json"))
	assert.True(t, os.IsNotExist(err))
}
