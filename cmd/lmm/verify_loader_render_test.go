package main

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
)

// TestRenderVerifyFinding_LoaderTierRowsArePrinted: before #424 the loader
// tier (#359) fell straight through renderVerifyFinding's switch, so `lmm
// verify` counted the issues in its summary and printed NO line for any of
// them - a user was told "1 issue(s)" with nothing to read, while the web
// UI rendered the same rows fine.
//
// Each row's Note is a complete sentence written by the check that raised
// it, so one generic arm prints the whole tier.
func TestRenderVerifyFinding_LoaderTierRowsArePrinted(t *testing.T) {
	for _, tc := range []struct {
		status  string
		modName string
		want    string
	}{
		{status: "loader_missing", want: "loader - no plugin will load"},
		{status: "loader_stale_log", want: "loader - launch the game once"},
		// #413 review F5: a game with BepInEx on another adapter.
		{status: "loader_adapter_ignored", want: "loader - game \"valheim\" declares the BepInEx loader, but its adapter is \"generic-files\""},
		{status: "loader_deployed_outside_loader", modName: "Jotunn", want: "Jotunn - 2 file(s) sit outside BepInEx/, including an assembly"},
		{status: "fixed_loader_deployed_outside_loader", modName: "Jotunn", want: "Fixed: Jotunn - re-laid out 2 file(s)"},
		// #413 final review F4: a BepInEx/ tree nested inside BepInEx/plugins/.
		{status: "loader_nested_tree", want: "X loader - BepInEx/plugins/BepInEx/ is a BepInEx/ tree nested inside BepInEx/plugins/"},
		{status: "loader_foreign_nested_tree", want: "? loader - BepInEx/plugins/Hand/BepInEx/ is a BepInEx/ tree nested inside BepInEx/plugins/"},
		{status: "fixed_loader_nested_tree", want: "Fixed: loader - removed 2 untracked link(s) from BepInEx/plugins/BepInEx/"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			note := strings.TrimPrefix(strings.SplitN(tc.want, " - ", 2)[1], "Fixed: ")
			out := captureStdout(t, func() error {
				renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
					Status: tc.status, ModName: tc.modName, Note: note,
				}})
				return nil
			})
			assert.Contains(t, out, tc.want)
		})
	}
}

// A status the CLI does not know still prints nothing, which is what the
// switch did for every unknown status before the generic arm existed.
func TestRenderVerifyFinding_AnUnknownStatusStillPrintsNothing(t *testing.T) {
	out := captureStdout(t, func() error {
		renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
			Status: "something_new", Note: "a note"}})
		return nil
	})
	assert.Empty(t, out)
}
