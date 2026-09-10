package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
)

// TestReportError_JSON_ExternalModError pins the wire shape of the ONE
// typed refusal every #269 external-mod rule returns - deploy of a named
// mod, enable, disable, update-apply, rollback, relink, `mod set-update
// auto` and the already-tracked install. It is the detailsCoverage entry
// for core.ExternalModError.
func TestReportError_JSON_ExternalModError(t *testing.T) {
	withJSONOutput(t)

	err := &core.ExternalModError{
		Op:      "disable",
		Mod:     domain.ModReference{SourceID: "steamworkshop", ModID: "3617086610", Version: "7987119735124793734"},
		ModName: "Workshop Item",
		Reason:  core.ReasonExternalNoToggle,
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"cannot disable Workshop Item: lmm cannot disable a Steam Workshop item - unsubscribe it in Steam, or use the game's own mod menu\",\n"+
		"  \"details\": {\n"+
		"    \"op\": \"disable\",\n"+
		"    \"mod\": {\n"+
		"      \"source_id\": \"steamworkshop\",\n"+
		"      \"mod_id\": \"3617086610\",\n"+
		"      \"version\": \"7987119735124793734\",\n"+
		"      \"locked\": false\n"+
		"    },\n"+
		"    \"mod_name\": \"Workshop Item\",\n"+
		"    \"reason\": \"lmm cannot disable a Steam Workshop item - unsubscribe it in Steam, or use the game's own mod menu\"\n"+
		"  }\n"+
		"}\n", out)
}
