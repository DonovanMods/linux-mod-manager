package core_test

// snapshot_restore_test.go is #350's headline claim, tested the only way
// that means anything: a fixture game directory is snapshotted, modded
// further, restored, and compared BYTE FOR BYTE against what it was.
//
// The other tests here cover the refusal semantics - the promise that a
// restore never quietly does less than it says.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// treeOf walks root and returns path -> content for every regular file and
// every symlink (recorded as its target), so two states can be compared
// exactly rather than approximately. A symlink is content: the difference
// between "the file is a link into the cache" and "the file IS those bytes"
// is precisely what a link method decides.
//
// A regular file's entry carries its PERMISSION bits too (review finding
// 6): "byte for byte" that ignores the mode would call a stock launcher
// script restored non-executable an exact restore.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			out[rel] = "-> " + target
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		out[rel] = fmt.Sprintf("%04o %s", info.Mode().Perm(), data)
		return nil
	})
	require.NoError(t, err)
	return out
}

// newRestoreFixture builds a game with two mods, one of which deploys over
// a stock file, and returns the service, the game and the seeded mods.
func newRestoreFixture(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, dataDir := newOriginalsService(t)
	installDir, modDir := t.TempDir(), t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: installDir, ModPath: modDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	stock := filepath.Join(modDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "keeper", "Keeper", "1.0", true,
		map[string][]byte{"Data/keeper.esp": []byte("keeper v1")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "keeper", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return svc, game, dataDir
}

// TestApplySnapshotRestore_ReturnsTheGameDirectoryToItsRecordedState is the
// claim #350 is for. A mod is added AFTER the snapshot, and that mod
// destroys a stock file on its way in; the restore has to remove the mod's
// files, put the stock file back, and leave the rest untouched - down to
// the byte.
func TestApplySnapshotRestore_ReturnsTheGameDirectoryToItsRecordedState(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()

	before := treeOf(t, game.ModPath)

	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	// Now make a mess: a second mod that overwrites stock content.
	seedNamedInstalledMod(t, svc, game, "src", "wrecker", "Wrecker", "2.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("WRECKED"), "Data/extra.esp": []byte("extra")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "wrecker", "2.0")
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	messy := treeOf(t, game.ModPath)
	require.NotEqual(t, before, messy, "the fixture must actually have changed, or the restore proves nothing")
	assert.Contains(t, messy[filepath.Join("Data", "shipped.esp")], "WRECKED")

	plan, err := svc.PlanSnapshotRestore(ctx, game, "known-good")
	require.NoError(t, err)
	assert.Equal(t, "known-good", plan.Snapshot)
	assert.Empty(t, plan.Refusals)
	require.Len(t, plan.Originals, 1)
	assert.Equal(t, core.SnapshotOriginalRestorable, plan.Originals[0].Status)

	result, err := svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.OriginalsRestored)
	assert.Positive(t, result.Purged)
	assert.Empty(t, result.OriginalsSkipped)
	assert.Empty(t, result.Refused)

	assert.Equal(t, before, treeOf(t, game.ModPath),
		"the game directory must come back byte-identical to the snapshotted state")
}

// TestApplySnapshotRestore_TakesASafetySnapshotFirst pins the default that
// makes restore itself reversible: a restore's whole purpose is to discard
// the present state, so the way back from it is on by default.
func TestApplySnapshotRestore_TakesASafetySnapshotFirst(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()

	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "known-good")
	require.NoError(t, err)
	result, err := svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err)

	require.NotEmpty(t, result.SafetySnapshot)
	listing, err := svc.ListSnapshots(ctx, "g1")
	require.NoError(t, err)
	names := map[string]bool{}
	for _, row := range listing.Snapshots {
		names[row.Name] = row.Auto
	}
	require.Contains(t, names, result.SafetySnapshot)
	assert.True(t, names[result.SafetySnapshot], "the safety copy is marked automatic")
}

func TestApplySnapshotRestore_NoSafetySnapshotSuppressesIt(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "known-good")
	require.NoError(t, err)
	result, err := svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.SafetySnapshot)

	listing, err := svc.ListSnapshots(ctx, "g1")
	require.NoError(t, err)
	require.Len(t, listing.Snapshots, 1)
}

// TestPlanSnapshotRestore_NamesAVersionTheSourceCanNoLongerServe is the
// refusal promise: the user learns a mod cannot come back BEFORE anything
// is purged, in the plan they are asked to approve - never as a surprise
// afterwards.
func TestPlanSnapshotRestore_NamesAVersionTheSourceCanNoLongerServe(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()

	// A mod in the profile whose source is not registered at all: the
	// strongest form of "cannot be served".
	seedNamedInstalledMod(t, svc, game, "gone-source", "ghost", "Ghost Mod", "3.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "gone-source", "ghost", "3.0")

	_, err := svc.CreateSnapshot(ctx, game, "default", "with-ghost")
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "with-ghost")
	require.NoError(t, err)

	require.Len(t, plan.Refusals, 1)
	assert.Equal(t, "ghost", plan.Refusals[0].ModID)
	assert.Equal(t, "Ghost Mod", plan.Refusals[0].Name, "the recorded name is used, since the source cannot be asked")
	assert.NotEmpty(t, plan.Refusals[0].Reason)

	var ghost *core.SnapshotRestoreMod
	for i := range plan.Mods {
		if plan.Mods[i].ModID == "ghost" {
			ghost = &plan.Mods[i]
		}
	}
	require.NotNil(t, ghost)
	assert.NotEmpty(t, ghost.Error, "the row itself carries the reason too")
}

// TestPlanSnapshotRestore_MarksAnOriginalItCannotVouchFor: a stored
// original whose bytes no longer match its recorded checksum is the one
// case where writing it back would be worse than not - lmm would be
// putting corrupted content into a game directory under the claim that it
// is the user's original.
func TestPlanSnapshotRestore_MarksAnOriginalItCannotVouchFor(t *testing.T) {
	svc, game, dataDir := newRestoreFixture(t)
	ctx := context.Background()

	seedNamedInstalledMod(t, svc, game, "src", "wrecker", "Wrecker", "2.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("WRECKED")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "wrecker", "2.0")
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	_, err = svc.CreateSnapshot(ctx, game, "default", "point")
	require.NoError(t, err)

	// Corrupt the stored original behind lmm's back.
	stored := filepath.Join(dataDir, "snapshots", "g1", "_originals", "files", "mod_path", "Data", "shipped.esp")
	require.NoError(t, os.WriteFile(stored, []byte("corrupted"), 0600))

	plan, err := svc.PlanSnapshotRestore(ctx, game, "point")
	require.NoError(t, err)
	require.Len(t, plan.Originals, 1)
	assert.Equal(t, core.SnapshotOriginalUnavailable, plan.Originals[0].Status)
	assert.Contains(t, plan.Originals[0].Reason, "does not match")

	result, err := svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
	require.NoError(t, err)
	assert.Zero(t, result.OriginalsRestored)
	require.Len(t, result.OriginalsSkipped, 1,
		"a partial restore says which parts were partial; it never reports plain success")
	assert.Equal(t, "Data/shipped.esp", result.OriginalsSkipped[0].RelativePath)

	// And the corrupted bytes were NOT written into the game directory.
	onDisk, err := os.ReadFile(filepath.Join(game.ModPath, "Data", "shipped.esp"))
	require.NoError(t, err)
	assert.NotEqual(t, "corrupted", string(onDisk))
}

// TestApplySnapshotRestore_RefusesAStalePlan pins Ruling 5 for this flow.
func TestApplySnapshotRestore_RefusesAStalePlan(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "known-good")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "late", "Late Arrival", "1.0", true,
		map[string][]byte{"late.esp": []byte("late")})

	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

func TestPlanSnapshotRestore_UnknownSnapshotIsTyped(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	_, err := svc.PlanSnapshotRestore(context.Background(), game, "never-taken")
	require.ErrorIs(t, err, core.ErrSnapshotNotFound)
}

// TestApplySnapshotRestore_RestoresTheProfileDocument pins that a restore
// puts the LOAD ORDER and the locks back, not just the files - the profile
// document is the desired state, and it is what the convergence reads.
func TestApplySnapshotRestore_RestoresTheProfileDocument(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()

	require.NoError(t, pm.SetModLock(ctx, "g1", "default", "src", "keeper", "1.0"))
	_, err := svc.CreateSnapshot(ctx, game, "default", "locked")
	require.NoError(t, err)

	require.NoError(t, pm.ClearModLock(ctx, "g1", "default", "src", "keeper"))
	after, err := pm.Get(ctx, "g1", "default")
	require.NoError(t, err)
	require.False(t, after.FindRef("src", "keeper").Locked, "guard: the lock really was cleared")

	plan, err := svc.PlanSnapshotRestore(ctx, game, "locked")
	require.NoError(t, err)
	assert.True(t, plan.ProfileChanged, "the plan says the profile file will be rewritten")

	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
	require.NoError(t, err)

	restored, err := pm.Get(ctx, "g1", "default")
	require.NoError(t, err)
	require.NotNil(t, restored.FindRef("src", "keeper"))
	assert.True(t, restored.FindRef("src", "keeper").Locked, "the lock came back with the profile document")
}

// TestApplySnapshotRestore_RestoresTheRecordedUpdatePolicy pins the
// settings stage: an update policy lives on the DB row, which no profile
// document can express, so the restore puts it back from the recorded rows.
func TestApplySnapshotRestore_RestoresTheRecordedUpdatePolicy(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()

	_, err := svc.SetModUpdatePolicy(ctx, "src", "keeper", "g1", "default", domain.UpdatePinned)
	require.NoError(t, err)
	_, err = svc.CreateSnapshot(ctx, game, "default", "pinned")
	require.NoError(t, err)

	_, err = svc.SetModUpdatePolicy(ctx, "src", "keeper", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "pinned")
	require.NoError(t, err)
	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
	require.NoError(t, err)

	mods, err := svc.GetInstalledMods(ctx, "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, domain.UpdatePinned, mods[0].UpdatePolicy)
}

// TestApplySnapshotRestore_EmitsAPhaseForEachStage pins the live stream a
// frontend renders: a restore is three visible stages, and a user watching
// it needs to know which one is running.
func TestApplySnapshotRestore_EmitsAPhaseForEachStage(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()

	seedNamedInstalledMod(t, svc, game, "src", "wrecker", "Wrecker", "2.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("WRECKED")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "wrecker", "2.0")
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	_, err = svc.CreateSnapshot(ctx, game, "default", "point")
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "point")
	require.NoError(t, err)

	var phases []string
	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true},
		func(e core.Event) {
			if fe, ok := e.(core.FlowEvent); ok {
				phases = append(phases, fe.FlowPhase().String())
			}
		})
	require.NoError(t, err)

	assert.Contains(t, phases, "deploy_purging")
	assert.Contains(t, phases, "snapshot_restoring_originals")
	assert.Contains(t, phases, "snapshot_original_restored")
	assert.Contains(t, phases, "snapshot_converging")
}

// TestApplySnapshotRestore_RestoreIsIdempotent: restoring twice in a row
// leaves the same state, which is what makes a restore safe to retry after
// an interrupted one.
func TestApplySnapshotRestore_RestoreIsIdempotent(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	_, err := svc.CreateSnapshot(ctx, game, "default", "known-good")
	require.NoError(t, err)

	var trees []map[string]string
	for range 2 {
		plan, err := svc.PlanSnapshotRestore(ctx, game, "known-good")
		require.NoError(t, err)
		_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{NoSafetySnapshot: true}, nil)
		require.NoError(t, err)
		trees = append(trees, treeOf(t, game.ModPath))
	}
	assert.Equal(t, trees[0], trees[1])
}

// newRestoreFixtureWithConfig is newRestoreFixture with a config.yaml
// written BEFORE the Service opens - the only way to exercise a config key,
// since core.Load runs once at construction.
func newRestoreFixtureWithConfig(t *testing.T, configYAML string) (*core.Service, *domain.Game, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	configDir, dataDir := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0644))

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	installDir, modDir := t.TempDir(), t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: installDir, ModPath: modDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedNamedInstalledMod(t, svc, game, "src", "keeper", "Keeper", "1.0", true,
		map[string][]byte{"Data/keeper.esp": []byte("keeper v1")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "keeper", "1.0")
	return svc, game, dataDir
}

// seedInstalledModInProfile is seedNamedInstalledMod for a profile other
// than "default" - the two-profile fixture review finding 2 needs.
func seedInstalledModInProfile(t *testing.T, svc *core.Service, game *domain.Game, profileName, sourceID, modID, name, version string, files map[string][]byte) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	for path, content := range files {
		require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, path, content))
	}
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: modID, SourceID: sourceID, Name: name, Version: version, GameID: game.ID,
		},
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	seedProfileWithMod(t, svc, game.ID, profileName, sourceID, modID, version)
}

// TestApplySnapshotRestore_CarriesTheProfileSwitch is review finding 2: a
// restore of a snapshot taken under a NON-ACTIVE profile used to purge that
// profile's installed set (nothing of which was on disk) and then deploy it
// ON TOP of whatever the active profile had deployed - two profiles' files
// in the game directory at once, with the other one still nominally active.
//
// A restore says "put this game back to the state this snapshot records",
// and that state includes which profile is the active one, so the restore
// carries the switch: the active profile is undeployed first and the
// snapshot's profile becomes the default.
func TestApplySnapshotRestore_CarriesTheProfileSwitch(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()

	// The snapshot is taken under "default", which is active now.
	_, err := svc.CreateSnapshot(ctx, game, "default", "under-default")
	require.NoError(t, err)

	// Switch to "other", whose one mod deploys a file of its own.
	seedInstalledModInProfile(t, svc, game, "other", "src", "otherling", "Otherling", "1.0",
		map[string][]byte{"Data/other.esp": []byte("other v1")})
	require.NoError(t, pm.SetDefault(ctx, game.ID, "other"))
	_, err = svc.DeployProfile(ctx, game, "other", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(game.ModPath, "Data", "other.esp"))

	plan, err := svc.PlanSnapshotRestore(ctx, game, "under-default")
	require.NoError(t, err)
	assert.Equal(t, "other", plan.ActiveProfile,
		"the plan must SAY that the restore changes the active profile")
	require.Len(t, plan.ToPurgeActive, 1, "and what it will undeploy to get there")

	result, err := svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "other", result.SwitchedFrom)

	active, err := pm.GetDefault(ctx, game.ID)
	require.NoError(t, err)
	assert.Equal(t, "default", active.Name, "the snapshot's profile is the active one after its restore")

	assert.NoFileExists(t, filepath.Join(game.ModPath, "Data", "other.esp"),
		"the other profile's deployment must not survive a restore of a different profile")
	assert.FileExists(t, filepath.Join(game.ModPath, "Data", "keeper.esp"),
		"and the snapshot's own mods must be deployed")
}

// TestApplySnapshotRestore_TheActiveProfileHasAFreshnessPrecondition is
// re-review finding N7. Stage 1 undeploys the ACTIVE profile's installed
// set, taken at plan time - and that set was the one input to the apply that
// no precondition re-checked, so a mod installed into the active profile
// between the preview and the confirmation was purged from a stale list and
// left deployed under a profile the restore had just switched away from.
// Ruling 5's precondition covers both profiles the plan touches now.
func TestApplySnapshotRestore_TheActiveProfileHasAFreshnessPrecondition(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()
	pm := svc.NewProfileManager()

	_, err := svc.CreateSnapshot(ctx, game, "default", "under-default")
	require.NoError(t, err)

	seedInstalledModInProfile(t, svc, game, "other", "src", "otherling", "Otherling", "1.0",
		map[string][]byte{"Data/other.esp": []byte("other v1")})
	require.NoError(t, pm.SetDefault(ctx, game.ID, "other"))
	_, err = svc.DeployProfile(ctx, game, "other", core.DeployOptions{}, nil)
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "under-default")
	require.NoError(t, err)
	require.Equal(t, "other", plan.ActiveProfile)
	require.Len(t, plan.ToPurgeActive, 1)

	// The world moves under the plan - in the ACTIVE profile, not the
	// snapshot's own.
	seedInstalledModInProfile(t, svc, game, "other", "src", "latecomer", "Latecomer", "1.0",
		map[string][]byte{"Data/late.esp": []byte("late v1")})
	_, err = svc.DeployProfile(ctx, game, "other", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(game.ModPath, "Data", "late.esp"))

	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan,
		"a restore must not act on a stale picture of the profile it undeploys")

	assert.FileExists(t, filepath.Join(game.ModPath, "Data", "late.esp"),
		"and it must not have started: the refusal is a precondition, not a partial")
}

// TestApplySnapshotRestore_ADisabledModComesBackDisabledAndUndeployed is
// review finding 3. domain.ModReference carries no enabled flag, so the
// snapshot's profile document lists a disabled mod like any other;
// PlanProfileApply therefore re-enabled it, the deploy stage put its files
// on disk, and the recorded enabled=false was written back afterwards with
// nothing undeployed - a wrong restore AND exactly the enabled=false,
// deployed=true desync #183's self-heal exists to clean up.
func TestApplySnapshotRestore_ADisabledModComesBackDisabledAndUndeployed(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()

	// A second mod, disabled before the snapshot is taken.
	seedNamedInstalledMod(t, svc, game, "src", "sleeper", "Sleeper", "1.0", true,
		map[string][]byte{"Data/sleeper.esp": []byte("sleeper v1")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "sleeper", "1.0")
	_, err := svc.DisableMod(ctx, game, "default", "src", "sleeper")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(game.ModPath, "Data", "sleeper.esp"))

	_, err = svc.CreateSnapshot(ctx, game, "default", "sleeper-off")
	require.NoError(t, err)

	// Turn it on again, so the restore has something to undo.
	_, err = svc.EnableMod(ctx, game, "default", "src", "sleeper")
	require.NoError(t, err)
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(game.ModPath, "Data", "sleeper.esp"))

	plan, err := svc.PlanSnapshotRestore(ctx, game, "sleeper-off")
	require.NoError(t, err)
	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err)

	mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	byID := map[string]domain.InstalledMod{}
	for _, m := range mods {
		byID[m.ID] = m
	}
	sleeper, ok := byID["sleeper"]
	require.True(t, ok)
	assert.False(t, sleeper.Enabled, "the snapshot recorded it disabled")
	assert.False(t, sleeper.Deployed, "and a disabled mod is not deployed")
	assert.NoFileExists(t, filepath.Join(game.ModPath, "Data", "sleeper.esp"),
		"the disabled mod's file must NOT be on disk after a restore")

	keeper, ok := byID["keeper"]
	require.True(t, ok)
	assert.True(t, keeper.Enabled, "and the enabled one is unaffected")
	assert.FileExists(t, filepath.Join(game.ModPath, "Data", "keeper.esp"))
}

// TestApplySnapshotRestore_ARestoredOriginalKeepsItsMode is review finding
// 6. OriginalFile recorded root, path, sha256, size, time, op and
// provenance - but no mode - and originalsStore.restore chmod-ed
// unconditionally to 0644. An executable file lmm replaced came back
// non-executable, which is not hypothetical for the shape this store
// exists to cover: mod_path == install_path (the BepInEx / #267 shape)
// puts launcher scripts, wrappers and shipped binaries squarely in range.
func TestApplySnapshotRestore_ARestoredOriginalKeepsItsMode(t *testing.T) {
	svc, game, _ := newRestoreFixture(t)
	ctx := context.Background()

	// A stock launcher script, executable, at a path a mod is about to
	// deploy over.
	launcher := filepath.Join(game.ModPath, "run.sh")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\necho stock\n"), 0755))

	_, err := svc.CreateSnapshot(ctx, game, "default", "with-launcher")
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "wrapper", "Wrapper", "1.0", true,
		map[string][]byte{"run.sh": []byte("#!/bin/sh\necho modded\n")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "wrapper", "1.0")
	_, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	plan, err := svc.PlanSnapshotRestore(ctx, game, "with-launcher")
	require.NoError(t, err)
	_, err = svc.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{}, nil)
	require.NoError(t, err)

	info, err := os.Stat(launcher)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0755), info.Mode().Perm(),
		"a restored original must keep the mode it had, or an executable comes back unrunnable")

	data, err := os.ReadFile(launcher)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho stock\n", string(data))
}
