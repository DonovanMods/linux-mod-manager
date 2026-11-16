package main

// #638: the CLI's plan previews name the mod a shared path passes to,
// grouped by that mod and capped.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandOffLines_GroupsByHeirAndCaps(t *testing.T) {
	a := core.HandOffMod{SourceID: "src", ModID: "a", Name: "Mod A"}
	b := core.HandOffMod{SourceID: "src", ModID: "b", Name: "Mod B"}
	c := core.HandOffMod{SourceID: "src", ModID: "c", Name: "Mod C"}
	handoffs := []core.HandOff{
		{Path: "Data/foo.dll", From: c, To: b},
		{Path: "d3d11.dll", Root: domain.RootInstallPath, From: c, To: a},
	}
	for j := range 7 {
		handoffs = append(handoffs, core.HandOff{Path: fmt.Sprintf("Data/%d.esp", j), From: b, To: a})
	}
	assert.Equal(t, []string{
		"9 shared file(s) expected to pass to the next mod in load order that ships them (decided again when it runs):",
		"  Data/foo.dll passes to Mod B (from Mod C)",
		"  <game root>/d3d11.dll, Data/0.esp, Data/1.esp, Data/2.esp, Data/3.esp and 3 more pass to Mod A (from Mod C, Mod B)",
	}, handOffLines(handoffs))
	assert.Nil(t, handOffLines(nil), "nothing to say says nothing")
}

func TestHandOffLines_CapsTheHeirs(t *testing.T) {
	from := core.HandOffMod{SourceID: "src", ModID: "z", Name: "Mod Z"}
	var handoffs []core.HandOff
	for j := range handOffHeirLimit + 3 {
		to := core.HandOffMod{SourceID: "src", ModID: fmt.Sprint(j), Name: fmt.Sprintf("Mod %d", j)}
		handoffs = append(handoffs, core.HandOff{Path: fmt.Sprintf("f%d", j), From: from, To: to})
	}
	handoffs = append(handoffs, core.HandOff{Path: "extra", From: from, To: handoffs[len(handoffs)-1].To})
	lines := handOffLines(handoffs)
	require.Len(t, lines, 1+handOffHeirLimit+1)
	assert.Equal(t, "  and 3 more mod(s) take 4 more file(s)", lines[len(lines)-1])
}

// setupHandOffCLITest deploys Mod A then Mod B, which both ship
// shared.esp: Mod B wins it.
func setupHandOffCLITest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupDoProfileSwitchTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "shared.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "shared.esp")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return svc, game
}

func TestDoProfileApply_DryRun_NamesTheHandOff(t *testing.T) {
	svc, game := setupHandOffCLITest(t)
	require.NoError(t, getProfileManager(svc).SetModDisabled(context.Background(), game.ID, "default", "src", "b", true))
	withProfileDryRun(t, &profileApplyDryRun)

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assert.Contains(t, out, "Will disable 1 mod(s):\n  - Mod B (b)\n"+
		"1 shared file(s) expected to pass to the next mod in load order that ships them (decided again when it runs):\n"+
		"  shared.esp passes to Mod A (from Mod B)\n")
}

func TestDoUninstall_DryRun_NamesTheHandOff(t *testing.T) {
	svc, game := setupHandOffCLITest(t)
	old := uninstallDryRun
	uninstallDryRun = true
	t.Cleanup(func() { uninstallDryRun = old })

	out := captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "b")
	})
	assert.Contains(t, out, "  1 shared file(s) expected to pass to the next mod in load order that ships them (decided again when it runs):\n"+
		"    shared.esp passes to Mod A (from Mod B)\n")
}

// The --dry-run --json document carries the plan's handoffs.
func TestJSONGolden_ProfileApplyDryRunHandOffs(t *testing.T) {
	svc, game := setupHandOffCLITest(t)
	require.NoError(t, getProfileManager(svc).SetModDisabled(context.Background(), game.ID, "default", "src", "b", true))
	withProfileDryRun(t, &profileApplyDryRun)

	out := runJSONCommand(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assertJSONCLIGolden(t, "profile_apply_dry_run_handoffs", out, game.ModPath, "<MODPATH>")
}

// A name or path carrying a terminal control sequence is printed safe, as
// every name the CLI prints from a source or an installer is.
func TestHandOffLines_SanitizesNamesAndPaths(t *testing.T) {
	lines := handOffLines([]core.HandOff{{
		Path: "Data/\x1b[31mred.esp",
		From: core.HandOffMod{SourceID: "src", ModID: "a", Name: "Mod \x1b]0;pwned\x07A"},
		To:   core.HandOffMod{SourceID: "src", ModID: "b", Name: "Mod‮B"},
	}})
	require.Len(t, lines, 2)
	for _, l := range lines {
		assert.NotContains(t, l, "\x1b")
		assert.NotContains(t, l, "\x07")
		assert.NotContains(t, l, "‮")
	}
	assert.Equal(t, domain.SanitizeLine("Data/\x1b[31mred.esp")+" passes to "+domain.SanitizeLine("Mod‮B")+" (from "+domain.SanitizeLine("Mod \x1b]0;pwned\x07A")+")", strings.TrimPrefix(lines[1], "  "))
}
