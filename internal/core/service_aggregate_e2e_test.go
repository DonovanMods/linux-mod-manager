package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAggregateSources wires three real sources for aggregate-search e2e
// coverage (#50 acceptance criterion 1):
//   - "local-mods": a directory source with one on-disk mod ("CoolLocalMod").
//   - "manifest-repo": a manifest source backed by a local mods.yaml (mod
//     "cool-remote"). Local-path manifests are read directly, so this needs
//     no HTTP server.
//   - "dead-repo": a manifest source pointed at an unroutable https URL.
//     Construction is pure (custom.New performs no I/O); the fetch only
//     fails once a search actually runs, so it degrades to a warning instead
//     of failing at setup.
//
// The game maps all three sources with the empty-string value, mirroring the
// README-documented convention that "this source applies to any game" — and
// pinning the Phase 2/3 lesson that an empty mapping must not blank out the
// mod's GameID.
func setupAggregateSources(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	dirRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dirRoot, "CoolLocalMod"), 0755))
	dirSrc, err := custom.New(custom.SourceDefinition{
		ID:        "local-mods",
		Name:      "Local Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: dirRoot},
	})
	require.NoError(t, err)

	manifestPath := filepath.Join(t.TempDir(), "mods.yaml")
	manifest := `
version: 1
mods:
  - id: cool-remote
    name: Cool Remote
    version: 1.0.0
    summary: A remotely published cool mod
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0644))
	manifestSrc, err := custom.New(custom.SourceDefinition{
		ID:       "manifest-repo",
		Name:     "Manifest Repo",
		Type:     custom.TypeManifest,
		Manifest: &custom.ManifestConfig{URL: manifestPath},
	})
	require.NoError(t, err)

	deadSrc, err := custom.New(custom.SourceDefinition{
		ID:        "dead-repo",
		Name:      "Dead Repo",
		Type:      custom.TypeManifest,
		AllowHTTP: false,
		Manifest:  &custom.ManifestConfig{URL: "https://127.0.0.1:1/mods.yaml"},
	})
	require.NoError(t, err)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	svc.RegisterSource(dirSrc)
	svc.RegisterSource(manifestSrc)
	svc.RegisterSource(deadSrc)

	game := &domain.Game{
		ID:         "testgame",
		Name:       "Test Game",
		ModPath:    t.TempDir(),
		DeployMode: domain.DeployCopy,
		SourceIDs:  map[string]string{"local-mods": "", "manifest-repo": "", "dead-repo": ""},
	}
	require.NoError(t, svc.AddGame(game))

	return svc, game
}

// TestAggregateSearchEndToEnd pins #50 acceptance criterion 1 with real
// source implementations: a directory source and a local-file manifest
// source both surface labeled results in one aggregate search, while a
// dead remote manifest source degrades to a single warning instead of
// hiding the working sources' results.
func TestAggregateSearchEndToEnd(t *testing.T) {
	svc, game := setupAggregateSources(t)
	ctx := context.Background()

	res, err := svc.SearchAllSources(ctx, game.ID, "cool", "", nil, 0, 20)
	require.NoError(t, err, "dead source must not fail the aggregate")

	bySource := map[string][]string{}
	for _, m := range res.Mods {
		bySource[m.SourceID] = append(bySource[m.SourceID], m.ID)
	}
	assert.Contains(t, bySource, "local-mods")
	assert.Contains(t, bySource, "manifest-repo")
	assert.Contains(t, bySource["local-mods"], "CoolLocalMod")
	assert.Contains(t, bySource["manifest-repo"], "cool-remote")

	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "dead-repo", res.Warnings[0].SourceID)

	// Every merged mod carries the lmm game id (the Phase 2/3 lesson).
	for _, m := range res.Mods {
		assert.Equal(t, game.ID, m.GameID)
	}
}

// TestAggregateSearchEndToEndSingleSourceStillWorks guards against a
// regression where wiring the aggregate path broke single-source search:
// the same three-source setup, queried through SearchMods with one
// explicit source, must still return that source's results untouched by
// the other two.
func TestAggregateSearchEndToEndSingleSourceStillWorks(t *testing.T) {
	svc, game := setupAggregateSources(t)
	ctx := context.Background()

	res, err := svc.SearchMods(ctx, "manifest-repo", game.ID, "cool", "", nil, 0, 20)
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	assert.Equal(t, "manifest-repo", res.Mods[0].SourceID)
	assert.Equal(t, "cool-remote", res.Mods[0].ID)
	assert.Equal(t, game.ID, res.Mods[0].GameID)
}
