package main

// #674: `lmm update` checks an .lmmc address's collection with a
// conditional GET, and prints the same collection line as a NexusMods
// collection's (#673).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func urlCollectionDoc(version string) string {
	return `lmm_collection: 1
id: shared-1
version: ` + version + `
name: shared
title: Shared Setup
game:
  steam_app_id: "1091500"
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1.0", disabled: true}
`
}

// urlRevisionServer serves the document, with an ETag per version and a
// 304 for a request presenting the current one.
type urlRevisionServer struct {
	*httptest.Server
	version  atomic.Value // string
	requests atomic.Int32
}

func newURLRevisionServer(t *testing.T) *urlRevisionServer {
	t.Helper()
	s := &urlRevisionServer{}
	s.version.Store("1")
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		v := s.version.Load().(string)
		etag := `"v` + v + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(urlCollectionDoc(v)))
	}))
	t.Cleanup(s.Close)
	return s
}

// importURLCollection imports srv's document as "shared", with one local
// mod installed so `lmm update` has something to check.
func importURLCollection(t *testing.T, srv *urlRevisionServer) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := urlImportService(t, srv.Server)
	oldNoInstall := profileImportNoInstall
	profileImportNoInstall = true
	t.Cleanup(func() { profileImportNoInstall = oldNoInstall })
	_, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionURLImport(context.Background(), svc, srv.URL+"/shared.yaml", "")
	})
	require.NoError(t, err)
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "own", SourceID: domain.SourceLocal, Name: "Own", Version: "1", GameID: game.ID},
		ProfileName: "shared", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	oldProfile := updateProfile
	updateProfile = "shared"
	t.Cleanup(func() { updateProfile = oldProfile })
	return svc, game
}

func TestDoUpdate_URLCollectionRevision(t *testing.T) {
	srv := newURLRevisionServer(t)
	svc, game := importURLCollection(t, srv)
	saved, err := svc.NewProfileManager().Get(context.Background(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, `"v1"`, saved.Collection.Origin.ETag, "the import recorded the address's ETag")

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Collection Shared Setup: up to date (revision 1).", "a 304")

	srv.version.Store("2")
	updateRefresh = true
	t.Cleanup(func() { updateRefresh = false })
	out, err = captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Collection Shared Setup: revision 2 available (installed 1) — 'lmm profile sync shared --collection' updates it.")
}

func TestJSONGolden_UpdateBulkURLCollectionRevision(t *testing.T) {
	srv := newURLRevisionServer(t)
	svc, game := importURLCollection(t, srv)
	srv.version.Store("2")
	withJSONOutput(t)
	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "update_bulk_url_collection_revision", out)
}

// #674 review: a document with no version is said as such, never
// "revision 0".
func TestDoUpdate_URLCollectionWithNoVersion(t *testing.T) {
	srv := newURLRevisionServer(t)
	srv.version.Store("0")
	svc, game := importURLCollection(t, srv)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Collection Shared Setup: checked, but its document carries no version, so lmm can't tell whether it's newer — 'lmm profile sync shared --collection' shows what changed.")
	assert.NotContains(t, out, "revision 0")
}
