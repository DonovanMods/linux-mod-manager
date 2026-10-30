package core_test

import (
	"context"
	"errors"
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
	row := battleNetRow(wowRoot(t, "_retail_"), "wow", "wow-retail", "_retail_")
	row.Launch.BattleNet.Product = "WoW"

	spec, err := svc.PrefillGameSpecFromDetected(row, core.GameSpec{})
	require.NoError(t, err)
	_, err = svc.AddGame(context.Background(), spec)
	require.NoError(t, err)

	saved, err := svc.LoadGamesFromDisk()
	require.NoError(t, err)
	require.Contains(t, saved, "wow-retail")
	assert.Equal(t, []int{79434}, saved["wow-retail"].CurseForgeVersionTypes())
	require.NotNil(t, saved["wow-retail"].Launch)
	assert.Equal(t, row.Launch.BattleNet, saved["wow-retail"].Launch.BattleNet)
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

// The mod-path match is for a flavor folder under a game configured at the
// WoW ROOT, and nothing wider: a game elsewhere whose mod path happens to
// be the same folder is not this candidate, and a selection must not
// "repair" it into one.
func TestConfiguredGameFor_ModPathMatchOnlyBelowTheConfiguredInstall(t *testing.T) {
	root := wowRoot(t, "_classic_")
	shared := filepath.Join(root, "_classic_", "Interface", "AddOns")
	elsewhere := t.TempDir()
	existing := map[string]*domain.Game{
		"other": {ID: "other", InstallPath: elsewhere, ModPath: shared},
	}
	row := battleNetRow(root, "wow_classic", "wow-classic", "_classic_")
	assert.Nil(t, core.ConfiguredGameFor(existing, row), "an unrelated install sharing a mod folder is another game")

	existing["wow"] = &domain.Game{ID: "wow", InstallPath: root, ModPath: shared}
	got := core.ConfiguredGameFor(existing, row)
	require.NotNil(t, got)
	assert.Equal(t, "wow", got.ID)
}

// ownersWoW is the owner's games.yaml: World of Warcraft - Forever added by
// hand, before detection, under the generic id "wow", at the _forever_
// flavor folder.
func ownersWoW(t *testing.T, svc *core.Service, root string) {
	t.Helper()
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		ID: "wow", Name: "World of Warcraft - Forever",
		InstallPath: filepath.Join(root, "_forever_"), ModPath: filepath.Join(root, "_forever_", "Interface", "AddOns"),
		SourceID: "curseforge", Identifier: "1",
	})
	require.NoError(t, err)
}

// #545 review: the owner's exact setup, with retail installed beside
// Forever. The path decides which row is the configured game, never the
// slug - so even a retail row slugged "wow" (the catalog's slug before it
// became wow-retail, or a user's override) is not the owner's "wow": that
// game is Forever, and selecting retail must never move its paths.
func TestConfiguredGameFor_OwnersWoWIsForeverWhateverRetailIsCalled(t *testing.T) {
	for _, retailSlug := range []string{"wow-retail", "wow"} {
		t.Run(retailSlug, func(t *testing.T) {
			svc := newGameAddService(t)
			root := wowRoot(t, "_retail_", "_forever_")
			ownersWoW(t, svc, root)
			retail := battleNetRow(root, "wow", retailSlug, "_retail_")
			forever := battleNetRow(root, "wowf", "wow-forever", "_forever_")

			listing, err := svc.GameDetectListing(context.Background(), []domain.DetectedGame{retail, forever}, nil, core.GameDetectListingOptions{})
			require.NoError(t, err)
			require.Len(t, listing.Games, 2)
			assert.False(t, listing.Games[0].AlreadyConfigured, "retail is a new game, not the owner's wow")
			assert.True(t, listing.Games[1].AlreadyConfigured, "the owner's wow is Forever")

			existing, err := svc.LoadGamesFromDisk()
			require.NoError(t, err)
			assert.Nil(t, core.ConfiguredGameFor(existing, retail))
			got := core.ConfiguredGameFor(existing, forever)
			require.NotNil(t, got)
			assert.Equal(t, "wow", got.ID)

			_, _, err = svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{retail, forever})
			if retailSlug == "wow" {
				// Retail cannot be added under an id Forever already has: it
				// is refused, never written over Forever.
				require.Error(t, err)
				assert.True(t, errors.Is(err, core.ErrGameExists), err)
			} else {
				require.NoError(t, err)
			}

			saved, err := svc.LoadGamesFromDisk()
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(root, "_forever_"), saved["wow"].InstallPath, "nothing ever moves wow")
			assert.Equal(t, filepath.Join(root, "_forever_", "Interface", "AddOns"), saved["wow"].ModPath)
			if retailSlug == "wow-retail" {
				require.Contains(t, saved, "wow-retail", "retail is its own new game")
				assert.Equal(t, filepath.Join(root, "_retail_"), saved["wow-retail"].InstallPath)
				assert.NotContains(t, saved, "wow-forever", "Forever repaired the owner's wow rather than adding a second game")
			}
		})
	}
}

// Path matches win over slug matches for every detector: a row whose slug
// is one game's id but whose install path is ANOTHER game's is that other
// game - the directory is the identity.
func TestConfiguredGameFor_APathMatchWinsOverASlugMatch(t *testing.T) {
	here, there := t.TempDir(), t.TempDir()
	existing := map[string]*domain.Game{
		"skyrim":       {ID: "skyrim", InstallPath: there, ModPath: there},
		"skyrim-steam": {ID: "skyrim-steam", InstallPath: here, ModPath: here},
	}
	row := domain.DetectedGame{Detector: domain.DetectorSteam, DetectorID: "1", Slug: "skyrim", InstallPath: here}
	got := core.ConfiguredGameFor(existing, row)
	require.NotNil(t, got)
	assert.Equal(t, "skyrim-steam", got.ID)

	// A Steam row matching nothing by path still matches by slug - the
	// repair of a game whose library moved.
	delete(existing, "skyrim-steam")
	got = core.ConfiguredGameFor(existing, row)
	require.NotNil(t, got)
	assert.Equal(t, "skyrim", got.ID)

	// A Battle.net row never does: its flavors share one root, so a slug
	// is no evidence of which flavor a configured game is.
	bnet := battleNetRow(wowRoot(t, "_classic_"), "wow_classic", "skyrim", "_classic_")
	assert.Nil(t, core.ConfiguredGameFor(existing, bnet))
}

// #545 review: a repair carries the catalog's CurseForge flavor and the
// launch prefill to a configured game that has neither...
func TestApplyDetectSelection_RepairGainsTheFlavorAndTheLaunch(t *testing.T) {
	svc := newGameAddService(t)
	root := wowRoot(t, "_classic_")
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		ID: "classic", Name: "Classic", InstallPath: filepath.Join(root, "_classic_"),
		ModPath: filepath.Join(root, "_classic_", "Interface", "AddOns"), SourceID: "curseforge", Identifier: "1",
	})
	require.NoError(t, err)
	row := battleNetRow(root, "wow_classic", "wow-classic", "_classic_")

	_, _, err = svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{row})
	require.NoError(t, err)
	saved, err := svc.LoadGamesFromDisk()
	require.NoError(t, err)
	require.NotContains(t, saved, "wow-classic")
	assert.Equal(t, []int{79434}, saved["classic"].CurseForgeVersionTypes())
	require.NotNil(t, saved["classic"].Launch)
	assert.Equal(t, row.Launch.BattleNet, saved["classic"].Launch.BattleNet)
}

// ...and never overwrites the user's own: a launch block or CurseForge
// setting the configured game already has is theirs.
func TestApplyDetectSelection_RepairKeepsTheUsersFlavorAndLaunch(t *testing.T) {
	svc := newGameAddService(t)
	root := wowRoot(t, "_classic_")
	install := filepath.Join(root, "_classic_")
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		ID: "classic", Name: "Classic", InstallPath: install,
		ModPath: filepath.Join(install, "Interface", "AddOns"), SourceID: "curseforge", Identifier: "1",
		CurseForge: &domain.GameCurseForge{GameVersionTypes: []int{67408}},
		Launch:     &domain.GameLaunch{Command: []string{"my-wow-launcher"}},
	})
	require.NoError(t, err)

	_, _, err = svc.ApplyDetectSelection(context.Background(), []domain.DetectedGame{battleNetRow(root, "wow_classic", "wow-classic", "_classic_")})
	require.NoError(t, err)
	saved, err := svc.LoadGamesFromDisk()
	require.NoError(t, err)
	assert.Equal(t, []int{67408}, saved["classic"].CurseForgeVersionTypes())
	require.NotNil(t, saved["classic"].Launch)
	assert.Equal(t, []string{"my-wow-launcher"}, saved["classic"].Launch.Command)
	assert.Nil(t, saved["classic"].Launch.BattleNet)
}
