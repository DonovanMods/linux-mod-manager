package main

// #79: what a credential failure looks like to a --json consumer. The
// remedy has to survive the wire, because the only fix is an action the
// user takes ("log in again", "chmod the key file") and a bare message
// string is not something a frontend can branch on.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// TestReportError_JSON_TokenKeyError pins core.TokenKeyError's --json
// envelope shape - the entry detailsCoverage names for it
// (details_coverage_test.go).
func TestReportError_JSON_TokenKeyError(t *testing.T) {
	withJSONOutput(t)

	err := &core.TokenKeyError{
		KeyPath: "/home/u/.local/share/lmm/key",
		Reason:  "missing",
		Err:     &db.KeyError{Path: "/home/u/.local/share/lmm/key", Reason: db.KeyMissing},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"the token-encryption key /home/u/.local/share/lmm/key is missing, so stored credentials cannot be read; run `lmm auth login <source>` again to store them under a new key\",\n"+
		"  \"details\": {\n"+
		"    \"key_path\": \"/home/u/.local/share/lmm/key\",\n"+
		"    \"reason\": \"missing\"\n"+
		"  }\n"+
		"}\n", out)
}

// TestReportError_JSON_TokenKeyError_UndecryptableNamesTheSource pins the
// per-source variant: one damaged row is reported as that row, so a
// frontend can offer "log in again" for exactly the source that broke.
func TestReportError_JSON_TokenKeyError_UndecryptableNamesTheSource(t *testing.T) {
	withJSONOutput(t)

	err := &core.TokenKeyError{
		KeyPath: "/home/u/.local/share/lmm/key",
		Reason:  "undecryptable",
		Sources: []string{"nexusmods"},
		Err:     &db.KeyError{Path: "/home/u/.local/share/lmm/key", Reason: db.KeyUndecryptable, SourceID: "nexusmods"},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"the stored credential for \\\"nexusmods\\\" could not be decrypted with /home/u/.local/share/lmm/key; run `lmm auth login nexusmods` again\",\n"+
		"  \"details\": {\n"+
		"    \"key_path\": \"/home/u/.local/share/lmm/key\",\n"+
		"    \"reason\": \"undecryptable\",\n"+
		"    \"sources\": [\n"+
		"      \"nexusmods\"\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}
