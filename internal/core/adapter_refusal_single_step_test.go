package core_test

// The single-step deploy paths honour an adapter refusal like every Plan
// does (#413). `lmm mod enable`, verify --fix's re-link and re-deploy
// repairs, and the merged-artifact resync every mutation ends with have no
// plan snapshot to carry the check, so each one asks the adapter itself
// before it deploys - and a refused game keeps its tree exactly as it was.
// The removals (disable, purge, uninstall, the uninstall-to-zero of a
// merged artifact) are the deliberate exception and still run.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// byteTreeSnapshot describes every entry under root - its type, a symlink's
// target and a regular file's content hash - so two snapshots are equal
// exactly when the tree is byte-identical.
func byteTreeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := rel + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			entry += " " + hex.EncodeToString(sum[:])
		}
		out = append(out, entry)
		return nil
	}))
	sort.Strings(out)
	return out
}

// refusalOf is the error every Plan gives game - the message and remedy a
// single-step flow must repeat.
func refusalOf(t *testing.T, svc *core.Service, game *domain.Game) error {
	t.Helper()
	_, err := svc.PlanDeploy(context.Background(), game, "default", core.DeployOptions{})
	require.Error(t, err, "fixture: every Plan refuses this game")
	return err
}

// onlyMod returns the one installed mod in game's default profile.
func onlyMod(t *testing.T, svc *core.Service, game *domain.Game) domain.InstalledMod {
	t.Helper()
	mods, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	return mods[0]
}

// TestEnableMod_RefusesWhereTheAdapterIsRefused: enabling a mod deploys
// it, so on a refused game it refuses - with the very error a deploy
// gives - and touches nothing. Under the identity routing a refused game
// falls back to, the deploy would link the seeded BepInEx/config files over
// the user's own.
func TestEnableMod_RefusesWhereTheAdapterIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, edit := range refusals {
		t.Run(name, func(t *testing.T) {
			svc, game, dotfile := newGameRootWithUserConfigs(t)
			mod := onlyMod(t, svc, game)
			_, err := svc.DisableMod(ctx, game, "default", mod.SourceID, mod.ID)
			require.NoError(t, err)
			game = refuse(t, svc, game, edit)
			want := refusalOf(t, svc, game)
			before := byteTreeSnapshot(t, game.InstallPath)

			_, err = svc.EnableMod(ctx, game, "default", mod.SourceID, mod.ID)
			require.Error(t, err)
			assert.Equal(t, want.Error(), err.Error(), "the refusal and remedy every other flow gives")

			assert.Equal(t, before, byteTreeSnapshot(t, game.InstallPath), "the tree is byte-identical")
			requireUserConfigsKept(t, game, dotfile)
			row, err := svc.GetInstalledMod(ctx, mod.SourceID, mod.ID, game.ID, "default")
			require.NoError(t, err)
			assert.False(t, row.Enabled, "the row stays disabled")
		})
	}

	t.Run("a precondition refusal", func(t *testing.T) {
		svc, game, _ := newPlanFixtureWithAdapter(t, nil)
		refused := switchTo(t, svc, game)
		_, err := svc.DisableMod(ctx, game, "default", "acme", "m1")
		require.NoError(t, err)
		refused.Store(true)

		_, err = svc.EnableMod(ctx, game, "default", "acme", "m1")
		require.Error(t, err)
		assert.ErrorIs(t, err, adapter.ErrPreconditionUnmet)
		assert.NoFileExists(t, filepath.Join(game.ModPath, "a.esp"))
	})
}

// switchTo registers a switchableRefuser as game's adapter, consenting
// until the returned flag is set.
func switchTo(t *testing.T, svc *core.Service, game *domain.Game) *atomic.Bool {
	t.Helper()
	refuse := &atomic.Bool{}
	svc.RegisterAdapter(switchableRefuser{refuse: refuse})
	game.Adapter = "switchable"
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return refuse
}

// TestVerifyFix_TheVersionRepairRefusesWhereTheAdapterIsRefused: the
// version repair renames the cache entry and then re-links the deployment
// into it, so it cannot run half of itself on a refused game - it is
// reported, with the refusal as its reason, and nothing moves.
func TestVerifyFix_TheVersionRepairRefusesWhereTheAdapterIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, game := newVersionRepairFixGame(t, true)
	refused := switchTo(t, svc, game)
	refused.Store(true)
	before := byteTreeSnapshot(t, game.ModPath)
	cacheEntry := svc.GetGameCache(game).ModPath(game.ID, "test-src", "mod1", "1.5")

	for _, fix := range []bool{false, true} {
		result, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull, Fix: fix}, nil)
		require.NoError(t, err)
		f := mismatchFinding(t, result.Findings)
		assert.Equal(t, "version_mismatch", f.Status, "fix=%t", fix)
		assert.False(t, f.Fixable, "fix=%t: --fix will not re-link on a refused game", fix)
		assert.Contains(t, f.FixableReason, "install the loader first", "fix=%t: the row names the refusal", fix)
		assert.Equal(t, 1, result.Issues, "fix=%t", fix)
	}

	assert.Equal(t, before, byteTreeSnapshot(t, game.ModPath), "the deployment is untouched")
	assert.DirExists(t, cacheEntry, "the cache entry was not renamed")
	row, err := svc.GetInstalledMod(ctx, "test-src", "mod1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", row.Version, "the record was not corrected")
	assert.True(t, row.Deployed)
}

// TestVerifyFix_TheLoaderRedeployRefusesWhereTheAdapterIsRefused: a
// BepInEx mod whose plugin is gone from the game directory is reported on a
// refused game too, but --fix does not re-deploy it - the identity routing
// would link the mod's configs over the user's.
func TestVerifyFix_TheLoaderRedeployRefusesWhereTheAdapterIsRefused(t *testing.T) {
	ctx := context.Background()
	for name, edit := range refusals {
		t.Run(name, func(t *testing.T) {
			svc, game := newVerifyLoaderService(t, &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative})
			bepinexInstall(t, game.InstallPath, "", domain.LoaderBootstrapNative, time.Now().Add(time.Hour))
			svc, game, dotfile := userConfigsOn(t, svc, game)
			require.NoError(t, os.Remove(filepath.Join(game.InstallPath, "BepInEx", "plugins", "Rooted.dll")))
			game = refuse(t, svc, game, edit)
			want := refusalOf(t, svc, game)
			before := byteTreeSnapshot(t, game.InstallPath)

			for _, fix := range []bool{false, true} {
				report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: fix, Force: true}, nil)
				require.NoError(t, err)
				row := findingWithStatus(report.Result, "loader_plugin_unlinked")
				require.NotNil(t, row, "fix=%t statuses: %v", fix, findingStatuses(report.Result))
				assert.False(t, row.Fixable, "fix=%t", fix)
				assert.Contains(t, row.FixableReason, want.Error(), "fix=%t: the row names the refusal and its remedy", fix)
			}

			assert.Equal(t, before, byteTreeSnapshot(t, game.InstallPath), "the tree is byte-identical")
			requireUserConfigsKept(t, game, dotfile)
		})
	}
}

// refusableCompileAdapter is a compile adapter with a precondition that can
// be switched on - the one refusal a compile game's merged-artifact resync
// could otherwise walk past, since it resolves the compiler just fine.
type refusableCompileAdapter struct {
	testCompileAdapter
	refuse *atomic.Bool
}

// CheckPreconditions implements adapter.Preconditioner.
func (a refusableCompileAdapter) CheckPreconditions(*domain.Game, []domain.InstalledMod) error {
	if a.refuse.Load() {
		return errLoaderMissing
	}
	return nil
}

// newRefusableCompileGame is newMergedPakTestGame with the given exmodz
// mods, their merged artifact built and deployed, and the compile adapter
// replaced by one whose precondition the returned flag switches on.
func newRefusableCompileGame(t *testing.T, mods ...string) (*core.Service, *domain.Game, *atomic.Bool) {
	t.Helper()
	ctx := context.Background()
	svc, game, _ := newMergedPakTestGame(t)
	for _, id := range mods {
		seedEnabledExmodzMod(t, svc, game, "fake-compiler", id, "1.0", id+"-file", []byte(id))
	}
	base, err := svc.AdapterFor(game)
	require.NoError(t, err)
	mc, ok := adapter.Compiler(base)
	require.True(t, ok)
	refuse := &atomic.Bool{}
	svc.RegisterAdapter(refusableCompileAdapter{testCompileAdapter: testCompileAdapter{MergeCompiler: mc}, refuse: refuse})
	_, err = svc.SyncMergedPak(ctx, game, "default")
	require.NoError(t, err)
	requireArtifactDeployed(t, game)
	return svc, game, refuse
}

// artifactBytes reads game's deployed merged artifact.
func artifactBytes(t *testing.T, game *domain.Game) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(game.ModPath, mergedArtifactName))
	require.NoError(t, err)
	return data
}

// TestMergedArtifact_ARefusedAdapterIsNeverRebuilt: every mutation that
// changes a merge input ends with a resync, and a resync DEPLOYS. On a
// game whose adapter refuses, the artifact stays exactly as it was and the
// flow says why - while removing it, which is what uninstalling the last
// source does, still happens.
func TestMergedArtifact_ARefusedAdapterIsNeverRebuilt(t *testing.T) {
	ctx := context.Background()

	t.Run("disable", func(t *testing.T) {
		svc, game, refuse := newRefusableCompileGame(t, "bear-mount", "wolf-mount")
		was := artifactBytes(t, game)
		refuse.Store(true)
		res, err := svc.DisableMod(ctx, game, "default", "fake-compiler", "wolf-mount")
		require.NoError(t, err)
		assert.Equal(t, was, artifactBytes(t, game), "the artifact was not rebuilt")
		assert.Contains(t, joinNotes(res.Notes, res.Warnings), "install the loader first")
	})

	t.Run("reorder", func(t *testing.T) {
		svc, game, refuse := newRefusableCompileGame(t, "bear-mount", "wolf-mount")
		was := artifactBytes(t, game)
		refuse.Store(true)
		err := svc.ReorderProfileMods(ctx, game.ID, "default", []domain.ModReference{
			{SourceID: "fake-compiler", ModID: "wolf-mount", Version: "1.0"},
			{SourceID: "fake-compiler", ModID: "bear-mount", Version: "1.0"},
		})
		require.NoError(t, err)
		assert.Equal(t, was, artifactBytes(t, game), "the artifact was not rebuilt")
	})

	t.Run("uninstall with a source left", func(t *testing.T) {
		svc, game, refuse := newRefusableCompileGame(t, "bear-mount", "wolf-mount")
		was := artifactBytes(t, game)
		refuse.Store(true)
		plan, err := svc.PlanUninstall(ctx, game, "default", "fake-compiler", "wolf-mount", core.UninstallOptions{})
		require.NoError(t, err)
		assert.Nil(t, plan.MergedArtifact, "the dry run says nothing happens to the artifact - the resync is refused")
		res, err := svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
		require.NoError(t, err)
		assert.Equal(t, was, artifactBytes(t, game))
		assert.Contains(t, joinNotes(res.Notes, res.Warnings), "install the loader first")
	})

	t.Run("uninstall the last source", func(t *testing.T) {
		svc, game, refuse := newRefusableCompileGame(t, "bear-mount")
		refuse.Store(true)
		plan, err := svc.PlanUninstall(ctx, game, "default", "fake-compiler", "bear-mount", core.UninstallOptions{})
		require.NoError(t, err)
		require.NotNil(t, plan.MergedArtifact)
		assert.Equal(t, core.MergedArtifactRemove, plan.MergedArtifact.Action)
		_, err = svc.ApplyUninstall(ctx, game, plan, core.UninstallOptions{})
		require.NoError(t, err)
		assert.False(t, artifactOnDisk(game), "a removal still runs")
	})

	t.Run("a missing artifact is not put back", func(t *testing.T) {
		svc, game, refuse := newRefusableCompileGame(t, "bear-mount")
		require.NoError(t, os.Remove(filepath.Join(game.ModPath, mergedArtifactName)))
		refuse.Store(true)
		_, err := svc.SyncMergedPak(ctx, game, "default")
		require.Error(t, err)
		assert.ErrorIs(t, err, adapter.ErrPreconditionUnmet)
		assert.False(t, artifactOnDisk(game), "the fast path's redeploy is a deploy too")
	})

	t.Run("verify --fix", func(t *testing.T) {
		svc, game, refuse := newRefusableCompileGame(t, "bear-mount", "wolf-mount")
		was := artifactBytes(t, game)
		// Change a merge input behind lmm's back, so the artifact is stale.
		require.NoError(t, svc.SetModEnabledForTest(ctx, "fake-compiler", "wolf-mount", game.ID, "default", false))
		refuse.Store(true)
		for _, fix := range []bool{false, true} {
			report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: fix, Force: true}, nil)
			require.NoError(t, err)
			row := findingWithStatus(report.Result, "stale_compile")
			require.NotNil(t, row, "fix=%t statuses: %v", fix, findingStatuses(report.Result))
			assert.False(t, row.Fixable, "fix=%t", fix)
			assert.Contains(t, row.FixableReason, "install the loader first", "fix=%t", fix)
		}
		assert.Equal(t, was, artifactBytes(t, game))
	})
}

// joinNotes flattens a flow's diagnostics for a substring assertion.
func joinNotes(groups ...[]string) string {
	var out string
	for _, g := range groups {
		for _, s := range g {
			out += s + "\n"
		}
	}
	return out
}

// TestInstaller_BuiltForARefusedGameDeploysNothing is the backstop under
// every gate above: an Installer core builds for a game whose adapter does
// not resolve refuses every deploy with that refusal, so a path that forgot
// to ask fails instead of deploying through the identity routing.
func TestInstaller_BuiltForARefusedGameDeploysNothing(t *testing.T) {
	ctx := context.Background()
	for name, edit := range refusals {
		t.Run(name, func(t *testing.T) {
			svc, game, dotfile := newGameRootWithUserConfigs(t)
			mod := onlyMod(t, svc, game)
			_, err := svc.DisableMod(ctx, game, "default", mod.SourceID, mod.ID)
			require.NoError(t, err)
			game = refuse(t, svc, game, edit)
			_, want := svc.AdapterFor(game)
			before := byteTreeSnapshot(t, game.InstallPath)

			installer := svc.GetInstallerForTest(game)
			err = installer.Install(ctx, game, &mod.Mod, "default")
			require.Error(t, err)
			assert.Equal(t, want.Error(), err.Error())
			err = installer.Replace(ctx, game, &mod.Mod, &mod.Mod, "default")
			require.Error(t, err)
			assert.Equal(t, want.Error(), err.Error())

			assert.Equal(t, before, byteTreeSnapshot(t, game.InstallPath))
			requireUserConfigsKept(t, game, dotfile)
		})
	}
}
