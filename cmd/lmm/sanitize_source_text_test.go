package main

// #594: a mod whose source metadata carries terminal control sequences
// (ESC CSI/OSC, a C1 CSI, a bidi override) must not reach the terminal
// through `lmm list`, `lmm mod show` or `lmm search`. With colour off the
// CLI writes no ESC of its own, so the output must hold none at all.

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cliEvilPayload = "\x1b[2J\x1b]0;pwned\x07\x1b]8;;https://evil.example\x1b\\\u009b2J\u202E"

func assertNoTerminalControls(t *testing.T, cmd, out string) {
	t.Helper()
	assert.NotContains(t, out, "\x1b", cmd+" printed an ESC byte")
	assert.NotContains(t, out, "\x07", cmd+" printed a BEL")
	assert.NotContains(t, out, "\u009b", cmd+" printed a C1 CSI")
	assert.NotContains(t, out, "\u202E", cmd+" printed a bidi override")
	assert.Contains(t, out, "Evil Mod", cmd+" still prints the name")
}

func TestCLI_SourceControlSequencesNeverReachTheTerminal(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	ctx := context.Background()

	evil := func() *domain.Mod {
		return &domain.Mod{
			ID: "evil", SourceID: "src", GameID: "g1",
			Name: "Evil Mod" + cliEvilPayload, Version: "1.0" + cliEvilPayload,
			Author: "Mallory" + cliEvilPayload, Summary: "Sum" + cliEvilPayload,
			Description: "Desc\r\n" + cliEvilPayload, CategoryName: "Gameplay" + cliEvilPayload,
			SourceURL: "https://example.com/" + cliEvilPayload,
		}
	}
	src.AddMod(evil(), []domain.DownloadableFile{{ID: "f1", Name: "Main" + cliEvilPayload, FileName: "evil.zip", IsPrimary: true}})
	src.searchResults = []domain.Mod{*evil()}

	// A row stored before the fix, raw.
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: *evil(), ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "evil", Version: "1.0"}))

	oldNoColor, oldVerbose, oldJSON := noColor, verbose, jsonOutput
	t.Cleanup(func() { noColor, verbose, jsonOutput = oldNoColor, oldVerbose, oldJSON })
	noColor, jsonOutput = true, false

	for _, v := range []bool{false, true} {
		verbose = v
		out := captureStdout(t, func() error { return doList(ctx, &cobra.Command{}, svc, game) })
		assertNoTerminalControls(t, "list", out)
	}
	verbose = false

	out := captureStdout(t, func() error { return doModShow(ctx, svc, game, "evil") })
	assertNoTerminalControls(t, "mod show", out)
	assert.True(t, strings.Contains(out, "Mallory"), "mod show prints the author")

	withSearchFlags(t, "src", 0)
	out = captureStdout(t, func() error { return doSearch(ctx, svc, game, []string{"Evil"}) })
	assertNoTerminalControls(t, "search", out)
}
