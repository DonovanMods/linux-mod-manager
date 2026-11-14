package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedCollectionYAML is a version-1 document with a hook, as a gist
// would serve it.
const sharedCollectionYAML = `lmm_collection: 1
name: shared
author: friend
game:
  steam_app_id: "1091500"
hooks:
  install:
    after_all: /usr/bin/true
mods:
  - {source_id: nexusmods, mod_id: "1", version: "1.0"}
`

// urlImportService is a Service whose downloads trust srv's test
// certificate, with a game the document's Steam app id matches.
func urlImportService(t *testing.T, srv *httptest.Server) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: srv.Client()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	game := &domain.Game{ID: "cyber", Name: "Cyberpunk 2077", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"steamworkshop": "1091500"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

func TestDoCollectionURLImport_NeverImportsHooksAndShowsTheAddressRedacted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sharedCollectionYAML))
	}))
	t.Cleanup(srv.Close)
	svc, game := urlImportService(t, srv)

	oldNoInstall, oldHooks := profileImportNoInstall, profileImportWithHooks
	profileImportNoInstall, profileImportWithHooks = true, true
	t.Cleanup(func() { profileImportNoInstall, profileImportWithHooks = oldNoInstall, oldHooks })

	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doCollectionURLImport(context.Background(), svc, srv.URL+"/shared.yaml?token=s3cret", "")
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "Collection: shared")
	assert.Contains(t, stdout, "fetched from "+srv.URL+"/shared.yaml?…")
	assert.Contains(t, stdout, "They will not be imported: hooks are never imported from a URL.", "--with-hooks does not change that")
	assert.Contains(t, stdout, "1 hook(s) were not imported: hooks are never imported from a URL.")
	assert.NotContains(t, stdout, "s3cret")

	saved, err := svc.NewProfileManager().Get(context.Background(), game.ID, "shared")
	require.NoError(t, err)
	assert.Equal(t, domain.GameHooksExplicit{}, saved.HooksExplicit)
	assert.Equal(t, srv.URL+"/shared.yaml?token=s3cret", saved.Collection.Origin.ImportURL)
}

func TestDoCollectionURLImport_JSON(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sharedCollectionYAML))
	}))
	t.Cleanup(srv.Close)
	svc, _ := urlImportService(t, srv)
	oldNoInstall := profileImportNoInstall
	profileImportNoInstall = true
	t.Cleanup(func() { profileImportNoInstall = oldNoInstall })

	out := runJSONCommand(t, func() error {
		return doCollectionURLImport(context.Background(), svc, srv.URL+"/shared.yaml?token=s3cret", "")
	})
	var got core.CollectionImportResult
	decodeStrict(t, out, &got)
	require.NotNil(t, got.Collection)
	assert.Equal(t, core.CollectionFromURL, got.Collection.Input)
	assert.False(t, got.Collection.HooksImportable)
	assert.Equal(t, srv.URL+"/shared.yaml?…", got.Collection.Origin.ImportURL)
	assert.Equal(t, 1, got.Result.HooksWithheld)
	assert.NotContains(t, out, "s3cret")
}

func TestRunProfileImport_RefusesAPlainHTTPAddress(t *testing.T) {
	restore := setImportFlags(t, "", "")
	defer restore()
	profileImportCmd.SetContext(context.Background())
	err := runProfileImport(profileImportCmd, []string{"http://example.invalid/c.lmmc?token=s3cret"})
	require.Error(t, err)
	assert.True(t, core.IsCollectionURLInvalid(err), "%v", err)
	assert.Contains(t, err.Error(), "https only")
	assert.False(t, strings.Contains(err.Error(), "s3cret"))
}
