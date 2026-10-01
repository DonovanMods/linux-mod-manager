package core_test

// End to end for #504's index-only update candidate: the real CurseForge
// source (over an httptest stand-in for the API) advertises an update whose
// file only latestFilesIndexes names and that sits beyond CurseForge's first
// 50-file page, and core's ApplyUpdate - which resolves the new file from the
// source's GetModFiles - must install exactly that file's id.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/curseforge"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cfModID        = 4242
	cfTypeMC120    = 75125
	cfTypeMC121    = 77784
	cfInstalledID  = 8_000_001 // the superseded Forge 1.20.1 install, 1.0.0
	cfTargetID     = 8_999_930 // the newest Forge 1.20.1 release (1.9.0): in latestFilesIndexes only
	cfFabricDecoy  = 8_999_990 // a Fabric file carrying the SAME 1.9.0 label, first in the listing
	cfFilesInTotal = 130
)

// cfTransport sends every request to the httptest server, whatever host the
// source's client was built for.
type cfTransport struct{ target *url.URL }

func (t cfTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func cfFile(id int, name string, mcType int, loader string) curseforge.File {
	return curseforge.File{
		ID: id, ModID: cfModID, DisplayName: name, FileName: name + ".jar", ReleaseType: curseforge.ReleaseTypeRelease,
		FileDate:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		GameVersions: []string{"1.20.1", loader},
		SortableGameVersions: []curseforge.SortableGameVersion{
			{GameVersionTypeID: mcType}, {GameVersionName: loader},
		},
	}
}

// cfAPI is a stand-in for the CurseForge endpoints an update check and apply
// touch, for one mod with cfFilesInTotal files (newest first), 50 to a page.
func cfAPI(t *testing.T) *httptest.Server {
	t.Helper()

	fabricDecoy := cfFile(cfFabricDecoy, "bigpack-fabric-1.20.1-1.9.0", cfTypeMC120, "Fabric")
	neo := cfFile(8_999_995, "bigpack-neoforge-1.21-2.0.0", cfTypeMC121, "NeoForge")
	installed := cfFile(cfInstalledID, "bigpack-forge-1.20.1-1.0.0", cfTypeMC120, "Forge")
	forge := cfFile(cfTargetID, "bigpack-forge-1.20.1-1.9.0", cfTypeMC120, "Forge")

	// The listing: the Fabric decoy first, the NeoForge file, then history; the
	// target sits at index 60 (second page), the installed file last.
	var listing []curseforge.File
	listing = append(listing, fabricDecoy, neo)
	for i := 2; i < cfFilesInTotal-1; i++ {
		f := cfFile(8_900_000+(cfFilesInTotal-i), fmt.Sprintf("bigpack-forge-1.20.1-1.%d.0", 100-i), cfTypeMC120, "Forge")
		if i == 60 {
			f = forge
		}
		listing = append(listing, f)
	}
	listing = append(listing, installed)
	require.Len(t, listing, cfFilesInTotal)
	byID := map[int]curseforge.File{}
	for _, f := range listing {
		byID[f.ID] = f
	}

	idx := func(f curseforge.File, mcType, loader int) curseforge.FileIndex {
		return curseforge.FileIndex{FileID: f.ID, Filename: f.FileName, ReleaseType: f.ReleaseType, GameVersionTypeID: mcType, ModLoader: loader}
	}
	mod := curseforge.Mod{
		ID: cfModID, Name: "Big Pack",
		LatestFiles: []curseforge.File{neo, fabricDecoy},
		LatestFilesIndexes: []curseforge.FileIndex{
			idx(neo, cfTypeMC121, curseforge.ModLoaderNeoForge),
			idx(fabricDecoy, cfTypeMC120, curseforge.ModLoaderFabric),
			// the newest Forge 1.20.1 release is not in latestFiles
			idx(forge, cfTypeMC120, curseforge.ModLoaderForge),
		},
	}

	var srv *httptest.Server
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(v))
	}
	mux.HandleFunc("POST /v1/mods", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, curseforge.APIResponse[[]curseforge.Mod]{Data: []curseforge.Mod{mod}})
	})
	mux.HandleFunc("GET /v1/mods/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, curseforge.APIResponse[curseforge.Mod]{Data: mod})
	})
	mux.HandleFunc("GET /v1/mods/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		index, _ := strconv.Atoi(r.URL.Query().Get("index"))
		pageSize := 50
		end := min(index+pageSize, len(listing))
		var page []curseforge.File
		if index < len(listing) {
			page = listing[index:end]
		}
		writeJSON(w, curseforge.PaginatedResponse[[]curseforge.File]{
			Data: page,
			Pagination: curseforge.Pagination{
				Index: index, PageSize: pageSize, ResultCount: len(page), TotalCount: len(listing),
			},
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
		_, _ = w.Write([]byte("payload of " + f.FileName))
	})
	srv = httptest.NewServer(mux)
	return srv
}

// TestApplyUpdate_InstallsAnIndexOnlyCandidateFromBeyondTheFirstFilesPage
// pins the contract the CurseForge check relies on: the update names its file
// by id (FileIDReplacements), that file is only in latestFilesIndexes and not
// on the first 50-file page of the listing, and ApplyUpdate installs exactly
// it - not the Fabric file that merely carries the same version label.
func TestApplyUpdate_InstallsAnIndexOnlyCandidateFromBeyondTheFirstFilesPage(t *testing.T) {
	srv := cfAPI(t)
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)

	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	cf := curseforge.New(&http.Client{Transport: cfTransport{target}}, "test-api-key")
	svc.RegisterSource(cf)

	oldName := "bigpack-forge-1.20.1-1.0.0.jar"
	old := seedUpdatableMod(t, svc, game, "curseforge", strconv.Itoa(cfModID), "Big Pack", "1.0.0",
		[]string{strconv.Itoa(cfInstalledID)}, map[string][]byte{oldName: []byte("old")})

	updates, err := cf.CheckUpdates(context.Background(), []domain.InstalledMod{*old})
	require.NoError(t, err)
	require.Len(t, updates, 1, "got %+v", updates)
	require.Equal(t, "1.9.0", updates[0].NewVersion)
	require.Equal(t, map[string]string{strconv.Itoa(cfInstalledID): strconv.Itoa(cfTargetID)}, updates[0].FileIDReplacements)

	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default", updates[0])
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.NoError(t, err)

	updated, err := svc.GetInstalledMod(context.Background(), "curseforge", strconv.Itoa(cfModID), "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{strconv.Itoa(cfTargetID)}, updated.FileIDs, "exactly the advertised file, not the same-labelled Fabric one")

	got, err := os.ReadFile(filepath.Join(gameDir, "bigpack-forge-1.20.1-1.9.0.jar"))
	require.NoError(t, err, "the Forge 1.9.0 file must be deployed")
	assert.True(t, strings.Contains(string(got), "bigpack-forge-1.20.1-1.9.0"), "deployed content is the Forge file's: %q", got)
	_, statErr := os.Stat(filepath.Join(gameDir, "bigpack-fabric-1.20.1-1.9.0.jar"))
	assert.True(t, os.IsNotExist(statErr), "the Fabric file must not be deployed")
}
