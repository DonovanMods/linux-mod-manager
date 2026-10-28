package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bepinexCuratedApps is #416's data half as the CLI sees it: the five
// curated entries this fake library holds that declare
// `loader: {kind: bepinex}`, each with the install root as its mod root
// (#358). The catalog declares nine (#409 added four Thunderstore-first
// games); these are the five with a Steam install fabricated below. The catalog-side pin on the same set
// is internal/source/steam's TestKnownGames_OnlyTheBepInExEntriesDeclareALoader.
var bepinexCuratedApps = []struct {
	appID, name, installDir, slug string
}{
	{"892970", "Valheim", "Valheim", "valheim"},
	{"1284190", "The Planet Crafter", "The Planet Crafter", "planet-crafter"},
	{"1466060", "Tainted Grail The Fall of Avalon", "Tainted Grail The Fall of Avalon", "tainted-grail-fall-of-avalon"},
	{"527230", "For The King", "For The King", "for-the-king"},
	{"2393970", "Human Host", "Human Host", "human-host"},
}

// fakeSteamLibraryWithBepInExApps fabricates one Steam library under a
// sandboxed HOME holding all five curated BepInEx apps, and returns app id
// -> install path. One library rather than five, because the acceptance
// claim is about a REAL scan: these five have to come out of the same
// steamapps directory the way a user's do, not one at a time.
func fakeSteamLibraryWithBepInExApps(t *testing.T) map[string]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")
	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	installs := make(map[string]string, len(bepinexCuratedApps))
	for _, a := range bepinexCuratedApps {
		install := filepath.Join(steamapps, "common", a.installDir)
		require.NoError(t, os.MkdirAll(install, 0755))
		acf := "\n\"AppState\"\n{\n\t\"appid\"\t\t\"" + a.appID + "\"\n\t\"name\"\t\t\"" + a.name +
			"\"\n\t\"installdir\"\t\t\"" + a.installDir + "\"\n}\n"
		require.NoError(t, os.WriteFile(filepath.Join(steamapps, "appmanifest_"+a.appID+".acf"), []byte(acf), 0644))
		installs[a.appID] = install
	}
	return installs
}

// TestGameDetectJSON_EveryBepInExAppDeclaresTheLoader is #416's acceptance
// check, offline: a real Steam library holding all five curated BepInEx
// apps, scanned by the real steam.DetectGames through the very call
// `lmm game detect` makes (app.DetectGames), rendered as the --json listing
// document a script reads, and then actually written to games.yaml.
//
// Each of the five must carry `loader.kind: bepinex` and a mod_path equal to
// its install root. Those two facts are one claim, not two: BepInEx lays its
// own tree down in the game root, so a declaration without the game-root mod
// path would deploy plugins to the wrong place, and a game-root mod path
// without the declaration is exactly the two-step setup with an error in the
// middle that #416 exists to remove.
//
// The listing document is reached with --include-unknown and no selection,
// which is the CLI's pure-query form (the counterpart to GET
// /api/v1/games/detect?all=1) - every other --json path answers with the
// APPLY result instead, which names ids rather than candidates. The second
// half below takes that path too, so both of `game detect`'s documents are
// pinned over the same real scan.
func TestGameDetectJSON_EveryBepInExAppDeclaresTheLoader(t *testing.T) {
	configDir = t.TempDir()
	svc := newGameDetectTestService(t)
	cmd, buf := newDetectCmd(t)
	withJSONOutput(t)
	gameDetectIncludeUnknown = true
	installs := fakeSteamLibraryWithBepInExApps(t)

	games, warnings, err := app.DetectGames(context.Background(), configDir, app.DetectOptions{NoWorkshop: true})
	require.NoError(t, err)

	var doc core.GameDetectListing
	stdout := captureStdout(t, func() error {
		return assertStdinNeverRead(t, func() error {
			return doGameDetect(context.Background(), cmd,
				bufio.NewReader(poisonReader{t}), svc, games, warnings)
		})
	})
	decodeSingleDoc(t, stdout, &doc)
	assert.Empty(t, buf.String(), "no console text may sit beside the document")

	found := make(map[string]domain.DetectedGame, len(doc.Games))
	for _, entry := range doc.Games {
		found[entry.SteamAppID] = entry.DetectedGame
	}
	for _, a := range bepinexCuratedApps {
		t.Run(a.slug, func(t *testing.T) {
			got, ok := found[a.appID]
			require.True(t, ok, "app %s (%s) is installed but absent from the listing", a.appID, a.slug)
			assert.Equal(t, a.slug, got.Slug)
			require.NotNil(t, got.Loader, "no loader declaration: the first plugin install would be refused (#359)")
			assert.Equal(t, domain.LoaderKindBepInEx, got.Loader.Kind)
			assert.Equal(t, installs[a.appID], got.ModPath,
				"a BepInEx game's mod root IS its install root (#358)")
			assert.Equal(t, installs[a.appID], got.InstallPath)
		})
	}

	// And nothing else on this library claims a loader - the library holds
	// only these five, so a sixth declaration would mean the catalog
	// declared one for an app that never asked.
	var declared []string
	for appID, got := range found {
		if got.Loader != nil {
			declared = append(declared, appID)
		}
	}
	sort.Strings(declared)
	assert.Equal(t, []string{"1284190", "1466060", "2393970", "527230", "892970"}, declared)

	// And the apply half over the same scan: the declaration has to reach
	// games.yaml, not merely the listing a script reads, or the first
	// `lmm install` is still refused.
	gameDetectIncludeUnknown, gameDetectAll = false, true
	var result core.GameDetectResult
	stdout = captureStdout(t, func() error {
		return assertStdinNeverRead(t, func() error {
			return doGameDetect(context.Background(), cmd,
				bufio.NewReader(poisonReader{t}), svc, games, warnings)
		})
	})
	decodeSingleDoc(t, stdout, &result)

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	for _, a := range bepinexCuratedApps {
		g, ok := saved[a.slug]
		require.True(t, ok, "games.yaml has no %s entry; saved: %v", a.slug, result.Saved)
		require.NotNil(t, g.Loader, "%s gained no loader: block", a.slug)
		assert.Equal(t, domain.LoaderKindBepInEx, g.Loader.Kind)
		assert.True(t, g.DeclaresBepInEx(), "the rules #358/#359 gate on must fire for %s", a.slug)
		assert.Equal(t, installs[a.appID], g.ModPath)
	}
}

// TestDoGameAdd_FromDetected_CuratedBepInExGameWritesTheLoader is the other
// door into the same games.yaml block: `lmm game add --from-detected 892970`
// goes through GameSpecFromDetected rather than GameFromDetected, so the
// declaration has to survive that conversion too - otherwise the two ways
// of adding one curated game disagree and only one of them can install a
// plugin.
func TestDoGameAdd_FromDetected_CuratedBepInExGameWritesTheLoader(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	// Valheim's curated entry maps thunderstore as well as nexusmods
	// (#409), and AddGame refuses a map naming a source nothing registers -
	// which is the check doing its job: the real binary registers both.
	// Never contacted: BaseURL is a dead port, and nothing here searches.
	svc.RegisterSource(thunderstore.New(thunderstore.Options{
		CacheDir: t.TempDir(), BaseURL: "http://127.0.0.1:1",
	}))
	install := fakeSteamGame(t, "892970", "Valheim", "Valheim")
	gameAddFromDetected = "892970"

	cmd, buf := newGameAddCmd()
	require.NoError(t, doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc))

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "valheim")
	g := saved["valheim"]
	require.NotNil(t, g.Loader, "games.yaml gained no loader: block")
	assert.Equal(t, domain.LoaderKindBepInEx, g.Loader.Kind)
	assert.True(t, g.DeclaresBepInEx(), "the rules #358/#359 gate on must fire for it")
	assert.Equal(t, install, g.ModPath, "a BepInEx game's mod root IS its install root (#358)")
	assert.Empty(t, g.Loader.Version,
		"the catalog declares no version: which BepInEx pack is installed is a fact about "+
			"the user's copy, which `lmm game show` reads off the game directory")
	assert.Contains(t, buf.String(), "Added Valheim (id: valheim)")
	assert.False(t, strings.Contains(buf.String(), "loader"),
		"nothing here claims lmm installed BepInEx - it only recorded that the game needs it")
}
