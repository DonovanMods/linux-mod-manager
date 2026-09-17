package main

// #476: `lmm import <archive>` reports, once, the files it left because
// another game records them (#473), in text and in --json.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const heldImportWarning = "held.esp was not replaced: game g2 records it too, so lmm left that game's file there"

// setupHeldImport is setupDoImportTest with a second game, g2, sharing the
// mod directory and deploying held.esp there.
func setupHeldImport(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoImportTest(t)
	g2 := &domain.Game{ID: "g2", Name: "Game 2", ModPath: game.ModPath, LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))
	require.NoError(t, svc.SaveGame(ctx, g2))
	pm := getProfileManager(svc)
	for _, g := range []*domain.Game{game, g2} {
		_, err := pm.Create(ctx, g.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, svc.GetGameCache(g2).Store("g2", "src", "j", "1.0", "held.esp", []byte("g2's")))
	seedSyncInstalledMod(t, svc, g2, "src", "j", "Mod j", "1.0", "default", true, nil)
	require.NoError(t, pm.AddMod(ctx, "g2", "default", domain.ModReference{SourceID: "src", ModID: "j", Version: "1.0"}))
	_, err := svc.DeployProfile(ctx, g2, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	archivePath := filepath.Join(t.TempDir(), "mine.zip")
	createTestArchive(t, archivePath, map[string]string{"held.esp": "mine", "own.esp": "mine"})
	return svc, game, archivePath
}

func TestDoImport_ReportsAFileKeptForAnotherGame(t *testing.T) {
	svc, game, archivePath := setupHeldImport(t)

	_, stderr, err := captureStdoutAndStderr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})

	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(stderr, "Warning: "+heldImportWarning+"\n"), stderr)
}

func TestDoImport_ReportsAFileKeptForAnotherGame_JSON(t *testing.T) {
	svc, game, archivePath := setupHeldImport(t)
	withJSONOutput(t)

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})

	require.NoError(t, err)
	var doc struct {
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &doc), stdout)
	assert.Equal(t, []string{heldImportWarning}, doc.Warnings)
}
