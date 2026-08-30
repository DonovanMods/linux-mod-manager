package main

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listNonVerbose runs doList without --verbose and returns stdout - the
// non-verbose counterpart to pin_visibility_test.go's listVerbose, for
// tests guarding that row tinting is identical on both paths (#193 round 2:
// it had only landed on the verbose branch).
func listNonVerbose(t *testing.T, svc *core.Service, game *domain.Game) string {
	t.Helper()
	oldVerbose, oldJSON := verbose, jsonOutput
	verbose, jsonOutput = false, false
	t.Cleanup(func() { verbose, jsonOutput = oldVerbose, oldJSON })

	return captureStdout(t, func() error {
		return doList(context.Background(), &cobra.Command{}, svc, game)
	})
}

// seedModWithState installs modID/name with an explicit enabled/deployed
// combination (seedDeployableMod always seeds Enabled: true, Deployed:
// false, which isn't enough to exercise every row-tint branch).
func seedModWithState(t *testing.T, svc *core.Service, game *domain.Game, modID, name string, enabled, deployed bool) {
	t.Helper()

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
		Deployed:     deployed,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(context.Background(), game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(context.Background(), game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: modID, Version: "1.0"}))
}

func rowFor(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	return ""
}

// TestList_Verbose_PlainWhenColorDisabled is the byte-stability regression
// guard: with color off (the default for piped/non-TTY output, and every
// test that doesn't force stdoutColorCapable), `lmm list -v` output must
// carry no ANSI escapes at all, regardless of each mod's enabled/deployed
// state. resetColorFlags undoes setupDoDeployTest's noColor=true so this
// proves the real TTY-detection gate keeps piped output plain, not the
// --no-color flag - captureStdout's os.Pipe swap gives stdoutColorCapable()
// a genuine non-TTY answer without any test-seam override.
func TestList_Verbose_PlainWhenColorDisabled(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	seedModWithState(t, svc, game, "a", "Enabled Deployed", true, true)
	seedModWithState(t, svc, game, "b", "Disabled Mod", false, false)
	seedModWithState(t, svc, game, "c", "Enabled Undeployed", true, false)

	out := listVerbose(t, svc, game, false)

	assert.NotContains(t, out, "\x1b[", "plain output must never contain ANSI escapes")
	assert.Contains(t, out, "Disabled Mod")
	assert.Contains(t, out, "Enabled Undeployed")
}

// TestList_Verbose_RowTinting guards #193's richer palette: the common,
// healthy case (enabled+deployed) must now be visibly colored too (green),
// not left untinted - #112's original "only flag anomalies" choice read as
// nearly plain in smoke feedback. Yellow (undeployed) and dim (disabled)
// are unchanged.
func TestList_Verbose_RowTinting(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		deployed bool
		wantANSI string
	}{
		{name: "disabled mod row is dimmed", enabled: false, deployed: false, wantANSI: ansiDim},
		{name: "enabled but undeployed row is yellow", enabled: true, deployed: false, wantANSI: ansiYellow},
		{name: "enabled and deployed row is green", enabled: true, deployed: true, wantANSI: ansiGreen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, game := setupDoDeployTest(t)
			// setupDoDeployTest forces noColor=true; undo it so the
			// stdoutColorCapable override below actually takes effect.
			resetColorFlags(t)
			seedModWithState(t, svc, game, "x", "Target Mod", tt.enabled, tt.deployed)

			withColorCapableStdout(t, true)
			out := listVerbose(t, svc, game, false)

			row := rowFor(out, "Target Mod")
			require.NotEmpty(t, row)
			assert.Contains(t, row, tt.wantANSI)
		})
	}
}

// TestList_Verbose_ColorNeverBreaksAlignment: stripping ANSI from a
// color-enabled run must reproduce the color-disabled run byte-for-byte -
// text/tabwriter pads by raw byte length, so this is the guard against a
// silent column-alignment regression (see printTable's doc comment).
func TestList_Verbose_ColorNeverBreaksAlignment(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	seedModWithState(t, svc, game, "a", "Enabled Deployed", true, true)
	seedModWithState(t, svc, game, "b", "Disabled Mod", false, false)
	seedModWithState(t, svc, game, "c", "Enabled Undeployed", true, false)

	withColorCapableStdout(t, false)
	plain := listVerbose(t, svc, game, false)

	withColorCapableStdout(t, true)
	colored := listVerbose(t, svc, game, false)

	assert.Equal(t, plain, stripANSI(colored))
}

// TestList_NonVerbose_PlainWhenColorDisabled mirrors
// TestList_Verbose_PlainWhenColorDisabled for the non-verbose path.
// resetColorFlags undoes setupDoDeployTest's noColor=true (review finding on
// PR #195: without it, this only proved --no-color suppresses color, not
// that the TTY-detection gate itself keeps piped output plain - a color
// leak into non-TTY output with noColor=false would have slipped through
// undetected).
func TestList_NonVerbose_PlainWhenColorDisabled(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	seedModWithState(t, svc, game, "a", "Enabled Deployed", true, true)
	seedModWithState(t, svc, game, "b", "Disabled Mod", false, false)
	seedModWithState(t, svc, game, "c", "Enabled Undeployed", true, false)

	out := listNonVerbose(t, svc, game)

	assert.NotContains(t, out, "\x1b[", "plain output must never contain ANSI escapes")
	assert.Contains(t, out, "Disabled Mod")
	assert.Contains(t, out, "Enabled Undeployed")
}

// TestList_NonVerbose_RowTinting is the round-2 regression guard: smoke
// feedback found `lmm list` (no -v) and `lmm list -v` colored
// INCONSISTENTLY, because the row-tint decision only ever ran on the
// verbose branch. Plain `lmm list` doesn't show the ENABLED/DEPLOYED
// columns, but the mods themselves are still enabled/deployed or not, so
// the same row-tint rules must apply identically here.
func TestList_NonVerbose_RowTinting(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		deployed bool
		wantANSI string
	}{
		{name: "disabled mod row is dimmed", enabled: false, deployed: false, wantANSI: ansiDim},
		{name: "enabled but undeployed row is yellow", enabled: true, deployed: false, wantANSI: ansiYellow},
		{name: "enabled and deployed row is green", enabled: true, deployed: true, wantANSI: ansiGreen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, game := setupDoDeployTest(t)
			resetColorFlags(t)
			seedModWithState(t, svc, game, "x", "Target Mod", tt.enabled, tt.deployed)

			withColorCapableStdout(t, true)
			out := listNonVerbose(t, svc, game)

			row := rowFor(out, "Target Mod")
			require.NotEmpty(t, row)
			assert.Contains(t, row, tt.wantANSI)
		})
	}
}

// TestList_NonVerbose_ColorNeverBreaksAlignment mirrors
// TestList_Verbose_ColorNeverBreaksAlignment for the non-verbose table.
func TestList_NonVerbose_ColorNeverBreaksAlignment(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	seedModWithState(t, svc, game, "a", "Enabled Deployed", true, true)
	seedModWithState(t, svc, game, "b", "Disabled Mod", false, false)
	seedModWithState(t, svc, game, "c", "Enabled Undeployed", true, false)

	withColorCapableStdout(t, false)
	plain := listNonVerbose(t, svc, game)

	withColorCapableStdout(t, true)
	colored := listNonVerbose(t, svc, game)

	assert.Equal(t, plain, stripANSI(colored))
}
