package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoGameAdd_OverwritesExistingDefaultProfileMods characterizes the
// pre-lift 'lmm game add' behaviour (v2 Phase 2 Task 21): saveGameConfig
// always writes a fresh default profile via config.SaveProfile,
// unconditionally discarding any mods an existing profile of the same name
// already had. Mirrors 'lmm game detect's repair overwrite
// (TestDoGameDetect_RepairWipesExistingDefaultProfileMods) so both call
// sites are pinned before they re-point to the same core helper.
func TestDoGameAdd_OverwritesExistingDefaultProfileMods(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))

	require.NoError(t, config.SaveProfile(configDir, &domain.Profile{
		Name:      "default",
		GameID:    "skyrimspecialedition",
		IsDefault: true,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "42", Version: "1.0"},
		},
	}))

	input := strings.Join([]string{
		"1", // select nexusmods (only registered source)
		"Skyrim Special Edition",
		"skyrimspecialedition",
		"/opt/games/skyrim",
		"",
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	profile, err := config.LoadProfile(configDir, "skyrimspecialedition", "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "'lmm game add' must wipe an existing default profile's mod list, matching 'lmm game detect's repair overwrite")
}
