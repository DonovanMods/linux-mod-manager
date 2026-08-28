package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef"))

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := doVerify(cmd, svc, game, nil)
	_ = w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	output := buf.String()
	assert.Contains(t, output, "RECOMPILE NEEDED")
	assert.Contains(t, output, "Bear Mount")
	assert.Contains(t, output, "base pak updated", "this fixture's staleness is a fingerprint mismatch, not a missing artifact")
	assert.Contains(t, output, "lmm update --all", "notify-policy rows aren't applied by bare 'lmm update' - the hint must name the flag that actually applies them")
}

// TestDoVerify_StaleCompile_NotDeployed_ReasonSaysSo is the #197 postsmoke
// UX regression test: the "RECOMPILE NEEDED" hint always said "base pak
// updated", even when the merged pak's fingerprint still matched and the
// real problem was a missing deployed artifact (the #197 I5 wedge case).
// CheckMergedPakStaleness now distinguishes the two - this proves doVerify
// actually surfaces the real reason instead of the fingerprint-mismatch
// text unconditionally.
func TestDoVerify_StaleCompile_NotDeployed_ReasonSaysSo(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	require.NoError(t, os.Remove(deployedPath))

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	out := captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	assert.Contains(t, out, "RECOMPILE NEEDED")
	assert.Contains(t, out, "not deployed")
	assert.NotContains(t, out, "base pak updated", "the fingerprint still matches - blaming the base pak here is false")
}

// TestDoVerify_StaleCompile_JSON is the --json sibling of the above.
func TestDoVerify_StaleCompile_JSON(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef"))

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
	_ = w.Close()
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
	// #197: a merged-pak staleness row's identity is the SYNTHETIC
	// merged-pak mod, not the contributing "bear-mount" mod.
	assert.Equal(t, "merged-pak", found.ModID)
	assert.Equal(t, "base pak updated", found.Note, "the JSON note should carry the same real reason as the text-mode hint")
	assert.GreaterOrEqual(t, out.Warnings, 1)
}

// TestDoVerify_HealthyExmodzMod_NoFileCountMismatch is the #197 I4
// regression test: a DeployCompile ".exmodz" mod is ingested as
// validate+retain ONLY (Task 2/3) - it has zero deployment members of its
// own by design, so the pre-#197-I4 file-count pre-pass (checksum-row
// count vs cache.ListFiles) reported a false "FILE COUNT MISMATCH" for
// EVERY healthy exmodz mod, unconditionally. This proves a healthy,
// up-to-date exmodz mod verifies with no file_count_mismatch row at all.
func TestDoVerify_HealthyExmodzMod_NoFileCountMismatch(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef"))
	// Sync so the merged pak is up to date - isolates the file-count check
	// from the (separately tested) stale_compile row.
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	verifyProfile = "default"
	jsonOutput = true
	t.Cleanup(func() { verifyProfile = ""; jsonOutput = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err = doVerify(cmd, svc, game, nil)
	_ = w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	var out verifyJSONOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	for _, f := range out.Files {
		assert.NotEqual(t, "file_count_mismatch", f.Status,
			"a healthy exmodz mod (validate+retain only, zero deployment members by design) must never report file_count_mismatch")
	}
}

// TestDoVerify_Fix_SyncsMergedPak is the #197 postsmoke regression test
// for doVerify: --fix can repair a VERSION MISMATCH (moves the cache dir
// and the recorded version) or redownload a file whose upstream content
// changed - both are merge-fingerprint inputs with no other seam to catch
// them. This proves `lmm verify --fix` reaches the merged pak at all: a
// deployed pak that goes missing (mirrors #197 I5's self-heal scenario -
// a failed prior deploy, or a purge that intentionally kept the cache
// entry) must be redeployed by a --fix run, even though nothing else was
// broken to repair.
func TestDoVerify_Fix_SyncsMergedPak(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef"))
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must be deployed")
	require.NoError(t, os.Remove(deployedPath))

	verifyProfile = "default"
	verifyFix = true
	t.Cleanup(func() { verifyProfile = ""; verifyFix = false })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	_ = captureStdout(t, func() error {
		return doVerify(cmd, svc, game, nil)
	})

	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "lmm verify --fix must sync the merged pak, redeploying it when missing")
}
