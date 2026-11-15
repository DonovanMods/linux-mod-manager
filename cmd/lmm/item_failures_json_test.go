package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// itemsError is a refusal naming several mods, as core's
// *AcquireRefusedError does (core.ItemFailuresOf finds it by its method).
type itemsError struct{ items []core.ItemFailure }

func (e itemsError) Error() string                    { return "nexusmods:266: gone\nnexusmods:57339: bad zip" }
func (e itemsError) ItemFailures() []core.ItemFailure { return e.items }

// TestReportError_JSON_ItemFailures (#655): a failure that names several
// mods lists them in the envelope's "items", one entry per mod, as the web
// UI's job envelope does.
func TestReportError_JSON_ItemFailures(t *testing.T) {
	withJSONOutput(t)

	failures := []core.ItemFailure{
		{SourceID: "nexusmods", ModID: "266", Name: "Skyrim Upscaled", Reason: "stored file no longer available upstream", Refusal: core.RefusalOther},
		{SourceID: "nexusmods", ModID: "57339", Reason: "zip: unsupported compression algorithm", Refusal: core.RefusalOther},
	}
	err := itemsError{items: failures}
	out := captureStdout(t, func() error { reportError(err); return nil })

	var envelope struct {
		Error string             `json:"error"`
		Items []core.ItemFailure `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope))
	assert.Equal(t, err.Error(), envelope.Error)
	assert.Equal(t, failures, envelope.Items)
}

// TestReportError_JSON_IncompleteImportHasNoItems: an incomplete import's
// details already list every item (details.failures), so it gains no
// second copy as "items".
func TestReportError_JSON_IncompleteImportHasNoItems(t *testing.T) {
	withJSONOutput(t)
	failures := []core.ItemFailure{{SourceID: "s", ModID: "1", Reason: "gone"}}
	err := &core.ProfileImportIncompleteError{Profile: "Big", Result: &core.ProfileImportResult{Failed: 1, Failures: failures}}
	out := captureStdout(t, func() error { reportError(err); return nil })
	var envelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &envelope))
	assert.NotContains(t, envelope, "items")
	assert.NotEmpty(t, envelope["details"].(map[string]any)["failures"])
}

// TestReportError_JSON_NoItemsForAPlainError: an error naming no mods
// gains no "items" key.
func TestReportError_JSON_NoItemsForAPlainError(t *testing.T) {
	withJSONOutput(t)
	out := captureStdout(t, func() error { reportError(errors.New("boom")); return nil })
	assert.Equal(t, "{\n  \"error\": \"boom\"\n}\n", out)
}
