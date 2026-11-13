package core_test

import (
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
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), []byte(manifest), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "bundled"), 0o755))
	archive := filepath.Join(t.TempDir(), "recipe.7z")
	out, err := exec.Command("7z", "a", "-bd", archive, filepath.Join(dir, "collection.json"), filepath.Join(dir, "bundled")).CombinedOutput()
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
	var downloads atomic.Int32
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.URL.Query().Get("sig") != "secret" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = w.Write(archive)
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
	assert.DirExists(t, filepath.Join(entry, "bundled"), "with what it ships beside the manifest")
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
