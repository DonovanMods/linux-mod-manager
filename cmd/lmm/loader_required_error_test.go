package main

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReportError_JSON_LoaderRequiredError pins the wire shape of #359's
// plan-time loader precondition. It is the detailsCoverage entry for
// core.LoaderRequiredError, and the SAME document `lmm serve` hands the SPA
// in a failed install job's error envelope - which is what the web UI
// renders its BepInEx setup steps from, rather than carrying a second copy
// of them in JavaScript.
func TestReportError_JSON_LoaderRequiredError(t *testing.T) {
	withJSONOutput(t)

	err := &core.LoaderRequiredError{
		GameID:  "lethal-company",
		Kind:    domain.LoaderKindBepInEx,
		ModName: "Skinwalkers",
		Layout:  "game-root-relative",
		Setup:   []string{"Install BepInEx into the game directory.", "Record it with `lmm game edit lethal-company --loader bepinex`."},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"Skinwalkers needs the BepInEx mod loader, which game \\\"lethal-company\\\" does not declare: install it into the game directory, then run `lmm game edit lethal-company --loader bepinex`\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"lethal-company\",\n"+
		"    \"kind\": \"bepinex\",\n"+
		"    \"mod_name\": \"Skinwalkers\",\n"+
		"    \"layout\": \"game-root-relative\",\n"+
		"    \"setup\": [\n"+
		"      \"Install BepInEx into the game directory.\",\n"+
		"      \"Record it with `lmm game edit lethal-company --loader bepinex`.\"\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}

// TestReportError_Human_LoaderRequiredError_PrintsTheSetupSteps is the
// terminal half: the steps are the whole value of the refusal, so the human
// path must not be the one place they are thrown away.
func TestReportError_Human_LoaderRequiredError_PrintsTheSetupSteps(t *testing.T) {
	err := &core.LoaderRequiredError{
		GameID: "valheim", Kind: domain.LoaderKindBepInEx, ModName: "ValheimPlus",
		Layout: "wrapped in a single directory",
		Setup:  []string{"First step.", "Second step."},
	}
	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })

	assert.Contains(t, out, "needs the BepInEx mod loader")
	assert.Contains(t, out, "First step.")
	assert.Contains(t, out, "Second step.")
	require.True(t, strings.Index(out, "First step.") < strings.Index(out, "Second step."),
		"the steps are ordered: install, record, verify")
}
