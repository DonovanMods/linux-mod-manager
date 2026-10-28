package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeGamesYAML drops a literal games.yaml into a fresh config dir.
func writeGamesYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(body), 0o644))
	return dir
}

func TestLoadGamesAdapterKey(t *testing.T) {
	t.Run("an explicit adapter loads verbatim", func(t *testing.T) {
		dir := writeGamesYAML(t, `games:
  lethal-company:
    name: Lethal Company
    install_path: /games/lethal-company
    mod_path: /games/lethal-company
    adapter: bepinex
`)
		games, err := config.LoadGames(dir)
		require.NoError(t, err)
		assert.Equal(t, "bepinex", games["lethal-company"].Adapter)
	})

	t.Run("an absent adapter is empty, which reads as the generic identity", func(t *testing.T) {
		dir := writeGamesYAML(t, `games:
  skyrim-se:
    name: Skyrim SE
    install_path: /games/skyrim-se
    mod_path: /games/skyrim-se/Data
`)
		games, err := config.LoadGames(dir)
		require.NoError(t, err)
		assert.Empty(t, games["skyrim-se"].Adapter)
	})

	t.Run("deploy_mode compile does NOT rewrite the field", func(t *testing.T) {
		// The compile => icarus derivation is core's, not this layer's:
		// this file is what lmm writes back, and a derived value landing
		// here would rewrite a games.yaml the user never edited (design
		// §2, "nothing is rewritten on save").
		dir := writeGamesYAML(t, `games:
  icarus:
    name: Icarus
    install_path: /games/icarus
    mod_path: /games/icarus/Mods
    deploy_mode: compile
`)
		games, err := config.LoadGames(dir)
		require.NoError(t, err)
		assert.Equal(t, domain.DeployCompile, games["icarus"].DeployMode)
		assert.Empty(t, games["icarus"].Adapter, "the derivation belongs to core's resolver, not the config layer")
	})

	t.Run("a syntactically invalid adapter name fails loud", func(t *testing.T) {
		dir := writeGamesYAML(t, `games:
  broken:
    name: Broken
    install_path: /games/broken
    mod_path: /games/broken
    adapter: "Not A Slug"
`)
		_, err := config.LoadGames(dir)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidAdapter)
		assert.Contains(t, err.Error(), "broken")
	})

	t.Run("an unknown-but-well-formed name is NOT this layer's problem", func(t *testing.T) {
		// Existence is core's question - it owns the registry - so a
		// games.yaml naming an adapter a future build ships still LOADS
		// here and fails later with the registered set in front of the
		// user.
		dir := writeGamesYAML(t, `games:
  someday:
    name: Someday
    install_path: /games/someday
    mod_path: /games/someday
    adapter: melonloader
`)
		games, err := config.LoadGames(dir)
		require.NoError(t, err)
		assert.Equal(t, "melonloader", games["someday"].Adapter)
	})
}

func TestSaveGamesAdapterRoundTrip(t *testing.T) {
	// §6.4's byte-identity proof: a pre-#353 games.yaml - including one
	// with deploy_mode: compile - loads and saves back unchanged, and a
	// game that DOES carry an adapter keeps it.
	const pre353 = `games:
    icarus:
        name: Icarus
        install_path: /games/icarus
        mod_path: /games/icarus/Mods
        sources:
            icarus: icarus
        deploy_mode: compile
`
	dir := writeGamesYAML(t, pre353)
	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	require.NoError(t, config.SaveGame(dir, games["icarus"]))

	after, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
	require.NoError(t, err)
	assert.Equal(t, pre353, string(after), "a games.yaml the user did not edit must round-trip byte-identically")

	t.Run("a configured adapter is written back", func(t *testing.T) {
		g := games["icarus"]
		g.Adapter = "icarus"
		require.NoError(t, config.SaveGame(dir, g))

		reloaded, err := config.LoadGames(dir)
		require.NoError(t, err)
		assert.Equal(t, "icarus", reloaded["icarus"].Adapter)

		body, err := os.ReadFile(filepath.Join(dir, "games.yaml"))
		require.NoError(t, err)
		assert.Contains(t, string(body), "adapter: icarus")
	})
}
