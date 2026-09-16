package core_test

// Deletion safety for verify's nested-BepInEx-tree repair (#413 fix round
// 4). --fix removes a link only on positive proof that it is THIS game's own
// unrecorded leftover; any doubt - another game's claim, an unreadable
// directory, a failed ownership lookup, a directory swapped for a symlink
// between the check and the removal, a run scoped to one mod - keeps the
// file and says so.

import (
	"context"
	"errors"
	"io/fs"
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

// newDualEntryGames is the v1+v2 migration shape: ONE install and two
// games.yaml entries - the untouched v1 entry, whose mod_path is
// BepInEx/plugins and which therefore stays on generic-files, and a new
// game-root entry that resolves to bepinex. cachePath, when set, is given
// to both, the way a user copying the old entry would.
func newDualEntryGames(t *testing.T, cachePath string) (svc *core.Service, v1, root *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc = newFlowsTestService(t)
	install := t.TempDir()
	bepinexInstall(t, install, "", domain.LoaderBootstrapNative, time.Now().Add(time.Hour))
	for _, g := range []*domain.Game{
		{ID: "valheim-v1", Name: "Valheim (v1)", InstallPath: install, ModPath: filepath.Join(install, "BepInEx", "plugins"), LinkMethod: domain.LinkSymlink, CachePath: cachePath},
		{ID: "valheim", Name: "Valheim", InstallPath: install, ModPath: install, LinkMethod: domain.LinkSymlink, CachePath: cachePath},
	} {
		require.NoError(t, svc.SaveGame(ctx, g))
		_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(ctx, g.ID)
		require.NoError(t, err)
	}
	v1, err := svc.GetGame("valheim-v1")
	require.NoError(t, err)
	root, err = svc.GetGame("valheim")
	require.NoError(t, err)
	require.Equal(t, "generic-files", svc.AdapterName(v1))
	require.Equal(t, "bepinex", svc.AdapterName(root))
	return svc, v1, root
}

// importInto imports one archive built from members into game's default
// profile.
func importInto(t *testing.T, svc *core.Service, game *domain.Game, name string, members map[string]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	createImportTestZip(t, path, members)
	_, err := svc.ImportArchive(context.Background(), game, "default", path, core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err, name)
}

// cachedFile finds the one file called name under dir.
func cachedFile(t *testing.T, dir, name string) string {
	t.Helper()
	var found string
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name {
			found = path
		}
		return err
	}))
	require.NotEmpty(t, found, "no %s under %s", name, dir)
	return found
}

// symlinkAt creates the directories for link and a symlink there to target.
func symlinkAt(t *testing.T, target, link string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(target, link))
}

// requireLink fails unless a symlink is still at path.
func requireLink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err, "%s must still be there", path)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "%s must still be a link", path)
}

// requireKeptAsForeign runs verify and verify --fix on game and requires
// both to report the nested tree as one lmm cannot prove is its own.
func requireKeptAsForeign(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	for _, fix := range []bool{false, true} {
		report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: fix, Force: true}, nil)
		require.NoError(t, err)
		statuses := findingStatuses(report.Result)
		assert.NotContains(t, statuses, "fixed_loader_nested_tree", "fix=%t", fix)
		assert.NotContains(t, statuses, "loader_nested_tree", "fix=%t", fix)
		assert.Contains(t, statuses, "loader_foreign_nested_tree", "fix=%t", fix)
	}
}

// TestVerifyFix_NeverRemovesAnotherGameEntrysDeployment is the review's
// N2c2: the v1 entry's own, recorded deployment of a BepInEx/-rooted
// archive IS a BepInEx/ tree inside the root entry's plugins directory. It
// belongs to the other entry, so the root entry's --fix must leave it -
// and its rows - exactly where they are.
func TestVerifyFix_NeverRemovesAnotherGameEntrysDeployment(t *testing.T) {
	ctx := context.Background()
	svc, v1, root := newDualEntryGames(t, "")
	importInto(t, svc, v1, "Rooted-1.0.0.zip", map[string]string{
		"BepInEx/plugins/Rooted.dll": "rooted",
		"BepInEx/config/rooted.cfg":  "key = value\n",
	})
	nested := filepath.Join(root.InstallPath, "BepInEx", "plugins", "BepInEx")
	requireLink(t, filepath.Join(nested, "plugins", "Rooted.dll"))

	requireKeptAsForeign(t, svc, root)

	requireLink(t, filepath.Join(nested, "plugins", "Rooted.dll"))
	requireLink(t, filepath.Join(nested, "config", "rooted.cfg"))
	for _, rel := range []string{"BepInEx/plugins/Rooted.dll", "BepInEx/config/rooted.cfg"} {
		_, _, found, err := svc.GetFileOwner(ctx, v1.ID, "default", rel)
		require.NoError(t, err)
		assert.True(t, found, "the v1 entry's record of %s is untouched", rel)
	}
}

// TestVerifyFix_ALinkIntoAnotherGamesCacheIsForeign: a link no game records
// is still not a provable leftover of THIS game when it points into another
// game's cache subtree - lmm's cache is shared by every game, and only this
// game's own part of it says the link is this game's.
func TestVerifyFix_ALinkIntoAnotherGamesCacheIsForeign(t *testing.T) {
	svc, v1, root := newDualEntryGames(t, "")
	importInto(t, svc, v1, "LoosePlugin-1.0.0.zip", map[string]string{"LoosePlugin.dll": "loose"})
	target := cachedFile(t, filepath.Join(svc.GlobalCacheDir(), v1.ID), "LoosePlugin.dll")
	stray := filepath.Join(root.InstallPath, "BepInEx", "plugins", "BepInEx", "plugins", "Stray.dll")
	symlinkAt(t, target, stray)

	requireKeptAsForeign(t, svc, root)
	requireLink(t, stray)
}

// TestVerifyFix_ALinkAnotherGameRecordsIsForeign: two entries sharing a
// cache_path share one cache subtree, so a link into it proves nothing
// about which of them placed it. The other entry's deployed_files row does.
func TestVerifyFix_ALinkAnotherGameRecordsIsForeign(t *testing.T) {
	svc, v1, root := newDualEntryGames(t, t.TempDir())
	importInto(t, svc, v1, "Rooted-1.0.0.zip", map[string]string{"BepInEx/plugins/Rooted.dll": "rooted"})
	deployed := filepath.Join(root.InstallPath, "BepInEx", "plugins", "BepInEx", "plugins", "Rooted.dll")
	requireLink(t, deployed)
	target, err := os.Readlink(deployed)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(target, root.CachePath+string(filepath.Separator)), "fixture: the link points into the shared cache_path, got %s", target)

	requireKeptAsForeign(t, svc, root)
	requireLink(t, deployed)
}

// newNestedLeftoverGame is a game-root bepinex game with a file in its own
// cache and the given nested-tree links (relative to BepInEx/plugins/BepInEx)
// pointing at it - each one a provable, unrecorded leftover.
func newNestedLeftoverGame(t *testing.T, links ...string) (svc *core.Service, game *domain.Game, target string) {
	t.Helper()
	svc, game = newGameRootBepInExGame(t)
	cache := svc.GetGameCache(game)
	require.NoError(t, cache.Store(game.ID, domain.SourceLocal, "gone", "1.0", "Gone.dll", []byte("gone")))
	target = cache.GetFilePath(game.ID, domain.SourceLocal, "gone", "1.0", "Gone.dll")
	for _, l := range links {
		symlinkAt(t, target, nestedPath(game, l))
	}
	return svc, game, target
}

// nestedPath is rel under game's BepInEx/plugins/BepInEx/.
func nestedPath(game *domain.Game, rel string) string {
	return filepath.Join(game.InstallPath, "BepInEx", "plugins", "BepInEx", filepath.FromSlash(rel))
}

// TestVerifyFix_AnUnreadableDirectoryMakesTheTreeForeign: lmm cannot say a
// tree holds nothing but its own leftovers when part of it could not be
// read, so the whole tree is left alone.
func TestVerifyFix_AnUnreadableDirectoryMakesTheTreeForeign(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	svc, game, _ := newNestedLeftoverGame(t, "plugins/L1.dll")
	locked := nestedPath(game, "plugins/locked")
	writeGameFile(t, locked, "secret.dll", "mine")
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	requireKeptAsForeign(t, svc, game)
	requireLink(t, nestedPath(game, "plugins/L1.dll"))
}

// renameDeployedFiles moves the deployed_files table out of the way, so
// every ownership lookup fails, and returns the undo.
func renameDeployedFiles(t *testing.T, svc *core.Service) func() {
	t.Helper()
	require.NoError(t, svc.ExecForTest(context.Background(), `ALTER TABLE deployed_files RENAME TO deployed_files_away`))
	return func() {
		require.NoError(t, svc.ExecForTest(context.Background(), `ALTER TABLE deployed_files_away RENAME TO deployed_files`))
	}
}

// TestVerifyFix_AFailedOwnershipLookupKeepsTheTree: the lookup that says a
// link is unrecorded is the proof the removal rests on, so a lookup that
// fails proves nothing.
func TestVerifyFix_AFailedOwnershipLookupKeepsTheTree(t *testing.T) {
	svc, game, _ := newNestedLeftoverGame(t, "plugins/L1.dll")
	var undo func()
	svc.SetNestedTreeHookForTest(func(stage string) {
		if stage == "classify" {
			undo = renameDeployedFiles(t, svc)
		}
	})
	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NotNil(t, undo, "the classify hook ran")
	undo()
	require.NoError(t, err)
	assert.Contains(t, findingStatuses(report.Result), "loader_foreign_nested_tree")
	assert.NotContains(t, findingStatuses(report.Result), "fixed_loader_nested_tree")
	requireLink(t, nestedPath(game, "plugins/L1.dll"))
}

// TestVerifyFix_OwnershipIsAskedAgainAtRemovalTime: another process (`lmm
// serve` deploying while the CLI verifies) can record a path between the
// classification and the removal, and the lookup can fail there too. Either
// way the link stays.
func TestVerifyFix_OwnershipIsAskedAgainAtRemovalTime(t *testing.T) {
	ctx := context.Background()

	t.Run("recorded meanwhile", func(t *testing.T) {
		svc, game, _ := newNestedLeftoverGame(t, "plugins/L1.dll", "plugins/L2.dll")
		svc.SetNestedTreeHookForTest(func(stage string) {
			if stage == "remove" {
				require.NoError(t, svc.ExecForTest(ctx,
					`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES (?, ?, ?, ?, ?)`,
					game.ID, "other", "BepInEx/plugins/BepInEx/plugins/L1.dll", "local", "late"))
			}
		})
		report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
		require.NoError(t, err)
		row := findingWithStatus(report.Result, "fixed_loader_nested_tree")
		require.NotNil(t, row, "statuses: %v", findingStatuses(report.Result))
		assert.Equal(t, "removed 1 untracked link(s) from BepInEx/plugins/BepInEx/", row.Note)
		requireLink(t, nestedPath(game, "plugins/L1.dll"))
		_, err = os.Lstat(nestedPath(game, "plugins/L2.dll"))
		assert.ErrorIs(t, err, fs.ErrNotExist)
	})

	t.Run("the lookup fails", func(t *testing.T) {
		svc, game, _ := newNestedLeftoverGame(t, "plugins/L1.dll")
		var undo func()
		svc.SetNestedTreeHookForTest(func(stage string) {
			if stage == "remove" {
				undo = renameDeployedFiles(t, svc)
			}
		})
		report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
		require.NotNil(t, undo, "the remove hook ran")
		undo()
		require.NoError(t, err)
		row := findingWithStatus(report.Result, "loader_nested_tree")
		require.NotNil(t, row, "statuses: %v", findingStatuses(report.Result))
		assert.Contains(t, row.FixableReason, "this --fix run could not remove every link")
		assert.False(t, row.Fixable)
		requireLink(t, nestedPath(game, "plugins/L1.dll"))
	})
}

// TestVerifyFix_ADirectorySwappedForASymlinkIsNotFollowed is the review's
// R5: between the classification and the removal a directory in the tree
// becomes a symlink to somewhere outside the game directory that holds a
// link of the same name. The removal must not reach through it.
func TestVerifyFix_ADirectorySwappedForASymlinkIsNotFollowed(t *testing.T) {
	svc, game, target := newNestedLeftoverGame(t, "plugins/a/L1.dll", "plugins/L3.dll")
	outside := t.TempDir()
	symlinkAt(t, target, filepath.Join(outside, "L1.dll"))
	svc.SetNestedTreeHookForTest(func(stage string) {
		if stage != "remove" {
			return
		}
		a := nestedPath(game, "plugins/a")
		require.NoError(t, os.RemoveAll(a))
		require.NoError(t, os.Symlink(outside, a))
	})

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	requireLink(t, filepath.Join(outside, "L1.dll"))
	requireLink(t, nestedPath(game, "plugins/a"))
	row := findingWithStatus(report.Result, "loader_nested_tree")
	require.NotNil(t, row, "a removal refused for doubt is reported: %v", findingStatuses(report.Result))
	assert.Contains(t, row.FixableReason, "this --fix run could not remove every link")
	_, err = os.Lstat(nestedPath(game, "plugins/L3.dll"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "a leftover whose directories are still real is removed")
}

// TestVerifyFix_TheWholeTreeMustBeTheOneClassified: a directory ABOVE the
// tree (BepInEx/ itself) swapped for a symlink between the classification
// and the removal makes every path in the tree name different files.
func TestVerifyFix_TheWholeTreeMustBeTheOneClassified(t *testing.T) {
	svc, game, target := newNestedLeftoverGame(t, "plugins/L1.dll")
	elsewhere := t.TempDir()
	symlinkAt(t, target, filepath.Join(elsewhere, "plugins", "BepInEx", "plugins", "L1.dll"))
	svc.SetNestedTreeHookForTest(func(stage string) {
		if stage != "remove" {
			return
		}
		bep := filepath.Join(game.InstallPath, "BepInEx")
		require.NoError(t, os.Rename(bep, bep+".real"))
		require.NoError(t, os.Symlink(elsewhere, bep))
	})

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	requireLink(t, filepath.Join(elsewhere, "plugins", "BepInEx", "plugins", "L1.dll"))
	requireLink(t, filepath.Join(game.InstallPath, "BepInEx.real", "plugins", "BepInEx", "plugins", "L1.dll"))
	assert.NotContains(t, findingStatuses(report.Result), "fixed_loader_nested_tree")
}

// TestVerifyFix_AModFilterLeavesNestedTreesAlone: a nested tree belongs to
// no one mod, so a --fix the user scoped to one mod (`lmm verify <mod>
// --fix`, the web UI's per-finding Repair) reports it and removes nothing.
func TestVerifyFix_AModFilterLeavesNestedTreesAlone(t *testing.T) {
	svc, game, _ := newNestedLeftoverGame(t, "plugins/L1.dll")
	for _, fix := range []bool{false, true} {
		report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: fix, ModFilter: "some-mod", Force: true}, nil)
		require.NoError(t, err)
		row := findingWithStatus(report.Result, "loader_nested_tree")
		require.NotNil(t, row, "fix=%t statuses: %v", fix, findingStatuses(report.Result))
		assert.False(t, row.Fixable, "fix=%t: a --fix scoped to a mod does not repair it", fix)
		assert.Equal(t, "a --fix limited to one mod leaves this directory alone - run `lmm verify --fix` without naming a mod to remove these links", row.FixableReason)
		assert.Equal(t, 1, report.Result.Issues, "fix=%t", fix)
		requireLink(t, nestedPath(game, "plugins/L1.dll"))
	}
}

// TestVerifyFix_OwnCacheLeftoversAreStillRemoved pins the positive half the
// tests above narrow: an unrecorded link into this game's own cache - the
// global cache's game subtree, or a per-game cache_path - is removed.
func TestVerifyFix_OwnCacheLeftoversAreStillRemoved(t *testing.T) {
	svc, game, _ := newNestedLeftoverGame(t, "plugins/L1.dll", "L2.dll")
	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	row := findingWithStatus(report.Result, "fixed_loader_nested_tree")
	require.NotNil(t, row, "statuses: %v", findingStatuses(report.Result))
	assert.Equal(t, "removed 2 untracked link(s) from BepInEx/plugins/BepInEx/", row.Note)
	_, err = os.Lstat(nestedPath(game, ""))
	assert.True(t, errors.Is(err, fs.ErrNotExist), "the emptied tree is pruned: %v", err)
}
