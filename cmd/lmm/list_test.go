package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCmd_NoGame(t *testing.T) {
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(listCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(listCmd); rootCmd.AddCommand(listCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"list"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
}

func TestListCmd_Structure(t *testing.T) {
	assert.Equal(t, "list", listCmd.Use)
	assert.NotEmpty(t, listCmd.Short)
	assert.NotEmpty(t, listCmd.Long)

	// Check flags
	assert.NotNil(t, listCmd.Flags().Lookup("profile"))
	assert.NotNil(t, listCmd.Flags().Lookup("profiles"))
}

// TestListCmd_DocMentionsLoadOrder guards #201: the help text must describe
// the mod ordering it actually shows (the profile's load order, which
// decides merge precedence) rather than staying silent about it or, worse,
// claiming the old install order.
func TestListCmd_DocMentionsLoadOrder(t *testing.T) {
	assert.Contains(t, listCmd.Long, "load order")
	assert.NotContains(t, strings.ToLower(listCmd.Long), "install order",
		"list must not claim install order - it shows profile load order")
}

func TestStatusCmd_Structure(t *testing.T) {
	assert.Equal(t, "status", statusCmd.Use)
	assert.NotEmpty(t, statusCmd.Short)
}

// TestList_NonVerboseMarksNonDefaultState is #397. The default view is
// ID / NAME / VERSION / AUTHOR, so a disabled, undeployed mod rendered
// identically to a live one - and the header count included it, which is
// how the final review's orphaned row read as a normal mod. Row tinting
// already carried the state, but colour is gone under --no-color, in a
// pipe, and for anyone who cannot see it.
func TestList_NonVerboseMarksNonDefaultState(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedModWithState(t, svc, game, "live", "Live Mod", true, true)
	seedModWithState(t, svc, game, "off", "Disabled Mod", false, false)
	seedModWithState(t, svc, game, "pending", "Undeployed Mod", true, false)

	out := listNonVerbose(t, svc, game)

	require.Contains(t, out, "STATE", "a non-default state needs a column to live in")
	assert.Regexp(t, `Disabled Mod.*disabled`, out)
	assert.Regexp(t, `Undeployed Mod.*not deployed`, out)
	assert.Contains(t, out, "1 disabled", "the header count says how many of its mods are off")
}

// TestList_NonVerboseAllLiveKeepsItsShape keeps #397's column from becoming
// permanent furniture: a profile whose mods are all enabled and deployed
// looks exactly as it always has - the same rule the EXTERNAL column
// already follows.
func TestList_NonVerboseAllLiveKeepsItsShape(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedModWithState(t, svc, game, "live", "Live Mod", true, true)

	out := listNonVerbose(t, svc, game)

	assert.NotContains(t, out, "STATE")
	assert.NotContains(t, out, "disabled")
}

// TestList_NamesDisabledRefsWithNoRow is #440: a ref the profile marks
// disabled with nothing installed is listed under the table, switched off
// and not downloaded, with the command that enables it - including when
// nothing at all is installed, an imported profile's usual state.
func TestList_NamesDisabledRefsWithNoRow(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "test", ModID: "off", Version: "2.0", Disabled: true}))

	out := listNonVerbose(t, svc, game)
	assert.Contains(t, out, "No mods installed.")
	assert.Contains(t, out, "Listed but not downloaded, switched off — 1 mod(s):\n  test:off 2.0\n")
	assert.Contains(t, out, "lmm install --id <mod-id>")

	seedModWithState(t, svc, game, "live", "Live Mod", true, true)
	out = listNonVerbose(t, svc, game)
	assert.Contains(t, out, "Live Mod")
	assert.Contains(t, out, "test:off 2.0", "the section follows the table too")
}
