package core_test

// #630: a profile override on a deployed mod's own file. The deployed file
// is replaced by a regular copy holding the override - the link into the
// shared cache is broken, never written through - and every deploy-side
// flow treats that copy as expected state: deploy leaves it, verify reports
// it as overridden rather than drift, removals remove it, and taking the
// override away puts the plain link back.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overrideKey is the override that lands on the mod file Data/k.ini: an
// override key is relative to the game's install path, and handoffGame's
// mod path is <install>/mods.
const overrideKey = "mods/Data/k.ini"

// overriddenFixture is a game deployed by method whose mod k ships
// Data/k.ini and Data/k.esp, listed by profile "default".
func overriddenFixture(t *testing.T, method domain.LinkMethod) *legacyFixture {
	t.Helper()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), method))
	f.overriddenProfile(t, "default", true, map[string]string{overrideKey: "tweaked\n"}, "k")
	f.cachedOnly(t, "default", "k", method, map[string]string{"Data/k.ini": "stock\n", "Data/k.esp": "plugin"})
	return f
}

// overriddenProfile writes profile name listing refs, with overrides.
func (f *legacyFixture) overriddenProfile(t *testing.T, name string, active bool, overrides map[string]string, refs ...string) {
	t.Helper()
	text := "name: " + name + "\ngame_id: " + f.game.ID + "\nmods:\n"
	for _, ref := range refs {
		text += "    - source_id: local\n      mod_id: " + ref + "\n      version: unknown\n"
	}
	if len(overrides) > 0 {
		text += "overrides:\n"
		for k, v := range overrides {
			text += "    " + k + ": " + yamlQuote(v) + "\n"
		}
	}
	if active {
		text += "is_default: true\n"
	}
	dir := filepath.Join(f.svc.ConfigDir(), "games", f.game.ID, "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(text), 0o644))
}

func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "\n", `\n`) + `"`
}

func (f *legacyFixture) iniPath() string {
	return filepath.Join(f.game.ModPath, "Data", "k.ini")
}

func (f *legacyFixture) cachedIni() string {
	return filepath.Join(f.svc.GetGameCache(f.game).ModPath(cache.Key{GameID: f.game.ID, SourceID: "local", ModID: "k", Version: "unknown"}), "Data", "k.ini")
}

// assertOverriddenCopy: the deployed path is a regular file of its own -
// one link, not the cache's - holding the override, and the cache entry
// still holds the mod's bytes.
func assertOverriddenCopy(t *testing.T, f *legacyFixture, want string) {
	t.Helper()
	info, err := os.Lstat(f.iniPath())
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "an overridden mod file is a regular file, not a link: %v", info.Mode())
	assert.EqualValues(t, 1, info.Sys().(*syscall.Stat_t).Nlink, "and shares its data with nothing")
	assert.Equal(t, want, readLive(t, f.iniPath()))
	assert.Equal(t, "stock\n", readLive(t, f.cachedIni()), "the cache entry stays pristine")
}

// assertPlainDeployment: the path is deployed as method deploys it.
func assertPlainDeployment(t *testing.T, f *legacyFixture, method domain.LinkMethod) {
	t.Helper()
	info, err := os.Lstat(f.iniPath())
	require.NoError(t, err)
	switch method {
	case domain.LinkSymlink:
		require.NotZero(t, info.Mode()&os.ModeSymlink, "the plain link is back")
		target, err := os.Readlink(f.iniPath())
		require.NoError(t, err)
		assert.Equal(t, f.cachedIni(), target)
	case domain.LinkHardlink:
		ci, err := os.Lstat(f.cachedIni())
		require.NoError(t, err)
		assert.True(t, os.SameFile(info, ci), "the plain hard link is back")
	default:
		assert.True(t, info.Mode().IsRegular())
	}
	assert.Equal(t, "stock\n", readLive(t, f.iniPath()))
}

// noOverrideNoise fails on any message about k.ini: an overridden mod
// file is expected state, which nothing warns about.
func noOverrideNoise(t *testing.T, what string, messages ...[]string) {
	t.Helper()
	for _, ms := range messages {
		for _, m := range ms {
			assert.NotContains(t, m, "k.ini", "%s says nothing about the overridden file", what)
		}
	}
}

func linkMethods() []domain.LinkMethod {
	return []domain.LinkMethod{domain.LinkSymlink, domain.LinkHardlink, domain.LinkCopy}
}

func TestOverriddenModFile_DeployBreaksTheLinkAndLeavesItOnRedeploy(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)

			result := f.deploy(t, "default", core.DeployOptions{})
			noOverrideNoise(t, "the first deploy", result.Warnings, result.Notes)
			assertOverriddenCopy(t, f, "tweaked\n")

			result = f.deploy(t, "default", core.DeployOptions{})
			noOverrideNoise(t, "a redeploy", result.Warnings, result.Notes)
			assertOverriddenCopy(t, f, "tweaked\n")
		})
	}
}

func TestOverriddenModFile_VerifyReportsItAndFixLeavesIt(t *testing.T) {
	ctx := context.Background()
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})

			for _, fix := range []bool{false, true} {
				report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Fix: fix, Force: true}, nil)
				require.NoError(t, err)
				var overridden []core.VerifyFinding
				for _, finding := range report.Result.Findings {
					assert.NotEqual(t, core.VerifyStatusDeployedModified, finding.Status, "an overridden mod file is not drift: %+v", finding)
					if finding.Status == core.VerifyStatusDeployedOverridden {
						overridden = append(overridden, finding)
					}
				}
				require.Len(t, overridden, 1, "fix=%v: %+v", fix, report.Result.Findings)
				assert.Equal(t, "Data/k.ini", overridden[0].FileID)
				assert.Equal(t, "k", overridden[0].ModID)
				assert.Contains(t, overridden[0].Note, "overridden by the profile")
				assert.False(t, overridden[0].Fixable)
				assert.Zero(t, report.Result.Issues)
				assert.Zero(t, report.Result.Warnings, "fix=%v: %+v", fix, report.Result.Findings)
				assertOverriddenCopy(t, f, "tweaked\n")
			}
		})
	}
}

func TestOverriddenModFile_PurgeAndUninstallRemoveTheCopy(t *testing.T) {
	ctx := context.Background()
	for _, method := range linkMethods() {
		t.Run(method.String()+"/purge", func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})

			plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			noOverrideNoise(t, "the purge", result.Warnings, result.Notes)
			_, err = os.Lstat(f.iniPath())
			assert.ErrorIs(t, err, os.ErrNotExist, "the overridden copy is removed with the mod")
			assert.Equal(t, "stock\n", readLive(t, f.cachedIni()))
			assert.Empty(t, f.recorded(t, "default", "k"))
		})
		t.Run(method.String()+"/uninstall", func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})

			result, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{KeepCache: true})
			require.NoError(t, err)
			noOverrideNoise(t, "the uninstall", result.Warnings)
			_, err = os.Lstat(f.iniPath())
			assert.ErrorIs(t, err, os.ErrNotExist, "the overridden copy is removed with the mod")
		})
		t.Run(method.String()+"/disable", func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})

			result, err := f.svc.DisableMod(ctx, f.game, "default", "local", "k")
			require.NoError(t, err)
			noOverrideNoise(t, "the disable", result.Warnings)
			_, err = os.Lstat(f.iniPath())
			assert.ErrorIs(t, err, os.ErrNotExist, "the overridden copy is removed with the mod")

			enabled, err := f.svc.EnableMod(ctx, f.game, "default", "local", "k")
			require.NoError(t, err)
			noOverrideNoise(t, "the enable", enabled.Warnings)
			assertOverriddenCopy(t, f, "tweaked\n")
		})
	}
}

func TestOverriddenModFile_RemovingTheOverrideRestoresThePlainDeployment(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")

			f.overriddenProfile(t, "default", true, nil, "k")
			result := f.deploy(t, "default", core.DeployOptions{})
			noOverrideNoise(t, "the deploy", result.Warnings, result.Notes)
			assertPlainDeployment(t, f, method)
		})
	}
}

func TestOverriddenModFile_ChangingTheOverrideRewritesTheCopy(t *testing.T) {
	f := overriddenFixture(t, domain.LinkSymlink)
	f.deploy(t, "default", core.DeployOptions{})

	f.overriddenProfile(t, "default", true, map[string]string{overrideKey: "retweaked\n"}, "k")
	result := f.deploy(t, "default", core.DeployOptions{})
	noOverrideNoise(t, "the deploy", result.Warnings, result.Notes)
	assertOverriddenCopy(t, f, "retweaked\n")
}

func TestOverriddenModFile_SwitchingProfilesRestoresAndReapplies(t *testing.T) {
	ctx := context.Background()
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.overriddenProfile(t, "plain", false, nil, "k")
			f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")

			switchTo := func(target string) []string {
				plan, err := f.svc.PlanProfileSwitch(ctx, f.game, target)
				require.NoError(t, err)
				result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
				require.NoError(t, err)
				return append(append([]string(nil), result.Warnings...), result.Notes...)
			}

			noOverrideNoise(t, "the switch away", switchTo("plain"))
			assertPlainDeployment(t, f, method)

			noOverrideNoise(t, "the switch back", switchTo("default"))
			assertOverriddenCopy(t, f, "tweaked\n")
		})
	}
}

func TestOverriddenModFile_ProfileApplyConvergesBothWays(t *testing.T) {
	ctx := context.Background()
	apply := func(t *testing.T, f *legacyFixture) []string {
		plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
		require.NoError(t, err)
		result, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
		require.NoError(t, err)
		return append(append([]string(nil), result.Warnings...), result.Notes...)
	}
	for _, method := range []domain.LinkMethod{domain.LinkSymlink, domain.LinkHardlink} {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.overriddenProfile(t, "default", true, nil, "k")
			f.deploy(t, "default", core.DeployOptions{})
			assertPlainDeployment(t, f, method)

			// The override is added by hand, then the profile applied.
			f.overriddenProfile(t, "default", true, map[string]string{overrideKey: "tweaked\n"}, "k")
			noOverrideNoise(t, "the apply", apply(t, f))
			assertOverriddenCopy(t, f, "tweaked\n")

			// And taken away again.
			f.overriddenProfile(t, "default", true, nil, "k")
			noOverrideNoise(t, "the apply", apply(t, f))
			assertPlainDeployment(t, f, method)
		})
	}
}

// TestOverriddenModFile_AReplaceKeepsTheOverride: a rollback replaces the
// mod's deployment with another version's, and the overridden file is
// placed as the override again rather than linked to the other version.
func TestOverriddenModFile_AReplaceKeepsTheOverride(t *testing.T) {
	ctx := context.Background()
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})
			gameCache := f.svc.GetGameCache(f.game)
			old := cache.Key{GameID: f.game.ID, SourceID: "local", ModID: "k", Version: "old"}
			require.NoError(t, gameCache.Store(old, "Data/k.ini", []byte("old stock\n")))
			require.NoError(t, gameCache.Store(old, "Data/k.esp", []byte("old plugin")))
			require.NoError(t, f.svc.ExecForTest(ctx, `UPDATE installed_mods SET previous_version = 'old' WHERE mod_id = 'k'`))

			plan, err := f.svc.PlanRollback(ctx, f.game, "default", "local", "k")
			require.NoError(t, err)
			result, err := f.svc.ApplyRollback(ctx, f.game, plan, core.RollbackOptions{}, nil)
			require.NoError(t, err)
			require.Equal(t, core.UpdateRolledBack, result.Status, "%+v", result)
			noOverrideNoise(t, "the rollback", result.Warnings)
			assertOverriddenCopy(t, f, "tweaked\n")
			assert.Equal(t, "old stock\n", readLive(t, filepath.Join(gameCache.ModPath(old), "Data", "k.ini")), "neither cache entry is written")
			assert.Equal(t, "old plugin", readLive(t, filepath.Join(f.game.ModPath, "Data", "k.esp")))

			// And the next deploy still says nothing about it.
			res := f.deploy(t, "default", core.DeployOptions{})
			noOverrideNoise(t, "the deploy", res.Warnings, res.Notes)
		})
	}
}

// purgeDefault plans and applies a purge of profile default.
func purgeDefault(t *testing.T, f *legacyFixture) *core.PurgeResult {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	return result
}

// replaceWithUsersFile puts the user's own regular file at path, never
// writing through what was there (a hard link would carry the edit into
// the cache).
func replaceWithUsersFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// TestOverriddenModFile_TheUsersFileIsSetAsideNotLost is the #630 review's
// blocker: the override pass replaced a file the deploy loop had just left
// as the user's, with no backup, and then recorded it as lmm's, so the next
// purge deleted the only copy. The user's file is now captured before the
// override is written, the deploy says so, and the purge puts it back.
func TestOverriddenModFile_TheUsersFileIsSetAsideNotLost(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String()+"/an edit of the overridden copy", func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})
			replaceWithUsersFile(t, f.iniPath(), "mine\n")

			result := f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")
			assert.True(t, containsLine(result.Warnings, "mods/Data/k.ini", "set it aside"), "the deploy says the user's file was set aside: %q", result.Warnings)

			purgeDefault(t, f)
			assert.Equal(t, "mine\n", readLive(t, f.iniPath()), "the purge puts the user's file back")
			assert.Equal(t, "stock\n", readLive(t, f.cachedIni()))
		})
		t.Run(method.String()+"/a replacement of the mod's file, then an override", func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.overriddenProfile(t, "default", true, nil, "k")
			f.deploy(t, "default", core.DeployOptions{})
			replaceWithUsersFile(t, f.iniPath(), "mine\n")

			f.overriddenProfile(t, "default", true, map[string]string{overrideKey: "tweaked\n"}, "k")
			result := f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")
			assert.True(t, containsLine(result.Warnings, "mods/Data/k.ini", "set it aside"), "%q", result.Warnings)

			purgeDefault(t, f)
			assert.Equal(t, "mine\n", readLive(t, f.iniPath()), "the purge puts the user's file back")
		})
	}
}

// TestOverriddenModFile_AStaleRecordOverAStockFileStillCapturesIt: another
// profile's record of a path says nothing about the stock file there now,
// so the override captures it before writing, as it would any game file.
func TestOverriddenModFile_AStaleRecordOverAStockFileStillCapturesIt(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
	// default deploys a mod of its own (a deploy with nothing to deploy
	// applies no overrides), which does not ship Data/k.ini.
	f.overriddenProfile(t, "default", true, map[string]string{overrideKey: "tweaked\n"}, "j")
	f.cachedOnly(t, "default", "j", domain.LinkSymlink, map[string]string{"Data/j.esp": "j"})
	require.NoError(t, os.MkdirAll(filepath.Dir(f.iniPath()), 0o755))
	require.NoError(t, os.WriteFile(f.iniPath(), []byte("stock game file\n"), 0o644))
	require.NoError(t, f.svc.ExecForTest(ctx,
		`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES (?, 'other', 'Data/k.ini', 'local', 'k')`, f.game.ID))

	result := f.deploy(t, "default", core.DeployOptions{})
	assert.Equal(t, "tweaked\n", readLive(t, f.iniPath()), "%q", result.Warnings)

	require.NoError(t, f.svc.RestoreOriginalsForTest(f.game))
	assert.Equal(t, "stock game file\n", readLive(t, f.iniPath()), "the stock file was captured before the override replaced it")
}

// TestOverriddenModFile_AFileAnotherGameRecordsIsLeft: a file a game sharing
// the mod directory records is that game's (#445); an override of it is
// refused with a warning naming the game, and the file is left as it is.
func TestOverriddenModFile_AFileAnotherGameRecordsIsLeft(t *testing.T) {
	sky, _, live := bothActiveState(t, domain.LinkCopy)
	sky.overriddenProfile(t, "default", true, map[string]string{"mods/Data/a.esp": "override"}, "k")

	result := sky.deploy(t, "default", core.DeployOptions{})
	assert.Equal(t, "sky2 a, USER-EDITED", liveBytes(t, live))
	assert.True(t, containsLine(result.Warnings, "mods/Data/a.esp", "sky2"), "%q", result.Warnings)
}

// TestOverriddenModFile_AHardlinkWithItsCacheGoneIsNotAnOverride is the
// #630 review's second finding: with no override anywhere, a hard link
// whose cache entry was deleted is a sole regular file matching its record,
// and every apply tried to "restore" it and warned once per file.
func TestOverriddenModFile_AHardlinkWithItsCacheGoneIsNotAnOverride(t *testing.T) {
	f := overriddenFixture(t, domain.LinkHardlink)
	f.overriddenProfile(t, "default", true, nil, "k")
	f.deploy(t, "default", core.DeployOptions{})
	entry := f.svc.GetGameCache(f.game).ModPath(cache.Key{GameID: f.game.ID, SourceID: "local", ModID: "k", Version: "unknown"})
	require.NoError(t, os.RemoveAll(entry))

	ctx := context.Background()
	plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	result, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	if err == nil {
		noOverrideNoise(t, "the apply", result.Warnings, result.Notes)
	} else {
		for _, w := range result.Warnings {
			assert.NotContains(t, w, "override", "nothing about overrides: %q", result.Warnings)
		}
	}
	assert.Equal(t, "stock\n", readLive(t, f.iniPath()), "the file is left as it is")
}

// TestOverriddenModFile_AUsersFileMatchingAnotherProfilesCopyIsLeft is the
// review's fifth finding: a user's file whose bytes happen to equal another
// profile's recorded override copy was judged lmm's by that record and
// replaced by the link. Only the path's own record (the acting profile's)
// can make a copy lmm's to restore.
func TestOverriddenModFile_AUsersFileMatchingAnotherProfilesCopyIsLeft(t *testing.T) {
	ctx := context.Background()
	f := overriddenFixture(t, domain.LinkSymlink)
	f.overriddenProfile(t, "plain", false, nil, "k")
	f.deploy(t, "default", core.DeployOptions{})
	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "plain")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assertPlainDeployment(t, f, domain.LinkSymlink)

	// The user's own file - byte for byte default's override copy.
	replaceWithUsersFile(t, f.iniPath(), "tweaked\n")
	applyProfile(t, f.svc, f.game, "plain")

	info, err := os.Lstat(f.iniPath())
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the user's file is not replaced by the link")
	assert.Equal(t, "tweaked\n", readLive(t, f.iniPath()))
}

// TestOverriddenModFile_ACopyThatCannotBeWrittenFallsBackToTheLink is the
// review's third finding: an overridden copy the installer cannot write -
// its directory leaves the mod path through a link - failed the whole mod's
// install, where the same override in the deploy's override pass is only a
// warning. The mod now deploys with its plain link there, and says why.
func TestOverriddenModFile_ACopyThatCannotBeWrittenFallsBackToTheLink(t *testing.T) {
	f := overriddenFixture(t, domain.LinkSymlink)
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(f.game.ModPath, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.game.ModPath, "Data")))

	result := f.deploy(t, "default", core.DeployOptions{})
	assert.Equal(t, 1, result.Deployed, "the mod deploys: %+v", result)
	assert.Empty(t, result.Skipped)
	assert.True(t, containsLine(result.Warnings, "Data/k.ini", "override could not be placed"), "%q", result.Warnings)
	info, err := os.Lstat(filepath.Join(outside, "k.ini"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the mod's own link is there instead")
	assert.Equal(t, "stock\n", readLive(t, f.cachedIni()))
}

// TestOverriddenModFile_VerifyReportsAStaleCopyAndFixRestoresTheLink is the
// review's fourth finding: once the override is taken out of the profile
// by hand, the copy lmm wrote still matches its record, so verify said
// nothing. It is now drift - override_stale, a warning - and --fix puts the
// mod's own file back.
func TestOverriddenModFile_VerifyReportsAStaleCopyAndFixRestoresTheLink(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkSymlink, domain.LinkHardlink} {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})
			f.overriddenProfile(t, "default", true, nil, "k")

			report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
			require.NoError(t, err)
			var stale []core.VerifyFinding
			for _, finding := range report.Result.Findings {
				if finding.Status == core.VerifyStatusOverrideStale {
					stale = append(stale, finding)
				}
			}
			require.Len(t, stale, 1, "%+v", report.Result.Findings)
			assert.Equal(t, "Data/k.ini", stale[0].FileID)
			assert.Contains(t, stale[0].Note, "no longer")
			assert.True(t, stale[0].Fixable, "%+v", stale[0])
			assert.Equal(t, 1, report.Result.Warnings)

			fixed, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
			require.NoError(t, err)
			var statuses []string
			for _, finding := range fixed.Result.Findings {
				statuses = append(statuses, finding.Status)
			}
			assert.Contains(t, statuses, core.VerifyStatusFixedOverrideStale)
			assert.Zero(t, fixed.Result.Warnings, "%+v", fixed.Result.Findings)
			assertPlainDeployment(t, f, method)

			again, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
			require.NoError(t, err)
			assert.Zero(t, again.Result.Warnings, "%+v", again.Result.Findings)
		})
	}
}

// TestOverriddenModFile_VerifyLeavesAFileAnotherGameRecords is the #630
// re-review's blocker: a hardlink or copy deploy over a path another game
// holds records THAT game's fingerprint on this profile's row, which
// matched the other game's file and differed from this mod's cache - so
// verify called it a stale override copy and --fix deleted the other
// game's file. A path another game records is never one.
func TestOverriddenModFile_VerifyLeavesAFileAnotherGameRecords(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkHardlink, domain.LinkCopy} {
		t.Run(method.String(), func(t *testing.T) {
			root := t.TempDir()
			sky := newLegacyFixture(t, handoffGame(t, "sky", root, method))
			sky2 := &legacyFixture{svc: sky.svc, game: handoffGame(t, "sky2", root, domain.LinkCopy)}
			require.NoError(t, sky.svc.SaveGame(ctx, sky2.game))
			sky.profile(t, "default", true, "k")
			sky2.profile(t, "default", true, "j")
			sky2.cachedOnly(t, "default", "j", domain.LinkCopy, map[string]string{"Data/a.esp": "sky2 a"})
			sky.cachedOnly(t, "default", "k", method, map[string]string{"Data/a.esp": "sky a", "Data/k.esp": "sky k"})
			sky2.deploy(t, "default", core.DeployOptions{})
			result := sky.deploy(t, "default", core.DeployOptions{})
			assert.True(t, containsLine(result.Warnings, "Data/a.esp", "sky2"), "%q", result.Warnings)
			live := filepath.Join(sky.game.ModPath, "Data", "a.esp")
			require.Equal(t, "sky2 a", readLive(t, live))

			for _, fix := range []bool{false, true} {
				report, err := sky.svc.VerifyReport(ctx, sky.game, "default", core.VerifyOptions{Fix: fix, Force: true}, nil)
				require.NoError(t, err)
				for _, f := range report.Result.Findings {
					assert.NotEqual(t, core.VerifyStatusOverrideStale, f.Status, "fix=%v: %+v", fix, f)
					assert.NotEqual(t, core.VerifyStatusFixedOverrideStale, f.Status, "fix=%v: %+v", fix, f)
				}
				assert.Equal(t, "sky2 a", readLive(t, live), "fix=%v: the other game's file is untouched", fix)
			}
			// Nor does a profile apply's convergence touch it.
			applyProfile(t, sky.svc, sky.game, "default")
			assert.Equal(t, "sky2 a", readLive(t, live))
		})
	}
}

// TestOverriddenModFile_TheNextDeploySaysWhatItPutThere is the re-review's
// nit: on the deploy after the user's file was set aside, the note said it
// "put the mod's version there" - it put the profile's override there.
func TestOverriddenModFile_TheNextDeploySaysWhatItPutThere(t *testing.T) {
	for _, method := range linkMethods() {
		t.Run(method.String(), func(t *testing.T) {
			f := overriddenFixture(t, method)
			f.deploy(t, "default", core.DeployOptions{})
			replaceWithUsersFile(t, f.iniPath(), "mine\n")
			f.deploy(t, "default", core.DeployOptions{})

			result := f.deploy(t, "default", core.DeployOptions{})
			assertOverriddenCopy(t, f, "tweaked\n")
			for _, w := range append(result.Warnings, result.Notes...) {
				assert.NotContains(t, w, "the mod's version", "%q", w)
			}
			assert.True(t, containsLine(result.Warnings, "Data/k.ini", "the profile's override there again"), "%q", result.Warnings)
			purgeDefault(t, f)
			assert.Equal(t, "mine\n", readLive(t, f.iniPath()), "still the user's file the purge puts back")
		})
	}
}
