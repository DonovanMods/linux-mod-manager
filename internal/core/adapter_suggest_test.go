package core_test

// #606: a game already in games.yaml is never rewritten to `adapter:
// bethesda`, but verify and the game page SUGGEST it when the curated Steam
// catalog names an adapter for the game's Steam app id and the game uses
// the identity by default. The catalog lookup is the composition root's
// (core cannot import internal/source/steam), registered with
// SetCatalogAdapters.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// suggestFixture is a Service with the bethesda adapter, a catalog that
// knows Skyrim SE's app id, and a Steam library in which one install path
// is that app.
func suggestFixture(t *testing.T) (*core.Service, string) {
	t.Helper()
	svc := newFlowsTestService(t)
	svc.RegisterAdapter(bethesda.New())
	install := t.TempDir()
	svc.SetLauncher(core.NewLauncher(func(path string) (core.SteamApp, bool) {
		if path == install {
			return core.SteamApp{AppID: "489830"}, true
		}
		return core.SteamApp{}, false
	}))
	svc.SetKnownGames(testKnownGames)
	return svc, install
}

// testKnownGames is a three-entry catalog: a game with an adapter, one whose
// adapter this lmm does not register, and one with none.
func testKnownGames() []core.KnownGame {
	return []core.KnownGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", SteamAppID: "489830", Adapter: "bethesda"},
		{Slug: "future-game", Name: "Future Game", SteamAppID: "999", Adapter: "not-registered"},
		{Slug: "fallout3", Name: "Fallout 3", SteamAppID: "22380"},
	}
}

func TestSuggestedAdapter(t *testing.T) {
	svc, install := suggestFixture(t)
	other := t.TempDir()
	for name, tt := range map[string]struct {
		game *domain.Game
		want string
	}{
		"a Steam-detected Skyrim SE with no adapter key": {
			&domain.Game{ID: "skyrim", InstallPath: install, ModPath: filepath.Join(install, "Data")}, "bethesda"},
		"an app id stated in games.yaml": {
			&domain.Game{ID: "skyrim", InstallPath: other, Launch: &domain.GameLaunch{SteamAppID: "489830"}}, "bethesda"},
		"already bethesda": {
			&domain.Game{ID: "skyrim", InstallPath: install, Adapter: "bethesda"}, ""},
		"an explicit generic-files is the user's choice": {
			&domain.Game{ID: "skyrim", InstallPath: install, Adapter: "generic-files"}, ""},
		"an app id the catalog has no adapter for": {
			&domain.Game{ID: "other", InstallPath: other, Launch: &domain.GameLaunch{SteamAppID: "22380"}}, ""},
		"a catalog adapter this lmm does not register": {
			&domain.Game{ID: "other", InstallPath: other, Launch: &domain.GameLaunch{SteamAppID: "999"}}, ""},
		"not a Steam game at all": {
			&domain.Game{ID: "other", InstallPath: other}, ""},
		// #606 owner request: `catalog:` says which known game an install
		// is when nothing else can - a GOG copy under an id of its own.
		"a catalog entry with an adapter": {
			&domain.Game{ID: "gog-skyrim", InstallPath: other, Catalog: "skyrim-se"}, "bethesda"},
		"a catalog entry with no adapter": {
			&domain.Game{ID: "gog-fo3", InstallPath: other, Catalog: "fallout3"}, ""},
		"the catalog entry wins over the app id": {
			&domain.Game{ID: "x", InstallPath: other, Catalog: "fallout3", Launch: &domain.GameLaunch{SteamAppID: "489830"}}, ""},
		"an unknown catalog slug falls back to the app id": {
			&domain.Game{ID: "x", InstallPath: other, Catalog: "typo", Launch: &domain.GameLaunch{SteamAppID: "489830"}}, "bethesda"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, svc.SuggestedAdapter(tt.game))
		})
	}
}

// Without a catalog registered there is nothing to suggest from.
func TestSuggestedAdapter_NoCatalog(t *testing.T) {
	svc := newFlowsTestService(t)
	svc.RegisterAdapter(bethesda.New())
	game := &domain.Game{ID: "skyrim", InstallPath: t.TempDir(), Launch: &domain.GameLaunch{SteamAppID: "489830"}}
	assert.Empty(t, svc.SuggestedAdapter(game))
}

// The game page reads it from GameStatus; a game with nothing to suggest
// carries no key at all (omitempty), so every existing document is
// unchanged.
func TestGameStatus_CarriesTheSuggestedAdapter(t *testing.T) {
	svc, install := suggestFixture(t)
	ctx := context.Background()
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", InstallPath: install, ModPath: filepath.Join(install, "Data")}
	require.NoError(t, svc.SaveGame(ctx, game))

	status, err := svc.GameStatus(ctx, game)
	require.NoError(t, err)
	assert.Equal(t, "bethesda", status.SuggestedAdapter)
}

// Verify reports the suggestion as a note: it counts towards neither
// tally, so it never changes `lmm verify`'s exit code, and it is never
// fixable - lmm does not rewrite a game's adapter.
func TestVerify_SuggestsTheCatalogAdapter(t *testing.T) {
	svc, install := suggestFixture(t)
	ctx := context.Background()
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", InstallPath: install, ModPath: filepath.Join(install, "Data")}
	require.NoError(t, svc.SaveGame(ctx, game))

	res, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, core.VerifyStatusAdapterSuggested)
	require.NotNil(t, f, "statuses: %v", findingStatuses(res.Result))
	assert.Equal(t, "generic-files", f.Recorded)
	assert.Equal(t, "bethesda", f.Effective)
	assert.Contains(t, f.Note, "bethesda")
	assert.False(t, f.Fixable)
	assert.Contains(t, f.FixableReason, "lmm game edit skyrim --adapter bethesda")
	assert.Zero(t, res.Result.Issues)
	assert.Zero(t, res.Result.Warnings)

	// Taking the suggestion silences it.
	game.Adapter = "bethesda"
	require.NoError(t, svc.SaveGame(ctx, game))
	res, err = svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, core.VerifyStatusAdapterSuggested))
}

// A stated app id answers without a library scan: launch.steam_app_id and a
// numeric steamworkshop mapping come first, and the scan - which reads every
// app manifest - is the last resort.
func TestSuggestedAdapter_StatedAppIDSkipsTheLibraryScan(t *testing.T) {
	svc := newFlowsTestService(t)
	svc.RegisterAdapter(bethesda.New())
	scans := 0
	svc.SetLauncher(core.NewLauncher(func(string) (core.SteamApp, bool) {
		scans++
		return core.SteamApp{}, false
	}))
	svc.SetKnownGames(testKnownGames)
	for _, g := range []*domain.Game{
		{ID: "a", InstallPath: t.TempDir(), Launch: &domain.GameLaunch{SteamAppID: "489830"}},
		{ID: "b", InstallPath: t.TempDir(), SourceIDs: map[string]string{"steamworkshop": "489830"}},
	} {
		assert.Equal(t, "bethesda", svc.SuggestedAdapter(g), g.ID)
	}
	assert.Zero(t, scans, "no library scan when games.yaml states the app id")

	assert.Empty(t, svc.SuggestedAdapter(&domain.Game{ID: "c", InstallPath: t.TempDir()}))
	assert.Equal(t, 1, scans, "the scan is the last resort")
}

// Review fix 1: switching an existing game's adapter leaves its installed
// mods in the layout they were ingested with, so every place that offers or
// makes the switch says so - in one sentence, core's.
func TestAdapterSwitchWarning(t *testing.T) {
	svc, install := suggestFixture(t)
	ctx := context.Background()
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", InstallPath: install, ModPath: filepath.Join(install, "Data")}
	require.NoError(t, svc.SaveGame(ctx, game))

	warning, err := svc.AdapterSwitchWarning(ctx, game, "bethesda")
	require.NoError(t, err)
	assert.Empty(t, warning, "nothing installed, nothing to re-lay out")

	seedInstalledMod(t, svc, game, "src", "a", "1.0", true, nil)
	seedInstalledMod(t, svc, game, "src", "b", "1.0", true, nil)

	warning, err = svc.AdapterSwitchWarning(ctx, game, "bethesda")
	require.NoError(t, err)
	assert.Contains(t, warning, "2 mod(s) already installed")
	assert.Contains(t, warning, "keep the layout they were installed with")
	assert.Contains(t, warning, "uninstall it")
	assert.Contains(t, warning, "move there at the next deploy, with no reinstall",
		"the bethesda adapter's game-root routing applies at deploy time (#629)")

	same, err := svc.AdapterSwitchWarning(ctx, game, "generic-files")
	require.NoError(t, err)
	assert.Empty(t, same, "no switch, no warning")

	status, err := svc.GameStatus(ctx, game)
	require.NoError(t, err)
	assert.Equal(t, "bethesda", status.SuggestedAdapter)
	assert.Equal(t, warning, status.SuggestedAdapterWarning, "the game page shows the same sentence")

	res, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, core.VerifyStatusAdapterSuggested)
	require.NotNil(t, f)
	assert.Contains(t, f.Note, warning, "the verify note says it too")
}

// KnownGames lists the registered catalog, by name; nil before the
// composition root registers one.
func TestKnownGames(t *testing.T) {
	svc := newFlowsTestService(t)
	assert.Nil(t, svc.KnownGames())
	svc.SetKnownGames(testKnownGames)
	got := svc.KnownGames()
	require.Len(t, got, 3)
	assert.Equal(t, []string{"fallout3", "future-game", "skyrim-se"}, []string{got[0].Slug, got[1].Slug, got[2].Slug})
}

// A hand-edited `catalog:` that names no entry is a verify note - counted
// in neither tally, naming the slug and how to see the valid ones - never
// a load failure (config.LoadGames reads it as written).
func TestVerify_UnknownCatalogSlugIsANote(t *testing.T) {
	svc, _ := suggestFixture(t)
	ctx := context.Background()
	game := &domain.Game{ID: "gog", Name: "GOG game", InstallPath: t.TempDir(), Catalog: "skyrim-sx"}
	require.NoError(t, svc.SaveGame(ctx, game))

	res, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, core.VerifyStatusCatalogUnknown)
	require.NotNil(t, f, "statuses: %v", findingStatuses(res.Result))
	assert.Contains(t, f.Note, `"skyrim-sx"`)
	assert.Contains(t, f.Note, "skyrim-se")
	assert.Contains(t, f.FixableReason, "lmm game edit gog --catalog")
	assert.Zero(t, res.Result.Issues)
	assert.Zero(t, res.Result.Warnings)

	game.Catalog = "skyrim-se"
	require.NoError(t, svc.SaveGame(ctx, game))
	res, err = svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, core.VerifyStatusCatalogUnknown))
}

// `lmm game edit --catalog` / PUT /api/v1/games/{id}: a known slug is set,
// "" clears it, and an unknown one is refused by field, naming the valid
// slugs. With no catalog registered there is nothing to check a slug
// against, so a slug is refused rather than written unchecked.
func TestEditGame_Catalog(t *testing.T) {
	svc, _ := suggestFixture(t)
	ctx := context.Background()
	game := &domain.Game{ID: "gog", Name: "GOG game", InstallPath: t.TempDir(), ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(ctx, game))

	set := func(v string) (*core.GameListEntry, error) {
		return svc.EditGame(ctx, "gog", core.GameEdit{Catalog: &v})
	}
	entry, err := set(" skyrim-se ")
	require.NoError(t, err)
	assert.Equal(t, "skyrim-se", entry.Catalog)

	_, err = set("skyrim-sx")
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "catalog", specErr.Field)
	assert.Contains(t, specErr.Reason, "fallout3, future-game, skyrim-se")
	saved, err := svc.GetGame("gog")
	require.NoError(t, err)
	assert.Equal(t, "skyrim-se", saved.Catalog, "a refused edit writes nothing")

	entry, err = set("")
	require.NoError(t, err)
	assert.Empty(t, entry.Catalog)

	bare := newFlowsTestService(t)
	require.NoError(t, bare.SaveGame(ctx, &domain.Game{ID: "gog", Name: "GOG game", InstallPath: t.TempDir(), ModPath: t.TempDir()}))
	v := "skyrim-se"
	_, err = bare.EditGame(ctx, "gog", core.GameEdit{Catalog: &v})
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "catalog", specErr.Field)
	empty := ""
	_, err = bare.EditGame(ctx, "gog", core.GameEdit{Catalog: &empty})
	require.NoError(t, err, "clearing needs no catalog")
}
