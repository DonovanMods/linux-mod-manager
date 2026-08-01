package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoVerify_StaleCompile_ReportedAsWarning proves `lmm verify` surfaces a
// #196 base-pak staleness row: text mode prints "RECOMPILE NEEDED" and
// --json reports status "stale_compile", counted as a warning (not an
// issue - it's self-healing via `lmm update`, not corruption).
func TestDoVerify_StaleCompile_ReportedAsWarning(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	require.NoError(t, svc.SaveFileChecksum("fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef"))

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := doVerify(cmd, svc, game, nil)
	w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	output := buf.String()
	assert.Contains(t, output, "RECOMPILE NEEDED")
	assert.Contains(t, output, "Bear Mount")
}

// TestDoVerify_StaleCompile_JSON is the --json sibling of the above.
func TestDoVerify_StaleCompile_JSON(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	require.NoError(t, svc.SaveFileChecksum("fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef"))

	verifyProfile = "default"
	jsonOutput = true
	t.Cleanup(func() { verifyProfile = ""; jsonOutput = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := doVerify(cmd, svc, game, nil)
	w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	var out verifyJSONOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	var found *verifyFileJSON
	for i := range out.Files {
		if out.Files[i].Status == "stale_compile" {
			found = &out.Files[i]
		}
	}
	require.NotNil(t, found, "expected a stale_compile row")
	assert.Equal(t, "bear-mount", found.ModID)
	assert.GreaterOrEqual(t, out.Warnings, 1)
}
