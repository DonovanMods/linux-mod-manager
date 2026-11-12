package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
)

// #606: verify's two game-level notes print a line in text mode - the
// suggestion of the catalog's adapter, with the command that takes it, and
// the bethesda adapter's "cannot tell which game" note. Both count towards
// neither tally, so the marker is "~", the informational one.
func TestRenderVerifyFinding_AdapterNotesArePrinted(t *testing.T) {
	out := captureStdout(t, func() error {
		renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
			Status:        core.VerifyStatusAdapterSuggested,
			Note:          "lmm's game catalog sets this game up with `adapter: bethesda`",
			FixableReason: "run: lmm game edit skyrim --adapter bethesda",
		}})
		return nil
	})
	assert.Contains(t, out, "~ adapter - lmm's game catalog sets this game up with `adapter: bethesda`")
	assert.Contains(t, out, "  run: lmm game edit skyrim --adapter bethesda")

	out = captureStdout(t, func() error {
		renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
			Status: "bethesda_game_unknown", Note: "lmm cannot tell which Bethesda game \"x\" is",
		}})
		return nil
	})
	assert.Contains(t, out, "~ adapter - lmm cannot tell which Bethesda game \"x\" is")
}
