package core_test

// snapshot_external_test.go is where #350's two halves - the originals
// store and snapshot create/restore - meet #269's EXTERNAL mods.
//
// The rule those two features have to agree on is one sentence: an
// external mod's files are never deployed, captured, undeployed or
// restored by lmm, because the Steam client owns them where they sit. Each
// test below pins one consequence of that sentence, and each one would
// have failed before the rebase that put the two features in the same
// tree: the plan resolved a Workshop item against a Tier-1 source that
// serves no files, and turned a perfectly restorable snapshot into a
// preview full of refusals for mods it was never going to touch.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newExternalSnapshotFixture is newSnapshotFixture with a Steam Workshop
// item alongside the managed mod: one mod lmm deployed over a stock file
// (so the originals store has something in it), and one mod Steam owns.
//
// It returns the Steam-owned directory, so every test can assert what it
// asserts about the whole feature in one line - nothing under there moved.
func newExternalSnapshotFixture(t *testing.T) (svc *core.Service, game *domain.Game, dataDir, steamDir string) {
	t.Helper()
	svc, dataDir = newOriginalsService(t)
	installDir, modDir := t.TempDir(), t.TempDir()
	game = &domain.Game{
		ID: "g1", Name: "Game", InstallPath: installDir, ModPath: modDir,
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"steamworkshop": "1133870"},
	}

	stock := filepath.Join(modDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as shipped"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")
	steamDir = seedExternalMod(t, svc, game, "default", "3617086610", "Workshop Item")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return svc, game, dataDir, steamDir
}

// steamTreeFingerprint reads every regular file under dir into a
// path->content map. Comparing two of them proves "nothing under
// ExternalPath was touched" as a fact about bytes rather than about which
// code paths a test believes it exercised.
func steamTreeFingerprint(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	}))
	return out
}

// --- the originals store ---

func TestExternal_OriginalsCapture_NeverStoresAnythingForAnExternalMod(t *testing.T) {
	svc, game, dataDir, steamDir := newExternalSnapshotFixture(t)
	before := steamTreeFingerprint(t, steamDir)

	// A second deploy is the capturing path: Installer.Install runs for
	// every mod the deploy resolves, and captureOriginal sits immediately
	// before linker.Deploy. The external mod is partitioned out before it
	// ever reaches an Installer, so nothing of Steam's can be stored.
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	doc, err := svc.CreateSnapshot(context.Background(), game, "default", "after-deploy")
	require.NoError(t, err)
	assert.Equal(t, 1, doc.Originals, "only the stock file the MANAGED mod replaced is in the store")

	snap, err := svc.LoadSnapshot(context.Background(), "g1", "after-deploy")
	require.NoError(t, err)
	for _, row := range snap.Originals {
		assert.NotEqual(t, "steamworkshop", row.SourceID,
			"the store must hold nothing lmm attributes to a Steam Workshop item")
		assert.NotContains(t, row.RelativePath, "3617086610")
	}
	// The whole claim, as bytes: Steam's directory is exactly as it was.
	assert.Equal(t, before, steamTreeFingerprint(t, steamDir))

	// And nothing of Steam's was linked into the game directory either.
	_, statErr := os.Lstat(filepath.Join(game.ModPath, "mod.pak"))
	assert.True(t, os.IsNotExist(statErr), "lmm deploys no file for a mod Steam already placed")
	assert.NoFileExists(t, filepath.Join(dataDir, "snapshots", "g1", "_originals", "files", "mod_path", "mod.pak"))
}

func TestExternal_Uninstall_NeverReachesTheOriginalsStore(t *testing.T) {
	svc, game, _, steamDir := newExternalSnapshotFixture(t)
	before := steamTreeFingerprint(t, steamDir)

	// Uninstalling the EXTERNAL mod is tracking-only (#269): it must not
	// go through Installer.Uninstall, which is where #350 puts a captured
	// original back. If it did, the stock file the MANAGED mod replaced
	// would come back under a mod that is still deployed.
	_, err := svc.UninstallMod(context.Background(), game, "default", "steamworkshop", "3617086610", core.UninstallOptions{})
	require.NoError(t, err)

	deployed := filepath.Join(game.ModPath, "Data", "shipped.esp")
	target, err := os.Readlink(deployed)
	require.NoError(t, err, "the managed mod's deployment must still be a link lmm owns")
	assert.NotEmpty(t, target)

	result, err := svc.CreateSnapshot(context.Background(), game, "default", "after-uninstall")
	require.NoError(t, err)
	assert.Equal(t, 1, result.Originals, "the replaced stock file is still held, not released")
	assert.Equal(t, before, steamTreeFingerprint(t, steamDir))
}

// --- the snapshot document ---

func TestExternal_Snapshot_RoundTripsTheExternalFields(t *testing.T) {
	svc, game, _, steamDir := newExternalSnapshotFixture(t)

	_, err := svc.CreateSnapshot(context.Background(), game, "default", "with-workshop")
	require.NoError(t, err)

	// Read back off DISK, not from the in-memory value: the two fields
	// #345 added to domain.InstalledMod have to survive the snapshot's own
	// JSON encoding, or a restore cannot tell a Workshop item from a mod
	// it is supposed to download.
	doc, err := svc.LoadSnapshot(context.Background(), "g1", "with-workshop")
	require.NoError(t, err)

	var recorded *domain.InstalledMod
	for i := range doc.Installed {
		if doc.Installed[i].ID == "3617086610" {
			recorded = &doc.Installed[i]
		}
	}
	require.NotNil(t, recorded, "an external mod is installed, so the snapshot records it")
	assert.True(t, recorded.External)
	assert.Equal(t, steamDir, recorded.ExternalPath)

	for _, f := range doc.DeployedFiles {
		assert.NotEqual(t, "steamworkshop", f.SourceID,
			"lmm deployed no file for it, so the deployed manifest has none to record")
	}
}

// --- restore ---

func TestExternal_PlanSnapshotRestore_AsksNoSourceAboutAWorkshopItem(t *testing.T) {
	svc, game, _, _ := newExternalSnapshotFixture(t)
	_, err := svc.CreateSnapshot(context.Background(), game, "default", "point-a")
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(context.Background(), game, "point-a")
	require.NoError(t, err)

	// No "steamworkshop" source is registered on this Service at all, so a
	// plan that resolved the row would fail to fetch it and record a
	// refusal. That it does not is the proof the short-circuit is taken.
	assert.Empty(t, plan.Refusals, "a Workshop item is not a mod the restore has to fetch")

	var row *core.SnapshotRestoreMod
	for i := range plan.Mods {
		if plan.Mods[i].ModID == "3617086610" {
			row = &plan.Mods[i]
		}
	}
	require.NotNil(t, row, "the preview accounts for every mod the snapshot recorded")
	assert.True(t, row.External)
	assert.False(t, row.ExternalMissing, "Steam still has it")
	assert.Empty(t, row.Error)
	assert.False(t, row.Cached, `"not cached" here must not read as "will be downloaded"`)

	// And it is out of the purge preview, named instead - PurgePlan's own
	// split, for the same reason: lmm undeploys none of them.
	for _, m := range plan.ToPurge {
		assert.False(t, m.External, "an external mod is not previewed as something that will be undeployed")
	}
	assert.Equal(t, []string{"Workshop Item"}, plan.External)
}

func TestExternal_ApplySnapshotRestore_LeavesTheWorkshopItemExactlyAsSteamHasIt(t *testing.T) {
	svc, game, _, steamDir := newExternalSnapshotFixture(t)
	before := steamTreeFingerprint(t, steamDir)

	_, err := svc.CreateSnapshot(context.Background(), game, "default", "point-a")
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(context.Background(), game, "point-a")
	require.NoError(t, err)
	result, err := svc.ApplySnapshotRestore(context.Background(), game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Refused, "nothing about a Workshop item is a refusal")
	assert.Empty(t, result.Warnings)

	// It stays TRACKED: the row survives the purge/converge round trip
	// with both #345 fields intact.
	mod, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3617086610", "g1", "default")
	require.NoError(t, err)
	assert.True(t, mod.External)
	assert.Equal(t, steamDir, mod.ExternalPath)
	assert.True(t, mod.Enabled)

	// Nothing under ExternalPath was read for content, moved or rewritten,
	// and nothing was linked out of it into the game directory.
	assert.Equal(t, before, steamTreeFingerprint(t, steamDir))
	_, statErr := os.Lstat(filepath.Join(game.ModPath, "mod.pak"))
	assert.True(t, os.IsNotExist(statErr), "a restore re-links lmm's own mods, never Steam's")
}

func TestExternal_SnapshotRestore_AMissingSteamDirectoryIsAFindingNotARefusal(t *testing.T) {
	svc, game, _, steamDir := newExternalSnapshotFixture(t)
	_, err := svc.CreateSnapshot(context.Background(), game, "default", "point-a")
	require.NoError(t, err)

	// The item was unsubscribed in Steam after the snapshot was taken.
	// lmm never held a copy, so it cannot put it back - and it never
	// claimed it would, which is why this must not stop the restore.
	require.NoError(t, os.RemoveAll(steamDir))

	plan, err := svc.PlanSnapshotRestore(context.Background(), game, "point-a")
	require.NoError(t, err)
	assert.Empty(t, plan.Refusals, "a gone subscription is not a refusal - lmm promised nothing about it")
	var row *core.SnapshotRestoreMod
	for i := range plan.Mods {
		if plan.Mods[i].ModID == "3617086610" {
			row = &plan.Mods[i]
		}
	}
	require.NotNil(t, row)
	assert.True(t, row.External)
	assert.True(t, row.ExternalMissing, "the preview says so before anything is touched")

	result, err := svc.ApplySnapshotRestore(context.Background(), game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err, "the restore completes; the Workshop item is reported, not fatal")
	assert.Empty(t, result.Refused)
	require.Len(t, result.Warnings, 1, "and it is unconditional, not verbose-gated")
	assert.Contains(t, result.Warnings[0], "Workshop Item")
	assert.Contains(t, result.Warnings[0], "re-subscribe in the Steam client")

	// Still tracked: lmm does not delete a row because Steam moved on.
	mod, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3617086610", "g1", "default")
	require.NoError(t, err)
	assert.True(t, mod.External)
}
