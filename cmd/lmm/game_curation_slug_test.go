package main

import (
	"bufio"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #406 review F1, at the surface the user actually reads. Curating a game
// gives it a hand-written slug, which for six of #406's games is NOT the
// slug detection derived from the Steam title before the entry existed. A
// user who had already added one of those had it listed as a fresh add, and
// taking it wrote a second games.yaml game at the same install path.

// TestDoGameDetect_ExistingGameUnderTheDerivedSlugIsMarkedConfigured drives
// the printed listing: the row for the curated slug carries [configured]
// because games.yaml already holds that install path under the pre-curation
// id, and "all" leaves it alone the way it leaves any configured game alone.
func TestDoGameDetect_ExistingGameUnderTheDerivedSlugIsMarkedConfigured(t *testing.T) {
	configDir = t.TempDir()
	install := t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{
		ID: "cyberpunk-2077", Name: "Cyberpunk 2077", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "cyberpunk2077"},
	}))

	svc := newGameDetectTestService(t)
	cmd, buf := newDetectCmd(t)
	games := []domain.DetectedGame{
		{SteamAppID: "1091500", Slug: "cyberpunk2077", Name: "Cyberpunk 2077",
			InstallPath: install, ModPath: install, NexusID: "cyberpunk2077", Known: true},
	}

	require.NoError(t, doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("all\n")), svc, games, nil))

	out := buf.String()
	assert.Contains(t, lineContaining(out, "cyberpunk2077"), "[configured]",
		"the same install path is already configured, under the slug detection used to derive")
	assert.Contains(t, out, "already configured", `"all" adds nothing here`)

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.NotContains(t, saved, "cyberpunk2077", "no second game at the same install path")
}

// TestDoGameAdd_FromDetected_RefusesADuplicateAtAConfiguredInstallPath: the
// other entry point into the same trap. `lmm game add --from-detected`
// prefills the curated slug, so before F1 it happily added a second game
// pointing at an install path games.yaml already covered.
func TestDoGameAdd_FromDetected_RefusesADuplicateAtAConfiguredInstallPath(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	install := fakeSteamGame(t, "1284190", "The Planet Crafter", "The Planet Crafter")
	// Added the way the user would have added it before the entry was
	// curated: through lmm, under the slug detection derived from the title.
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		ID: "the-planet-crafter", Name: "The Planet Crafter", InstallPath: install,
		ModPath:  filepath.Join(install, "BepInEx", "plugins"),
		SourceID: "nexusmods", Identifier: "planetcrafter",
	})
	require.NoError(t, err)
	gameAddFromDetected = "1284190"

	cmd, _ := newGameAddCmd()
	err = doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)

	require.ErrorIs(t, err, core.ErrGameExists)
	assert.Contains(t, err.Error(), "the-planet-crafter",
		"the refusal must name the game the user already has, not the curated slug")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.NotContains(t, saved, "planet-crafter")
}
