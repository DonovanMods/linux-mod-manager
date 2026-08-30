package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoGameDetect_RepairWipesExistingDefaultProfileMods characterizes the
// pre-lift 'lmm game detect' repair path (v2 Phase 2 Task 21): naming an
// already-configured game's number in the selection prompt unconditionally
// overwrites its default profile via config.SaveProfile, discarding any
// mods already recorded there (see doGameDetect's unconditional
// domain.Profile{Mods: nil} + config.SaveProfile call). Pinned here on the
// pre-lift code so ApplyGameDetect/ProfileManager.CreateOrResetDefault can
// be verified to preserve it byte-for-byte.
func TestDoGameDetect_RepairWipesExistingDefaultProfileMods(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Stale Name"}))
	require.NoError(t, config.SaveProfile(configDir, &domain.Profile{
		Name:      "default",
		GameID:    "skyrim-se",
		IsDefault: true,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "42", Version: "1.0"},
		},
	}))

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}

	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("1\n"))

	err := doGameDetect(context.Background(), cmd, reader, svc, games, nil)
	require.NoError(t, err)

	profile, err := config.LoadProfile(configDir, "skyrim-se", "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "repairing a configured game must wipe its default profile's mod list, matching 'lmm game add's unconditional overwrite")
}
