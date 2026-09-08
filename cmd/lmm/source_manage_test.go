package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
)

// TestReportError_JSON_SourceInUseError pins core.SourceInUseError's
// --json envelope shape - the entry detailsCoverage names for it
// (details_coverage_test.go).
func TestReportError_JSON_SourceInUseError(t *testing.T) {
	withJSONOutput(t)

	err := &core.SourceInUseError{SourceID: "my-mods", Games: []string{"alpha", "zeta"}}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"source \\\"my-mods\\\" is configured for 2 game(s): [alpha zeta]\",\n"+
		"  \"details\": {\n"+
		"    \"source_id\": \"my-mods\",\n"+
		"    \"games\": [\n"+
		"      \"alpha\",\n"+
		"      \"zeta\"\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}
