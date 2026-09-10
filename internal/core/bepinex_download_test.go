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
	"time"

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

// TestDownloadIngest_BepInEx_RefusedIngestKeepsTheArchiveForTheRetry is
// review F9. The loader precondition lands at ingest, which is the earliest
// point an archive's SHAPE is knowable - but the refusal discarded the bytes
// with the staging directory, so the re-run after `lmm game edit --loader
// bepinex` downloaded the whole archive again. For a NexusMods free-tier
// user that is a second manual download.
//
// The end state of the refusal is unchanged and still asserted here: nothing
// deployed, no cache entry, no DB row. Only the downloaded FILE is kept.
func TestDownloadIngest_BepInEx_RefusedIngestKeepsTheArchiveForTheRetry(t *testing.T) {
	fixture := newBepInExDownloadFixture(t, map[string]string{
		"BepInEx/plugins/Thing.dll": "assembly",
		"manifest.json":             "{}",
	}, false)

	err := fixture.download(t)
	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)
	assert.Equal(t, int64(1), fixture.downloads.Load(), "the archive was fetched once")

	assert.False(t, fixture.svc.GetGameCache(fixture.game).Exists(
		fixture.game.ID, fixture.mod.SourceID, fixture.mod.ID, fixture.mod.Version),
		"a refused ingest commits no cache entry")

	// The user does what the error told them to.
	_, err = fixture.svc.UpdateGameLoader(context.Background(), fixture.game.ID,
		&core.LoaderSpec{Kind: domain.LoaderKindBepInEx})
	require.NoError(t, err)
	game, err := fixture.svc.GetGame(fixture.game.ID)
	require.NoError(t, err)
	fixture.game = game

	require.NoError(t, fixture.download(t))
	assert.Equal(t, int64(1), fixture.downloads.Load(),
		"the retry reuses the archive the refusal kept - no second download")

	cached, err := fixture.svc.GetGameCache(fixture.game).ListFiles(
		fixture.game.ID, fixture.mod.SourceID, fixture.mod.ID, fixture.mod.Version)
	require.NoError(t, err)
	require.Len(t, cached, 1)
	assert.Equal(t, filepath.FromSlash("BepInEx/plugins/Thing.dll"), cached[0])
}

// Only the loader precondition retains. A framework pack is refused for a
// reason no game edit fixes - the archive is BepInEx itself, not a mod - so
// nothing is kept and the next attempt is an ordinary download. Keeping
// every refused archive would be a general download cache, which this is
// deliberately not.
func TestDownloadIngest_BepInEx_AFrameworkPackRefusalRetainsNothing(t *testing.T) {
	fixture := newBepInExDownloadFixture(t, map[string]string{
		"BepInEx/core/BepInEx.Preloader.dll": "preloader",
		"winhttp.dll":                        "proxy",
		"manifest.json":                      "{}",
	}, true)

	require.ErrorIs(t, fixture.download(t), core.ErrBepInExFrameworkPack)
	assert.Equal(t, int64(1), fixture.downloads.Load())

	require.ErrorIs(t, fixture.download(t), core.ErrBepInExFrameworkPack)
	assert.Equal(t, int64(2), fixture.downloads.Load(),
		"nothing was retained, so the second attempt fetches again")
}

// A successful ingest leaves no retained copy behind: the bytes are in the
// cache, so a second one would be dead weight in the user's data directory.
func TestDownloadIngest_BepInEx_ASuccessfulIngestRetainsNothing(t *testing.T) {
	fixture := newBepInExDownloadFixture(t, map[string]string{
		"BepInEx/plugins/Thing.dll": "assembly",
		"manifest.json":             "{}",
	}, true)
	require.NoError(t, fixture.download(t))

	retained := filepath.Join(fixture.svc.DataDirForTest(), "downloads", "retained")
	entries, err := os.ReadDir(retained)
	if err == nil {
		assert.Empty(t, entries, "a successful ingest keeps no archive")
	} else {
		assert.True(t, os.IsNotExist(err), "unexpected error reading %s: %v", retained, err)
	}
}

// TestRetainedDownloads_AnAbandonedArchiveIsSweptOnTheNextServiceOpen is
// re-review R4. sweepRetainedDownloads had exactly one caller - the way IN
// to a NEW retention - so an entry was only ever swept by a LATER refusal.
// A user who abandons one install and never has another ingest refused kept
// the archive indefinitely, which is the opposite of what the TTL is for and
// of what the file's own comment promised.
func TestRetainedDownloads_AnAbandonedArchiveIsSweptOnTheNextServiceOpen(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Close())

	// One retention the user walked away from a month ago, one from a
	// refusal they are still in the middle of answering.
	base := filepath.Join(cfg.DataDir, "downloads", "retained")
	abandoned := filepath.Join(base, "00000000000000000000000000000000")
	current := filepath.Join(base, "ffffffffffffffffffffffffffffffff")
	for _, dir := range []string{abandoned, current} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Thing-1.0.0.zip"), []byte("bytes"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "download.json"),
			[]byte(`{"file_name":"Thing-1.0.0.zip","size":5}`), 0o600))
	}
	// Backdated LAST: writing the files above bumps the directory's mtime.
	long := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(abandoned, long, long))

	reopened, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	_, err = os.Stat(abandoned)
	assert.True(t, os.IsNotExist(err),
		"an archive past the TTL is swept when the service opens, not left until another refusal happens to sweep it")
	_, err = os.Stat(current)
	assert.NoError(t, err, "and one still inside the TTL is exactly what the user is coming back for")
}

// TestDownloadIngest_BepInEx_ACopyModeSuccessDropsTheRetainedArchive is the
// second half of R4: dropRetainedDownload was called only on the EXTRACT
// path's success, while the DeployCompile and DeployCopy branches return
// earlier. A retention therefore survived a later successful ingest of the
// same file that took one of those branches - dead weight in the data
// directory for the rest of the TTL, holding a second copy of bytes that are
// already in the cache.
func TestDownloadIngest_BepInEx_ACopyModeSuccessDropsTheRetainedArchive(t *testing.T) {
	fixture := newBepInExDownloadFixture(t, map[string]string{
		"BepInEx/plugins/Thing.dll": "assembly",
		"manifest.json":             "{}",
	}, false)

	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, fixture.download(t), &loaderErr)
	require.Equal(t, int64(1), fixture.downloads.Load())

	retained := filepath.Join(fixture.svc.DataDirForTest(), "downloads", "retained")
	entries, err := os.ReadDir(retained)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the refusal kept the archive for the retry")

	// The user declares the loader, and this game deploys its mods as-is
	// rather than extracting them - so the retry succeeds down the COPY
	// branch instead of the extract one.
	fixture.game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
	fixture.game.DeployMode = domain.DeployCopy
	require.NoError(t, fixture.svc.SaveGame(context.Background(), fixture.game))

	require.NoError(t, fixture.download(t))
	assert.Equal(t, int64(1), fixture.downloads.Load(), "the retry still reuses the kept archive")

	entries, err = os.ReadDir(retained)
	require.NoError(t, err)
	assert.Empty(t, entries, "the bytes are in the cache now, so the retained copy is dead weight")
}
