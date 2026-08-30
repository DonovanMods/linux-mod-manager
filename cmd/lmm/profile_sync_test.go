package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Characterization captures for doProfileSync, recorded on the PRE-LIFT code
// (v2 Phase 2 Unit J Task 15, #290) so the engine's move into
// core.PlanProfileSync/ApplyProfileSync is provably byte-identical. Only one
// test existed before this file (profile_compile_test.go's
// TestDoProfileSync_DeployCompile_AddingDriftedModDeploysMergedPak, which
// stays as-is); these add:
//
//   - each diff bucket (add/remove/update), byte-exact
//   - the missing-profile auto-create path
//   - the already-in-sync fast path (no prompt read at all)
//   - the "Proceed? [Y/n]: " prompt and its "Cancelled." branch
//   - Ruling 9: a LOCKED profile ref makes the toUpdate loop's UpsertMod
//     refuse, swallowed into a --verbose-only warning
//   - the end-of-sync merged-pak failure, printed unconditionally to stderr
//     regardless of --verbose (unlike the three loops' own diagnostics)
//
// Frozen from here on: a diff in any of these is a defect, never a
// re-record.

// syncVerbose sets the global --verbose flag for the duration of a test.
func syncVerbose(t *testing.T, v bool) {
	t.Helper()
	orig := verbose
	verbose = v
	t.Cleanup(func() { verbose = orig })
}

// seedSyncInstalledMod saves an installed-mod DB row directly (no cache
// entry, no deploy) - doProfileSync's diff never looks past
// GetInstalledMods/GetInstalledMod, so a bare DB row is all any of these
// tests need.
func seedSyncInstalledMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, version, profileName string, enabled bool, fileIDs []string) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: version, GameID: game.ID},
		ProfileName:  profileName,
		Enabled:      enabled,
		FileIDs:      fileIDs,
		UpdatePolicy: domain.UpdateNotify,
	}))
}

// TestDoProfileSync_ToAdd_PrintsAndAddsMod pins the toAdd bucket: a mod
// installed+enabled in the DB but absent from profile.Mods is added, with
// its display name resolved via GetInstalledMod ("+ Name (source:id)").
func TestDoProfileSync_ToAdd_PrintsAndAddsMod(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Syncing profile: default\n\n"+
		"Will add to profile:\n"+
		"  + Add One (src:add1)\n"+
		"\nProceed? [Y/n]: "+
		"✓ Synced profile: default\n", out)

	pm := getProfileManager(svc)
	profile, err := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "add1", profile.Mods[0].ModID)
}

// TestDoProfileSync_ToRemove_PrintsAndRemovesRef pins the toRemove bucket: a
// profile ref with no matching enabled DB row is removed. Unlike toAdd/
// toUpdate, doProfileSync never resolved a display name for this bucket -
// it always printed the bare "source:id".
func TestDoProfileSync_ToRemove_PrintsAndRemovesRef(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "rem1", Version: "1.0"}))

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Syncing profile: default\n\n"+
		"Will remove from profile:\n"+
		"  - src:rem1\n"+
		"\nProceed? [Y/n]: "+
		"✓ Synced profile: default\n", out)

	profile, err := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods)
}

// TestDoProfileSync_ToUpdate_PrintsAndBackfillsFileIDs pins the toUpdate
// bucket: a mod present in both, where the DB row carries FileIDs the
// profile's own ref is missing, is backfilled via UpsertMod.
func TestDoProfileSync_ToUpdate_PrintsAndBackfillsFileIDs(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "upd1", "Upd One", "1.0", "default", true, []string{"main"})
	pm := getProfileManager(svc)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "upd1", Version: "1.0"}))

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Syncing profile: default\n\n"+
		"Will update FileIDs for:\n"+
		"  ~ Upd One (src:upd1)\n"+
		"\nProceed? [Y/n]: "+
		"✓ Synced profile: default\n", out)

	profile, err := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, []string{"main"}, profile.Mods[0].FileIDs)
}

// TestDoProfileSync_MissingProfile_AutoCreatesThenAdds pins the
// auto-create path: a profile with no profile.yaml on disk is created
// on the fly, then diffed as if it started empty.
func TestDoProfileSync_MissingProfile_AutoCreatesThenAdds(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "auto1", "New Auto", "1.0", "newprof", true, nil)

	pm := getProfileManager(svc)
	_, err := pm.Get(context.Background(), game.ID, "newprof")
	require.Error(t, err, "precondition: newprof must not exist yet")

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, []string{"newprof"})
	})

	assert.Equal(t, "Syncing profile: newprof\n\n"+
		"Will add to profile:\n"+
		"  + New Auto (src:auto1)\n"+
		"\nProceed? [Y/n]: "+
		"✓ Synced profile: newprof\n", out)

	profile, err := pm.Get(context.Background(), game.ID, "newprof")
	require.NoError(t, err, "the profile must have been auto-created")
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "auto1", profile.Mods[0].ModID)
}

// TestDoProfileSync_AlreadyInSync_PrintsMessageWithoutPrompting pins the
// no-changes fast path: when all three buckets are empty, doProfileSync
// prints its message and returns without ever reading the confirmation
// prompt.
func TestDoProfileSync_AlreadyInSync_PrintsMessageWithoutPrompting(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})

	assert.Equal(t, "Profile default is already in sync.\n", out)
}

// TestDoProfileSync_MissingProfile_EmptyDiff_StillCreatesProfile pins the
// pre-lift engine's other missing-profile behavior (review Important #1 on
// Task 15, #290): pm.Create fired unconditionally on ErrProfileNotFound
// BEFORE the diff was even computed, so a profile name with nothing to sync
// into it still got a profile.yaml written - silently, with the same
// "already in sync" message and no prompt.
func TestDoProfileSync_MissingProfile_EmptyDiff_StillCreatesProfile(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)

	pm := getProfileManager(svc)
	_, err := pm.Get(context.Background(), game.ID, "newprof")
	require.Error(t, err, "precondition: newprof must not exist yet")

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, []string{"newprof"})
	})

	assert.Equal(t, "Profile newprof is already in sync.\n", out)

	profile, err := pm.Get(context.Background(), game.ID, "newprof")
	require.NoError(t, err, "a missing profile must still be created even with nothing to sync into it")
	assert.Empty(t, profile.Mods)
}

// TestDoProfileSync_DeclinedPrompt_PrintsPromptAndCancels pins the prompt
// itself (printed with no trailing newline, so "Cancelled." lands on the
// same line) and the fact that declining mutates nothing.
func TestDoProfileSync_DeclinedPrompt_PrintsPromptAndCancels(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "dec1", "Decline Me", "1.0", "default", true, nil)

	var out string
	withStdin(t, "n\n", func() {
		out = captureStdout(t, func() error {
			return doProfileSync(context.Background(), svc, game, nil)
		})
	})

	assert.Equal(t, "Syncing profile: default\n\n"+
		"Will add to profile:\n"+
		"  + Decline Me (src:dec1)\n"+
		"\nProceed? [Y/n]: "+
		"Cancelled.\n", out)

	pm := getProfileManager(svc)
	profile, err := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "declining must not mutate the profile")
}

// TestDoProfileSync_JSONOutputReturnsConfirmationRequired pins the
// non-interactive rule (v2 Phase 3 Ruling 2) at doProfileSync's "Proceed?"
// prompt: under --json with no -y, the sync must fail with
// core.ErrConfirmationRequired before ever reading stdin, and the profile
// must not be mutated.
func TestDoProfileSync_JSONOutputReturnsConfirmationRequired(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)
	withJSONOutput(t)

	err := assertStdinNeverRead(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})

	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	pm := getProfileManager(svc)
	profile, perr := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, perr)
	assert.Empty(t, profile.Mods, "must not mutate the profile")
}

// TestDoProfileSync_YesFlagSkipsPromptEntirely pins -y: the prompt text
// never prints and the sync proceeds without reading stdin.
func TestDoProfileSync_YesFlagSkipsPromptEntirely(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)
	oldYes := profileSyncYes
	profileSyncYes = true
	t.Cleanup(func() { profileSyncYes = oldYes })

	out := captureStdout(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})

	assert.NotContains(t, out, "Proceed?")
	assert.Equal(t, "Syncing profile: default\n\n"+
		"Will add to profile:\n"+
		"  + Add One (src:add1)\n"+
		"✓ Synced profile: default\n", out)

	pm := getProfileManager(svc)
	profile, perr := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, perr)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "add1", profile.Mods[0].ModID)
}

// TestDoProfileSync_YesFlagUnderJSON_ProceedsWithoutReadingStdin guards the
// combination the Task 9 review flagged as untested: -y under --json
// together completes the sync rather than hitting the --json/stdin guard.
func TestDoProfileSync_YesFlagUnderJSON_ProceedsWithoutReadingStdin(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	withJSONOutput(t)
	seedSyncInstalledMod(t, svc, game, "src", "add1", "Add One", "1.0", "default", true, nil)
	oldYes := profileSyncYes
	profileSyncYes = true
	t.Cleanup(func() { profileSyncYes = oldYes })

	var syncErr error
	out := captureStdout(t, func() error {
		syncErr = assertStdinNeverRead(t, func() error {
			return doProfileSync(context.Background(), svc, game, nil)
		})
		return nil
	})

	require.NoError(t, syncErr)
	assert.NotContains(t, out, "Proceed?")
	// v2 Phase 3 Ruling 15: under --json the run's whole output is the
	// ProfileSyncResult document - the preview and the "✓ Synced" line the
	// plain path prints are suppressed, so the sync is asserted from the
	// document (and from the profile itself).
	var doc core.ProfileSyncResult
	decodeSingleDoc(t, out, &doc)
	assert.Equal(t, 1, doc.Added)

	profile, err := getProfileManager(svc).Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "add1", profile.Mods[0].ModID)
}

// syncLockRefusalDetail is the exact ProfileSyncResult.Warnings entry #294
// (Ruling 5) records when the toUpdate loop's UpsertMod is refused by a
// LOCKED ref: core wraps ProfileManager.UpsertMod's own refusal sentence as
// "could not update <source>:<mod>: <err>", with no "Warning: " prefix
// baked in. syncLockRefusalWarning is the stderr line the CLI renders.
const syncLockRefusalDetail = "could not update src:lock1: mod is locked: src:lock1 is locked at v1.0 in profile \"default\" - refusing to record v2.0; move the lock with 'lmm mod lock -s src -p default lock1 <version>' or unlock with 'lmm mod unlock -s src -p default lock1'"

const syncLockRefusalWarning = "Warning: " + syncLockRefusalDetail + "\n"

// syncLockRefusalFixture seeds the one scenario every #294 sync capture
// needs: an installed "src:lock1" at v2.0 whose profile ref is LOCKED at
// v1.0, so the toUpdate loop's UpsertMod (the ref IS the lock's target, and
// the DB's version differs) is refused.
func syncLockRefusalFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupDoProfileSwitchTest(t)
	seedSyncInstalledMod(t, svc, game, "src", "lock1", "Locked One", "2.0", "default", true, []string{"main"})
	require.NoError(t, getProfileManager(svc).AddMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "lock1", Version: "1.0", Locked: true}))
	return svc, game
}

// TestDoProfileSync_ToUpdate_LockedRefRefusalWarnsUnconditionally pins #294
// (Ruling 5), the behaviour fix Ruling 9 deferred to Phase 3: a LOCKED
// profile ref makes the toUpdate loop's UpsertMod refuse, and doProfileSync
// now surfaces that refusal as an unconditional stderr "Warning: ..." line -
// identical with and without --verbose, and never on stdout - instead of the
// --verbose-only stdout warning it used to be. Still never fatal to the sync.
func TestDoProfileSync_ToUpdate_LockedRefRefusalWarnsUnconditionally(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verbose bool
	}{
		{"quiet", false},
		{"verbose", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := syncLockRefusalFixture(t)
			syncVerbose(t, tc.verbose)

			out, stderr, err := captureStdoutStderrErr(t, func() error {
				return doProfileSync(context.Background(), svc, game, nil)
			})
			require.NoError(t, err)

			assert.Contains(t, out, "✓ Synced profile: default\n", "the refusal must not be fatal to the sync")
			assert.NotContains(t, out, "Warning:", "#294: the refusal left stdout entirely")
			assert.Equal(t, syncLockRefusalWarning, stderr,
				"#294: the refusal is an unconditional stderr warning, identical at both verbosities")

			profile, err := getProfileManager(svc).Get(context.Background(), game.ID, "default")
			require.NoError(t, err)
			require.Len(t, profile.Mods, 1)
			assert.Empty(t, profile.Mods[0].FileIDs, "the locked ref's FileIDs must NOT have been backfilled")
		})
	}
}

// TestDoProfileSync_ToUpdate_LockedRefRefusal_JSON is #294's --json framing
// sibling (Ruling 15 / Unit P): the new warning reaches the document via
// ProfileSyncResult.Warnings and NOT stderr - exactly one document on
// stdout, nothing on stderr.
func TestDoProfileSync_ToUpdate_LockedRefRefusal_JSON(t *testing.T) {
	svc, game := syncLockRefusalFixture(t)
	syncVerbose(t, false)
	oldYes := profileSyncYes
	profileSyncYes = true
	t.Cleanup(func() { profileSyncYes = oldYes })

	var doc core.ProfileSyncResult
	raw := runJSONCommand(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})
	decodeSingleDoc(t, raw, &doc)

	assert.Equal(t, 1, doc.Updated)
	require.Len(t, doc.Warnings, 1)
	assert.Equal(t, syncLockRefusalDetail, doc.Warnings[0])
}

// TestDoProfileSync_DeployCompile_MergedPakSyncFailureWarnsUnconditionally
// complements profile_compile_test.go's success-path capture: the
// end-of-sync merged-pak diagnostics print to stderr UNCONDITIONALLY
// (#197), unlike the three loops' own --verbose-gated warnings above. A
// missing base pak makes the sync fail outright.
func TestDoProfileSync_DeployCompile_MergedPakSyncFailureWarnsUnconditionally(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	game.SourceIDs = map[string]string{"fake-compiler": "external-icarus-id"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	const modID, version, fileID = "bear-mount", "1.0", "exmodz-file"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("bear-bytes")))
	seedSyncInstalledMod(t, svc, game, "fake-compiler", modID, "Bear Mount", version, "default", true, []string{fileID})

	syncVerbose(t, false)
	_, stderr, err := captureStdoutStderrErr(t, func() error {
		return doProfileSync(context.Background(), svc, game, nil)
	})
	require.NoError(t, err)

	assert.Contains(t, stderr, "Warning: could not sync merged pak: ")
}

// syncTwoUpdateCancelFixture extends syncLockRefusalFixture with a second,
// unlocked toUpdate entry ("src:mod2", also needing a FileIDs backfill) so
// the resulting ProfileSyncPlan.ToUpdate has two entries in profile.Mods
// order: the locked lock1 first (whose UpsertMod refusal records the #294
// warning), mod2 second. A fresh call builds a fully independent fixture
// (own temp dirs/service), so the same scenario can be run twice - once to
// measure, once to assert.
func syncTwoUpdateCancelFixture(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := syncLockRefusalFixture(t)
	seedSyncInstalledMod(t, svc, game, "src", "mod2", "Mod Two", "2.0", "default", true, []string{"main2"})
	require.NoError(t, getProfileManager(svc).AddMod(context.Background(), game.ID, "default", domain.ModReference{SourceID: "src", ModID: "mod2"}))
	return svc, game
}

// TestDoProfileSync_LockedRefWarningSurvivesFatalContextCancellation pins
// Task 13 review round 1's Important 1 fix for doProfileSync: unlike
// doProfileSwitch (whose fatal path is the final SetDefault call),
// ApplyProfileSync's toUpdate loop never returns fatally on its own - the
// only reachable "warning already recorded, then fatal" path is ctx
// cancellation between two toUpdate entries. The first entry (locked)
// records the #294 warning; cancellation must land on the second entry's
// loop-top ctx.Err() check, before it is ever processed. Before the fix,
// this warning was dropped because doProfileSync printed result.Warnings
// only on the success path. See cmd/lmm/profile_apply_test.go's identical
// counting/cancelAfterNCalls helpers and their doc comments for why the
// live threshold is measured rather than hard-coded.
func TestDoProfileSync_LockedRefWarningSurvivesFatalContextCancellation(t *testing.T) {
	countSvc, countGame := syncTwoUpdateCancelFixture(t)
	oldYes := profileSyncYes
	profileSyncYes = true
	t.Cleanup(func() { profileSyncYes = oldYes })

	counter := &countingCoreContext{Context: context.Background()}
	require.NoError(t, doProfileSync(counter, countSvc, countGame, nil),
		"the measurement pass must update both mods uncancelled")
	require.Positive(t, counter.calls, "the measurement pass must observe at least one core ctx.Err() check")

	svc, game := syncTwoUpdateCancelFixture(t)
	inner, cancel := context.WithCancel(context.Background())
	ctx := &cancelAfterNCalls{Context: inner, cancel: cancel, live: counter.calls - 1}

	out, stderr, err := captureStdoutStderrErr(t, func() error {
		return doProfileSync(ctx, svc, game, nil)
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, syncLockRefusalWarning, stderr,
		"Important 1: the accumulated #294 warning must survive the fatal path, not be silently dropped")
	assert.NotContains(t, out, "✓ Synced profile", "the sync must not have completed")

	profile, err := getProfileManager(svc).Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	for _, mr := range profile.Mods {
		if mr.ModID == "mod2" {
			assert.Empty(t, mr.FileIDs, "mod2 must never have been reached")
		}
	}
}
