package main

// Tests for issue #102: `lmm update <mod-id>` and `lmm update rollback <id>`
// ignore the persistent --json flag on every success path; only failures
// emitted JSON (via Execute's error reporting). These pin core.UpdateApplyResult's
// one-document-per-invocation contract for applySingleUpdate's statuses
// (updated / up_to_date / skipped-pinned / available-dry-run), doUpdate's
// local-mod branch, and doUpdateRollback's rolled_back status - plus the two
// "failure produces no document" guards.
//
// Reuses setupDoUpdateTest/seedInstalledForUpdate (update_test.go),
// setupRollbackReadyMod's pattern (update_rollback_test.go), captureStdout
// (auth_status_test.go), and the json.Decoder.InputOffset() single-document
// check (local_update_test.go).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeSingleDoc decodes exactly one JSON document from raw into out and
// asserts nothing else follows it - the same trailing-bytes check
// TestDoUpdate_CheckFailed_JSONCarriesError uses, generalized for reuse here.
func decodeSingleDoc(t *testing.T, raw string, out any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	require.NoError(t, dec.Decode(out))
	trailing := strings.TrimSpace(raw[dec.InputOffset():])
	assert.Empty(t, trailing, "stdout must hold exactly one JSON document, found trailing content")
}

// withJSONOutput sets jsonOutput = true for the duration of the test,
// restoring it afterward - the same pattern local_update_test.go's
// TestDoUpdate_JSONReportsSkipped and TestDoUpdate_CheckFailed_JSONCarriesError
// use.
func withJSONOutput(t *testing.T) {
	t.Helper()
	old := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = old })
}

// TestApplySingleUpdate_JSON is table-driven over applySingleUpdate's
// success-path statuses, since all four share setupDoUpdateTest/
// seedInstalledForUpdate scaffolding and only differ in mod state, flags, and
// the expected document.
func TestApplySingleUpdate_JSON(t *testing.T) {
	t.Run("updated", func(t *testing.T) {
		withJSONOutput(t)
		svc, game, src := setupDoUpdateTest(t)
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
		src.AddDownload("new-1", []byte("new-content"))
		// HTML changelog: the JSON document must carry the cleaned form, same
		// as the human output - upstream changelogs are HTML and consumers
		// should not have to strip tags themselves.
		src.changelogs["mod1"] = "<b>Fixed</b> bugs.<br>More &amp; more."

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		var doc core.UpdateApplyResult
		decodeSingleDoc(t, out, &doc)
		assert.Equal(t, "mod1", doc.Mod.ModID)
		assert.Equal(t, "Mod One", doc.Name)
		assert.Equal(t, "1.0", doc.FromVersion)
		assert.Equal(t, "2.0", doc.ToVersion)
		assert.Equal(t, "Fixed bugs.\nMore & more.", doc.Changelog)
		assert.Equal(t, "updated", doc.Status.String())
		assert.Empty(t, doc.Reason)
		assert.NotContains(t, out, "Updating")
		assert.NotContains(t, out, "✓ Updated")
	})

	t.Run("up_to_date", func(t *testing.T) {
		withJSONOutput(t)
		svc, game, _ := setupDoUpdateTest(t)
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
		// No AddMod call: fakeUpdateSource's CheckUpdates finds no registered
		// mod, so no update is produced and the mod reports as current.

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		var doc core.UpdateApplyResult
		decodeSingleDoc(t, out, &doc)
		assert.Equal(t, "mod1", doc.Mod.ModID)
		assert.Equal(t, "Mod One", doc.Name)
		assert.Equal(t, "1.0", doc.FromVersion)
		assert.Empty(t, doc.ToVersion)
		assert.Equal(t, "up_to_date", doc.Status.String())
		assert.Empty(t, doc.Reason)
	})

	t.Run("skipped pinned", func(t *testing.T) {
		withJSONOutput(t)
		svc, game, _ := setupDoUpdateTest(t)
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
		// SetModUpdatePolicy, not a direct mod.UpdatePolicy mutation +
		// SaveInstalledMod: SaveInstalledMod deliberately preserves the
		// existing update_policy on an update (see its doc comment) so a
		// reinstall can never silently clear a user's --pin/--auto choice -
		// PlanUpdate (#289) re-reads the mod from the DB, so a policy change
		// that never reached the DB is no longer masked by trusting the
		// caller's in-memory copy the way pre-lift applySingleUpdate did.
		require.NoError(t, svc.SetModUpdatePolicy(context.Background(), "test-src", "mod1", "g1", "default", domain.UpdatePinned))

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		var doc core.UpdateApplyResult
		decodeSingleDoc(t, out, &doc)
		assert.Equal(t, "mod1", doc.Mod.ModID)
		assert.Equal(t, "1.0", doc.FromVersion)
		assert.Empty(t, doc.ToVersion)
		assert.Equal(t, "skipped", doc.Status.String())
		assert.Equal(t, "pinned", doc.Reason)
		assert.NotContains(t, out, "Unpin with", "the unpin hint is human-output only")
	})

	t.Run("available dry-run", func(t *testing.T) {
		withJSONOutput(t)
		svc, game, src := setupDoUpdateTest(t)
		updateDryRun = true
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
		// Deliberately no AddDownload: if applyUpdate were ever called under
		// dry-run, the download would 404 and surface as a visible failure.
		src.changelogs["mod1"] = "<b>Fixed</b> bugs."

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		var doc core.UpdateApplyResult
		decodeSingleDoc(t, out, &doc)
		assert.Equal(t, "1.0", doc.FromVersion)
		assert.Equal(t, "2.0", doc.ToVersion)
		assert.Equal(t, "Fixed bugs.", doc.Changelog, "JSON carries the cleaned changelog, not raw HTML")
		assert.Equal(t, "available", doc.Status.String())

		updated, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, "1.0", updated.Version, "dry-run must not mutate anything")
	})

	t.Run("verbose does not contaminate stdout", func(t *testing.T) {
		// FIX 1 regression: applyUpdate's progress callback used to gate its
		// "Downloading: %.1f%%" prints on verbose alone, so --json --verbose
		// leaked progress text onto stdout ahead of (and mixed into) the JSON
		// document. Now it must also require !jsonOutput.
		withJSONOutput(t)
		svc, game, src := setupDoUpdateTest(t)
		verbose = true
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
		// Kept under net/http's 2048-byte auto-Content-Length threshold so
		// TotalBytes is known and "Downloading: %.1f%%" would fire under plain
		// --verbose (see TestApplySingleUpdate_HappyPath_PrintsExpectedOutput's
		// "verbose" subtest) - proof this test would fail without FIX 1.
		src.AddDownload("new-1", []byte(strings.Repeat("x", 1024)))
		src.changelogs["mod1"] = "Fixed bugs."

		out := captureStdout(t, func() error {
			return applySingleUpdate(context.Background(), svc, game, mod, "default")
		})

		var doc core.UpdateApplyResult
		decodeSingleDoc(t, out, &doc)
		assert.Equal(t, "updated", doc.Status.String())
		assert.NotContains(t, out, "Downloading")
		assert.NotContains(t, out, "Updating")
	})

	t.Run("apply failure produces no document", func(t *testing.T) {
		withJSONOutput(t)
		svc, game, src := setupDoUpdateTest(t)
		mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "2.0", GameID: "g1"},
			[]domain.DownloadableFile{{ID: "new-1", FileName: "mod1-new.esp", IsPrimary: true}})
		// Deliberately no AddDownload: the download 404s, so ApplyUpdate fails.

		var callErr error
		out := captureStdout(t, func() error {
			callErr = applySingleUpdate(context.Background(), svc, game, mod, "default")
			return nil // capture output regardless of error; assert callErr separately
		})

		require.Error(t, callErr)
		assert.Empty(t, strings.TrimSpace(out), "a failed apply must not emit any JSON document")
	})
}

// TestDoUpdate_JSON_LocalMod covers doUpdate's own local-mod branch
// (update.go ~:155-159), reached via a single mod-id argument rather than
// applySingleUpdate directly.
func TestDoUpdate_JSON_LocalMod(t *testing.T) {
	withJSONOutput(t)
	svc, game := localUpdateGame(t)
	seedLocalMod(t, svc, game, "localA", "Local A")

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, []string{"localA"})
	})

	var doc core.UpdateApplyResult
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, "localA", doc.Mod.ModID)
	assert.Equal(t, "Local A", doc.Name)
	assert.Equal(t, "1.0", doc.FromVersion, "FIX 3: local-mod skip must report the installed version, matching the pinned branch")
	assert.Equal(t, "skipped", doc.Status.String())
	assert.Equal(t, "local", doc.Reason)
	assert.NotContains(t, out, "no remote source")
}

// TestDoUpdateRollback_JSON covers doUpdateRollback's success path: status
// "rolled_back", from/to reversed relative to the update that produced them.
func TestDoUpdateRollback_JSON(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupRollbackReadyMod(t)

	out := captureStdout(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "mod1")
	})

	var doc core.RollbackResult
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, "mod1", doc.Mod.ModID)
	assert.Equal(t, "test-src", doc.Mod.SourceID)
	assert.Equal(t, "1.0", doc.Mod.Version, "the ref carries the version rolled back TO")
	assert.Equal(t, "Mod One", doc.ModName)
	assert.Equal(t, "2.0", doc.FromVersion)
	assert.Equal(t, "1.0", doc.ToVersion)
	assert.Equal(t, "rolled_back", doc.Status.String())
	assert.NotContains(t, out, "Rolling back")
	assert.NotContains(t, out, "✓ Rolled back")
}

// TestDoUpdateRollback_JSON_VerboseDoesNotContaminateStdout is FIX 1's
// rollback-side regression: doUpdateRollback's own local progress func gated
// its UpdateNote print on verbose alone, same defect as applyUpdate's. There
// is no simple way to force an UpdateNote event here, so this pins the
// achievable half of the contract - exactly one JSON document on stdout, none
// of the human-output header/footer text - under --json --verbose together.
func TestDoUpdateRollback_JSON_VerboseDoesNotContaminateStdout(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupRollbackReadyMod(t)
	verbose = true

	out := captureStdout(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "mod1")
	})

	var doc core.UpdateApplyResult
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, "rolled_back", doc.Status.String())
	assert.NotContains(t, out, "Rolling back")
	assert.NotContains(t, out, "✓ Rolled back")
}

// TestDoUpdateRollback_JSON_GuardErrorNoDocument covers the "no previous
// version" guard: doUpdateRollback must fail before printing anything,
// JSON or otherwise - matching TestDoUpdateRollback_NoPreviousVersion_ReturnsExactError's
// non-JSON assertion but for the --json path.
func TestDoUpdateRollback_JSON_GuardErrorNoDocument(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupDoUpdateTest(t)
	mod := seedInstalledForUpdate(t, svc, game, "test-src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1.esp": []byte("content")})
	require.Empty(t, mod.PreviousVersion)

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdateRollback(context.Background(), svc, game, "mod1")
		return nil
	})

	require.Error(t, callErr)
	assert.Equal(t, "no previous version available for rollback", callErr.Error())
	assert.Empty(t, out, "the guard error must never emit a JSON document")
}

// TestDoUpdateRollback_JSON_NotInstalled_GuardErrorNoDocument covers the
// "mod not found" guard under --json (v2 Phase 2 Unit I Task 10
// characterization, #289 - this branch previously had no dedicated capture):
// doUpdateRollback must fail before printing anything, JSON or otherwise -
// matching TestDoUpdateRollback_NotInstalled_ReturnsExactError's non-JSON
// assertion but for the --json path.
func TestDoUpdateRollback_JSON_NotInstalled_GuardErrorNoDocument(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupDoUpdateTest(t)

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdateRollback(context.Background(), svc, game, "mod1")
		return nil
	})

	require.Error(t, callErr)
	assert.Equal(t, "mod not found: mod1", callErr.Error())
	assert.Empty(t, out, "the guard error must never emit a JSON document")
}

// TestDoUpdateRollback_JSON_MissingCache_GuardErrorNoDocument covers the
// "previous version not found in cache" guard under --json (v2 Phase 2 Unit
// I Task 10 characterization, #289 - this branch previously had no
// dedicated capture): doUpdateRollback must fail before printing anything,
// JSON or otherwise - matching TestDoUpdateRollback_MissingCache_ReturnsExactError's
// non-JSON assertion but for the --json path.
func TestDoUpdateRollback_JSON_MissingCache_GuardErrorNoDocument(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupRollbackReadyMod(t)
	require.NoError(t, svc.GetGameCache(game).Delete("g1", "test-src", "mod1", "1.0"))

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdateRollback(context.Background(), svc, game, "mod1")
		return nil
	})

	require.Error(t, callErr)
	assert.Equal(t, "previous version 1.0 not found in cache", callErr.Error())
	assert.Empty(t, out, "the guard error must never emit a JSON document")
}

// TestDoUpdate_JSON_ZeroInstalled_SingleMod is FIX 2's single-mod case:
// doUpdate's "No mods installed." shortcut used to return nil (exit 0)
// before --json ever got a look, silently swallowing what should be a
// not-found error. Under --json with a mod-id argument and zero installed
// mods, it must instead fall through to the standard lookup and produce the
// same "not found in profile" error any other absent mod would - no document,
// no early "No mods installed." text, so Execute's reportError renders the
// sole {"error":...}.
func TestDoUpdate_JSON_ZeroInstalled_SingleMod(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupDoUpdateTest(t)

	var callErr error
	out := captureStdout(t, func() error {
		callErr = doUpdate(context.Background(), svc, game, []string{"mod1"})
		return nil
	})

	require.Error(t, callErr)
	assert.Contains(t, callErr.Error(), "not found in profile")
	assert.Empty(t, out, "no document, and no 'No mods installed.' text, on the --json single-mod path")
}

// TestDoUpdate_JSON_ZeroInstalled_Bulk is FIX 2's bulk case: under --json
// with no mod-id argument and zero installed mods, doUpdate must emit the
// existing bulk core.UpdateCheckReport document (empty updates, zero skipped
// counts) instead of the plain-text "No mods installed." line, so a --json
// caller always gets a parseable document regardless of profile state.
func TestDoUpdate_JSON_ZeroInstalled_Bulk(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupDoUpdateTest(t)

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	var doc core.UpdateCheckReport
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, game.ID, doc.GameID)
	assert.Empty(t, doc.Updates)
	assert.Equal(t, 0, doc.Skipped.Pinned)
	assert.Equal(t, 0, doc.Skipped.Local)
	assert.Empty(t, doc.ErrorMessage)
}

// TestDoUpdate_PlainText_ZeroInstalled_Unchanged guards the other half of
// FIX 2: plain (non-JSON) mode must keep printing "No mods installed." and
// exiting 0 exactly as before, for both the bulk and single-mod forms - the
// fix is scoped to jsonOutput only.
func TestDoUpdate_PlainText_ZeroInstalled_Unchanged(t *testing.T) {
	t.Run("bulk", func(t *testing.T) {
		svc, game, _ := setupDoUpdateTest(t)

		var callErr error
		out := captureStdout(t, func() error {
			callErr = doUpdate(context.Background(), svc, game, nil)
			return nil
		})

		assert.NoError(t, callErr)
		assert.Equal(t, "No mods installed.\n", out)
	})

	t.Run("single mod", func(t *testing.T) {
		svc, game, _ := setupDoUpdateTest(t)

		var callErr error
		out := captureStdout(t, func() error {
			callErr = doUpdate(context.Background(), svc, game, []string{"mod1"})
			return nil
		})

		assert.NoError(t, callErr)
		assert.Equal(t, "No mods installed.\n", out)
	})
}
