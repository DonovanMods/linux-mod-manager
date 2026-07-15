package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoProfileApply_VisibleUnderLMMGameIDWhenSourceMappingDiffers reproduces
// the same install-orphan bug (re-review finding: same class as commit
// 4b3154e) for the profile.go "install missing mods" save site in
// doProfileApply. When a game's `sources:` mapping points a source at a
// value different from the lmm game ID (the README-documented pattern for
// manifest game_ids filtering, e.g. `my-repo: skyrim` under game
// `testgame`), Service.GetMod correctly stamps the source-mapped value onto
// mod.GameID for querying the source — but that value must NOT leak into
// the persisted InstalledMod. Before the fix, doProfileApply saved *mod
// as-is, so the row landed under GameID "skyrim" while every other DB read
// (list/update/uninstall) queries by the lmm game ID "testgame", orphaning
// the install even though it was deployed to disk.
func TestDoProfileApply_VisibleUnderLMMGameIDWhenSourceMappingDiffers(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    summary: Makes things cooler
    game_ids: [skyrim]
    files:
      - id: main
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        url: %s/files/cool-mod-1.2.0.zip
        primary: true
`, srv.URL)
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(manifest)) })
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("archive bytes")) })

	src, err := custom.New(custom.SourceDefinition{
		ID:        "e2e-repo",
		Name:      "E2E Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: true, // httptest serves plain http
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	// Game "testgame" maps the manifest source to a DIFFERENT, non-empty
	// value ("skyrim") — the README-documented pattern for game_ids filtering.
	game := &domain.Game{
		ID:         "testgame",
		Name:       "Test Game",
		ModPath:    t.TempDir(),
		DeployMode: domain.DeployCopy,
		SourceIDs:  map[string]string{"e2e-repo": "skyrim"},
	}
	require.NoError(t, svc.AddGame(game))

	pm := getProfileManager(svc)
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{
		SourceID: "e2e-repo",
		ModID:    "cool-mod",
		Version:  "1.2.0",
	}))

	// Auto-confirm the "Proceed?" prompt so the real save path executes
	// without needing to fake stdin.
	origProfileApplyYes := profileApplyYes
	t.Cleanup(func() { profileApplyYes = origProfileApplyYes })
	profileApplyYes = true

	require.NoError(t, doProfileApply(context.Background(), svc, game, []string{"default"}))

	installed, err := svc.GetInstalledMods(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, installed, 1, "installed mod must be visible under the lmm game ID after profile apply")
	assert.Equal(t, "cool-mod", installed[0].ID)
	assert.Equal(t, "testgame", installed[0].GameID, "persisted GameID must be normalized to the lmm game, not the source-mapped value")

	orphaned, err := svc.GetInstalledMods("skyrim", "default")
	require.NoError(t, err)
	assert.Empty(t, orphaned, "installed mod must not be filed under the source-mapped game ID")
}
