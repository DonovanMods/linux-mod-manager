package core_test

// End to end for #544: the real CurseForge source (over an httptest stand-in
// for the API) serving one addon published for two WoW flavors under
// CurseForge's single WoW game id. A game configured with
// curseforge.game_version_types resolves, installs and updates to its own
// flavor's file; an unconfigured game keeps today's newest-of-any-flavor
// choice.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	wowRetailType  = 517
	wowClassicType = 67408
	wowAddonID     = 7101
	wowRetailOnly  = 7102
)

func wowAddonFile(id int, name string, date string, versionType int) curseforge.File {
	d, err := time.Parse(time.RFC3339, date)
	if err != nil {
		panic(err)
	}
	return curseforge.File{
		ID: id, ModID: wowAddonID, DisplayName: name, FileName: name + ".zip",
		ReleaseType: curseforge.ReleaseTypeRelease, FileDate: d,
		SortableGameVersions: []curseforge.SortableGameVersion{{GameVersionTypeID: versionType}},
	}
}

var (
	wowRetailFile     = wowAddonFile(9003, "Addon-3.0.0", "2026-03-01T00:00:00Z", wowRetailType)
	wowClassicFile    = wowAddonFile(9002, "Addon-2.5.0", "2026-02-01T00:00:00Z", wowClassicType)
	wowClassicOldFile = wowAddonFile(9001, "Addon-2.4.0", "2026-01-01T00:00:00Z", wowClassicType)
	wowRetailOnlyFile = wowAddonFile(9501, "RetailOnly-1.0.0", "2026-03-01T00:00:00Z", wowRetailType)
)

// wowAPI stands in for the CurseForge endpoints install and update touch. Each
// download is a zip holding "<display name>.toc" whose content is "payload of
// <file name>", so which file was installed can be read off the deployed file
// itself.
func wowAPI(t *testing.T) *httptest.Server {
	t.Helper()
	idx := func(f curseforge.File, versionType int) curseforge.FileIndex {
		return curseforge.FileIndex{FileID: f.ID, Filename: f.FileName, ReleaseType: f.ReleaseType, GameVersionTypeID: versionType}
	}
	mods := map[int]curseforge.Mod{
		wowAddonID: {
			ID: wowAddonID, GameID: 1, Name: "Flavored Addon",
			LatestFiles:        []curseforge.File{wowClassicFile, wowRetailFile},
			LatestFilesIndexes: []curseforge.FileIndex{idx(wowRetailFile, wowRetailType), idx(wowClassicFile, wowClassicType)},
		},
		wowRetailOnly: {
			ID: wowRetailOnly, GameID: 1, Name: "Retail Only",
			LatestFiles:        []curseforge.File{wowRetailOnlyFile},
			LatestFilesIndexes: []curseforge.FileIndex{idx(wowRetailOnlyFile, wowRetailType)},
		},
	}
	listings := map[int][]curseforge.File{
		wowAddonID:    {wowRetailFile, wowClassicFile, wowClassicOldFile},
		wowRetailOnly: {wowRetailOnlyFile},
	}
	byID := map[int]curseforge.File{}
	for _, files := range listings {
		for _, f := range files {
			byID[f.ID] = f
		}
	}

	var srv *httptest.Server
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(v))
	}
	modOf := func(r *http.Request) (curseforge.Mod, bool) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		m, ok := mods[id]
		return m, ok
	}
	mux.HandleFunc("POST /v1/mods", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, curseforge.APIResponse[[]curseforge.Mod]{Data: []curseforge.Mod{mods[wowAddonID], mods[wowRetailOnly]}})
	})
	mux.HandleFunc("GET /v1/mods/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := modOf(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, curseforge.APIResponse[curseforge.Mod]{Data: m})
	})
	mux.HandleFunc("GET /v1/mods/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		m, _ := modOf(r)
		files := listings[m.ID]
		writeJSON(w, curseforge.PaginatedResponse[[]curseforge.File]{
			Data:       files,
			Pagination: curseforge.Pagination{PageSize: 50, ResultCount: len(files), TotalCount: len(files)},
		})
	})
	mux.HandleFunc("GET /v1/mods/{id}/files/{fid}/download-url", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, curseforge.StringDownloadURL{Data: srv.URL + "/dl/" + r.PathValue("fid")})
	})
	mux.HandleFunc("GET /v1/mods/{id}/files/{fid}", func(w http.ResponseWriter, r *http.Request) {
		fid, _ := strconv.Atoi(r.PathValue("fid"))
		f, ok := byID[fid]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, curseforge.APIResponse[curseforge.File]{Data: f})
	})
	mux.HandleFunc("GET /dl/{fid}", func(w http.ResponseWriter, r *http.Request) {
		fid, _ := strconv.Atoi(r.PathValue("fid"))
		f, ok := byID[fid]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// A real addon archive: one .toc named for the file's display name.
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		entry, err := zw.Create(f.DisplayName + ".toc")
		require.NoError(t, err)
		_, err = entry.Write([]byte("payload of " + f.FileName))
		require.NoError(t, err)
		require.NoError(t, zw.Close())
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(buf.Bytes())
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newWoWService is a Service whose games.yaml holds game (so the Service
// resolves it by id, as every flow does), with the real CurseForge source
// pointed at api.
func newWoWService(t *testing.T, api *httptest.Server, game *domain.Game) *core.Service {
	t.Helper()
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	require.NoError(t, config.SaveGame(cfg.ConfigDir, game))
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	target, err := url.Parse(api.URL)
	require.NoError(t, err)
	svc.RegisterSource(curseforge.New(&http.Client{Transport: cfTransport{target}}, "test-api-key"))
	return svc
}

func wowGame(t *testing.T, id string, types ...int) *domain.Game {
	dir := t.TempDir()
	game := &domain.Game{
		ID: id, Name: id, InstallPath: dir, ModPath: dir, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"curseforge": "1"},
	}
	if len(types) > 0 {
		game.CurseForge = &domain.GameCurseForge{GameVersionTypes: types}
	}
	return game
}

// installWoWAddon plans and applies an install of the flavored addon into
// game and returns the installed row.
func installWoWAddon(t *testing.T, svc *core.Service, game *domain.Game) *domain.InstalledMod {
	t.Helper()
	ctx := context.Background()
	game, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	plan, err := svc.PlanInstall(ctx, game, "default", "curseforge", strconv.Itoa(wowAddonID), false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	installed, err := svc.GetInstalledMod(ctx, "curseforge", strconv.Itoa(wowAddonID), game.ID, "default")
	require.NoError(t, err)
	return installed
}

func TestCurseForgeVersionTypes_InstallPicksTheGamesFlavor(t *testing.T) {
	api := wowAPI(t)
	for _, tc := range []struct {
		name    string
		types   []int
		wantID  int
		wantVer string
	}{
		{"classic", []int{wowClassicType}, wowClassicFile.ID, "2.5.0"},
		{"retail", []int{wowRetailType}, wowRetailFile.ID, "3.0.0"},
		{"unconfigured keeps the newest of any flavor", nil, wowRetailFile.ID, "3.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			game := wowGame(t, "wow", tc.types...)
			svc := newWoWService(t, api, game)

			installed := installWoWAddon(t, svc, game)
			assert.Equal(t, []string{strconv.Itoa(tc.wantID)}, installed.FileIDs)
			assert.Equal(t, tc.wantVer, installed.Version)

			got, err := os.ReadFile(filepath.Join(game.ModPath, "Addon-"+tc.wantVer+".toc"))
			require.NoError(t, err, "the chosen flavor's file is deployed")
			assert.Equal(t, "payload of Addon-"+tc.wantVer+".zip", string(got))
		})
	}
}

// ResolveModVersion (`--version`) only matches the game's flavor: Classic has
// no 3.0.0, so asking for it is "no such version", never the Retail file.
func TestCurseForgeVersionTypes_VersionResolutionStaysInTheFlavor(t *testing.T) {
	api := wowAPI(t)
	game := wowGame(t, "wow-classic", wowClassicType)
	svc := newWoWService(t, api, game)
	game, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	mod := &domain.Mod{ID: strconv.Itoa(wowAddonID), SourceID: "curseforge", GameID: "1"}

	files, err := svc.ResolveModVersion(context.Background(), game, "curseforge", mod, "2.4.0")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, strconv.Itoa(wowClassicOldFile.ID), files[0].ID)

	_, err = svc.ResolveModVersion(context.Background(), game, "curseforge", mod, "3.0.0")
	require.Error(t, err, "3.0.0 is a Retail file")
}

// A mod with no file for the game's flavor is reported as having no
// installable file for this game.
func TestCurseForgeVersionTypes_NoFileForTheFlavorRefusesTheInstall(t *testing.T) {
	api := wowAPI(t)
	game := wowGame(t, "wow-classic", wowClassicType)
	svc := newWoWService(t, api, game)
	game, err := svc.GetGame(game.ID)
	require.NoError(t, err)

	_, err = svc.PlanInstall(context.Background(), game, "default", "curseforge", strconv.Itoa(wowRetailOnly), false)
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrNoFileForGame)
	assert.Contains(t, err.Error(), strconv.Itoa(wowClassicType))
}

// The update check and ApplyUpdate keep a Classic install on Classic files:
// with a recorded file id (the FileIDReplacements pin names the Classic
// file) and without one (an import, where the version comparison decides and
// ApplyUpdate resolves the new version's file from the game's own list).
func TestCurseForgeVersionTypes_UpdateStaysInTheFlavor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fileIDs  []string
		wantPins map[string]string
	}{
		{"recorded file id", []string{strconv.Itoa(wowClassicOldFile.ID)},
			map[string]string{strconv.Itoa(wowClassicOldFile.ID): strconv.Itoa(wowClassicFile.ID)}},
		{"imported without file ids", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := wowAPI(t)
			game := wowGame(t, "wow-classic", wowClassicType)
			svc := newWoWService(t, api, game)
			ctx := context.Background()
			game, err := svc.GetGame(game.ID)
			require.NoError(t, err)

			old := seedUpdatableMod(t, svc, game, "curseforge", strconv.Itoa(wowAddonID), "Flavored Addon", "2.4.0",
				tc.fileIDs, map[string][]byte{"Addon-2.4.0.toc": []byte("old")})

			updates, err := svc.NewUpdater().CheckUpdates(ctx, game, []domain.InstalledMod{*old}, nil, core.UpdateCheckOptions{})
			require.NoError(t, err)
			require.Len(t, updates, 1)
			assert.Equal(t, "2.5.0", updates[0].NewVersion, "the Classic file, not Retail's newer 3.0.0")
			assert.Equal(t, tc.wantPins, updates[0].FileIDReplacements)

			plan, err := svc.PlanUpdate(ctx, game, "default", "curseforge", strconv.Itoa(wowAddonID))
			require.NoError(t, err)
			_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
			require.NoError(t, err)

			updated, err := svc.GetInstalledMod(ctx, "curseforge", strconv.Itoa(wowAddonID), game.ID, "default")
			require.NoError(t, err)
			assert.Equal(t, []string{strconv.Itoa(wowClassicFile.ID)}, updated.FileIDs)
			assert.Equal(t, "2.5.0", updated.Version)
		})
	}
}

// A Retail game's update check, by contrast, offers the Retail file - the
// same addon, the same source game id, a different lmm game.
func TestCurseForgeVersionTypes_RetailUpdatesToRetail(t *testing.T) {
	api := wowAPI(t)
	game := wowGame(t, "wow-retail", wowRetailType)
	svc := newWoWService(t, api, game)
	game, err := svc.GetGame(game.ID)
	require.NoError(t, err)

	// Imported without file ids: the version comparison decides, among the
	// game's own flavor only.
	old := seedUpdatableMod(t, svc, game, "curseforge", strconv.Itoa(wowAddonID), "Flavored Addon", "2.4.0",
		nil, map[string][]byte{"Addon-2.4.0.toc": []byte("old")})

	updates, err := svc.NewUpdater().CheckUpdates(context.Background(), game, []domain.InstalledMod{*old}, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "3.0.0", updates[0].NewVersion)
}
