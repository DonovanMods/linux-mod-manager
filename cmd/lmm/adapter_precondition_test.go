package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
)

// TestReportError_JSON_AdapterPreconditionError pins the wire shape of
// #353's adapter refusal: the game, the adapter and the remedy reach the
// envelope as DATA, not only inside the sentence, so a frontend can render
// "install BepInEx first" as an actionable step rather than a string.
func TestReportError_JSON_AdapterPreconditionError(t *testing.T) {
	withJSONOutput(t)

	err := &core.AdapterPreconditionError{
		GameID:  "lethal-company",
		Adapter: "bepinex",
		Reason:  "BepInEx is not installed in the game directory; run the game once after installing it",
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"game \\\"lethal-company\\\": adapter \\\"bepinex\\\" refused: BepInEx is not installed in the game directory; run the game once after installing it\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"lethal-company\",\n"+
		"    \"adapter\": \"bepinex\",\n"+
		"    \"reason\": \"BepInEx is not installed in the game directory; run the game once after installing it\"\n"+
		"  }\n"+
		"}\n", out)
}
