package custom

// #541: a mod that has left a custom source's listing is reported per mod as
// a *source.ModNotFoundError, so core lists it in catalog_missing instead of
// failing the check (api) or saying nothing (directory, manifest). Only the
// listing's own positive answer counts: a 404/410 from get_mod, or a scan or
// fetch that was READ and does not hold the id. A listing that could not be
// read says nothing about any mod.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// missingIDs is the ids SplitModNotFound - the call core's update check
// makes - takes out of err, and what it leaves.
func missingIDs(err error) ([]string, error) {
	missing, rest := source.SplitModNotFound(err)
	ids := make([]string, 0, len(missing))
	for _, nf := range missing {
		ids = append(ids, nf.ModID)
	}
	return ids, rest
}

// assertNoneMissing fails if err reports any mod as gone.
func assertNoneMissing(t *testing.T, err error) {
	t.Helper()
	var nf *source.ModNotFoundError
	assert.False(t, errors.As(err, &nf), "a listing that could not be read says nothing about any mod: %v", err)
	assert.False(t, errors.Is(err, domain.ErrModNotFound), "an unreadable listing is not ErrModNotFound: %v", err)
}

// --- api ---------------------------------------------------------------

// goneAPIServer answers get_mod for live, 404 for gone404, 410 for gone410,
// and 500 for broken.
func goneAPIServer(t *testing.T) *API {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/mods/live", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": "live", "name": "Live Mod", "latest_version": "2.0.0"}`))
	})
	mux.HandleFunc("/mods/gone404", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no such mod"}`, http.StatusNotFound)
	})
	mux.HandleFunc("/mods/gone410", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"withdrawn"}`, http.StatusGone)
	})
	mux.HandleFunc("/mods/broken", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "backend unavailable", http.StatusInternalServerError)
	})
	a, err := NewAPI(apiDef(srv.URL))
	require.NoError(t, err)
	return a
}

func TestAPIGetMod_404And410AreErrModNotFound(t *testing.T) {
	a := goneAPIServer(t)
	for _, id := range []string{"gone404", "gone410"} {
		_, err := a.GetMod(context.Background(), "skyrim", id)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrModNotFound, "get_mod answering %s is the API's own 'no such mod'", id)
		assert.Contains(t, err.Error(), id, "the error names the mod")
	}
}

func TestAPIGetMod_OtherStatusesAreNotErrModNotFound(t *testing.T) {
	a := goneAPIServer(t)
	_, err := a.GetMod(context.Background(), "skyrim", "broken")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrModNotFound)
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestAPICheckUpdates_GoneModsAreModNotFoundErrors(t *testing.T) {
	a := goneAPIServer(t)
	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "gone404", SourceID: "my-api", Name: "Old One", Version: "1.0", GameID: "skyrim"}},
		{Mod: domain.Mod{ID: "live", SourceID: "my-api", Name: "Live Mod", Version: "1.0.0", GameID: "skyrim"}},
		{Mod: domain.Mod{ID: "gone410", SourceID: "my-api", Name: "Old Two", Version: "1.0", GameID: "skyrim"}},
	}

	updates, err := a.CheckUpdates(context.Background(), installed)
	require.Len(t, updates, 1, "the gone mods do not hide the rest")
	assert.Equal(t, "live", updates[0].InstalledMod.ID)
	assert.Equal(t, "2.0.0", updates[0].NewVersion)

	ids, rest := missingIDs(err)
	assert.Equal(t, []string{"gone404", "gone410"}, ids)
	assert.NoError(t, rest, "the gone mods were all that went wrong")
}

func TestAPICheckUpdates_AFailedRequestIsNotAGoneMod(t *testing.T) {
	a := goneAPIServer(t)
	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "broken", SourceID: "my-api", Name: "Flaky", Version: "1.0", GameID: "skyrim"}},
		{Mod: domain.Mod{ID: "gone404", SourceID: "my-api", Name: "Old One", Version: "1.0", GameID: "skyrim"}},
	}

	_, err := a.CheckUpdates(context.Background(), installed)
	ids, rest := missingIDs(err)
	assert.Equal(t, []string{"gone404"}, ids)
	require.Error(t, rest, "a 500 is still a failed check")
	assert.Contains(t, rest.Error(), "HTTP 500")
	assertNoneMissing(t, rest)
}

// --- directory ---------------------------------------------------------

func TestDirectoryCheckUpdates_ModGoneFromTheScanIsModNotFoundError(t *testing.T) {
	d := newTestDirectory(t) // BiggerBackpack is at 1.2.0
	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Name: "Bigger Backpack", Version: "1.0.0"}},
		{Mod: domain.Mod{ID: "Removed", SourceID: "my-mods", Name: "Removed", Version: "1.0"}},
		{Mod: domain.Mod{ID: "PlainMod-0.5", SourceID: "my-mods", Name: "PlainMod", Version: "0.5"}},
	}

	updates, err := d.CheckUpdates(context.Background(), installed)
	require.Len(t, updates, 1)
	assert.Equal(t, "BiggerBackpack", updates[0].InstalledMod.ID)

	ids, rest := missingIDs(err)
	assert.Equal(t, []string{"Removed"}, ids)
	assert.NoError(t, rest)
}

func TestDirectoryCheckUpdates_AnUnreadableDirectoryMarksNothingMissing(t *testing.T) {
	root := t.TempDir()
	d, err := NewDirectory(SourceDefinition{ID: "my-mods", Name: "My Mods", Type: TypeDirectory, Directory: &DirectoryConfig{Path: root}})
	require.NoError(t, err)
	require.NoError(t, os.Remove(root)) // the scan itself now fails

	updates, err := d.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "A", SourceID: "my-mods", Version: "1.0"}},
		{Mod: domain.Mod{ID: "B", SourceID: "my-mods", Version: "1.0"}},
	})
	require.Error(t, err, "a failed scan is a failed check")
	assert.Empty(t, updates)
	assertNoneMissing(t, err)
}

func TestDirectoryFind_MissingModIsErrModNotFound(t *testing.T) {
	d := newTestDirectory(t)
	_, err := d.GetMod(context.Background(), "", "Removed")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	_, err = d.GetModFiles(context.Background(), &domain.Mod{ID: "Removed"})
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestDirectoryFind_AnUnreadableDirectoryIsNotErrModNotFound(t *testing.T) {
	root := t.TempDir()
	d, err := NewDirectory(SourceDefinition{ID: "my-mods", Name: "My Mods", Type: TypeDirectory, Directory: &DirectoryConfig{Path: root}})
	require.NoError(t, err)
	require.NoError(t, os.Remove(root))

	_, err = d.GetMod(context.Background(), "", "A")
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrModNotFound)
}

// --- manifest ----------------------------------------------------------

func TestManifestCheckUpdates_ModGoneFromTheManifestIsModNotFoundError(t *testing.T) {
	m := newLocalManifest(t) // cool-mod is at 1.2.0
	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "removed", SourceID: "my-repo", Name: "Removed", Version: "1.0"}},
		{Mod: domain.Mod{ID: "cool-mod", SourceID: "my-repo", Name: "Cool Mod", Version: "1.0.0"}},
		{Mod: domain.Mod{ID: "other-mod", SourceID: "my-repo", Name: "Other Mod", Version: "0.9.0"}},
	}

	updates, err := m.CheckUpdates(context.Background(), installed)
	require.Len(t, updates, 1)
	assert.Equal(t, "cool-mod", updates[0].InstalledMod.ID)

	ids, rest := missingIDs(err)
	assert.Equal(t, []string{"removed"}, ids)
	assert.NoError(t, rest)
}

func TestManifestCheckUpdates_AFailedFetchMarksNothingMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer srv.Close()
	def := manifestDef(srv.URL + "/mods.yaml")
	def.AllowHTTP = true
	m, err := NewManifest(def)
	require.NoError(t, err)

	updates, err := m.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "cool-mod", SourceID: "my-repo", Version: "1.0.0"}},
	})
	require.Error(t, err, "a failed fetch is a failed check")
	assert.Empty(t, updates)
	assertNoneMissing(t, err)
}

func TestManifestCheckUpdates_AnUnparseableManifestMarksNothingMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nmods: [ {id: \n"), 0o644))
	m, err := NewManifest(manifestDef(path))
	require.NoError(t, err)

	_, err = m.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "cool-mod", SourceID: "my-repo", Version: "1.0.0"}},
	})
	require.Error(t, err)
	assertNoneMissing(t, err)
}

func TestManifestFindMod_MissingModIsErrModNotFound(t *testing.T) {
	m := newLocalManifest(t)
	_, err := m.GetMod(context.Background(), "", "removed")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	_, err = m.GetModFiles(context.Background(), &domain.Mod{ID: "removed"})
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

// --- an empty listing --------------------------------------------------
//
// A listing that was read but holds NOTHING is far more likely an unmounted
// mount point, an emptied sync folder or a truncated manifest than every
// installed mod having been withdrawn at once - so it says nothing about any
// mod, the same as a listing that could not be read.

func TestDirectoryCheckUpdates_AnEmptyDirectoryMarksNothingMissing(t *testing.T) {
	root := t.TempDir() // exists, readable, empty
	d, err := NewDirectory(SourceDefinition{ID: "my-mods", Name: "My Mods", Type: TypeDirectory, Directory: &DirectoryConfig{Path: root}})
	require.NoError(t, err)

	updates, err := d.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "A", SourceID: "my-mods", Version: "1.0"}},
		{Mod: domain.Mod{ID: "B", SourceID: "my-mods", Version: "1.0"}},
	})
	assert.Empty(t, updates)
	assertNoneMissing(t, err)
}

func TestManifestCheckUpdates_AnEmptyManifestMarksNothingMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mods.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nmods: []\n"), 0o644))
	m, err := NewManifest(manifestDef(path))
	require.NoError(t, err)

	updates, err := m.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "cool-mod", SourceID: "my-repo", Version: "1.0.0"}},
	})
	assert.Empty(t, updates)
	assertNoneMissing(t, err)
}
