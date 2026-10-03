package main

// #528: `lmm game detect`'s repair of a configured game found at a new
// install path is held to `lmm game edit --install-path`'s policy. One that
// is not a valid move is a per-game finding: the command reports it (and
// fails with the typed error), and the rest of the selection is added.

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoGameDetect_RefusesAnInstallPathRepairAndAddsTheRest(t *testing.T) {
	configDir = t.TempDir()
	svc := newGameDetectTestService(t)
	old := t.TempDir()
	game := &domain.Game{
		ID: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: old,
		ModPath: filepath.Join(old, "Data"), SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"},
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)
	deployOneFileForTest(t, svc, game, "default", "m1")

	oldSelect := gameDetectSelect
	gameDetectSelect = "1,2"
	t.Cleanup(func() { gameDetectSelect = oldSelect })
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	moved, other := t.TempDir(), t.TempDir()
	detected := []domain.DetectedGame{
		{
			Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: moved,
			ModPath: filepath.Join(moved, "Data"), NexusID: "skyrimspecialedition", Known: true,
		},
		{
			Slug: "fallout4", Name: "Fallout 4", InstallPath: other,
			ModPath: filepath.Join(other, "Data"), NexusID: "fallout4", Known: true,
		},
	}

	err = doGameDetect(context.Background(), cmd, bufio.NewReader(strings.NewReader("")), svc, detected, nil)
	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Contains(t, err.Error(), "not repairing skyrim-se")
	assert.Contains(t, err.Error(), "`lmm purge --game skyrim-se --profile default`")
	assert.Contains(t, buf.String(), "Added: Fallout 4 (fallout4)", "the rest of the selection is added, and named right")
	assert.NotContains(t, buf.String(), "Added: Skyrim")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Equal(t, old, saved["skyrim-se"].InstallPath, "games.yaml is untouched for the refused game")
	assert.Contains(t, saved, "fallout4")
}
