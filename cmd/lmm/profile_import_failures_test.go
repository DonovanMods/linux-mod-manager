package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileImportWithAFailure sets up an accepted import whose only requested
// mod is absent from its configured source. The profile is saved before the
// fetch, so it exercises import's partial-result path rather than a fatal
// plan or profile-save failure.
func profileImportWithAFailure(t *testing.T) (*core.Service, *domain.Game, []byte) {
	t.Helper()
	svc, game, _ := setupDoProfileImportTest(t)
	setFlag(t, &profileImportYes, true)
	return svc, game, buildImportProfileData(t, game.ID, "target", []domain.ModReference{{
		SourceID: "test-src", ModID: "missing", Version: "2.0",
	}})
}

// TestDoProfileImport_AFailedModIsNotSuccessful is #485: the profile is
// still saved and the normal summary still names its failed mod, but the
// command returns the typed incomplete error and therefore exits non-zero.
func TestDoProfileImport_AFailedModIsNotSuccessful(t *testing.T) {
	svc, game, data := profileImportWithAFailure(t)
	setFlag(t, &jsonOutput, false)

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})

	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, exitError, exitCodeFor(err))
	assert.Equal(t, 1, incomplete.Result.Failed)
	assert.Contains(t, stdout, "Error: failed to fetch mod:")
	assert.Contains(t, stdout, "--- Summary ---")
	assert.Contains(t, stdout, "Failed: 1")
	_, profileErr := svc.NewProfileManager().Get(context.Background(), game.ID, "target")
	assert.NoError(t, profileErr, "the profile's partial import is retained")
}

// TestDoProfileImport_JSON_AFailedModIsTheEnvelope pins the additive JSON
// error contract: command output remains quiet, and reportError turns the
// incomplete error's Details into the complete ProfileImportResult document.
func TestDoProfileImport_JSON_AFailedModIsTheEnvelope(t *testing.T) {
	svc, game, data := profileImportWithAFailure(t)
	withJSONOutput(t)

	stdout, stderr, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})
	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)

	envelope := captureStdout(t, func() error { reportError(err); return nil })
	var doc struct {
		Error   string `json:"error"`
		Details struct {
			Failed   int                `json:"failed"`
			Failures []core.ItemFailure `json:"failures"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope), &doc), envelope)
	assert.Equal(t, err.Error(), doc.Error)
	assert.Equal(t, 1, doc.Details.Failed)
	require.Len(t, doc.Details.Failures, 1)
	assert.Equal(t, "missing", doc.Details.Failures[0].ModID)
}
