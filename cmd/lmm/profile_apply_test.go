package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Characterization captures for doProfileApply, recorded on the PRE-LIFT
// code (v2 Phase 2 Unit J Task 14, #290) so the engine's move into
// core.PlanProfileApply/ApplyProfileApply is provably byte-identical. The 11
// TestDoProfileApply_* tests that already existed pin the diff buckets, the
// #94/#95/#96 semantics and the merged-pak sync; these add the printed lines
// they left uncovered:
//
//   - the plan header/footer and the disable+enable block, byte-exact
//   - the "Proceed? [Y/n]: " prompt and its "Cancelled." branch
//   - a disabled mod whose cache entry is gone (a re-download, NOT an enable)
//   - the install loop's per-mod error lines (fetch failure, no files) and
//     the fact that the loop continues past them
//   - the --verbose-only warnings: the disable loop's undeploy failure and
//     (ruling 9) the swallowed UpsertMod lock refusal
//
// Frozen from here on: a diff in any of these is a defect, never a
// re-record.

// applyYes sets --yes for the duration of a test, so doProfileApply runs
// without a prompt.
func applyYes(t *testing.T) {
	t.Helper()
	orig := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = orig })
}

// applyVerbose sets the global --verbose flag for the duration of a test.
func applyVerbose(t *testing.T, v bool) {
	t.Helper()
	orig := verbose
	verbose = v
	t.Cleanup(func() { verbose = orig })
}

// TestDoProfileApply_DisableAndEnable_PrintsExactOutput pins the complete
// console output of a mixed disable+enable apply: the "Applying profile"
// header and its blank line, both "Will ..." blocks with their item lines,
// the per-mod "✓ Disabled"/"✓ Enabled" lines in bucket order, and the
// trailing "✓ Applied profile" line preceded by its own blank line. No
// install bucket here on purpose - the install loop's download progress
// line is the only non-deterministic part of this command's output, so the
// byte-exact anchor covers everything around it and the install lines are
// pinned by Contains assertions elsewhere.
func TestDoProfileApply_DisableAndEnable_PrintsExactOutput(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	// Installed + enabled, absent from profile.Mods -> disable.
	seedApplyCandidateMod(t, svc, game, "src", "dis1", "Dis One", "1.0", true, map[string][]byte{"dis1.esp": []byte("dis")})
	// Installed + disabled + cached, referenced by profile.Mods -> enable.
	seedApplyCandidateMod(t, svc, game, "src", "en1", "En One", "1.0", false, map[string][]byte{"en1.esp": []byte("en")})
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "en1", Version: "1.0"}))

	applyYes(t)

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Applying profile: default\n\n"+
		"Will disable 1 mod(s):\n"+
		"  - Dis One (dis1)\n"+
		"Will enable 1 mod(s):\n"+
		"  + En One (en1)\n"+
		"  ✓ Disabled: Dis One\n"+
		"  ✓ Enabled: En One\n"+
		"\n✓ Applied profile: default\n", out)

	dis, err := svc.GetInstalledMod(context.Background(), "src", "dis1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, dis.Enabled, "the disabled mod's DB row must be flipped")
	en, err := svc.GetInstalledMod(context.Background(), "src", "en1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, en.Enabled, "the enabled mod's DB row must be flipped")
	_, err = os.Lstat(filepath.Join(game.ModPath, "en1.esp"))
	assert.NoError(t, err, "the enabled mod must be deployed")
}

// TestDoProfileApply_DeclinedPrompt_PrintsPromptAndCancels pins the prompt
// itself (printed with no trailing newline, so "Cancelled." lands on the
// same line) and the fact that declining mutates nothing. Note the prompt
// sits AFTER the whole plan print and BEFORE any mutation.
func TestDoProfileApply_DeclinedPrompt_PrintsPromptAndCancels(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "ins1", Version: "1.0"}))

	var out string
	withStdin(t, "n\n", func() {
		out = captureStdout(t, func() error {
			return doProfileApply(context.Background(), svc, game, nil)
		})
	})

	assert.Equal(t, "Applying profile: default\n\n"+
		"Will install 1 mod(s):\n"+
		"  ↓ src:ins1 v1.0\n"+
		"\nProceed? [Y/n]: "+
		"Cancelled.\n", out)

	_, err := svc.GetInstalledMod(context.Background(), "src", "ins1", game.ID, "default")
	assert.Error(t, err, "declining must not install anything")
}

// TestDoProfileApply_JSONOutputReturnsConfirmationRequired pins the
// non-interactive rule (v2 Phase 3 Ruling 2) at doProfileApply's "Proceed?"
// prompt: under --json with no -y, the apply must fail with
// core.ErrConfirmationRequired before ever reading stdin, and nothing gets
// installed.
func TestDoProfileApply_JSONOutputReturnsConfirmationRequired(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "ins1", Version: "1.0"}))
	withJSONOutput(t)

	err := assertStdinNeverRead(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	_, dbErr := svc.GetInstalledMod(context.Background(), "src", "ins1", game.ID, "default")
	assert.Error(t, dbErr, "must not install anything")
}

// TestDoProfileApply_DisabledModCacheGone_SchedulesRedownload pins the
// enable-bucket's cache guard: an installed-but-disabled profile mod whose
// cache entry has been pruned cannot simply be re-deployed, so it is
// scheduled as an INSTALL (carrying the DB row's own FileIDs, not the
// profile's) rather than an enable, and the apply re-downloads it.
func TestDoProfileApply_DisabledModCacheGone_SchedulesRedownload(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN"},
		})
	src.AddDownload("main", []byte("plugin content"))

	// Installed + disabled, FileIDs recorded, but NOTHING in the cache.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
		FileIDs:      []string{"main"},
	}))
	require.False(t, svc.GetGameCache(game).Exists(game.ID, "test-src", "mod1", "1.0"),
		"precondition: the cache entry must be gone")
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.0"}))

	applyYes(t)

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "Will install 1 mod(s):\n  ↓ test-src:mod1 v1.0\n")
	assert.NotContains(t, out, "Will enable", "a mod with no cache entry must not be classified as a plain enable")
	assert.Contains(t, out, "    ✓ Installed: Mod One\n")

	assert.True(t, svc.GetGameCache(game).Exists(game.ID, "test-src", "mod1", "1.0"), "the mod must be re-downloaded")
	installed, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, installed.Enabled, "the re-downloaded mod must end up enabled")
	assert.True(t, installed.Deployed)
	_, err = os.Lstat(filepath.Join(game.ModPath, "mod1.esp"))
	assert.NoError(t, err, "the re-downloaded mod must be deployed")
}

// TestDoProfileApply_InstallLoop_PerModErrorsContinue pins the install
// loop's own header/per-mod lines and its two uncovered mod-fatal error
// wordings (a failed GetMod and a mod the source lists no files for), plus
// the loop's continue-past-failure behaviour: a later, healthy mod still
// installs.
func TestDoProfileApply_InstallLoop_PerModErrorsContinue(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	// "ghost" is never registered with the source -> GetMod fails.
	src.AddMod(&domain.Mod{ID: "empty", SourceID: "test-src", Name: "Empty Mod", Version: "1.0", GameID: game.ID}, nil)
	src.AddMod(&domain.Mod{ID: "good", SourceID: "test-src", Name: "Good Mod", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main", FileName: "good.esp", IsPrimary: true, Category: "MAIN"},
		})
	src.AddDownload("main", []byte("plugin content"))

	for _, id := range []string{"ghost", "empty", "good"} {
		require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: id, Version: "1.0"}))
	}

	applyYes(t)

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "Applying profile: default\n\n")
	assert.Contains(t, out, "Will install 3 mod(s):\n  ↓ test-src:ghost v1.0\n  ↓ test-src:empty v1.0\n  ↓ test-src:good v1.0\n")
	assert.Contains(t, out, "\nInstalling missing mods...\n")
	assert.Contains(t, out, "  Installing test-src:ghost...\n    Error: failed to fetch mod: ")
	assert.Contains(t, out, "  Installing test-src:empty...\n    Error: no downloadable files\n")
	assert.Contains(t, out, "  Installing test-src:good...\n")
	assert.Contains(t, out, "    ✓ Installed: Good Mod\n")
	assert.Contains(t, out, "\n✓ Applied profile: default\n")

	_, err := svc.GetInstalledMod(context.Background(), "test-src", "ghost", game.ID, "default")
	assert.Error(t, err, "a mod that failed to fetch must not be installed")
	_, err = svc.GetInstalledMod(context.Background(), "test-src", "empty", game.ID, "default")
	assert.Error(t, err, "a mod with no downloadable files must not be installed")
	good, err := svc.GetInstalledMod(context.Background(), "test-src", "good", game.ID, "default")
	require.NoError(t, err, "the loop must continue past both failures")
	assert.Equal(t, "1.0", good.Version)
}

// TestDoProfileApply_VerboseNotePath_UndeployFailurePrintsUnderVerbose pins
// the disable loop's --verbose-only warning (a failed Uninstall) and the
// fact that the mod is still reported as disabled afterwards - the CLI twin
// of core's SwitchDisableNote contract.
func TestDoProfileApply_VerboseNotePath_UndeployFailurePrintsUnderVerbose(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)

	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")
	deployInstalledMod(t, svc, game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default")
	// Corrupt the deployed symlink so Uninstall fails deterministically.
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0o644))
	// Drop it from the profile so the apply classifies it as a disable.
	require.NoError(t, pm.RemoveMod(context.Background(), game.ID, "default", "src", "1"))

	applyYes(t)
	applyVerbose(t, true)

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "  Warning: failed to undeploy Test Mod: ")
	assert.Contains(t, out, "  ✓ Disabled: Test Mod\n")
}

// applyLockRefusalFixture seeds the one scenario every #294 apply capture
// needs: an installable "test-src:mod1" v1.1 whose profile ref is LOCKED
// with no recorded version, so the post-install UpsertMod (which records
// the version actually installed) hits the lock gate (#143).
func applyLockRefusalFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupDoProfileSwitchTest(t)

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"test-src": game.ID}

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.1", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.1"},
		})
	src.AddDownload("main", []byte("plugin content"))

	require.NoError(t, getProfileManager(svc).AddMod(context.Background(), game.ID, "default", domain.ModReference{
		SourceID: "test-src", ModID: "mod1", Locked: true,
	}))

	applyYes(t)
	return svc, game
}

// applyLockRefusalDetail is the exact ProfileApplyResult.Warnings entry
// #294 (Ruling 5) records for applyLockRefusalFixture's refusal: core wraps
// ProfileManager.UpsertMod's own refusal sentence as "could not update
// profile: <err>", with no "Warning: " prefix baked in.
// applyLockRefusalWarning is the stderr line the CLI renders from it.
const applyLockRefusalDetail = "could not update profile: mod is locked: test-src:mod1 is locked at v in profile \"default\" - refusing to record v1.1; move the lock with 'lmm mod lock -s test-src -p default mod1 <version>' or unlock with 'lmm mod unlock -s test-src -p default mod1'"

const applyLockRefusalWarning = "Warning: " + applyLockRefusalDetail + "\n"

// TestDoProfileApply_LockedRef_UpsertRefusalWarnsUnconditionally pins #294
// (Ruling 5), the behaviour fix ruling 9 deferred to Phase 3: the
// post-install ProfileManager.UpsertMod call records the version actually
// installed, a LOCKED profile ref refuses that write (#143), and
// doProfileApply now surfaces the refusal as an unconditional stderr
// "Warning: ..." line - identical with and without --verbose, and never on
// stdout - instead of the --verbose-only stdout note it used to be. The mod
// still counts as installed.
func TestDoProfileApply_LockedRef_UpsertRefusalWarnsUnconditionally(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verbose bool
	}{
		{"verbose", true},
		{"quiet", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := applyLockRefusalFixture(t)
			pm := getProfileManager(svc)
			applyVerbose(t, tc.verbose)

			out, stderr, err := captureStdoutStderrErr(t, func() error {
				return doProfileApply(context.Background(), svc, game, nil)
			})
			require.NoError(t, err)

			assert.Contains(t, out, "    ✓ Installed: Mod One\n", "the lock refusal must not fail the install")
			assert.Equal(t, applyLockRefusalWarning, stderr,
				"#294: the refusal is an unconditional stderr warning, identical at both verbosities")
			assert.NotContains(t, out, "could not update profile",
				"#294: the refusal left stdout entirely - it is no longer a --verbose-only note")

			installed, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "default")
			require.NoError(t, err)
			assert.Equal(t, "1.1", installed.Version)

			profile, err := pm.Get(context.Background(), game.ID, "default")
			require.NoError(t, err)
			require.Len(t, profile.Mods, 1)
			assert.Empty(t, profile.Mods[0].Version, "the locked ref must be left unwritten")
		})
	}
}

// TestDoProfileApply_LockedRef_UpsertRefusal_JSON is #294's --json framing
// sibling (Ruling 15 / Unit P): the new warning reaches the document via
// ProfileApplyResult.Warnings and NOT stderr - exactly one document on
// stdout, nothing on stderr.
func TestDoProfileApply_LockedRef_UpsertRefusal_JSON(t *testing.T) {
	svc, game := applyLockRefusalFixture(t)
	applyVerbose(t, false)

	var doc core.ProfileApplyResult
	raw := runJSONCommand(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	decodeSingleDoc(t, raw, &doc)

	assert.Equal(t, 1, doc.Installed)
	assert.Empty(t, doc.Notes, "#294: the refusal is no longer a note")
	require.Len(t, doc.Warnings, 1)
	assert.Equal(t, applyLockRefusalDetail, doc.Warnings[0])
}

// applyTwoModCancelFixture extends applyLockRefusalFixture with a second,
// unlocked, installable mod ("second-src:mod2") so the resulting
// ProfileApplyPlan.ToInstall has two entries: the locked mod1 first (whose
// UpsertMod refusal records the #294 warning), mod2 second. A fresh call
// builds a fully independent fixture (own temp dirs/service), so the same
// scenario can be run twice - once to measure, once to assert.
func applyTwoModCancelFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := applyLockRefusalFixture(t)
	pm := getProfileManager(svc)

	src := newFakeInstallSource("second-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	game.SourceIDs["second-src"] = game.ID
	src.AddMod(&domain.Mod{ID: "mod2", SourceID: "second-src", Name: "Mod Two", Version: "1.0", GameID: game.ID},
		[]domain.DownloadableFile{
			{ID: "main", Name: "Main", FileName: "mod2.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"},
		})
	src.AddDownload("main", []byte("plugin content"))
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "second-src", ModID: "mod2"}))

	return svc, game
}

// errCalledFromCore reports whether the caller of the current Err() method
// (one frame further up, i.e. two frames from HERE) is internal/core's own
// code - as opposed to database/sql's (*Rows).awaitDone, which watches a
// query's context on its OWN background goroutine and calls ctx.Err()
// asynchronously once the query completes. That watcher call is real but
// non-deterministic relative to the calling goroutine's own progress (it can
// land before or after any given line of core's code runs), so counting it
// would make cancelOnceTrue/cancelAfterNCalls racy; only core's own
// synchronous, same-goroutine ctx.Err() checks (beginOp, checkPlanFresh, the
// loops' own top-of-iteration checks) are deterministic enough to count.
func errCalledFromCore() bool {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return false
	}
	name := runtime.FuncForPC(pc).Name()
	return strings.Contains(name, "linux-mod-manager/v2/internal/core.")
}

// countingCoreContext counts every synchronous, same-goroutine ctx.Err()
// call a run's own code makes (see errCalledFromCore), reporting itself live
// throughout - used to MEASURE, rather than guess, exactly how many such
// checks a full, uncancelled run makes, so a later run's cancelAfterNCalls
// can target the loop's LAST such check (one less than the total) without
// pinning a magic number tied to beginOp/checkPlanFresh/loop internals that
// aren't this test's contract. Not goroutine-safe by design: the call it
// instruments runs on the calling goroutine only.
type countingCoreContext struct {
	context.Context
	calls int
}

func (c *countingCoreContext) Err() error {
	if errCalledFromCore() {
		c.calls++
	}
	return c.Context.Err()
}

// cancelAfterNCalls reports itself live for the first `live` synchronous,
// same-goroutine ctx.Err() calls a run's own code makes (see
// errCalledFromCore) and cancelled for every one after - mirrors
// internal/core/service_download_local_test.go's cancelAfterFirstEntry, with
// a configurable live count since a caller's own loop-top ctx.Err() checks
// are not the first ones a run makes (beginOp/checkPlanFresh check first).
// Not goroutine-safe by design: the loop it instruments runs on the calling
// goroutine only.
type cancelAfterNCalls struct {
	context.Context
	cancel context.CancelFunc
	live   int
	calls  int
}

func (c *cancelAfterNCalls) Err() error {
	if !errCalledFromCore() {
		return c.Context.Err()
	}
	c.calls++
	if c.calls > c.live {
		c.cancel()
	}
	return c.Context.Err()
}

// TestDoProfileApply_LockedRefWarningSurvivesFatalContextCancellation pins
// Task 13 review round 1's Important 1 fix for doProfileApply: unlike
// doProfileSwitch (whose fatal path is the final SetDefault call),
// ApplyProfileApply's ToInstall loop never returns fatally on its own - the
// only reachable "warning already recorded, then fatal" path is ctx
// cancellation between two ToInstall entries. The first entry (locked)
// records the #294 warning; cancellation must land on the second entry's
// loop-top ctx.Err() check, before it is ever processed. Before the fix,
// this warning was dropped because doProfileApply printed result.Warnings
// only on the success path.
//
// The exact number of ctx.Err() calls a run makes (beginOp, checkPlanFresh,
// each loop-top check, ...) is an internal-implementation detail, not a
// contract - so rather than pin a magic number, this measures it directly:
// an uninstrumented pass over an identical, independent fixture runs both
// mods to completion while counting every core-originated ctx.Err() call,
// then the real pass treats all but the LAST of those calls as live -
// guaranteeing cancellation lands on that last call, mod2's own loop-top
// check, before mod2 is ever touched.
func TestDoProfileApply_LockedRefWarningSurvivesFatalContextCancellation(t *testing.T) {
	countSvc, countGame := applyTwoModCancelFixture(t)
	counter := &countingCoreContext{Context: context.Background()}
	require.NoError(t, doProfileApply(counter, countSvc, countGame, nil),
		"the measurement pass must install both mods uncancelled")
	require.Positive(t, counter.calls, "the measurement pass must observe at least one core ctx.Err() check")

	svc, game := applyTwoModCancelFixture(t)
	inner, cancel := context.WithCancel(context.Background())
	// live is counter.calls-2, not -1: v2 Phase 3 Task 18 gave
	// ProfileManager.UpsertMod its own ctx.Err() guard, so mod2's iteration
	// now makes TWO core-originated checks (the loop-top check, then
	// UpsertMod's internal one) rather than one - the LAST measured call is
	// UpsertMod's, which runs after mod2 is already downloaded, deployed and
	// DB-saved. Targeting one call earlier still lands on mod2's loop-top
	// check, before any of that runs, preserving this test's original intent.
	ctx := &cancelAfterNCalls{Context: inner, cancel: cancel, live: counter.calls - 2}

	out, stderr, err := captureStdoutStderrErr(t, func() error {
		return doProfileApply(ctx, svc, game, nil)
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, applyLockRefusalWarning, stderr,
		"Important 1: the accumulated #294 warning must survive the fatal path, not be silently dropped")
	assert.Contains(t, out, "    ✓ Installed: Mod One\n")
	assert.NotContains(t, out, "Mod Two", "mod2 must never have been reached")

	_, err = svc.GetInstalledMod(context.Background(), "second-src", "mod2", game.ID, "default")
	assert.Error(t, err, "the second mod must never have been installed")
}

// TestDoProfileApply_CancellationInsideUpsertMod_IsFatalNotWarned pins the
// guard at internal/core/profile_apply.go's ToInstall loop, which the sibling
// test above does NOT reach: that one targets mod2's loop-top check (one call
// earlier), so removing the guard leaves it green.
//
// Here the cancellation lands on the LAST core-originated ctx.Err() call of a
// full run - UpsertMod's own guard, which fires only after mod2 has already
// been downloaded, deployed and written to the DB. The property is that the
// run still ends fatally: without the guard, ApplyProfileApply absorbs the
// cancellation into the #294 warning path (a business refusal), counts mod2
// as installed and returns success. mod2 IS in the DB either way - the
// cancellation arrives after that write, which is exactly why the warning
// path is the wrong home for it.
//
// v2 Phase 3 Ruling 16 deliberately leaves this site as a re-check rather
// than a completing write: the profile ref goes unwritten here, which is what
// keeps the accumulated #294 warning on stderr.
func TestDoProfileApply_CancellationInsideUpsertMod_IsFatalNotWarned(t *testing.T) {
	countSvc, countGame := applyTwoModCancelFixture(t)
	counter := &countingCoreContext{Context: context.Background()}
	require.NoError(t, doProfileApply(counter, countSvc, countGame, nil),
		"the measurement pass must install both mods uncancelled")
	require.Positive(t, counter.calls, "the measurement pass must observe at least one core ctx.Err() check")

	svc, game := applyTwoModCancelFixture(t)
	inner, cancel := context.WithCancel(context.Background())
	ctx := &cancelAfterNCalls{Context: inner, cancel: cancel, live: counter.calls - 1}

	_, stderr, err := captureStdoutStderrErr(t, func() error {
		return doProfileApply(ctx, svc, game, nil)
	})

	require.Error(t, err, "a cancellation inside UpsertMod must not be absorbed as a business refusal")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, applyLockRefusalWarning, stderr,
		"the accumulated #294 warning must still reach stderr on this fatal path")

	_, err = svc.GetInstalledMod(context.Background(), "second-src", "mod2", game.ID, "default")
	assert.NoError(t, err, "the cancellation lands AFTER mod2's DB write - that is the window this guard covers")
}
