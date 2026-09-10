package main

// snapshot_test.go covers the `lmm snapshot` command surface (#350): the
// four subcommands' plain-text rendering, the --dry-run/--json ordering
// rule (Ruling 15), and the refusal that a partial restore must never read
// as a complete one.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupSnapshotTest builds a service with one deployed mod over a stock
// file, resets the snapshot flags, and returns both.
func setupSnapshotTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: t.TempDir(), ModPath: gameDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	stock := filepath.Join(gameDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0o755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0o644))

	seedSnapshotMod(t, svc, game, "keeper", "Keeper", "Data/keeper.esp")
	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	old := struct {
		profile, name              string
		yes, dry, force, noSafety  bool
		json, verb, nocolor, hooks bool
	}{snapshotProfile, snapshotName, snapshotYes, snapshotRestoreDry, snapshotRestoreForce, snapshotNoSafety, jsonOutput, verbose, noColor, noHooks}
	snapshotProfile, snapshotName = "", ""
	snapshotYes, snapshotRestoreDry, snapshotRestoreForce, snapshotNoSafety = true, false, false, false
	jsonOutput, verbose, noColor, noHooks = false, false, true, false
	t.Cleanup(func() {
		snapshotProfile, snapshotName = old.profile, old.name
		snapshotYes, snapshotRestoreDry, snapshotRestoreForce, snapshotNoSafety = old.yes, old.dry, old.force, old.noSafety
		jsonOutput, verbose, noColor, noHooks = old.json, old.verb, old.nocolor, old.hooks
	})
	return svc, game
}

func seedSnapshotMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, file string) {
	t.Helper()
	ctx := context.Background()
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", modID, "1.0", file, []byte(name+" v1")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: modID, SourceID: "src", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	require.NoError(t, svc.NewProfileManager().UpsertMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: modID, Version: "1.0"}))
}

func TestSnapshotCreate_PrintsWhatItRecorded(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	snapshotName = "before-tweaks"

	out := captureStdout(t, func() error {
		result, err := svc.CreateSnapshot(context.Background(), game, "default", snapshotName)
		if err != nil {
			return err
		}
		printSnapshotCreated(result, game)
		return nil
	})
	assert.Contains(t, out, "Recorded snapshot before-tweaks for Game (profile: default)")
	assert.Contains(t, out, "1 mod(s), 1 deployed file(s), 0 stored original(s)",
		"nothing has been replaced yet in this fixture, and the count must say so")
	assert.Contains(t, out, "before-tweaks.json")
}

func TestSnapshotList_EmptyTellsTheUserHowToMakeOne(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	out := captureStdout(t, func() error { return doSnapshotList(context.Background(), svc, game) })
	assert.Contains(t, out, "No snapshots for Game")
	assert.Contains(t, out, "lmm snapshot create --game g1")
}

func TestSnapshotList_RendersNewestFirstAndMarksAutomaticOnes(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "older")
	require.NoError(t, err)
	_, err = svc.CreateSnapshot(ctx, game, "default", core.AutoSnapshotName(core.OpDeploy, time.Now()))
	require.NoError(t, err)

	out := captureStdout(t, func() error { return doSnapshotList(ctx, svc, game) })
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "older")
	assert.Contains(t, out, "auto-deploy-")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.GreaterOrEqual(t, len(lines), 4)
}

func TestSnapshotDelete_SaysTheOriginalsWereKept(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "doomed")
	require.NoError(t, err)

	out := captureStdout(t, func() error { return doSnapshotDelete(ctx, svc, game, "doomed") })
	assert.Contains(t, out, "Deleted snapshot doomed.")
	assert.Contains(t, out, "stored originals were kept")
}

// TestSnapshotRestore_DryRunChangesNothingAndNamesEveryStage pins the
// preview: it is the whole basis on which a user says yes.
func TestSnapshotRestore_DryRunChangesNothingAndNamesEveryStage(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	seedSnapshotMod(t, svc, game, "wrecker", "Wrecker", "Data/shipped.esp")
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	wrecked, err := os.ReadFile(filepath.Join(game.ModPath, "Data", "shipped.esp"))
	require.NoError(t, err)

	snapshotRestoreDry = true
	out := captureStdout(t, func() error { return doSnapshotRestore(ctx, svc, game, "known-good") })

	assert.Contains(t, out, "Restore Game to snapshot known-good")
	assert.Contains(t, out, "Will undeploy 2 mod(s).")
	assert.Contains(t, out, "Will put back 1 stored original(s).")
	assert.Contains(t, out, "Will restore 1 mod(s) at their recorded versions")
	assert.Contains(t, out, "Keeper 1.0")

	still, err := os.ReadFile(filepath.Join(game.ModPath, "Data", "shipped.esp"))
	require.NoError(t, err)
	assert.Equal(t, wrecked, still, "a dry run must change nothing")
}

// TestSnapshotRestore_ReportsTheRestore pins the readout, including the
// line that says the restore itself is reversible.
func TestSnapshotRestore_ReportsTheRestore(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	seedSnapshotMod(t, svc, game, "wrecker", "Wrecker", "Data/shipped.esp")
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	out := captureStdout(t, func() error { return doSnapshotRestore(ctx, svc, game, "known-good") })
	assert.Contains(t, out, "Undeploying the current mods...")
	assert.Contains(t, out, "Putting back 1 stored original(s)...")
	assert.Contains(t, out, "Restored known-good.")
	assert.Contains(t, out, "Originals put back: 1")
	assert.Contains(t, out, "The state before this restore was recorded as auto-snapshot_restore-")

	restored, err := os.ReadFile(filepath.Join(game.ModPath, "Data", "shipped.esp"))
	require.NoError(t, err)
	assert.Equal(t, "as the game shipped", string(restored))
}

// TestSnapshotRestore_DeclinedAtThePromptChangesNothing pins the negative
// answer, which for a destructive command is the one that has to be right.
func TestSnapshotRestore_DeclinedAtThePromptChangesNothing(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	seedSnapshotMod(t, svc, game, "wrecker", "Wrecker", "Data/shipped.esp")
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	snapshotYes = false
	stdinFromString(t, "n\n")
	err = doSnapshotRestore(ctx, svc, game, "known-good")
	require.ErrorIs(t, err, ErrCancelled)

	still, err := os.ReadFile(filepath.Join(game.ModPath, "Data", "shipped.esp"))
	require.NoError(t, err)
	assert.Equal(t, "Wrecker v1", string(still), "a declined restore leaves the game exactly as it was")
}

// TestSnapshotRestore_JSONRefusesTheUnanswerablePrompt pins Ruling 2: under
// --json stdin is never read, so a run with no --yes refuses instead of
// hanging.
func TestSnapshotRestore_JSONRefusesTheUnanswerablePrompt(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	snapshotYes, jsonOutput = false, true
	err = doSnapshotRestore(ctx, svc, game, "known-good")
	require.ErrorIs(t, err, core.ErrConfirmationRequired)
}

func TestSnapshotRestore_UnknownNameIsReportedAsNotFound(t *testing.T) {
	svc, game := setupSnapshotTest(t)
	err := doSnapshotRestore(context.Background(), svc, game, "never-taken")
	require.ErrorIs(t, err, core.ErrSnapshotNotFound)
}

func TestHumanBytes(t *testing.T) {
	for _, tt := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {-1, "0 B"}, {512, "512 B"},
		{2048, "2.0 KB"}, {1024 * 1024 * 3 / 2, "1.5 MB"},
	} {
		assert.Equal(t, tt.want, humanBytes(tt.in))
	}
}

// stdinFromString points os.Stdin at s for the duration of the test - the
// only way to answer a prompt that reads os.Stdin directly.
func stdinFromString(t *testing.T, s string) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(s)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
}

// TestReportError_JSON_SnapshotRestorePartialError pins the --json error
// envelope for a restore that failed partway through (#350). It is the one
// failure in the product where "it failed" is not enough to act on: the
// user's next move depends entirely on which stage stopped, so the
// envelope's "details" carries the SAME result document a successful
// restore returns.
func TestReportError_JSON_SnapshotRestorePartialError(t *testing.T) {
	withJSONOutput(t)

	err := &core.SnapshotRestorePartialError{
		Err: errors.New("converging back to known-good: disk full"),
		Result: &core.SnapshotRestoreResult{
			Snapshot: "known-good", Profile: "default",
			SafetySnapshot: "auto-snapshot_restore-20260909-140506",
			Purged:         3, OriginalsRestored: 2,
		},
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"converging back to known-good: disk full\",\n"+
		"  \"details\": {\n"+
		"    \"result\": {\n"+
		"      \"snapshot\": \"known-good\",\n"+
		"      \"profile\": \"default\",\n"+
		"      \"safety_snapshot\": \"auto-snapshot_restore-20260909-140506\",\n"+
		"      \"purged\": 3,\n"+
		"      \"originals_restored\": 2,\n"+
		"      \"disabled\": 0,\n"+
		"      \"enabled\": 0,\n"+
		"      \"installed\": 0,\n"+
		"      \"replaced\": 0,\n"+
		"      \"deployed\": 0\n"+
		"    }\n"+
		"  }\n"+
		"}\n", out)
}

// TestSnapshotRestorePartialError_UnwrapsToTheFailure pins that the
// wrapper does not hide what went wrong from errors.Is.
func TestSnapshotRestorePartialError_UnwrapsToTheFailure(t *testing.T) {
	inner := errors.New("disk full")
	err := error(&core.SnapshotRestorePartialError{Err: inner, Result: &core.SnapshotRestoreResult{}})
	require.ErrorIs(t, err, inner)
	assert.Equal(t, "disk full", err.Error())
}
