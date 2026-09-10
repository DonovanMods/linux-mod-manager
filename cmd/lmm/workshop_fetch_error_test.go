package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
)

// TestReportError_JSON_WorkshopFetchError pins the wire shape of #269 Tier
// 3's typed download failure - the one type behind an anonymous refusal, an
// unavailable item, a missing steamcmd and an unclassified tool failure.
// It is the detailsCoverage entry for core.WorkshopFetchError, and the SAME
// document `lmm serve` hands the SPA in a failed install job's error
// envelope, which is what the mod page renders its steamcmd explainer from.
func TestReportError_JSON_WorkshopFetchError(t *testing.T) {
	withJSONOutput(t)

	err := &core.WorkshopFetchError{
		AppID:           "431960",
		PublishedFileID: "3000000002",
		Reason:          "This game's publisher does not allow anonymous Workshop downloads.",
		Tool:            "steamcmd",
		Err:             domain.ErrWorkshopAnonymousRefused,
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"downloading Steam Workshop item 3000000002: This game's publisher does not allow anonymous Workshop downloads.\",\n"+
		"  \"details\": {\n"+
		"    \"app_id\": \"431960\",\n"+
		"    \"published_file_id\": \"3000000002\",\n"+
		"    \"reason\": \"This game's publisher does not allow anonymous Workshop downloads.\",\n"+
		"    \"tool\": \"steamcmd\"\n"+
		"  }\n"+
		"}\n", out)
}

// TestReportError_JSON_WorkshopFetchError_UnclassifiedCarriesTheOutputTail
// covers the other populated shape: a tool failure lmm has no name for
// reports what the tool actually said rather than inventing a diagnosis.
func TestReportError_JSON_WorkshopFetchError_UnclassifiedCarriesTheOutputTail(t *testing.T) {
	withJSONOutput(t)

	err := &core.WorkshopFetchError{
		AppID:           "1133870",
		PublishedFileID: "3000000009",
		Reason:          "steamcmd failed to download item 3000000009.",
		Tool:            "steamcmd",
		OutputTail:      "steamcmd: something went wrong",
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"downloading Steam Workshop item 3000000009: steamcmd failed to download item 3000000009.\",\n"+
		"  \"details\": {\n"+
		"    \"app_id\": \"1133870\",\n"+
		"    \"published_file_id\": \"3000000009\",\n"+
		"    \"reason\": \"steamcmd failed to download item 3000000009.\",\n"+
		"    \"tool\": \"steamcmd\",\n"+
		"    \"output_tail\": \"steamcmd: something went wrong\"\n"+
		"  }\n"+
		"}\n", out)
}

// TestReportError_Human_WorkshopFetchError_PrintsTheToolsOutput is the
// terminal half: --json carries the output tail in the envelope, so the
// human path must not be the one place the only evidence of an
// unclassified tool failure gets thrown away.
func TestReportError_Human_WorkshopFetchError_PrintsTheToolsOutput(t *testing.T) {
	err := &core.WorkshopFetchError{
		PublishedFileID: "3000000009",
		Reason:          "steamcmd failed to download item 3000000009.",
		Tool:            "steamcmd",
		OutputTail:      "line one\nline two",
	}
	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })

	assert.Contains(t, out, "steamcmd failed to download item 3000000009.")
	assert.Contains(t, out, "steamcmd output:")
	assert.Contains(t, out, "  line one")
	assert.Contains(t, out, "  line two")
}

// TestReportError_Human_WorkshopFetchError_ClassifiedPrintsNoOutput is the
// other side of the same rule: a refusal lmm can explain says its sentence
// and stops. Dumping a subprocess's log under an answer the user can act
// on is noise.
func TestReportError_Human_WorkshopFetchError_ClassifiedPrintsNoOutput(t *testing.T) {
	err := &core.WorkshopFetchError{
		PublishedFileID: "3000000002",
		Reason:          "This game's publisher does not allow anonymous Workshop downloads.",
		Tool:            "steamcmd",
	}
	out, _ := captureStderrErr(t, func() error { reportError(err); return nil })

	assert.Contains(t, out, "does not allow anonymous Workshop downloads")
	assert.NotContains(t, out, "steamcmd output:")
}
