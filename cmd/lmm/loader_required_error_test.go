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

// TestReportError_JSON_LoaderRequiredError_CarriesTheVersion is the same
// document as reported by a source that reads the requirement off a
// package's own metadata (#409) rather than off an archive's shape: one
// extra member, "version", which the archive half can never know and which
// the first setup step needs in order to say WHICH BepInEx build to
// install. It is additive - the golden above, whose Version is empty, is
// byte-identical to what it always was.
//
// The envelope is what this file pins; the SENTENCES that constructor
// produces are pinned by internal/core's own
// loader_required_error_from_source.golden, built through the shipping
// constructor rather than by hand.
func TestReportError_JSON_LoaderRequiredError_CarriesTheVersion(t *testing.T) {
	withJSONOutput(t)

	err := &core.LoaderRequiredError{
		GameID: "lethal-company", Kind: domain.LoaderKindBepInEx, Version: "5.4.2100",
		ModName: "Skinwalkers",
		Layout:  "the package declares a dependency on the BepInEx framework",
		Setup:   []string{"Install BepInEx 5.4.2100 into the game directory."},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Contains(t, out, "\"kind\": \"bepinex\"")
	assert.Contains(t, out, "\"version\": \"5.4.2100\"")
	assert.Contains(t, out, "\"layout\": \"the package declares a dependency on the BepInEx framework\"")
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
