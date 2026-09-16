package core_test

// A BepInEx/ tree nested inside BepInEx/plugins/ is what a BepInEx/-rooted
// archive becomes when it is deployed into the plugins directory as
// packaged - the v1 configuration - and what is left behind, live and
// unrecorded, when that game's mod_path moves to the game root before a
// purge. BepInEx scans plugins/ recursively, so a plugin in it loads a
// second time beside its correct copy, and BepInEx reads no config in it
// (#413 final review F4).

import (
	"context"
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

// TestVerify_ANestedBepInExTreeLmmLeftBehindIsRepaired follows the refusal
// the way it used to read - move mod_path, with the v1 deployment still in
// place - and requires verify to find the copy that stays behind, and
// --fix to remove it.
func TestVerify_ANestedBepInExTreeLmmLeftBehindIsRepaired(t *testing.T) {
	ctx := context.Background()
	svc, game := newRefusedBepInExGame(t, nil)

	moved := *game
	moved.ModPath = game.InstallPath
	require.NoError(t, svc.SaveGame(ctx, &moved))
	game, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	plan, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
	require.NoError(t, err)
	_, err = svc.ApplyDeploy(ctx, game, plan, core.DeployOptions{}, nil)
	require.NoError(t, err)

	nested := filepath.Join(game.InstallPath, "BepInEx", "plugins", "BepInEx")
	require.DirExists(t, nested, "the v1 copy of the rooted archive is still there")

	report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	row := findingWithStatus(report.Result, "loader_nested_tree")
	require.NotNil(t, row, "statuses: %v", findingStatuses(report.Result))
	assert.Equal(t, "BepInEx/plugins/BepInEx/ is a BepInEx/ tree nested inside BepInEx/plugins/, holding 2 link(s) into lmm's mod cache that no profile records - "+
		"BepInEx loads every plugin anywhere under BepInEx/plugins/, so a plugin in it loads a second time beside the copy deployed where it belongs, and BepInEx reads no config in it", row.Note)
	assert.True(t, row.Fixable)
	assert.Empty(t, row.FixableReason)

	fixed, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	row = findingWithStatus(fixed.Result, "fixed_loader_nested_tree")
	require.NotNil(t, row, "statuses: %v", findingStatuses(fixed.Result))
	assert.Equal(t, "removed 2 untracked link(s) from BepInEx/plugins/BepInEx/", row.Note)
	assert.False(t, row.Fixable)
	assert.NoDirExists(t, nested)

	clean, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	for _, f := range clean.Result.Findings {
		assert.Equal(t, "ok", f.Status, "nothing is left to report: %+v", f)
	}
	assert.Zero(t, clean.Result.Issues)
	assert.Zero(t, clean.Result.Warnings)
	assert.Equal(t, []string{
		"BepInEx/config/rooted.cfg",
		"BepInEx/plugins/CoolFolder/CoolFolder.dll",
		"BepInEx/plugins/CoolFolder/assets.bundle",
		"BepInEx/plugins/LoosePlugin-1.0.0/LoosePlugin.dll",
		"BepInEx/plugins/Rooted.dll",
	}, deployedModTree(t, game.InstallPath))
}

// newGameRootBepInExGame is a correctly configured BepInEx game: BepInEx
// installed and mod_path at the game root, so it resolves to bepinex.
func newGameRootBepInExGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := newVerifyLoaderService(t, nil)
	bepinexInstall(t, game.InstallPath, "", domain.LoaderBootstrapNative, time.Now().Add(time.Hour))
	require.Equal(t, "bepinex", svc.AdapterName(game))
	return svc, game
}

// writeGameFile writes content at root/rel, creating its directories.
func writeGameFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// TestVerify_ANestedBepInExTreeLmmDidNotPlaceIsOnlyReported: nothing proves
// a hand-extracted archive, or a link into somewhere other than lmm's
// cache, is lmm's to remove - so it is reported as a warning, --fix leaves
// every byte of it where it is, and the row says why.
func TestVerify_ANestedBepInExTreeLmmDidNotPlaceIsOnlyReported(t *testing.T) {
	ctx := context.Background()
	svc, game := newGameRootBepInExGame(t)
	hand := writeGameFile(t, game.InstallPath, "BepInEx/plugins/Hand/bepinex/plugins/Hand.dll", "hand")
	elsewhere := writeGameFile(t, t.TempDir(), "Other.dll", "other")
	link := filepath.Join(game.InstallPath, "BepInEx", "plugins", "Hand", "bepinex", "plugins", "Other.dll")
	require.NoError(t, os.Symlink(elsewhere, link))

	for _, fix := range []bool{false, true} {
		report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: fix, Force: true}, nil)
		require.NoError(t, err)
		row := findingWithStatus(report.Result, "loader_foreign_nested_tree")
		require.NotNil(t, row, "fix=%t statuses: %v", fix, findingStatuses(report.Result))
		assert.Equal(t, "BepInEx/plugins/Hand/bepinex/ is a BepInEx/ tree nested inside BepInEx/plugins/, holding 2 file(s) lmm has no record of placing - "+
			"BepInEx loads every plugin anywhere under BepInEx/plugins/, so a plugin in it loads from there (a second time, if it is also installed where it belongs), and BepInEx reads no config in it", row.Note)
		assert.False(t, row.Fixable)
		assert.Equal(t, "lmm cannot tell that every file in it is its own, so --fix leaves the directory alone - move what belongs to BepInEx up into BepInEx/, or delete the directory", row.FixableReason)
		assert.Equal(t, 1, report.Result.Warnings, "fix=%t: a warning, not an issue", fix)
		assert.Zero(t, report.Result.Issues)
		assert.FileExists(t, hand)
		_, err = os.Lstat(link)
		assert.NoError(t, err)
	}
}

// TestVerify_ANestedBepInExTreeIsReportedOnlyWhereItIsALeftover: a path a
// profile RECORDS is where lmm deploys a file today - its layout put it
// there, and the per-mod checks own it - and a game on another adapter has
// no BepInEx layout to be nested inside (the v1 game's own deployment of a
// rooted archive is exactly this shape, and exactly what it asked for).
func TestVerify_ANestedBepInExTreeIsReportedOnlyWhereItIsALeftover(t *testing.T) {
	ctx := context.Background()

	t.Run("a recorded deployment", func(t *testing.T) {
		svc, game := newGameRootBepInExGame(t)
		seedInstalledMod(t, svc, game, "local", "odd", "1.0", true, map[string][]byte{
			"BepInEx/plugins/BepInEx/plugins/Odd.dll": []byte("odd"),
		})
		seedProfileWithMod(t, svc, game.ID, "default", "local", "odd", "1.0")
		res, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
		require.NoError(t, err)
		require.Empty(t, res.Skipped)
		_, err = os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", "BepInEx", "plugins", "Odd.dll"))
		require.NoError(t, err)

		report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		for _, status := range findingStatuses(report.Result) {
			assert.False(t, strings.Contains(status, "nested_tree"), "statuses: %v", findingStatuses(report.Result))
		}
	})

	t.Run("a game on another adapter", func(t *testing.T) {
		svc, game := newGameRootBepInExGame(t)
		acknowledged := *game
		acknowledged.Adapter = "generic-files"
		require.NoError(t, svc.SaveGame(ctx, &acknowledged))
		writeGameFile(t, game.InstallPath, "BepInEx/plugins/BepInEx/plugins/Hand.dll", "hand")

		report, err := svc.VerifyReport(ctx, &acknowledged, "default", core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		for _, status := range findingStatuses(report.Result) {
			assert.False(t, strings.Contains(status, "nested_tree"), "statuses: %v", findingStatuses(report.Result))
		}
	})

	t.Run("an empty directory", func(t *testing.T) {
		svc, game := newGameRootBepInExGame(t)
		require.NoError(t, os.MkdirAll(filepath.Join(game.InstallPath, "BepInEx", "plugins", "BepInEx", "config"), 0o755))
		report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		for _, status := range findingStatuses(report.Result) {
			assert.False(t, strings.Contains(status, "nested_tree"), "nothing in it loads or is misread: %v", findingStatuses(report.Result))
		}
	})
}
