package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

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
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "en1", Version: "1.0"}))

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

	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "ins1", Version: "1.0"}))

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
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "mod1", Version: "1.0"}))

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
		require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: id, Version: "1.0"}))
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
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))
	// Corrupt the deployed symlink so Uninstall fails deterministically.
	deployedPath := filepath.Join(game.ModPath, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0o644))
	// Drop it from the profile so the apply classifies it as a disable.
	require.NoError(t, pm.RemoveMod(game.ID, "default", "src", "1"))

	applyYes(t)
	applyVerbose(t, true)

	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "  Warning: failed to undeploy Test Mod: ")
	assert.Contains(t, out, "  ✓ Disabled: Test Mod\n")
}

// TestDoProfileApply_LockedRef_UpsertRefusalIsVerboseOnly pins ruling 9: the
// post-install ProfileManager.UpsertMod call records the version actually
// installed, a LOCKED profile ref refuses that write (#143), and
// doProfileApply swallows the refusal into a --verbose-only warning - the
// mod still counts as installed. Phase 2 preserves this byte-for-byte; the
// behaviour fix is filed for Phase 3.
func TestDoProfileApply_LockedRef_UpsertRefusalIsVerboseOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verbose bool
	}{
		{"verbose prints the warning", true},
		{"quiet swallows it", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, game := setupDoProfileSwitchTest(t)
			pm := getProfileManager(svc)

			src := newFakeInstallSource("test-src")
			t.Cleanup(src.Close)
			svc.RegisterSource(src)
			game.SourceIDs = map[string]string{"test-src": game.ID}

			src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.1", GameID: game.ID},
				[]domain.DownloadableFile{
					{ID: "main", Name: "Main", FileName: "mod1.esp", IsPrimary: true, Category: "MAIN", Version: "1.1"},
				})
			src.AddDownload("main", []byte("plugin content"))

			// A locked ref with no recorded version: the install stamps
			// "1.1", so UpsertMod's lock gate refuses the version move.
			require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{
				SourceID: "test-src", ModID: "mod1", Locked: true,
			}))

			applyYes(t)
			applyVerbose(t, tc.verbose)

			out := captureStdout(t, func() error {
				return doProfileApply(context.Background(), svc, game, nil)
			})

			assert.Contains(t, out, "    ✓ Installed: Mod One\n", "the lock refusal must not fail the install")
			if tc.verbose {
				assert.Contains(t, out, "    Warning: could not update profile: ")
				assert.Contains(t, out, "is locked at v")
			} else {
				assert.NotContains(t, out, "could not update profile",
					"the refusal is --verbose-only today (ruling 9)")
			}

			installed, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", game.ID, "default")
			require.NoError(t, err)
			assert.Equal(t, "1.1", installed.Version)

			profile, err := pm.Get(game.ID, "default")
			require.NoError(t, err)
			require.Len(t, profile.Mods, 1)
			assert.Empty(t, profile.Mods[0].Version, "the locked ref must be left unwritten")
		})
	}
}
