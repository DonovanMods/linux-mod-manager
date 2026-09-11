package core_test

// #412: a curated known-games entry's `adapter:` reaches games.yaml through
// BOTH doors into it - GameFromDetected (`lmm game detect`'s apply) and
// GameSpecFromDetected (`lmm game add --from-detected`, and POST
// /api/v1/games' from_steam_app_id prefill). The two disagreeing about the
// same curated game is the failure #416 already pinned for `loader:`, and
// the adapter is the same shape of fact: only a curated entry can carry
// one, because nothing on disk says what a game does with mod content.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// detectedIcarus is the shipped catalog's Icarus row as detection reports
// it: `deploy_mode: compile` AND an explicit `adapter: icarus`, which is
// what the entry now says rather than leaving the derivation to do it.
func detectedIcarus(install string) domain.DetectedGame {
	return domain.DetectedGame{
		SteamAppID: "1149460", Slug: "icarus", Name: "Icarus",
		InstallPath: install, ModPath: install + "/Icarus/Content/Paks/mods",
		DeployMode: "compile", Adapter: "icarus",
		Sources: map[string]string{"icarus": "icarus"}, Known: true,
	}
}

func TestGameFromDetected_CarriesTheAdapter(t *testing.T) {
	game, err := core.GameFromDetected(detectedIcarus(t.TempDir()))
	require.NoError(t, err)
	assert.Equal(t, "icarus", game.Adapter)
	assert.Equal(t, domain.DeployCompile, game.DeployMode,
		"the two keys coexist for 2.0 - the adapter does not replace deploy_mode here")
}

// A candidate with no curated adapter writes no `adapter:` key at all -
// which is every other game in the shipped catalog, and every UNKNOWN
// candidate, both of which read as generic-files.
func TestGameFromDetected_NoAdapterWithoutACuratedOne(t *testing.T) {
	game, err := core.GameFromDetected(detectedSkyrim(t.TempDir()))
	require.NoError(t, err)
	assert.Empty(t, game.Adapter)
}

func TestGameSpecFromDetected_CarriesTheAdapter(t *testing.T) {
	spec := core.GameSpecFromDetected(detectedIcarus(t.TempDir()), core.GameSpec{})
	assert.Equal(t, "icarus", spec.Adapter)
}

// An explicit --adapter wins over the curated one, the way every other
// prefilled field here behaves.
func TestGameSpecFromDetected_ExplicitAdapterWins(t *testing.T) {
	spec := core.GameSpecFromDetected(detectedIcarus(t.TempDir()), core.GameSpec{Adapter: "generic-files"})
	assert.Equal(t, "generic-files", spec.Adapter)
}
