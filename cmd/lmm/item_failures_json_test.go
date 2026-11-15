package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReportError_JSON_ItemFailures (#655): a failure that names several
// mods lists them in the envelope's "items", one entry per mod, as the web
// UI's job envelope does - after "details", which is unchanged.
func TestReportError_JSON_ItemFailures(t *testing.T) {
	withJSONOutput(t)

	failures := []core.ItemFailure{
		{SourceID: "nexusmods", ModID: "266", Name: "Skyrim Upscaled", Reason: "stored file no longer available upstream"},
		{SourceID: "nexusmods", ModID: "57339", Reason: "zip: unsupported compression algorithm"},
	}
	err := &core.ProfileImportIncompleteError{Profile: "Big", Result: &core.ProfileImportResult{Failed: 2, Failures: failures}}
	out := captureStdout(t, func() error { reportError(err); return nil })

	var envelope struct {
		Error   string             `json:"error"`
		Details map[string]any     `json:"details"`
		Items   []core.ItemFailure `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope))
	assert.Equal(t, err.Error(), envelope.Error)
	assert.NotEmpty(t, envelope.Details["failures"], "details is the whole result, as before")
	assert.Equal(t, failures, envelope.Items)
}

// TestReportError_JSON_NoItemsForAPlainError: an error naming no mods
// gains no "items" key.
func TestReportError_JSON_NoItemsForAPlainError(t *testing.T) {
	withJSONOutput(t)
	out := captureStdout(t, func() error { reportError(errors.New("boom")); return nil })
	assert.Equal(t, "{\n  \"error\": \"boom\"\n}\n", out)
}
