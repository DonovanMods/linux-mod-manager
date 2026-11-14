package core_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recipeSource is a source with revisioned collections (#607): the list is
// canned, the recipe archive's link points at a test server, and the
// "parser" reads a tiny JSON stand-in for a real recipe, so these tests pin
// core's half - fetch, cache, extract, convert - and nothing of any real
// source's format (internal/app runs the real NexusMods recipes).
type recipeSource struct {
	*mockSource
	collection source.Collection
	resolveErr error
	gotGame    string
	gotRef     string
	archiveURL string
	urlErr     error
	urlCalls   atomic.Int32
}

func (s *recipeSource) ResolveGameCollection(_ context.Context, gameSourceID, ref string) (source.Collection, error) {
	s.gotGame, s.gotRef = gameSourceID, ref
	return s.collection, s.resolveErr
}

func (s *recipeSource) ManifestArchiveURL(context.Context, source.Collection) (string, error) {
	s.urlCalls.Add(1)
	return s.archiveURL, s.urlErr
}

// ParseManifest reads {"name":..., "mods":[{"name","modId","fileId"}]}.
func (s *recipeSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	data, err := fs.ReadFile(root, "collection.json")
	if err != nil {
		return source.CollectionManifest{}, err
	}
	var doc struct {
		Name string `json:"name"`
		Mods []struct {
			Name   string `json:"name"`
			ModID  string `json:"modId"`
			FileID string `json:"fileId"`
			Type   string `json:"type"`
		} `json:"mods"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return source.CollectionManifest{}, err
	}
	m := source.CollectionManifest{Name: doc.Name}
	for _, mod := range doc.Mods {
		m.Mods = append(m.Mods, source.ManifestMod{Name: mod.Name, Version: "1", SourceType: "nexus",
			ModID: mod.ModID, FileID: mod.FileID, UpdatePolicy: "exact", ModType: mod.Type})
	}
	return m, nil
}

// recipeArchive builds a .7z holding collection.json (skipping the test
// when no 7z is installed, as every 7z test here does).
func recipeArchive(t *testing.T, manifest string) []byte {
	t.Helper()
	return recipeArchiveOf(t, func(dir string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), []byte(manifest), 0o644))
		require.NoError(t, os.Mkdir(filepath.Join(dir, "bundled"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "bundled", "file.txt"), []byte("bundled"), 0o644))
	})
}

// recipeArchiveOf builds a .7z of whatever fill puts in a directory, links
// stored as links (-snl).
func recipeArchiveOf(t *testing.T, fill func(dir string)) []byte {
	t.Helper()
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	dir := t.TempDir()
	fill(dir)
	archive := filepath.Join(t.TempDir(), "recipe.7z")
	cmd := exec.Command("7z", "a", "-bd", "-snl", archive, ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	return data
}

type recipeFixture struct {
	svc       *core.Service
	game      *domain.Game
	src       *recipeSource
	cacheDir  string
	downloads *atomic.Int32
}

func newRecipeFixture(t *testing.T, archive []byte) *recipeFixture {
	t.Helper()
	return newRecipeFixtureWith(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sig") != "secret" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = w.Write(archive)
	})
}

// newRecipeFixtureWith is newRecipeFixture with the CDN's handler given.
func newRecipeFixtureWith(t *testing.T, serve http.HandlerFunc) *recipeFixture {
	t.Helper()
	var downloads atomic.Int32
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		serve(w, r)
	}))
	t.Cleanup(cdn.Close)
	cacheDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: cacheDir, DownloadClient: cdn.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := &recipeSource{
		mockSource: newMockSource("nexusmods"),
		collection: source.Collection{ID: "abcdef", Name: "Listed", Revision: 3, Author: "curator",
			URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Handle: "/v2/collections/1/revisions/2/download_link",
			Items: []source.CollectionItem{{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Ten", UpdatePolicy: "exact"}}},
		archiveURL: cdn.URL + "/recipe.7z?sig=secret",
	}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	return &recipeFixture{svc: svc, game: game, src: src, cacheDir: cacheDir, downloads: &downloads}
}

const tinyRecipe = `{"name": "Recipe Name", "mods": [{"name": "Ten", "modId": "10", "fileId": "100"}, {"name": "Eleven", "modId": "11", "fileId": "110"}]}`

func TestConvertSourceCollection_FetchesCachesAndConverts(t *testing.T) {
	f := newRecipeFixture(t, recipeArchive(t, tinyRecipe))
	ctx := context.Background()

	c, err := f.svc.ConvertSourceCollection(ctx, f.game, "nexusmods", "abcdef@3", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.Equal(t, "skyrimspecialedition", f.src.gotGame, "resolved for the game's own id on the source")
	assert.Equal(t, "abcdef@3", f.src.gotRef, "the reference passes through as typed")
	assert.Empty(t, c.ManifestError)
	assert.Empty(t, c.Unsupported)
	assert.Equal(t, "Recipe Name", c.Name)
	require.Len(t, c.Document.Items, 2)
	assert.Equal(t, []string{"100"}, c.Document.Items[0].Ref.FileIDs)
	assert.Equal(t, "Skyrim", c.Document.Game.Name)
	assert.Equal(t, "skyrimspecialedition", c.Document.Game.Sources["nexusmods"])

	entry := filepath.Join(f.cacheDir, "skyrim", "collections", "nexusmods-abcdef", "3")
	assert.FileExists(t, filepath.Join(entry, "collection.json"), "the recipe is kept in its own cache entry")
	assert.NoDirExists(t, filepath.Join(entry, "bundled"), "only the manifest is extracted until C5 needs the rest")
	assert.Equal(t, int32(1), f.downloads.Load())

	_, err = f.svc.ConvertSourceCollection(ctx, f.game, "nexusmods", "abcdef@3", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), f.downloads.Load(), "a re-plan of the same revision is free")
	assert.Equal(t, int32(1), f.src.urlCalls.Load())
}

func TestConvertSourceCollection_GameCachePath(t *testing.T) {
	f := newRecipeFixture(t, recipeArchive(t, tinyRecipe))
	f.game.CachePath = t.TempDir()
	_, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.game.CachePath, "collections", "nexusmods-abcdef", "3", "collection.json"),
		"a per-game cache_path drops the game segment, as mod entries do")
}

func TestConvertSourceCollection_ModTypesFollowTheGamesAdapter(t *testing.T) {
	f := newRecipeFixture(t, recipeArchive(t, `{"name": "R", "mods": [{"name": "SKSE", "modId": "1", "fileId": "2", "type": "dinput"}]}`))
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	require.Len(t, c.Unsupported, 1, "a game with no adapter routes no mod type")
	assert.Equal(t, "mod_type:dinput", c.Unsupported[0].Feature)
}

func TestConvertSourceCollection_RecipeUnavailableStillConverts(t *testing.T) {
	f := newRecipeFixture(t, nil)
	f.src.urlErr = domain.ErrAuthRequired
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err, "the plan still renders the list")
	require.Len(t, c.Unsupported, 1)
	assert.Equal(t, core.CollectionFeatureManifestUnavailable, c.Unsupported[0].Feature)
	assert.Contains(t, c.ManifestError, "sign")
	require.Len(t, c.Items, 1)
	assert.Equal(t, "Ten", c.Items[0].Name)
}

func TestConvertSourceCollection_NoSevenZipIsSaidPlainly(t *testing.T) {
	f := newRecipeFixture(t, nil)
	t.Setenv("PATH", t.TempDir())
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.Contains(t, c.ManifestError, "7z")
	assert.Equal(t, int32(0), f.downloads.Load(), "checked before anything is downloaded")
	assert.Equal(t, core.CollectionFeatureManifestUnavailable, c.Unsupported[0].Feature)
}

func TestConvertSourceCollection_NeverShowsTheSignedLink(t *testing.T) {
	f := newRecipeFixture(t, nil)
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	f.src.archiveURL = strings.Replace(f.src.archiveURL, "sig=secret", "sig=wrong-secret-value", 1)
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, c.ManifestError)
	assert.NotContains(t, c.ManifestError, "wrong-secret-value", "a signed link is a credential")
	assert.NotContains(t, c.ManifestError, "sig=")
}

func TestConvertSourceCollection_Refusals(t *testing.T) {
	f := newRecipeFixture(t, nil)
	ctx := context.Background()

	_, err := f.svc.ConvertSourceCollection(ctx, f.game, "curseforge", "abcdef", core.CollectionConvertOptions{})
	require.Error(t, err)

	plain := newMockSource("plain")
	f.svc.RegisterSource(plain)
	f.game.SourceIDs["plain"] = "x"
	_, err = f.svc.ConvertSourceCollection(ctx, f.game, "plain", "abcdef", core.CollectionConvertOptions{})
	require.ErrorIs(t, err, source.ErrNotSupported)
	assert.True(t, core.IsBadCollectionRef(err))

	delete(f.game.SourceIDs, "nexusmods")
	_, err = f.svc.ConvertSourceCollection(ctx, f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nexusmods")
	f.game.SourceIDs["nexusmods"] = "skyrimspecialedition"

	f.src.resolveErr = errors.Join(source.ErrInvalidReference, errors.New("no such collection"))
	_, err = f.svc.ConvertSourceCollection(ctx, f.game, "nexusmods", "zzz", core.CollectionConvertOptions{})
	assert.True(t, core.IsBadCollectionRef(err))
}

// #607 review 2: a highly compressed collection.json is refused from the
// archive's listing, before anything is inflated.
func TestConvertSourceCollection_RefusesAnOversizedManifestBeforeExtracting(t *testing.T) {
	big := `{"name": "x", "pad": "` + strings.Repeat(" ", 40<<20) + `"}`
	f := newRecipeFixture(t, recipeArchive(t, big))
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.Contains(t, c.ManifestError, "byte limit")
	assert.Equal(t, core.CollectionFeatureManifestUnavailable, c.Unsupported[0].Feature)
	assert.NoDirExists(t, filepath.Join(f.cacheDir, "skyrim", "collections", "nexusmods-abcdef", "3"), "nothing is cached")
}

// #607 review 3: a collection.json that is a link - to a file anywhere -
// is never extracted or read.
func TestConvertSourceCollection_RefusesASymlinkedManifest(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"name": "SECRET"}`), 0o600))
	archive := recipeArchiveOf(t, func(dir string) {
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "collection.json")))
	})
	f := newRecipeFixture(t, archive)
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.Contains(t, c.ManifestError, "not a regular file")
	assert.NotEqual(t, "SECRET", c.Name)
}

// A cache entry lmm did not write - its collection.json a link out of it -
// is replaced, never read.
func TestConvertSourceCollection_ReplacesAPlantedCacheEntry(t *testing.T) {
	f := newRecipeFixture(t, recipeArchive(t, tinyRecipe))
	outside := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"name": "SECRET"}`), 0o600))
	entry := filepath.Join(f.cacheDir, "skyrim", "collections", "nexusmods-abcdef", "3")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(entry, "collection.json")))

	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.Equal(t, "Recipe Name", c.Name, "the recipe was fetched again")
	assert.Equal(t, int32(1), f.downloads.Load())
	info, err := os.Lstat(filepath.Join(entry, "collection.json"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
}

// #607 review 4: a cache_path on another filesystem than the data
// directory's staging (EXDEV on the rename) is copied into place.
func TestConvertSourceCollection_CacheOnAnotherFilesystem(t *testing.T) {
	f := newRecipeFixture(t, recipeArchive(t, tinyRecipe))
	f.game.CachePath = t.TempDir()
	var renames int
	t.Cleanup(core.SetRenameRecipeForTest(func(oldpath, newpath string) error {
		renames++
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}))
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	assert.Empty(t, c.ManifestError)
	assert.Equal(t, 1, renames)
	assert.FileExists(t, filepath.Join(f.game.CachePath, "collections", "nexusmods-abcdef", "3", "collection.json"))
	matches, err := filepath.Glob(filepath.Join(f.game.CachePath, "collections", ".recipe-*"))
	require.NoError(t, err)
	assert.Empty(t, matches, "the copy's temporary directory is gone")
}

// #607 re-review 1: a redirect's target is a signed link the caller never
// saw; a transport error after it must not carry it into the plan.
func TestConvertSourceCollection_NeverShowsARedirectsSignedLink(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	f := newRecipeFixtureWith(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/recipe.7z" {
			http.Redirect(w, r, "/f.7z?md5=SECRET2&user_id=99&expires=1", http.StatusFound)
			return
		}
		hj, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := hj.Hijack()
		require.NoError(t, err)
		_ = conn.Close() // the connection drops mid-request
	})
	c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, c.ManifestError)
	assert.NotContains(t, c.ManifestError, "SECRET2")
	assert.NotContains(t, c.ManifestError, "user_id")
}

// #607 re-review 2, 3: a recipe that is not a 7z - a zip whose comment
// carries a terminal escape, a tar whose collection.json is a hard link -
// is refused from its listing, and nothing of it reaches the message.
func TestConvertSourceCollection_RefusesARecipeThatIsNot7z(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create("collection.json")
	require.NoError(t, err)
	_, _ = w.Write([]byte(tinyRecipe))
	require.NoError(t, zw.SetComment("\x1b]8;;https://evil.example\x07click\x1b]8;;\x07"))
	require.NoError(t, zw.Close())

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "a.json", Mode: 0o644, Size: int64(len(tinyRecipe)), Typeflag: tar.TypeReg}))
	_, _ = tw.Write([]byte(tinyRecipe))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "collection.json", Linkname: "a.json", Typeflag: tar.TypeLink}))
	require.NoError(t, tw.Close())

	for name, archive := range map[string][]byte{"zip": zipBuf.Bytes(), "tar with a hard link": tarBuf.Bytes()} {
		t.Run(name, func(t *testing.T) {
			f := newRecipeFixture(t, archive)
			c, err := f.svc.ConvertSourceCollection(context.Background(), f.game, "nexusmods", "abcdef", core.CollectionConvertOptions{})
			require.NoError(t, err)
			assert.Contains(t, c.ManifestError, "not a 7z archive")
			assert.NotContains(t, c.ManifestError, "\x1b")
			assert.NotContains(t, c.ManifestError, "evil.example")
			assert.NotEqual(t, "Recipe Name", c.Name)
		})
	}
}
