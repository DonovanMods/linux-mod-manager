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
