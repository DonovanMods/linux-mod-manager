package core_test

// plugins_review_test.go holds #616's independent-review probes, each a
// case that lost or overwrote something of the user's before its fix.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *pluginFixture) writeList(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(f.listDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.listDir, "Plugins.txt"), []byte(content), 0o644))
}

func (f *pluginFixture) external(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, n), pluginBytes(0), 0o644))
	}
}

func (f *pluginFixture) purge(t *testing.T) *core.PurgeResult {
	t.Helper()
	plan, err := f.svc.PlanPurge(context.Background(), f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	res, err := f.svc.ApplyPurge(context.Background(), f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	return res
}

func (f *pluginFixture) reorder(t *testing.T) {
	t.Helper()
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, f.svc.ReorderProfileMods(context.Background(), f.game.ID, "default", profile.Mods))
}

// B1: the game's Data folder on a drive that is not mounted. Before the
// fix every plugin looked uninstalled, and a reorder wrote an empty list
// over the user's Creations.
func TestPluginsReview_UnreadableModPathLeavesTheListAlone(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Creation.esp")
	f.writeList(t, "*Creation.esp\n")
	f.deploy(t)
	require.Equal(t, "*Creation.esp\n*A.esp\n", f.list(t))

	require.NoError(t, os.Rename(f.game.ModPath, f.game.ModPath+".away"))
	f.reorder(t)
	assert.Equal(t, "*Creation.esp\n*A.esp\n", f.list(t), "a plugin folder lmm cannot read is never taken for an empty one")

	res := f.verify(t, false)
	row := findingWithStatus(res, core.PluginFindingUnwritable)
	require.NotNil(t, row, "statuses: %v", findingStatuses(res))
	assert.Contains(t, row.Note, f.game.ModPath, "the finding names the folder")
	assert.GreaterOrEqual(t, res.Issues, 1)
	assert.Equal(t, "*Creation.esp\n*A.esp\n", f.list(t))
}

// B1, an empty mount point: the folder reads, and holds nothing. The
// database still says the profile deploys plugins, so the folder is not
// the game's.
func TestPluginsReview_EmptyMountPointLeavesTheListAlone(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Creation.esp")
	f.writeList(t, "*Creation.esp\n")
	f.deploy(t)

	require.NoError(t, os.Rename(f.game.ModPath, f.game.ModPath+".away"))
	require.NoError(t, os.MkdirAll(f.game.ModPath, 0o755))
	f.reorder(t)
	assert.Equal(t, "*Creation.esp\n*A.esp\n", f.list(t))
}

// B1, an empty mount point with no plugin of lmm's deployed: the game's own
// masters are missing too, which no real Data folder is without.
func TestPluginsReview_AFolderWithoutTheGamesMastersIsNotTheGames(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Creation.esp")
	f.writeList(t, "*Creation.esp\n")
	f.deploy(t)
	_, err := f.svc.DisableMod(context.Background(), f.game, "default", "src", "a")
	require.NoError(t, err)
	require.Equal(t, "*Creation.esp\n", f.list(t))

	require.NoError(t, os.Rename(f.game.ModPath, f.game.ModPath+".away"))
	require.NoError(t, os.MkdirAll(f.game.ModPath, 0o755))
	f.reorder(t)
	assert.Equal(t, "*Creation.esp\n", f.list(t))
}

// B2: an external plugin the user turned on after lmm's first write stays
// on through a purge; before the fix it went back to its kept state.
func TestPluginsReview_PurgeKeepsAnExternalTurnedOnSince(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Foo.esp")
	f.writeList(t, "Foo.esp\n")
	f.deploy(t)
	require.Equal(t, "Foo.esp\n*A.esp\n", f.list(t))

	f.writeList(t, "*Foo.esp\n*A.esp\n") // turned on in the game
	res := f.purge(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListRestored, res.Plugins.Status)
	assert.Equal(t, "*Foo.esp\n", f.list(t))
}

// B2: an external the user moved since keeps its new place, and an entry
// only the kept file has (one lmm dropped because it was not installed)
// comes back where it was.
func TestPluginsReview_PurgeKeepsTheUsersOrderAndRestoresWhatLmmDropped(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "X.esp", "Y.esp")
	f.writeList(t, "*X.esp\n*Old.esp\n*Y.esp\n")
	f.deploy(t)
	require.Equal(t, "*X.esp\n*Y.esp\n*A.esp\n", f.list(t), "Old.esp is not installed, so lmm drops it")

	f.writeList(t, "*Y.esp\n*X.esp\n*A.esp\n") // the user swapped them
	f.purge(t)
	assert.Equal(t, "*Y.esp\n*X.esp\n*Old.esp\n", f.list(t))
}

// B2: nothing outside lmm changed: the kept file comes back byte for byte.
func TestPluginsReview_PurgeRestoresBytesWhenOnlyLmmChanged(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "X.esp")
	original := "# mine\r\n*X.esp\r\n   \r\n*NotInstalled.esp\r\n"
	f.writeList(t, original)
	f.deploy(t)
	f.purge(t)
	assert.Equal(t, original, f.list(t))
}

// B2, Skyrim LE: both files keep what the user changed since lmm's first
// write - before the fix both came back byte for byte.
func TestPluginsReview_PurgeKeepsExternalChangesInSkyrimLE(t *testing.T) {
	f := newPluginFixture(t)
	f.game.ID = "skyrim"
	f.listDir = filepath.Join(f.prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim")
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Foo.esp", "Bar.esp")
	order := filepath.Join(f.listDir, "loadorder.txt")
	f.writeList(t, "")
	require.NoError(t, os.WriteFile(order, []byte("Skyrim.esm\nFoo.esp\n"), 0o644))
	f.deploy(t)
	require.Equal(t, "A.esp\n", f.list(t))
	require.Equal(t, "Skyrim.esm\nFoo.esp\nA.esp\n", readFileString(t, order))

	// The user turns Foo on and adds Bar.
	f.writeList(t, "Foo.esp\nA.esp\nBar.esp\n")
	require.NoError(t, os.WriteFile(order, []byte("Skyrim.esm\nFoo.esp\nA.esp\nBar.esp\n"), 0o644))
	f.purge(t)
	assert.Equal(t, "Foo.esp\nBar.esp\n", f.list(t))
	assert.Equal(t, "Skyrim.esm\nFoo.esp\nBar.esp\n", readFileString(t, order))
}

// Review 3: a save the game made between lmm's read and its write is read
// again, not written over.
func TestPluginsReview_AListSavedMidSyncIsReadAgain(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Bought.esp")
	f.writeList(t, "")
	once := false
	defer core.SetPluginListBeforeWriteForTest(func() {
		if !once {
			once = true
			f.writeList(t, "*Bought.esp\n") // the Creations menu saves
		}
	})()
	res := f.deploy(t)
	assert.Equal(t, core.PluginListWritten, res.Plugins.Status)
	assert.Equal(t, "*Bought.esp\n*A.esp\n", f.list(t))
}

// Review 3: a list that keeps changing is left to the game, with a finding.
func TestPluginsReview_AListThatKeepsChangingIsLeftAlone(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.writeList(t, "")
	n := 0
	defer core.SetPluginListBeforeWriteForTest(func() {
		n++
		f.writeList(t, "# save "+string(rune('0'+n))+"\n")
	})()
	res := f.deploy(t)
	assert.Equal(t, core.PluginListSkipped, res.Plugins.Status)
	require.NotEmpty(t, res.Plugins.Findings)
	assert.Equal(t, core.PluginFindingUnwritable, res.Plugins.Findings[0].Code)
	assert.Equal(t, "# save 2\n", f.list(t), "the game's last save stands")
}

// Review 4: a copy-with-replace that leaves the copy disabled removes the
// replaced plugin, and the list follows.
func TestPluginsReview_ACopyThatTurnsAPluginOffUpdatesTheList(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.deploy(t)
	require.Equal(t, "*A.esp\n", f.list(t))

	ctx := context.Background()
	pm := f.svc.NewProfileManager()
	_, err := pm.Create(ctx, f.game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, f.game.ID, "other", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", Disabled: true}))
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "a", SourceID: "src", Name: "a", Version: "1.0", GameID: f.game.ID},
		ProfileName: "other", UpdatePolicy: domain.UpdateNotify, Enabled: false,
	}))
	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "other", To: "default", SourceID: "src", ModID: "a"})
	require.NoError(t, err)
	_, err = f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{Replace: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "", f.list(t), "the replaced, now disabled, plugin leaves the list")
}

// Review 6: the list is final before uninstall.after_all (an uninstall)
// and before purge's uninstall.after_all.
func TestPluginsReview_WrittenBeforeTheUninstallAfterAllHooks(t *testing.T) {
	for _, flow := range []string{"uninstall", "purge"} {
		t.Run(flow, func(t *testing.T) {
			f := newPluginFixture(t)
			f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
			f.mod(t, "b", map[string][]byte{"B.esp": pluginBytes(0)})
			f.deploy(t)
			list := filepath.Join(f.listDir, "Plugins.txt")
			seen := filepath.Join(t.TempDir(), "seen.txt")
			script := filepath.Join(t.TempDir(), "after_all.sh")
			require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat '"+list+"' > '"+seen+"' 2>/dev/null || echo ABSENT > '"+seen+"'\n"), 0o755))
			seedHooks(t, f.svc, f.game, "default", domain.GameHooks{Uninstall: domain.HookConfig{AfterAll: script}})

			if flow == "uninstall" {
				_, err := f.svc.UninstallMod(context.Background(), f.game, "default", "src", "a", core.UninstallOptions{})
				require.NoError(t, err)
				assert.Equal(t, "*B.esp\n", readFileString(t, seen), "the hook saw the list without the uninstalled plugin")
				return
			}
			f.purge(t)
			assert.Equal(t, "ABSENT\n", readFileString(t, seen), "the hook saw the purge's restore (lmm created the list, so it is gone)")
		})
	}
}

// Review 8: an external plugin whose name uses a byte Windows-1252 leaves
// undefined is kept, written back exactly as it was read.
func TestPluginsReview_KeepsAnExternalWithAnUndefinedByte(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Odd\u0081.esp")
	f.writeList(t, "*Odd\x81.esp\n")
	f.deploy(t)
	assert.Equal(t, "*Odd\x81.esp\n*A.esp\n", f.list(t))
}

// Review 8, Skyrim LE: an external whose name has no Windows-1252 spelling
// stays in loadorder.txt (UTF-8), though Plugins.txt cannot hold it.
func TestPluginsReview_KeepsANon1252ExternalInLoadorderTxt(t *testing.T) {
	f := newPluginFixture(t)
	f.game.ID = "skyrim"
	f.listDir = filepath.Join(f.prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim")
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "日本.esp")
	order := filepath.Join(f.listDir, "loadorder.txt")
	f.writeList(t, "")
	require.NoError(t, os.WriteFile(order, []byte("Skyrim.esm\n日本.esp\n"), 0o644))
	f.deploy(t)
	assert.Equal(t, "Skyrim.esm\n日本.esp\nA.esp\n", readFileString(t, order))
	assert.Equal(t, "A.esp\n", f.list(t))
}

// Review 9: verify counts a list it cannot read as an issue, as the deploy
// reports it.
func TestPluginsReview_VerifyCountsAnUnreadableListAsAnIssue(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.deploy(t)
	list := filepath.Join(f.listDir, "Plugins.txt")
	require.NoError(t, os.Remove(list))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "elsewhere.txt"), list))
	res := f.verify(t, false)
	row := findingWithStatus(res, core.PluginFindingUnwritable)
	require.NotNil(t, row, "statuses: %v", findingStatuses(res))
	assert.GreaterOrEqual(t, res.Issues, 1)
}

// Review 10: the game moved to another prefix. A purge puts the kept list
// back into the prefix the game uses now instead of failing forever.
func TestPluginsReview_PurgeAfterThePrefixMoved(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.writeList(t, "# mine\n")
	f.deploy(t)

	moved := filepath.Join(t.TempDir(), "pfx")
	require.NoError(t, os.Rename(f.prefix, moved))
	f.game.Prefix = moved
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	f.listDir = filepath.Join(moved, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition")

	res := f.purge(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListRestored, res.Plugins.Status)
	assert.Equal(t, "# mine\n", f.list(t))
	assert.Empty(t, readPrefixOriginals(t, f.dataDir, f.game.ID))
}

// Re-review 1: the prefix is away for now - a Steam library on a drive that
// is unplugged - and the game resolves to no other prefix. The kept copy
// stays, with a finding, and a purge once the drive is back puts it back;
// before the fix it was dropped, and the pre-lmm list lost for good.
func TestPluginsReview_PurgeKeepsACopyWhosePrefixIsAwayForNow(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.writeList(t, "# mine\n")
	f.deploy(t)

	away := f.prefix + ".unplugged"
	require.NoError(t, os.Rename(f.prefix, away))
	res := f.purge(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListSkipped, res.Plugins.Status)
	require.NotEmpty(t, res.Plugins.Findings)
	assert.Contains(t, res.Plugins.Findings[0].Message, "cannot be found now")
	assert.Len(t, readPrefixOriginals(t, f.dataDir, f.game.ID), 1, "the kept copy stays")

	require.NoError(t, os.Rename(away, f.prefix)) // the drive is back
	again := f.purge(t)
	require.NotNil(t, again.Plugins)
	assert.Equal(t, core.PluginListRestored, again.Plugins.Status)
	assert.Equal(t, "# mine\n", f.list(t))
	assert.Empty(t, readPrefixOriginals(t, f.dataDir, f.game.ID))
}

// Re-review 1, the nit: re-targeted into a prefix the game moved to, an
// entry only the kept file has goes back switched off when its plugin is
// not installed.
func TestPluginsReview_PurgeIntoAMovedPrefixDoesNotSwitchOnMissingPlugins(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.external(t, "Mine.esp")
	f.writeList(t, "*Mine.esp\n*Absent.esp\n")
	f.deploy(t)

	require.NoError(t, os.RemoveAll(f.prefix))
	fresh := prefixTree(t, t.TempDir(), "steamuser")
	f.game.Prefix = fresh
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	f.listDir = filepath.Join(fresh, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition")

	res := f.purge(t)
	require.NotNil(t, res.Plugins)
	assert.Equal(t, core.PluginListRestored, res.Plugins.Status)
	assert.Equal(t, "*Mine.esp\nAbsent.esp\n", f.list(t))
}

// Re-review 2: `deploy --purge` whose purge removes the only mod and leaves
// nothing to deploy still writes the list (it returned before the sync).
func TestPluginsReview_DeployPurgeWithNothingLeftSyncsTheList(t *testing.T) {
	f := newPluginFixture(t)
	f.mod(t, "a", map[string][]byte{"A.esp": pluginBytes(0)})
	f.deploy(t)
	require.Equal(t, "*A.esp\n", f.list(t))

	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "default")
	require.NoError(t, err)
	profile.Mods[0].Disabled = true
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), profile))
	require.NoError(t, f.svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "a", SourceID: "src", Name: "a", Version: "1.0", GameID: f.game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: false, Deployed: true,
	}))

	res, err := f.svc.DeployProfile(context.Background(), f.game, "default", core.DeployOptions{Purge: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Deployed)
	assertAbsent(t, filepath.Join(f.game.ModPath, "A.esp"), "the purge pass removed it")
	require.NotNil(t, res.Plugins, "the deploy reports the list it synced")
	assert.Equal(t, "", f.list(t))
}
