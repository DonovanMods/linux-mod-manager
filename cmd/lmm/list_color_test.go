package main

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedModWithState installs modID/name with an explicit enabled/deployed
// combination (seedDeployableMod always seeds Enabled: true, Deployed:
// false, which isn't enough to exercise every row-tint branch).
func seedModWithState(t *testing.T, svc *core.Service, game *domain.Game, modID, name string, enabled, deployed bool) {
	t.Helper()

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
		Deployed:     deployed,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: modID, Version: "1.0"}))
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
// state.
func TestList_Verbose_PlainWhenColorDisabled(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedModWithState(t, svc, game, "a", "Enabled Deployed", true, true)
	seedModWithState(t, svc, game, "b", "Disabled Mod", false, false)
	seedModWithState(t, svc, game, "c", "Enabled Undeployed", true, false)

	out := listVerbose(t, svc, game, false)

	assert.NotContains(t, out, "\x1b[", "plain output must never contain ANSI escapes")
	assert.Contains(t, out, "Disabled Mod")
	assert.Contains(t, out, "Enabled Undeployed")
}

func TestList_Verbose_RowTinting(t *testing.T) {
	tests := []struct {
		name           string
		enabled        bool
		deployed       bool
		wantANSI       string
		wantNoOtherFor []string
	}{
		{name: "disabled mod row is dimmed", enabled: false, deployed: false, wantANSI: ansiDim},
		{name: "enabled but undeployed row is yellow", enabled: true, deployed: false, wantANSI: ansiYellow},
		{name: "enabled and deployed row is untinted", enabled: true, deployed: true, wantANSI: ""},
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
			if tt.wantANSI == "" {
				assert.NotContains(t, row, "\x1b[", "an enabled+deployed row should not be tinted")
			} else {
				assert.Contains(t, row, tt.wantANSI)
			}
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
