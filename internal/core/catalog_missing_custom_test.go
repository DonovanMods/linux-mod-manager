package core_test

// #541: the custom source types report a mod gone from their listing through
// #539's seam, so the update check lists it in CatalogMissing beside the
// other mods' updates - driven here through the real sources, not a fake.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// customGoneSource builds each custom source type holding "live" at 2.0
// and not holding "gone".
func customGoneSource(t *testing.T, typ string) source.ModSource {
	t.Helper()
	def := custom.SourceDefinition{ID: "mine", Name: "Mine", Type: typ}
	switch typ {
	case custom.TypeDirectory:
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "live"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "live", "ModInfo.xml"),
			[]byte(`<xml><Name value="live"/><Version value="2.0"/></xml>`), 0o644))
		def.Directory = &custom.DirectoryConfig{Path: root}
	case custom.TypeManifest:
		path := filepath.Join(t.TempDir(), "mods.yaml")
		require.NoError(t, os.WriteFile(path, []byte(`
version: 1
mods:
  - id: live
    name: Live Mod
    version: "2.0"
    files:
      - id: main
        filename: live.zip
        url: https://files.test/live.zip
`), 0o644))
		def.Manifest = &custom.ManifestConfig{URL: path}
	case custom.TypeAPI:
		mux := http.NewServeMux()
		mux.HandleFunc("/mods/live", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id": "live", "name": "Live Mod", "version": "2.0"}`))
		})
		mux.HandleFunc("/mods/gone", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		def.AllowHTTP = true
		def.API = &custom.APIConfig{
			BaseURL:   srv.URL,
			Endpoints: custom.APIEndpoints{GetMod: &custom.EndpointConfig{Path: "/mods/{mod_id}"}},
			Mappings:  custom.APIMappings{Mod: map[string]string{"id": "id", "name": "name", "version": "version"}},
		}
	}
	src, err := custom.New(def)
	require.NoError(t, err)
	return src
}

func TestCheckGameUpdateReport_CustomSourcesReportAGoneModInCatalogMissing(t *testing.T) {
	for _, typ := range []string{custom.TypeDirectory, custom.TypeManifest, custom.TypeAPI} {
		t.Run(typ, func(t *testing.T) {
			svc := newFlowsTestService(t)
			svc.RegisterSource(customGoneSource(t, typ))
			game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
			require.NoError(t, svc.SaveGame(context.Background(), game))
			for id, name := range map[string]string{"live": "Live Mod", "gone": "Old One"} {
				require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
					Mod:         domain.Mod{ID: id, SourceID: "mine", Name: name, Version: "1.0", GameID: game.ID},
					ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
				}))
				seedProfileWithMod(t, svc, game.ID, "default", "mine", id, "1.0")
			}

			report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
			require.NoError(t, err, "a gone mod is reported, not a failed check")
			assert.Empty(t, report.ErrorMessage)
			require.Len(t, report.Updates, 1)
			assert.Equal(t, "live", report.Updates[0].InstalledMod.ID)
			assert.Equal(t, []core.CatalogModRef{{SourceID: "mine", ModID: "gone", Name: "Old One"}}, report.CatalogMissing)
		})
	}
}
