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

// TestDoInstall_VisibleUnderLMMGameIDWhenSourceMappingDiffers reproduces the
// install-orphan bug (final-review finding 1): when a game's `sources:`
// mapping points a source at a value different from the lmm game ID (the
// README-documented pattern for manifest game_ids filtering, e.g.
// `my-repo: skyrim` under game `testgame`), the search/GetMod path correctly
// stamps the source-mapped value onto mod.GameID for querying the source —
// but that same value must NOT leak into the persisted InstalledMod. Before
// the fix, doInstall saved *mod as-is, so the row landed under GameID
// "skyrim" while every other DB read (list/update/uninstall) queries by the
// lmm game ID "testgame", orphaning the install.
func TestDoInstall_VisibleUnderLMMGameIDWhenSourceMappingDiffers(t *testing.T) {
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

	// Drive the same package-level flags runInstall would set, skipping
	// interactive prompts so the real doInstall save path executes.
	origInstallSource, origInstallProfile, origInstallModID := installSource, installProfile, installModID
	origInstallFileID, origInstallYes, origInstallShowArchived := installFileID, installYes, installShowArchived
	origSkipVerify, origInstallForce, origInstallNoDeps := skipVerify, installForce, installNoDeps
	origNoHooks := noHooks
	t.Cleanup(func() {
		installSource, installProfile, installModID = origInstallSource, origInstallProfile, origInstallModID
		installFileID, installYes, installShowArchived = origInstallFileID, origInstallYes, origInstallShowArchived
		skipVerify, installForce, installNoDeps = origSkipVerify, origInstallForce, origInstallNoDeps
		noHooks = origNoHooks
	})

	installSource = ""
	installProfile = ""
	installModID = "cool-mod"
	installFileID = ""
	installYes = true
	installShowArchived = false
	skipVerify = true
	installForce = true
	installNoDeps = true
	noHooks = true

	require.NoError(t, doInstall(context.Background(), svc, game, nil))

	installed, err := svc.GetInstalledMods(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, installed, 1, "installed mod must be visible under the lmm game ID after install")
	assert.Equal(t, "cool-mod", installed[0].ID)
	assert.Equal(t, "testgame", installed[0].GameID, "persisted GameID must be normalized to the lmm game, not the source-mapped value")

	orphaned, err := svc.GetInstalledMods("skyrim", "default")
	require.NoError(t, err)
	assert.Empty(t, orphaned, "installed mod must not be filed under the source-mapped game ID")
}

// TestBatchInstallMods_VisibleUnderLMMGameIDWhenSourceMappingDiffers covers
// the second install-orphan save site (batchInstallMods, used by both
// multi-select search installs and dependency-resolved installs). Same bug,
// same fix: the persisted GameID must be normalized to the lmm game.
func TestBatchInstallMods_VisibleUnderLMMGameIDWhenSourceMappingDiffers(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	manifest := fmt.Sprintf(`
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
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
		AllowHTTP: true,
		Manifest:  &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{
		ID:         "testgame",
		Name:       "Test Game",
		ModPath:    t.TempDir(),
		DeployMode: domain.DeployCopy,
		SourceIDs:  map[string]string{"e2e-repo": "skyrim"},
	}
	require.NoError(t, svc.AddGame(game))

	origInstallShowArchived, origSkipVerify, origInstallForce := installShowArchived, skipVerify, installForce
	origNoHooks := noHooks
	t.Cleanup(func() {
		installShowArchived, skipVerify, installForce = origInstallShowArchived, origSkipVerify, origInstallForce
		noHooks = origNoHooks
	})
	installShowArchived = false
	skipVerify = true
	installForce = true
	noHooks = true

	ctx := context.Background()
	mod, err := svc.GetMod(ctx, "e2e-repo", game.ID, "cool-mod")
	require.NoError(t, err)
	require.Equal(t, "skyrim", mod.GameID, "GetMod stamps the source-mapped GameID for querying the source")

	require.NoError(t, batchInstallMods(ctx, svc, game, []*domain.Mod{mod}, "default"))

	installed, err := svc.GetInstalledMods(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, installed, 1, "installed mod must be visible under the lmm game ID after batch install")
	assert.Equal(t, "cool-mod", installed[0].ID)
	assert.Equal(t, "testgame", installed[0].GameID)

	orphaned, err := svc.GetInstalledMods("skyrim", "default")
	require.NoError(t, err)
	assert.Empty(t, orphaned, "installed mod must not be filed under the source-mapped game ID")
}
