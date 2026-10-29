package curseforge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #544: a game's curseforge.game_version_types restricts every file choice to
// files carrying one of those gameVersionTypeIds. The addon below is
// published for Retail and Classic (CurseForge's one WoW game, id 1), the way
// a real multi-flavor addon document comes back: latestFiles in no promised
// order, one latestFilesIndexes entry per flavor, and the full file list
// newest first.
const flavorModID = 7001

var (
	flavorRetail     = wowFile(5003, "Addon-3.0.0", ReleaseTypeRelease, "2026-03-01T00:00:00Z", typeRetail)
	flavorClassic    = wowFile(5002, "Addon-2.5.0", ReleaseTypeRelease, "2026-02-01T00:00:00Z", typeClassic)
	flavorClassicOld = wowFile(5001, "Addon-2.4.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeClassic)
)

func flavorAddon() Mod {
	return Mod{
		ID: flavorModID, GameID: 1, Name: "Flavored Addon",
		LatestFiles: []File{flavorClassic, flavorRetail},
		LatestFilesIndexes: []FileIndex{
			indexFor(flavorRetail, typeRetail),
			indexFor(flavorClassic, typeClassic),
		},
	}
}

// retailOnlyAddon has no Classic file at all.
func retailOnlyAddon() Mod {
	f := wowFile(6001, "RetailThing-1.0.0", ReleaseTypeRelease, "2026-03-01T00:00:00Z", typeRetail)
	return Mod{
		ID: 7002, GameID: 1, Name: "Retail Only",
		LatestFiles:        []File{f},
		LatestFilesIndexes: []FileIndex{indexFor(f, typeRetail)},
	}
}

// flavorAPI serves the mod document, the batch lookup, the file list and a
// search answering both addons, and records each search's query string.
type flavorAPI struct {
	*httptest.Server
	mu       sync.Mutex
	searches []string
}

func newFlavorAPI(t *testing.T) *flavorAPI {
	t.Helper()
	api := &flavorAPI{}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(v))
	}
	files := []File{flavorRetail, flavorClassic, flavorClassicOld}
	byID := map[int]File{}
	for _, f := range files {
		byID[f.ID] = f
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/mods/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("slug") == "" { // not exactNameHit's lookup
			api.mu.Lock()
			api.searches = append(api.searches, r.URL.RawQuery)
			api.mu.Unlock()
		}
		writeJSON(w, PaginatedResponse[[]Mod]{
			Data:       []Mod{flavorAddon(), retailOnlyAddon()},
			Pagination: Pagination{PageSize: 20, ResultCount: 2, TotalCount: 2},
		})
	})
	mux.HandleFunc("POST /v1/mods", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, APIResponse[[]Mod]{Data: []Mod{flavorAddon(), retailOnlyAddon()}})
	})
	mux.HandleFunc("GET /v1/mods/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "7002" {
			writeJSON(w, APIResponse[Mod]{Data: retailOnlyAddon()})
			return
		}
		writeJSON(w, APIResponse[Mod]{Data: flavorAddon()})
	})
	mux.HandleFunc("GET /v1/mods/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		data := files
		if r.PathValue("id") == "7002" {
			data = retailOnlyAddon().LatestFiles
		}
		writeJSON(w, PaginatedResponse[[]File]{Data: data, Pagination: Pagination{PageSize: 50, ResultCount: len(data), TotalCount: len(data)}})
	})
	mux.HandleFunc("GET /v1/mods/{id}/files/{fid}", func(w http.ResponseWriter, r *http.Request) {
		fid, _ := strconv.Atoi(r.PathValue("fid"))
		f, ok := byID[fid]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, APIResponse[File]{Data: f})
	})
	api.Server = httptest.NewServer(mux)
	t.Cleanup(api.Close)
	return api
}

func (a *flavorAPI) source() *CurseForge {
	cf := New(a.Client(), "test-api-key")
	cf.client.SetBaseURL(a.URL)
	return cf
}

// gameCtx is a context for a call made for a WoW game accepting types (none:
// a game with no setting).
func gameCtx(types ...int) context.Context {
	game := &domain.Game{ID: "wow", SourceIDs: map[string]string{"curseforge": "1"}}
	if len(types) > 0 {
		game.CurseForge = &domain.GameCurseForge{GameVersionTypes: types}
	}
	return source.WithGame(context.Background(), game)
}

func fileIDs(files []domain.DownloadableFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.ID
	}
	return out
}

func TestGameVersionTypes_GetModVersionIsTheConfiguredFlavors(t *testing.T) {
	cf := newFlavorAPI(t).source()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"classic", gameCtx(typeClassic), "2.5.0"},
		{"retail", gameCtx(typeRetail), "3.0.0"},
		{"unconfigured: the newest of any flavor, as before", gameCtx(), "3.0.0"},
		{"no game at all", context.Background(), "3.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod, err := cf.GetMod(tc.ctx, "1", strconv.Itoa(flavorModID))
			require.NoError(t, err)
			assert.Equal(t, tc.want, mod.Version)
		})
	}
}

func TestGameVersionTypes_GetModFilesListsOnlyTheConfiguredFlavors(t *testing.T) {
	cf := newFlavorAPI(t).source()
	mod := &domain.Mod{ID: strconv.Itoa(flavorModID), SourceID: "curseforge", GameID: "1"}

	classic, err := cf.GetModFiles(gameCtx(typeClassic), mod)
	require.NoError(t, err)
	assert.Equal(t, []string{"5002", "5001"}, fileIDs(classic))
	assert.True(t, classic[0].IsPrimary, "the newest file of the flavor is the primary one")
	assert.False(t, classic[1].IsPrimary)

	retail, err := cf.GetModFiles(gameCtx(typeRetail), mod)
	require.NoError(t, err)
	assert.Equal(t, []string{"5003"}, fileIDs(retail))
	assert.True(t, retail[0].IsPrimary)

	both, err := cf.GetModFiles(gameCtx(typeRetail, typeClassic), mod)
	require.NoError(t, err)
	assert.Equal(t, []string{"5003", "5002", "5001"}, fileIDs(both))

	all, err := cf.GetModFiles(gameCtx(), mod)
	require.NoError(t, err)
	assert.Equal(t, []string{"5003", "5002", "5001"}, fileIDs(all), "unconfigured: every file, as before")
}

// A mod with no file for the configured flavor has nothing installable for
// this game - never another flavor's file.
func TestGameVersionTypes_NoFileForTheFlavorIsAnError(t *testing.T) {
	cf := newFlavorAPI(t).source()
	mod := &domain.Mod{ID: "7002", SourceID: "curseforge", GameID: "1"}

	files, err := cf.GetModFiles(gameCtx(typeClassic), mod)
	require.Error(t, err)
	assert.Empty(t, files)
	assert.ErrorIs(t, err, source.ErrNoFileForGame)
	assert.Contains(t, err.Error(), strconv.Itoa(typeClassic), "the error names the configured type ids")
}

func TestGameVersionTypes_SearchFiltersOutModsWithoutTheFlavor(t *testing.T) {
	api := newFlavorAPI(t)
	cf := api.source()

	res, err := cf.Search(gameCtx(typeClassic), source.SearchQuery{GameID: "1", Query: "addon", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1, "the Retail-only addon has nothing for Classic")
	assert.Equal(t, strconv.Itoa(flavorModID), res.Mods[0].ID)
	assert.Equal(t, "2.5.0", res.Mods[0].Version, "the hit shows the flavor's own version")

	res, err = cf.Search(gameCtx(), source.SearchQuery{GameID: "1", Query: "addon", PageSize: 20})
	require.NoError(t, err)
	assert.Len(t, res.Mods, 2, "unconfigured: every hit, as before")
	assert.Equal(t, "3.0.0", res.Mods[0].Version)

	_, err = cf.Search(gameCtx(typeRetail, typeClassic), source.SearchQuery{GameID: "1", Query: "addon", PageSize: 20})
	require.NoError(t, err)

	api.mu.Lock()
	defer api.mu.Unlock()
	require.Len(t, api.searches, 3)
	assert.Contains(t, api.searches[0], "gameVersionTypeId=67408", "one configured type is filtered server-side, so pages stay full")
	assert.NotContains(t, api.searches[1], "gameVersionTypeId")
	assert.NotContains(t, api.searches[2], "gameVersionTypeId", "the API filters on one type only; two are filtered here")
}

func TestGameVersionTypes_CheckUpdatesOffersOnlyTheConfiguredFlavor(t *testing.T) {
	cf := newFlavorAPI(t).source()

	installed := func(version string, fileIDs ...string) domain.InstalledMod {
		return domain.InstalledMod{
			Mod:     domain.Mod{ID: strconv.Itoa(flavorModID), SourceID: "curseforge", Name: "Flavored Addon", Version: version, GameID: "1"},
			FileIDs: fileIDs,
		}
	}

	t.Run("classic install updates to the classic file", func(t *testing.T) {
		updates, err := cf.CheckUpdates(gameCtx(typeClassic), []domain.InstalledMod{installed("2.4.0", "5001")})
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "2.5.0", updates[0].NewVersion)
		assert.Equal(t, map[string]string{"5001": "5002"}, updates[0].FileIDReplacements)
	})

	t.Run("retail install is current", func(t *testing.T) {
		updates, err := cf.CheckUpdates(gameCtx(typeRetail), []domain.InstalledMod{installed("3.0.0", "5003")})
		require.NoError(t, err)
		assert.Empty(t, updates)
	})

	// Without recorded file ids (an import) the check compares versions,
	// and only the configured flavor's files are candidates.
	t.Run("classic import without file ids", func(t *testing.T) {
		updates, err := cf.CheckUpdates(gameCtx(typeClassic), []domain.InstalledMod{installed("2.4.0")})
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "2.5.0", updates[0].NewVersion)
	})

	t.Run("unconfigured import keeps today's newest-of-any-flavor answer", func(t *testing.T) {
		updates, err := cf.CheckUpdates(gameCtx(), []domain.InstalledMod{installed("2.4.0")})
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "3.0.0", updates[0].NewVersion)
	})

	t.Run("a mod with no file for the flavor offers nothing", func(t *testing.T) {
		inst := domain.InstalledMod{
			Mod:     domain.Mod{ID: "7002", SourceID: "curseforge", Name: "Retail Only", Version: "0.9.0", GameID: "1"},
			FileIDs: []string{"6000"},
		}
		updates, err := cf.CheckUpdates(gameCtx(typeClassic), []domain.InstalledMod{inst})
		require.NoError(t, err)
		assert.Empty(t, updates)
	})
}

// GetDependencies reads the dependencies of the configured flavor's newest
// file, not of whatever file the listing puts first.
func TestGameVersionTypes_DependenciesComeFromTheFlavorsFile(t *testing.T) {
	retail := flavorRetail
	retail.Dependencies = []FileDependency{{ModID: 111, RelationType: RelationRequiredDependency}}
	classic := flavorClassic
	classic.Dependencies = []FileDependency{{ModID: 222, RelationType: RelationRequiredDependency}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/files") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(PaginatedResponse[[]File]{Data: []File{retail, classic}}))
	}))
	defer srv.Close()
	cf := New(srv.Client(), "k")
	cf.client.SetBaseURL(srv.URL)
	mod := &domain.Mod{ID: strconv.Itoa(flavorModID), GameID: "1"}

	deps, err := cf.GetDependencies(gameCtx(typeClassic), mod)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, "222", deps[0].ModID)

	deps, err = cf.GetDependencies(gameCtx(), mod)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	assert.Equal(t, "111", deps[0].ModID, "unconfigured: the first file's, as before")
}
