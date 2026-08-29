package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDoUpdateRecompileTest builds a DeployCompile game with a registered
// merge-compiler-capable source and an ENABLED exmodz mod, deliberately
// leaving the merged pak un-generated so `lmm update` reports/applies a
// #197 merge-needed row end to end through the CLI. linkMethod is LinkCopy
// so a successful regen+redeploy is provable from the on-disk deployed
// bytes (a symlink would trivially reflect an in-place cache swap on its
// own).
func setupDoUpdateRecompileTest(t *testing.T) (*core.Service, *domain.Game, *compilerInstallSource, string) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	oldSource, oldProfile, oldAll, oldDryRun, oldForce := updateSource, updateProfile, updateAll, updateDryRun, updateForce
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	updateSource = "fake-compiler"
	updateProfile = ""
	updateAll = false
	updateDryRun = false
	updateForce = false
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		updateSource, updateProfile, updateAll, updateDryRun, updateForce = oldSource, oldProfile, oldAll, oldDryRun, oldForce
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	const modID, version, fileID = "bear-mount", "3.3", "exmodz-file-id"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("retained-exmodz-bytes")))

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{fileID},
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), im))

	pm := svc.NewProfileManager()
	_, cerr := pm.Create(game.ID, "default")
	require.NoError(t, cerr)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	return svc, game, compiler, filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
}

// TestDoUpdate_JSON_ReportsRecompileNeeded proves the bulk --json contract
// gained the additive recompile_needed/reason fields (#196) without
// disturbing the rest of the row shape.
func TestDoUpdate_JSON_ReportsRecompileNeeded(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := doUpdate(context.Background(), svc, game, nil)
	_ = w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	var out core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	require.Len(t, out.Updates, 1)
	row := out.Updates[0]
	// #197: a merged-pak staleness row's identity is the SYNTHETIC
	// merged-pak mod (domain.SourceMerged/"merged-pak"), not the
	// contributing "bear-mount" mod - CheckMergedPakStaleness is
	// profile-scoped, not per-mod.
	assert.Equal(t, "merged-pak", row.InstalledMod.ID)
	assert.Equal(t, "merged", row.InstalledMod.Version)
	assert.Equal(t, "merged", row.NewVersion, "a staleness row's available version equals current - nothing has a real version change")
	assert.True(t, row.RecompileNeeded)
	assert.Equal(t, "base pak updated", row.RecompileReason,
		"the wire now carries core's own reason, not the CLI's hardcoded \"stale_compile\"")
}

// TestApplySingleUpdate_Recompile_AppliesAndRedeploys drives `lmm update
// bear-mount` end to end: the stale compile is recompiled from its retained
// source and redeployed, proven via the on-disk (LinkCopy) deployed bytes.
func TestApplySingleUpdate_Recompile_AppliesAndRedeploys(t *testing.T) {
	svc, game, compiler, deployedPath := setupDoUpdateRecompileTest(t)

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	err = applySingleUpdate(context.Background(), svc, game, mod, "default")
	require.NoError(t, err)
	assert.Equal(t, 1, compiler.compileCalls)

	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "retained-exmodz-bytes", string(data), "redeploy must reflect the freshly recompiled bytes")
}

// TestApplySingleUpdate_Recompile_JSON proves the single-mod --json contract
// reports "recompiled" for an applied staleness row, with ToVersion equal
// to FromVersion.
func TestApplySingleUpdate_Recompile_JSON(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err = applySingleUpdate(context.Background(), svc, game, mod, "default")
	_ = w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	var out core.UpdateApplyResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "recompiled", out.Status.String())
	assert.Equal(t, "3.3", out.FromVersion)
	assert.Equal(t, "3.3", out.ToVersion)
}

// TestApplySingleUpdate_RecompileLocked_Text characterizes the recompile
// branch's locked refusal (text) before the Task 9 PlanUpdate lift - no
// existing test combined "recompile needed" with "locked".
func TestApplySingleUpdate_RecompileLocked_Text(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	setLockedForUpdate(t, svc, game, "fake-compiler", "bear-mount", "3.3")

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, "Recompile needed for Bear Mount (base pak updated) — but it is locked at v3.3.")
	assert.Contains(t, out, "Move the lock: lmm mod lock -s fake-compiler -p default bear-mount 3.3   |   Unlock: lmm mod unlock -s fake-compiler -p default bear-mount")
	assert.NotContains(t, out, "Recompiling", "must never print the applying header - it never applies")
	assert.Equal(t, 0, compiler.compileCalls, "a locked mod must never actually recompile")
}

// TestApplySingleUpdate_RecompileLocked_JSON is
// TestApplySingleUpdate_RecompileLocked_Text's --json sibling.
func TestApplySingleUpdate_RecompileLocked_JSON(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	setLockedForUpdate(t, svc, game, "fake-compiler", "bear-mount", "3.3")
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	var doc core.UpdateApplyResult
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "bear-mount", doc.Mod.ModID)
	assert.Equal(t, "3.3", doc.FromVersion)
	assert.Equal(t, "3.3", doc.ToVersion)
	assert.Equal(t, "skipped", doc.Status.String())
	assert.Equal(t, "locked", doc.Reason)
}

// TestApplySingleUpdate_RecompileDryRun_Text characterizes the recompile
// branch's --dry-run text (unlocked) before the Task 9 PlanUpdate lift - no
// existing test drove this branch under --dry-run.
func TestApplySingleUpdate_RecompileDryRun_Text(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	updateDryRun = true

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Equal(t, "Recompiling Bear Mount (base pak updated)...\n(dry-run: no changes applied)\n", out)
	assert.Equal(t, 0, compiler.compileCalls, "dry-run must never actually recompile")
}

// TestApplySingleUpdate_RecompileDryRun_JSON is
// TestApplySingleUpdate_RecompileDryRun_Text's --json sibling.
func TestApplySingleUpdate_RecompileDryRun_JSON(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	updateDryRun = true
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	var doc core.UpdateApplyResult
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "bear-mount", doc.Mod.ModID)
	assert.Equal(t, "3.3", doc.FromVersion)
	assert.Equal(t, "3.3", doc.ToVersion)
	assert.Equal(t, "recompile_available", doc.Status.String())
	assert.Equal(t, 0, compiler.compileCalls, "dry-run must never actually recompile")
}

// TestApplySingleUpdate_Recompile_PrintsExpectedText characterizes the
// recompile branch's applied-success text before the Task 9 PlanUpdate lift:
// TestApplySingleUpdate_Recompile_AppliesAndRedeploys proves the file-level
// effect but never asserted the printed header/footer.
func TestApplySingleUpdate_Recompile_PrintsExpectedText(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, "Recompiling Bear Mount (base pak updated)...\n")
	assert.Contains(t, out, "\n✓ Recompiled: Bear Mount (base pak updated)\n")
}

// TestApplySingleUpdate_Recompile_PrintsMergeWarnings is the #197 M4
// regression test: ApplyMergedPakRegen's merge warnings (e.g. an
// asset-path collision - "a loud warning" per the CHANGELOG) travel
// through result.Warnings, not a progress event (ApplyMergedPakRegen only
// ever emits UpdateDownloadDone) - applyRecompile used to watch for
// UpdateWarning/UpdateNote progress phases that never fire, silently
// dropping every merge warning on `lmm update`'s apply path.
func TestApplySingleUpdate_Recompile_PrintsMergeWarnings(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	compiler.mergeWarnings = []string{"asset collision: fixture warning"}

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	oldStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	os.Stderr = w
	err = applySingleUpdate(context.Background(), svc, game, mod, "default")
	_ = w.Close()
	os.Stderr = oldStderr
	require.NoError(t, err)

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	assert.Contains(t, buf.String(), "asset collision: fixture warning", "a merge warning must reach the CLI, not be silently dropped")
}

// TestApplySingleUpdate_Recompile_JSON_MergeWarningsNoStderrLeak is
// TestApplySingleUpdate_Recompile_PrintsMergeWarnings' --json sibling and
// pins Task 11 re-review round 2 (the applyRecompile leak the round-1
// re-review flagged as the same defect class as New Finding 1):
// applyRecompile's own unconditional stderr print of ApplyMergedPakRegen's
// merge warnings ran even under --json, both leaking onto stderr and
// duplicating the warnings the jsonOutput branch below re-attaches to the
// document.
func TestApplySingleUpdate_Recompile_JSON_MergeWarningsNoStderrLeak(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	compiler.mergeWarnings = []string{"asset collision: fixture warning"}
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	mod, err := svc.GetInstalledMod(context.Background(), "fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	stdout, stderr, applyErr := captureStdoutStderrErr(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})
	require.NoError(t, applyErr)
	assert.Empty(t, stderr, "a merge warning must not leak to stderr under --json (Ruling 15)")

	var doc core.UpdateApplyResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &doc))
	assert.Contains(t, doc.Warnings, "asset collision: fixture warning", "the warning must still reach the document")
}
