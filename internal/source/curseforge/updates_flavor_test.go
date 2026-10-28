package curseforge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WoW's flavors are told apart by gameVersionTypeId, not by anything in a
// file's name (#504).
const (
	typeRetail    = 517
	typeClassic   = 67408
	typeCata      = 77522
	typeClientEnv = 9638 // sortableGameVersions also carries non-flavor types like Client/Server
)

// wowFile builds one latestFiles entry. The flavor types go on the file's
// sortableGameVersions, the way the API reports them.
func wowFile(id int, display string, release int, date string, flavors ...int) File {
	d, err := time.Parse(time.RFC3339, date)
	if err != nil {
		panic(err)
	}
	f := File{
		ID:          id,
		DisplayName: display,
		FileName:    display + ".zip",
		ReleaseType: release,
		FileDate:    d,
	}
	for _, ft := range flavors {
		f.SortableGameVersions = append(f.SortableGameVersions, SortableGameVersion{GameVersionTypeID: ft})
	}
	f.SortableGameVersions = append(f.SortableGameVersions, SortableGameVersion{GameVersionTypeID: typeClientEnv})
	return f
}

// indexFor builds the latestFilesIndexes entry pointing at f for one flavor.
func indexFor(f File, flavor int) FileIndex {
	return FileIndex{FileID: f.ID, Filename: f.FileName, ReleaseType: f.ReleaseType, GameVersionTypeID: flavor}
}

// flavorServer answers the batch POST /v1/mods with the given mods and
// GET /v1/mods/{id}/files/{fileId} with files, counting each kind.
func flavorServer(t *testing.T, mods []Mod, files map[int]File, batch, single *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/mods":
			batch.Add(1)
			assert.NoError(t, json.NewEncoder(w).Encode(APIResponse[[]Mod]{Data: mods}))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/files/"):
			single.Add(1)
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			fid, err := strconv.Atoi(parts[len(parts)-1])
			if err != nil {
				http.Error(w, "bad file id", http.StatusBadRequest)
				return
			}
			f, ok := files[fid]
			if !ok {
				http.NotFound(w, r)
				return
			}
			assert.NoError(t, json.NewEncoder(w).Encode(APIResponse[File]{Data: f}))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
}

func newFlavorTestSource(srv *httptest.Server) *CurseForge {
	cf := New(srv.Client(), "test-api-key")
	cf.client.SetBaseURL(srv.URL)
	return cf
}

func wowInstalled(modID int, name, version string, fileIDs ...int) domain.InstalledMod {
	ids := make([]string, len(fileIDs))
	for i, id := range fileIDs {
		ids[i] = strconv.Itoa(id)
	}
	return domain.InstalledMod{
		Mod:     domain.Mod{ID: strconv.Itoa(modID), SourceID: "curseforge", Name: name, Version: version, GameID: "1"},
		FileIDs: ids,
	}
}

// realWoWMods is the shape of a real GetMods answer for the addons in #504:
// latestFiles holds the newest file of EACH flavor in no particular order,
// with an old Classic file ahead of the current Retail one.
func realWoWMods() []Mod {
	questieClassic := wowFile(4000001, "Questie-10.10.0-b1", ReleaseTypeBeta, "2023-03-01T00:00:00Z", typeClassic)
	questieRetail := wowFile(6500002, "Questie-12.0.3+v1.0.4", ReleaseTypeRelease, "2026-08-01T00:00:00Z", typeRetail)

	tomtomClassic := wowFile(3100001, "TomTom-1.0.6-beta", ReleaseTypeBeta, "2021-02-01T00:00:00Z", typeClassic)
	tomtomRetail := wowFile(6400003, "TomTom-4.3.11-release", ReleaseTypeRelease, "2026-07-01T00:00:00Z", typeRetail)
	tomtomRetailBeta := wowFile(6450004, "TomTom-4.3.12-beta", ReleaseTypeBeta, "2026-07-20T00:00:00Z", typeRetail)

	rareClassic := wowFile(3000005, "RareScanner-9.0.1", ReleaseTypeRelease, "2022-01-01T00:00:00Z", typeCata)
	rareRetail := wowFile(5200001, "RareScanner-1.60.1", ReleaseTypeRelease, "2026-06-01T00:00:00Z", typeRetail)

	moveRetail := wowFile(6200020, "MoveAny-1.12.27", ReleaseTypeRelease, "2026-09-01T00:00:00Z", typeRetail)
	moveClassic := wowFile(6200030, "MoveAny-1.12.28-classic", ReleaseTypeRelease, "2026-09-10T00:00:00Z", typeClassic)

	return []Mod{
		{
			ID: 1001, Name: "Questie",
			LatestFiles:        []File{questieClassic, questieRetail},
			LatestFilesIndexes: []FileIndex{indexFor(questieClassic, typeClassic), indexFor(questieRetail, typeRetail)},
		},
		{
			ID: 1002, Name: "TomTom",
			LatestFiles:        []File{tomtomClassic, tomtomRetailBeta, tomtomRetail},
			LatestFilesIndexes: []FileIndex{indexFor(tomtomClassic, typeClassic), indexFor(tomtomRetail, typeRetail), indexFor(tomtomRetailBeta, typeRetail)},
		},
		{
			ID: 1003, Name: "RareScanner",
			LatestFiles:        []File{rareClassic, rareRetail},
			LatestFilesIndexes: []FileIndex{indexFor(rareClassic, typeCata), indexFor(rareRetail, typeRetail)},
		},
		{
			ID: 1004, Name: "MoveAny",
			LatestFiles:        []File{moveClassic, moveRetail},
			LatestFilesIndexes: []FileIndex{indexFor(moveClassic, typeClassic), indexFor(moveRetail, typeRetail)},
		},
	}
}

// The installed MoveAny file (1.12.26) has been superseded, so it is in
// neither latestFiles nor latestFilesIndexes; only GetModFile knows it is a
// Retail file.
func realWoWInstalledFiles() map[int]File {
	return map[int]File{
		6100010: wowFile(6100010, "MoveAny-1.12.26", ReleaseTypeRelease, "2026-08-15T00:00:00Z", typeRetail),
	}
}

func realWoWInstalled() []domain.InstalledMod {
	return []domain.InstalledMod{
		wowInstalled(1001, "Questie", "12.0.3+v1.0.4", 6500002),
		wowInstalled(1002, "TomTom", "4.3.11-release", 6400003),
		wowInstalled(1003, "RareScanner", "1.60.1", 5200001),
		wowInstalled(1004, "MoveAny", "1.12.26", 6100010),
	}
}

// TestCheckUpdates_MultiFlavorLatestFilesAreNotDowngrades is #504: the check
// took latestFiles[0] as "the latest" and reported any difference, so an
// old Classic file ahead of the current Retail one read as an update to a
// version years older. Only MoveAny has a genuine update, and it must be the
// Retail file - the newer Classic file is not what that install follows.
func TestCheckUpdates_MultiFlavorLatestFilesAreNotDowngrades(t *testing.T) {
	var batch, single atomic.Int32
	srv := flavorServer(t, realWoWMods(), realWoWInstalledFiles(), &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(), realWoWInstalled())
	require.NoError(t, err)

	require.Len(t, updates, 1, "only MoveAny has an update; got %+v", updates)
	assert.Equal(t, "1004", updates[0].InstalledMod.ID)
	assert.Equal(t, "1.12.27", updates[0].NewVersion)
	assert.Equal(t, map[string]string{"6100010": "6200020"}, updates[0].FileIDReplacements,
		"the update must name the exact file it advertises so ApplyUpdate installs that one")

	assert.EqualValues(t, 1, batch.Load())
	assert.EqualValues(t, 1, single.Load(),
		"only MoveAny is ambiguous (superseded install, several flavors): one GetModFile, not one per mod")
}

// TestCheckUpdates_InstalledFlavorKnownMakesNoExtraCall: when every installed
// file is still the newest of its flavor, latestFilesIndexes already says
// which flavor it is and no GetModFile is spent.
func TestCheckUpdates_InstalledFlavorKnownMakesNoExtraCall(t *testing.T) {
	var batch, single atomic.Int32
	srv := flavorServer(t, realWoWMods(), nil, &batch, &single)
	defer srv.Close()

	installed := realWoWInstalled()[:3] // Questie, TomTom, RareScanner
	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(), installed)
	require.NoError(t, err)

	assert.Empty(t, updates)
	assert.Zero(t, single.Load(), "an installed file found in latestFiles/latestFilesIndexes needs no lookup")
}

// TestCheckUpdates_SingleFlavorSupersededInstallMakesNoExtraCall: a mod whose
// latestFiles span one flavor has nothing to be ambiguous about.
func TestCheckUpdates_SingleFlavorSupersededInstallMakesNoExtraCall(t *testing.T) {
	retail := wowFile(7000002, "Plain-2.0.0", ReleaseTypeRelease, "2026-09-01T00:00:00Z", typeRetail)
	mods := []Mod{{ID: 2001, Name: "Plain", LatestFiles: []File{retail}, LatestFilesIndexes: []FileIndex{indexFor(retail, typeRetail)}}}

	var batch, single atomic.Int32
	srv := flavorServer(t, mods, nil, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(2001, "Plain", "1.0.0", 7000001)})
	require.NoError(t, err)

	require.Len(t, updates, 1)
	assert.Equal(t, "2.0.0", updates[0].NewVersion)
	assert.Zero(t, single.Load())
}

// TestCheckUpdates_FlavorLookupFailureOffersNothingAndIsReported: when the
// installed file's flavor cannot be learned, the mod is neither updated nor
// silently dropped - it is named once in the skipped error.
func TestCheckUpdates_FlavorLookupFailureOffersNothingAndIsReported(t *testing.T) {
	var batch, single atomic.Int32
	srv := flavorServer(t, realWoWMods(), nil /* every GetModFile is a 404 */, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(1004, "MoveAny", "1.12.26", 6100010)})

	assert.Empty(t, updates)
	require.Error(t, err)
	assert.Equal(t, 1, strings.Count(err.Error(), "MoveAny (id 1004)"), "one reason per mod: %v", err)
}

// TestCheckUpdates_FlavorLookupHonoursCancellation: a cancelled context during
// the extra lookup is the context's error, not a per-mod skip.
func TestCheckUpdates_FlavorLookupHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mods := realWoWMods()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(APIResponse[[]Mod]{Data: mods})
			return
		}
		cancel()
		http.Error(w, "gone", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newFlavorTestSource(srv).CheckUpdates(ctx,
		[]domain.InstalledMod{wowInstalled(1004, "MoveAny", "1.12.26", 6100010)})
	assert.ErrorIs(t, err, context.Canceled)
}

// TestCheckUpdates_NoRecordedFileIDsComparesVersions: an install with no file
// ids (older installs, imports) falls back to the shared version comparator,
// and a candidate that compares OLDER is never an update.
func TestCheckUpdates_NoRecordedFileIDsComparesVersions(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		want      string // "" = no update
	}{
		{"older candidate is not an update", "12.0.3", ""},
		{"same version is not an update", "11.0.0", ""},
		{"newer candidate is an update", "10.0.0", "11.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := wowFile(1, "Thing-9.0.1", ReleaseTypeRelease, "2022-01-01T00:00:00Z", typeClassic)
			cur := wowFile(2, "Thing-11.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeRetail)
			mods := []Mod{{ID: 3001, Name: "Thing", LatestFiles: []File{old, cur}}}

			var batch, single atomic.Int32
			srv := flavorServer(t, mods, nil, &batch, &single)
			defer srv.Close()

			updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
				[]domain.InstalledMod{wowInstalled(3001, "Thing", tc.installed)})
			require.NoError(t, err)
			if tc.want == "" {
				assert.Empty(t, updates)
				return
			}
			require.Len(t, updates, 1)
			assert.Equal(t, tc.want, updates[0].NewVersion)
			assert.Empty(t, updates[0].FileIDReplacements, "no installed file id, nothing to replace")
			assert.Zero(t, single.Load())
		})
	}
}

// TestModToDomain_VersionIsTheNewestFileNotTheFirst: Search/GetMod show a
// version too, and it must not be whichever flavor's file the API listed first.
func TestModToDomain_VersionIsTheNewestFileNotTheFirst(t *testing.T) {
	old := wowFile(10, "Thing-9.0.1", ReleaseTypeRelease, "2022-01-01T00:00:00Z", typeClassic)
	cur := wowFile(30, "Thing-11.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeRetail)
	mid := wowFile(20, "Thing-10.0.0", ReleaseTypeRelease, "2024-01-01T00:00:00Z", typeCata)

	got := modToDomain(Mod{ID: 1, Name: "Thing", LatestFiles: []File{old, cur, mid}}, "1")
	assert.Equal(t, "11.0.0", got.Version)

	// Same date: the higher file id is the newer upload.
	a := wowFile(40, "Tie-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeRetail)
	b := wowFile(41, "Tie-1.0.1", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeClassic)
	got = modToDomain(Mod{ID: 2, Name: "Tie", LatestFiles: []File{b, a}}, "1")
	assert.Equal(t, "1.0.1", got.Version)
}

// TestCheckUpdates_ReleaseTypeIsNotDowngraded: an install of a release file is
// not offered a newer beta/alpha of its own flavor (TomTom has one above).
func TestCheckUpdates_ReleaseTypeIsNotDowngraded(t *testing.T) {
	var batch, single atomic.Int32
	srv := flavorServer(t, realWoWMods(), nil, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(1002, "TomTom", "4.3.11-release", 6400003)})
	require.NoError(t, err)
	assert.Empty(t, updates, "the only newer retail file is a beta")
}
