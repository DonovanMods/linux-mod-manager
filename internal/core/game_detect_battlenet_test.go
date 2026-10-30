package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wowRoot makes a World of Warcraft root with one folder per flavor, the
// layout #545's detector reports a game per flavor of.
func wowRoot(t *testing.T, flavors ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "drive_c", "Program Files (x86)", "World of Warcraft")
	for _, f := range flavors {
		require.NoError(t, os.MkdirAll(filepath.Join(root, f, "Interface", "AddOns"), 0o755))
	}
	return root
}

// battleNetRow is a detected WoW flavor, as the Battle.net detector reports
// one: the install path is the flavor's own folder.
func battleNetRow(root, product, slug, flavor string) domain.DetectedGame {
	install := filepath.Join(root, flavor)
	return domain.DetectedGame{
		Detector: domain.DetectorBattleNet, DetectorID: product, Slug: slug, Name: "World of Warcraft " + flavor,
		InstallPath: install, ModPath: filepath.Join(install, "Interface", "AddOns"),
		Sources: map[string]string{"curseforge": "1"}, Known: true,
		CurseForge: &domain.GameCurseForge{GameVersionTypes: []int{79434}},
		Launch: &domain.GameLaunch{Via: domain.LaunchViaBattleNet, BattleNet: &domain.BattleNetLaunch{
			Prefix: "/home/user/Games/battlenet", Runner: domain.BattleNetRunnerUmu, Proton: "GE-Proton",
		}},
	}
}

// #545: the catalog's CurseForge flavor (#544) and the launch prefill reach
// the game a detect selection writes - each a copy the game owns.
func TestGameFromDetected_CarriesTheFlavorAndTheLaunch(t *testing.T) {
	row := battleNetRow(wowRoot(t, "_classic_"), "wow_classic", "wow-classic", "_classic_")

	game, err := core.GameFromDetected(row)
	require.NoError(t, err)
	assert.Equal(t, []int{79434}, game.CurseForgeVersionTypes())
	require.NotNil(t, game.Launch)
	assert.Equal(t, row.Launch.BattleNet, game.Launch.BattleNet)

	game.CurseForge.GameVersionTypes[0] = 1
	game.Launch.BattleNet.Prefix = "/elsewhere"
	assert.Equal(t, 79434, row.CurseForge.GameVersionTypes[0], "the game owns its own block")
	assert.Equal(t, "/home/user/Games/battlenet", row.Launch.BattleNet.Prefix, "the game owns its own launch")
}

// The from-detected add carries them too, and writes them to games.yaml.
func TestAddGameFromDetected_WritesTheFlavorAndTheLaunch(t *testing.T) {
	svc := newGameAddService(t)
	row := battleNetRow(wowRoot(t, "_retail_"), "wow", "wow", "_retail_")
	row.Launch.BattleNet.Product = "WoW"

	spec, err := svc.PrefillGameSpecFromDetected(row, core.GameSpec{})
	require.NoError(t, err)
	_, err = svc.AddGame(context.Background(), spec)
	require.NoError(t, err)

	saved, err := svc.LoadGamesFromDisk()
	require.NoError(t, err)
	require.Contains(t, saved, "wow")
	assert.Equal(t, []int{79434}, saved["wow"].CurseForgeVersionTypes())
	require.NotNil(t, saved["wow"].Launch)
	assert.Equal(t, row.Launch.BattleNet, saved["wow"].Launch.BattleNet)
}

// #545: the owner's case. A WoW flavor already configured - under any id,
// with the flavor folder as its install path or with the WoW root as its
// install path and the flavor's AddOns folder as its mod path - is that
// game, so the listing marks it configured instead of offering it again.
// Another flavor of the same root is NOT that game.
func TestGameDetectListing_AConfiguredWoWFlavorIsRecognised(t *testing.T) {
	tests := []struct {
		name             string
		install, modPath func(root string) string
	}{
		{"flavor folder as the install path",
			func(root string) string { return filepath.Join(root, "_forever_") },
			func(root string) string { return filepath.Join(root, "_forever_", "Interface", "AddOns") }},
		{"WoW root as the install path",
			func(root string) string { return root },
			func(root string) string { return filepath.Join(root, "_forever_", "Interface", "AddOns") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newGameAddService(t)
			root := wowRoot(t, "_forever_", "_classic_")
			_, err := svc.AddGame(context.Background(), core.GameSpec{
				ID: "wow", Name: "World of Warcraft - Forever",
				InstallPath: tc.install(root), ModPath: tc.modPath(root),
				SourceID: "curseforge", Identifier: "1",
			})
			require.NoError(t, err)

			listing, err := svc.GameDetectListing(context.Background(), []domain.DetectedGame{
				battleNetRow(root, "wowf", "wow-forever", "_forever_"),
				battleNetRow(root, "wow_classic", "wow-classic", "_classic_"),
			}, nil, core.GameDetectListingOptions{})
			require.NoError(t, err)

			require.Len(t, listing.Games, 2)
			assert.True(t, listing.Games[0].AlreadyConfigured, "games.yaml's wow is this flavor")
			assert.False(t, listing.Games[1].AlreadyConfigured, "another flavor of the same root is another game")

			spec, err := svc.PrefillGameSpecFromDetected(listing.Games[0].DetectedGame, core.GameSpec{})
			require.NoError(t, err)
			assert.Equal(t, "wow", spec.ID, "an add resolves to the configured id, so AddGame refuses the duplicate")
		})
	}
}
