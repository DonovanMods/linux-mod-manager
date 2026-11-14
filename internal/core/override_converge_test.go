package core_test

// #631: a profile switch and a profile apply converge the profile's
// overrides on files that are not a mod's own (a stock game INI) as well:
// the target profile's are written, a previous profile's the target does
// not set go back to what they replaced - the game's original from the
// originals store, or nothing - and both plans preview it. A file the user
// changed after lmm wrote it is never overwritten.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nonModKeys are the non-mod override keys the tests run over, one per
// place a game file can be: at the game root, outside the mod path, and
// under the mod path (handoffGame's mod path is <install>/mods) at a path
// no mod ships.
var nonModKeys = []string{"Engine.ini", "mods/Data/User.ini"}

// nonModFixture is a game deployed by method whose mod k ships Data/k.ini
// and Data/k.esp, and whose stock file key holds "stock\n". Profile
// "default" (active) and "alt" both list k; default overrides key with
// "tweaked\n", alt sets no overrides.
func nonModFixture(t *testing.T, method domain.LinkMethod, key string) *legacyFixture {
	t.Helper()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), method))
	f.overriddenProfile(t, "default", true, map[string]string{key: "tweaked\n"}, "k")
	f.overriddenProfile(t, "alt", false, nil, "k")
	files := map[string]string{"Data/k.ini": "stock\n", "Data/k.esp": "plugin"}
	f.cachedOnly(t, "default", "k", method, files)
	f.cachedOnly(t, "alt", "k", method, files)
	f.stock(t, key, "stock\n")
	return f
}

// stock writes the game's own file at key, relative to the install path.
func (f *legacyFixture) stock(t *testing.T, key, content string) {
	t.Helper()
	path := f.gameFile(key)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// gameFile is key's path in the game directory.
func (f *legacyFixture) gameFile(key string) string {
	return filepath.Join(f.game.InstallPath, filepath.FromSlash(key))
}

// switchPlan plans a switch to target.
func (f *legacyFixture) switchPlan(t *testing.T, target string) *core.SwitchPlan {
	t.Helper()
	plan, err := f.svc.PlanProfileSwitch(context.Background(), f.game, target)
	require.NoError(t, err)
	return plan
}

// switchTo plans and applies a switch to target, returning its plan and
// every message it reported.
func (f *legacyFixture) switchTo(t *testing.T, target string) (*core.SwitchPlan, []string) {
	t.Helper()
	plan := f.switchPlan(t, target)
	result, err := f.svc.ApplyProfileSwitch(context.Background(), f.game, plan, nil)
	require.NoError(t, err)
	return plan, append(append([]string(nil), result.Warnings...), result.Notes...)
}

// applyPlan plans an apply of profile.
func (f *legacyFixture) applyPlan(t *testing.T, profile string) *core.ProfileApplyPlan {
	t.Helper()
	plan, err := f.svc.PlanProfileApply(context.Background(), f.game, profile)
	require.NoError(t, err)
	return plan
}

// applyProfile plans and applies profile, returning its plan and every
// message it reported. A plan with no changes is not applied, as the CLI
// does not apply one.
func (f *legacyFixture) applyProfile(t *testing.T, profile string) (*core.ProfileApplyPlan, []string) {
	t.Helper()
	plan := f.applyPlan(t, profile)
	if plan.NoChanges {
		return plan, nil
	}
	result, err := f.svc.ApplyProfileApply(context.Background(), f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	return plan, append(append([]string(nil), result.Warnings...), result.Notes...)
}

// gameOverride is the plan entry for the non-mod override key.
func gameOverride(key string, action core.OverrideChangeAction) core.OverrideChange {
	return core.OverrideChange{Path: key, Root: domain.RootInstallPath, Action: action}
}

// noMessageAbout fails on any message naming key.
func noMessageAbout(t *testing.T, key string, messages []string) {
	t.Helper()
	for _, m := range messages {
		assert.NotContains(t, m, filepath.Base(key), "nothing is reported about %s", key)
	}
}

func TestNonModOverride_SwitchRestoresAndReapplies(t *testing.T) {
	for _, key := range nonModKeys {
		for _, method := range linkMethods() {
			t.Run(key+"/"+method.String(), func(t *testing.T) {
				f := nonModFixture(t, method, key)
				f.deploy(t, "default", core.DeployOptions{})
				require.Equal(t, "tweaked\n", readLive(t, f.gameFile(key)))

				// The first switch enables alt's own row of k, and takes
				// default's override away.
				plan, msgs := f.switchTo(t, "alt")
				assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverrideRestore)}, plan.Overrides)
				noMessageAbout(t, key, msgs)
				assert.Equal(t, "stock\n", readLive(t, f.gameFile(key)), "the switch put the game's own file back")

				// Back: both profiles have k live, so only the override
				// changes - and the plan says so.
				plan, msgs = f.switchTo(t, "default")
				assert.False(t, plan.NoChanges, "a switch that changes an override has changes")
				assert.Empty(t, plan.ToEnable)
				assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverridePlace)}, plan.Overrides)
				noMessageAbout(t, key, msgs)
				assert.Equal(t, "tweaked\n", readLive(t, f.gameFile(key)), "the switch wrote the profile's override")

				plan, msgs = f.switchTo(t, "alt")
				assert.False(t, plan.NoChanges)
				assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverrideRestore)}, plan.Overrides)
				noMessageAbout(t, key, msgs)
				assert.Equal(t, "stock\n", readLive(t, f.gameFile(key)))
			})
		}
	}
}

// The two profiles override the same file differently: the switch
// rewrites it, and the game's original stays kept for the profile without
// an override.
func TestNonModOverride_SwitchUpdatesAnotherProfilesOverride(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.overriddenProfile(t, "alt", false, map[string]string{"Engine.ini": "alt's\n"}, "k")
	f.deploy(t, "default", core.DeployOptions{})
	f.switchTo(t, "alt")
	require.Equal(t, "alt's\n", readLive(t, f.gameFile("Engine.ini")))

	plan, _ := f.switchTo(t, "default")
	assert.Equal(t, []core.OverrideChange{gameOverride("Engine.ini", core.OverrideUpdate)}, plan.Overrides)
	assert.Equal(t, "tweaked\n", readLive(t, f.gameFile("Engine.ini")))

	f.overriddenProfile(t, "alt", false, nil, "k")
	f.switchTo(t, "alt")
	assert.Equal(t, "stock\n", readLive(t, f.gameFile("Engine.ini")), "the original is the game's, not the other profile's override")
}

func TestNonModOverride_ApplyPlacesUpdatesAndRestores(t *testing.T) {
	for _, key := range nonModKeys {
		t.Run(key, func(t *testing.T) {
			f := nonModFixture(t, domain.LinkSymlink, key)
			f.overriddenProfile(t, "default", true, nil, "k")
			f.deploy(t, "default", core.DeployOptions{})
			require.Equal(t, "stock\n", readLive(t, f.gameFile(key)))

			// The override is added by hand, then the profile applied.
			f.overriddenProfile(t, "default", true, map[string]string{key: "tweaked\n"}, "k")
			plan, msgs := f.applyProfile(t, "default")
			assert.False(t, plan.NoChanges)
			assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverridePlace)}, plan.Overrides)
			noMessageAbout(t, key, msgs)
			assert.Equal(t, "tweaked\n", readLive(t, f.gameFile(key)))

			again := f.applyPlan(t, "default")
			assert.True(t, again.NoChanges, "the game matches the profile now")
			assert.Empty(t, again.Overrides)

			f.overriddenProfile(t, "default", true, map[string]string{key: "retweaked\n"}, "k")
			plan, _ = f.applyProfile(t, "default")
			assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverrideUpdate)}, plan.Overrides)
			assert.Equal(t, "retweaked\n", readLive(t, f.gameFile(key)))

			// Taken out of the profile by hand: lmm recorded what it
			// wrote, so the apply knows the file is still its own.
			f.overriddenProfile(t, "default", true, nil, "k")
			plan, msgs = f.applyProfile(t, "default")
			assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverrideRestore)}, plan.Overrides)
			noMessageAbout(t, key, msgs)
			assert.Equal(t, "stock\n", readLive(t, f.gameFile(key)))
			assert.True(t, f.applyPlan(t, "default").NoChanges)
		})
	}
}

// #445/#466's rule: a file the user changed after lmm wrote the override
// is theirs. The switch leaves it, says so, and keeps the original.
func TestNonModOverride_AFileTheUserChangedIsKept(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.deploy(t, "default", core.DeployOptions{})
	f.switchTo(t, "alt")
	f.switchTo(t, "default")
	require.NoError(t, os.WriteFile(f.gameFile("Engine.ini"), []byte("mine\n"), 0o644))

	plan, msgs := f.switchTo(t, "alt")
	assert.Equal(t, []core.OverrideChange{gameOverride("Engine.ini", core.OverrideKeep)}, plan.Overrides, "nothing is restored over the user's file, and the plan says so")
	assert.True(t, plan.NoChanges)
	assert.Equal(t, "mine\n", readLive(t, f.gameFile("Engine.ini")), "the user's file stays")
	assert.Contains(t, strings.Join(msgs, "\n"), "Engine.ini", "and the switch says why the original did not come back")
	assert.NotEmpty(t, readOriginalsManifest(t, f.svc.DataDirForTest(), f.game.ID), "the original is still kept")
}

// An override that created a file the game did not have: taking it away
// removes the file, since there is nothing to put back.
func TestNonModOverride_AFileTheOverrideCreatedIsRemoved(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.overriddenProfile(t, "default", true, map[string]string{"Saved/New.ini": "made\n"}, "k")
	f.deploy(t, "default", core.DeployOptions{})
	require.Equal(t, "made\n", readLive(t, f.gameFile("Saved/New.ini")))

	plan, msgs := f.switchTo(t, "alt")
	assert.Contains(t, plan.Overrides, gameOverride("Saved/New.ini", core.OverrideRestore))
	noMessageAbout(t, "Saved/New.ini", msgs)
	assertAbsent(t, f.gameFile("Saved/New.ini"), "the file the override created is gone")
	assert.Equal(t, "stock\n", readLive(t, f.gameFile("Engine.ini")), "and the stock file is untouched")
}

// A file the game had with the very content an override sets was not
// written by lmm, so it is no file lmm created: it stays.
func TestNonModOverride_AFileThatAlreadyHeldTheOverrideStays(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.stock(t, "Engine.ini", "tweaked\n")
	f.deploy(t, "default", core.DeployOptions{})

	plan, _ := f.switchTo(t, "alt")
	assert.Empty(t, plan.Overrides)
	assert.Equal(t, "tweaked\n", readLive(t, f.gameFile("Engine.ini")), "the game's own file is never removed")
}

// A switch to the profile that is already active changes nothing - not
// even an override.
func TestNonModOverride_AlreadyActiveSwitchHasNoOverrides(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	plan := f.switchPlan(t, "default")
	assert.True(t, plan.AlreadyActive)
	assert.Empty(t, plan.Overrides)
}

// #630's overridden mod files are previewed too: a switch between two
// profiles with the same mods that only adds or takes away an override of
// a mod's own file is a change, naming the mod.
func TestOverriddenModFile_SwitchPlanPreviewsTheCopy(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.overriddenProfile(t, "alt", false, nil, "k")
			f.cachedOnly(t, "alt", "k", method, map[string]string{"Data/k.ini": "stock\n", "Data/k.esp": "plugin"})
			f.deploy(t, "default", core.DeployOptions{})
			f.switchTo(t, "alt")
			assertPlainDeployment(t, f, method)

			modFile := func(action core.OverrideChangeAction) core.OverrideChange {
				return core.OverrideChange{Path: "Data/k.ini", Action: action, SourceID: "local", ModID: "k"}
			}
			plan, _ := f.switchTo(t, "default")
			assert.False(t, plan.NoChanges)
			assert.Equal(t, []core.OverrideChange{modFile(core.OverridePlace)}, plan.Overrides)
			assertOverriddenCopy(t, f, "tweaked\n")

			plan, _ = f.switchTo(t, "alt")
			assert.False(t, plan.NoChanges)
			assert.Equal(t, []core.OverrideChange{modFile(core.OverrideRestore)}, plan.Overrides)
			assertPlainDeployment(t, f, method)
		})
	}
}

// The #630 gap, under every method and the copy method above all: an
// override of a mod's file taken out of the profile by hand, then the
// profile applied (not deployed), puts the mod's own file back - lmm
// remembers where it wrote overridden copies, so no file is hashed to find
// them.
func TestOverriddenModFile_ApplyAfterTheOverrideIsDeletedByHand(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")

			f.overriddenProfile(t, "default", true, nil, "k")
			plan, msgs := f.applyProfile(t, "default")
			assert.False(t, plan.NoChanges)
			assert.Equal(t, []core.OverrideChange{{Path: "Data/k.ini", Action: core.OverrideRestore, SourceID: "local", ModID: "k"}}, plan.Overrides)
			noOverrideNoise(t, "the apply", msgs)
			assertPlainDeployment(t, f, method)
			assert.True(t, f.applyPlan(t, "default").NoChanges)
		})
	}
}

// verify finds the same leftover copy under copy, and --fix repairs it.
func TestOverriddenModFile_VerifyFindsALeftoverCopyUnderCopy(t *testing.T) {
	f := overriddenFixture(t, domain.LinkCopy)
	f.deploy(t, "default", core.DeployOptions{})
	f.overriddenProfile(t, "default", true, nil, "k")

	report, err := f.svc.VerifyReport(context.Background(), f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	var stale []string
	for _, finding := range report.Result.Findings {
		if finding.Status == core.VerifyStatusOverrideStale {
			stale = append(stale, finding.FileID)
		}
	}
	assert.Equal(t, []string{"Data/k.ini"}, stale)
}

// A game-root mod file (#629) is a mod file, not a game file: its
// override is converged as one, and the switch never restores an
// "original" over it.
func TestNonModOverride_AGameRootModFileIsAModFile(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	writeRootProfile(t, svc, map[string]string{"enbseries/effect.fx": "tuned\n"})
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	writeRootProfile(t, svc, nil)
	plan, err := svc.PlanProfileApply(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Equal(t, []core.OverrideChange{{Path: "enbseries/effect.fx", Root: domain.RootInstallPath, Action: core.OverrideRestore, SourceID: "src", ModID: "enb"}}, plan.Overrides)
	_, err = svc.ApplyProfileApply(context.Background(), game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(game.InstallPath, "enbseries", "effect.fx")
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the plain link is back")
}

// The overridden copy's fingerprint is shared with every profile that
// records the path; a purge of the profile that is not active still keeps
// the file the active one uses.
func TestOverriddenModFile_PurgingTheOtherProfileKeepsTheLiveCopy(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.overriddenProfile(t, "alt", false, nil, "k")
			f.cachedOnly(t, "alt", "k", method, map[string]string{"Data/k.ini": "stock\n", "Data/k.esp": "plugin"})
			f.deploy(t, "default", core.DeployOptions{})
			f.switchTo(t, "alt")
			f.switchTo(t, "default")
			assertOverriddenCopy(t, f, "tweaked\n")

			f.purge(t, "alt")
			assertOverriddenCopy(t, f, "tweaked\n")
		})
	}
}

// deployPlan plans a deploy of profile.
func (f *legacyFixture) deployPlan(t *testing.T, profile string) *core.DeployPlan {
	t.Helper()
	plan, err := f.svc.PlanDeploy(context.Background(), f.game, profile, core.DeployOptions{})
	require.NoError(t, err)
	return plan
}

// Owner ruling on #631: `lmm deploy` takes a dropped game-file override
// away too, by the same rule, and its plan previews the game files its
// overrides change.
func TestNonModOverride_DeployRestoresADroppedOverride(t *testing.T) {
	for _, key := range nonModKeys {
		t.Run(key, func(t *testing.T) {
			f := nonModFixture(t, domain.LinkSymlink, key)
			assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverridePlace)}, f.deployPlan(t, "default").Overrides)
			f.deploy(t, "default", core.DeployOptions{})
			require.Equal(t, "tweaked\n", readLive(t, f.gameFile(key)))
			assert.Empty(t, f.deployPlan(t, "default").Overrides, "nothing left to change")

			f.overriddenProfile(t, "default", true, nil, "k")
			assert.Equal(t, []core.OverrideChange{gameOverride(key, core.OverrideRestore)}, f.deployPlan(t, "default").Overrides)
			result := f.deploy(t, "default", core.DeployOptions{})
			noMessageAbout(t, key, append(result.Warnings, result.Notes...))
			assert.Equal(t, "stock\n", readLive(t, f.gameFile(key)), "the deploy put the game's own file back")
			assert.Empty(t, f.deployPlan(t, "default").Overrides)
		})
	}
}

func TestNonModOverride_DeployKeepsAFileTheUserChanged(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.deploy(t, "default", core.DeployOptions{})
	require.NoError(t, os.WriteFile(f.gameFile("Engine.ini"), []byte("mine\n"), 0o644))
	f.overriddenProfile(t, "default", true, nil, "k")

	assert.Equal(t, []core.OverrideChange{gameOverride("Engine.ini", core.OverrideKeep)}, f.deployPlan(t, "default").Overrides)
	result := f.deploy(t, "default", core.DeployOptions{})
	assert.Equal(t, "mine\n", readLive(t, f.gameFile("Engine.ini")))
	assert.Contains(t, strings.Join(result.Warnings, "\n"), "Engine.ini")
}

// setAsideFiles is every file under the originals store's set-aside folder,
// by its content.
func (f *legacyFixture) setAsideFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(f.svc.DataDirForTest(), "snapshots", f.game.ID, "_originals", "set-aside")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, rerr := os.ReadFile(path)
			require.NoError(t, rerr)
			out[string(b)] = path
		}
		return nil
	})
	return out
}

// usersEditOverAnotherOverride is the review's blocker state: default and
// alt override Engine.ini differently, the game has run both, and default
// is live with the user's edit over its override.
func usersEditOverAnotherOverride(t *testing.T) *legacyFixture {
	t.Helper()
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.overriddenProfile(t, "alt", false, map[string]string{"Engine.ini": "alt's\n"}, "k")
	f.deploy(t, "default", core.DeployOptions{})
	f.switchTo(t, "alt")
	f.switchTo(t, "default")
	require.Equal(t, "tweaked\n", readLive(t, f.gameFile("Engine.ini")))
	require.NoError(t, os.WriteFile(f.gameFile("Engine.ini"), []byte("mine\n"), 0o644))
	return f
}

// Review blocker: an override written over a file the user changed after
// lmm wrote one, where the game's original is already kept, sets the
// user's file aside first and says where - by switch, apply and deploy -
// and the game's original still comes back when no override sets it.
func TestNonModOverride_TheUsersChangedFileIsSetAsideNotLost(t *testing.T) {
	flows := []struct {
		name string
		run  func(t *testing.T, f *legacyFixture) ([]core.OverrideChange, []string)
	}{
		{"switch", func(t *testing.T, f *legacyFixture) ([]core.OverrideChange, []string) {
			plan, msgs := f.switchTo(t, "alt")
			return plan.Overrides, msgs
		}},
		{"apply", func(t *testing.T, f *legacyFixture) ([]core.OverrideChange, []string) {
			f.overriddenProfile(t, "default", true, map[string]string{"Engine.ini": "alt's\n"}, "k")
			plan, msgs := f.applyProfile(t, "default")
			return plan.Overrides, msgs
		}},
		{"deploy", func(t *testing.T, f *legacyFixture) ([]core.OverrideChange, []string) {
			f.overriddenProfile(t, "default", true, map[string]string{"Engine.ini": "alt's\n"}, "k")
			plan := f.deployPlan(t, "default")
			result := f.deploy(t, "default", core.DeployOptions{})
			return plan.Overrides, append(result.Warnings, result.Notes...)
		}},
	}
	for _, flow := range flows {
		t.Run(flow.name, func(t *testing.T) {
			f := usersEditOverAnotherOverride(t)
			changes, msgs := flow.run(t, f)
			assert.Equal(t, []core.OverrideChange{gameOverride("Engine.ini", core.OverrideReplaceChanged)}, changes)
			assert.Equal(t, "alt's\n", readLive(t, f.gameFile("Engine.ini")), "the override still applies")
			kept := f.setAsideFiles(t)
			require.Contains(t, kept, "mine\n", "the user's file is kept")
			assert.Contains(t, strings.Join(msgs, "\n"), kept["mine\n"], "and the flow says where")

			// Nothing overrides it any more: the game's own file returns.
			f.overriddenProfile(t, "default", false, nil, "k")
			f.overriddenProfile(t, "alt", false, nil, "k")
			live := "default"
			if flow.name == "switch" {
				live = "alt"
			}
			f.overriddenProfile(t, live, true, nil, "k")
			f.applyProfile(t, live)
			assert.Equal(t, "stock\n", readLive(t, f.gameFile("Engine.ini")))
		})
	}
}

// Review nit: a file the user changed is listed in the plan as kept, so
// a switch and an apply show the same thing, and it is no change.
func TestNonModOverride_AKeptFileIsListedAndIsNoChange(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.deploy(t, "default", core.DeployOptions{})
	require.NoError(t, os.WriteFile(f.gameFile("Engine.ini"), []byte("mine\n"), 0o644))
	f.overriddenProfile(t, "default", true, nil, "k")

	plan := f.applyPlan(t, "default")
	assert.True(t, plan.NoChanges)
	assert.Equal(t, []core.OverrideChange{gameOverride("Engine.ini", core.OverrideKeep)}, plan.Overrides)
	assert.Equal(t, []core.OverrideChange{gameOverride("Engine.ini", core.OverrideKeep)}, f.deployPlan(t, "default").Overrides)
}

// Review nit: a file the override created goes with the folders it
// created, and only those.
func TestNonModOverride_TheFoldersAnOverrideCreatedGoWithIt(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	require.NoError(t, os.MkdirAll(f.gameFile("Saved"), 0o755))
	f.overriddenProfile(t, "default", true, map[string]string{"Saved/Config/Linux/New.ini": "made\n", "Saved/Config/Other/Keep.ini": "keep\n"}, "k")
	f.deploy(t, "default", core.DeployOptions{})
	require.NoError(t, os.WriteFile(f.gameFile("Saved/Config/Other/Game.sav"), []byte("the game's"), 0o644))

	f.overriddenProfile(t, "default", true, nil, "k")
	f.applyProfile(t, "default")
	assertAbsent(t, f.gameFile("Saved/Config/Linux"), "the folder the override created is gone")
	assertAbsent(t, f.gameFile("Saved/Config/Other/Keep.ini"), "")
	assert.FileExists(t, f.gameFile("Saved/Config/Other/Game.sav"), "a folder with the game's file in it stays")
	assert.DirExists(t, f.gameFile("Saved"), "a folder the game had stays")
}

// Review nit: an override edited between a plan and its confirm makes the
// plan stale.
func TestNonModOverride_AnOverrideEditedAfterThePlanIsStale(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.deploy(t, "default", core.DeployOptions{})
	ctx := context.Background()

	f.overriddenProfile(t, "default", true, map[string]string{"Engine.ini": "v2\n"}, "k")
	apply := f.applyPlan(t, "default")
	f.overriddenProfile(t, "default", true, map[string]string{"Engine.ini": "v3\n"}, "k")
	_, err := f.svc.ApplyProfileApply(ctx, f.game, apply, core.ProfileApplyOptions{}, nil)
	assert.ErrorIs(t, err, core.ErrStalePlan)

	sw := f.switchPlan(t, "alt")
	f.overriddenProfile(t, "alt", false, map[string]string{"Engine.ini": "alt's\n"}, "k")
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, sw, nil)
	assert.ErrorIs(t, err, core.ErrStalePlan)
}

// Review nit: the write is recorded before it is made, with the content it
// replaces, so a crash between the two leaves a record that still knows
// the file - lmm's own earlier write - and nothing of lmm's is ever taken
// for the user's file or the game's original.
func TestNonModOverride_ACrashBetweenRecordAndWriteLosesNothing(t *testing.T) {
	f := nonModFixture(t, domain.LinkSymlink, "Engine.ini")
	f.deploy(t, "default", core.DeployOptions{})
	require.NoError(t, f.svc.RecordOverrideWriteForTest(f.game.ID, "Engine.ini", []byte("v2\n"), []byte("tweaked\n")))
	require.Equal(t, "tweaked\n", readLive(t, f.gameFile("Engine.ini")), "the write never landed")

	f.overriddenProfile(t, "default", true, map[string]string{"Engine.ini": "v2\n"}, "k")
	plan, msgs := f.applyProfile(t, "default")
	assert.Equal(t, []core.OverrideChange{gameOverride("Engine.ini", core.OverrideUpdate)}, plan.Overrides, "the file is still lmm's own")
	noMessageAbout(t, "Engine.ini", msgs)
	assert.Empty(t, f.setAsideFiles(t))
	assert.Equal(t, "v2\n", readLive(t, f.gameFile("Engine.ini")))

	f.overriddenProfile(t, "default", true, nil, "k")
	f.applyProfile(t, "default")
	assert.Equal(t, "stock\n", readLive(t, f.gameFile("Engine.ini")), "the game's original is still the game's")
}
