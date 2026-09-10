package core_test

// snapshot_test.go covers `snapshot create|list|delete` (#350): what the
// document records, what the listing says and in what order, and the two
// refusals (a taken name, a name that is not a file name).

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

// newSnapshotFixture returns a Service with one deployed mod, a stock file
// the deploy replaced, and a profile override over another - so a snapshot
// taken from it exercises all four halves of the document at once.
func newSnapshotFixture(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, dataDir := newOriginalsService(t)
	installDir, modDir := t.TempDir(), t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: installDir, ModPath: modDir,
		LinkMethod: domain.LinkSymlink,
	}

	stock := filepath.Join(modDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as shipped"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return svc, game, dataDir
}

func TestCreateSnapshot_RecordsTheWholeArrangement(t *testing.T) {
	svc, game, dataDir := newSnapshotFixture(t)

	result, err := svc.CreateSnapshot(context.Background(), game, "default", "before-tweaks")
	require.NoError(t, err)
	assert.Equal(t, "before-tweaks", result.Name)
	assert.Equal(t, "g1", result.GameID)
	assert.Equal(t, "default", result.Profile)
	assert.False(t, result.Auto)
	assert.False(t, result.CreatedAt.IsZero())
	assert.Equal(t, 1, result.Mods)
	assert.Equal(t, 1, result.DeployedFiles)
	assert.Equal(t, 1, result.Originals, "the stock file the deploy replaced is in force")
	assert.Positive(t, result.SizeBytes)
	assert.Equal(t, filepath.Join(dataDir, "snapshots", "g1", "before-tweaks.json"), result.Path)
	assert.FileExists(t, result.Path)

	doc, err := svc.LoadSnapshot(context.Background(), "g1", "before-tweaks")
	require.NoError(t, err)

	require.NotNil(t, doc.ProfileDocument, "the profile export is the desired state a restore converges to")
	require.Len(t, doc.ProfileDocument.Mods, 1)
	assert.Equal(t, "m1", doc.ProfileDocument.Mods[0].ModID)
	assert.Equal(t, "1.0", doc.ProfileDocument.Mods[0].Version)

	require.Len(t, doc.Installed, 1, "the DB rows carry what a profile ref cannot - policy, convert_paks, flags")
	assert.Equal(t, "m1", doc.Installed[0].ID)
	assert.Equal(t, domain.UpdateNotify, doc.Installed[0].UpdatePolicy)

	require.Len(t, doc.DeployedFiles, 1)
	assert.Equal(t, "Data/shipped.esp", doc.DeployedFiles[0].RelativePath)
	assert.Equal(t, "src", doc.DeployedFiles[0].SourceID)
	assert.NotEmpty(t, doc.DeployedFiles[0].SHA256, "the manifest is a record, not a guess: every present path is hashed")
	assert.False(t, doc.DeployedFiles[0].Missing)

	require.Len(t, doc.Originals, 1)
	assert.Equal(t, "Data/shipped.esp", doc.Originals[0].RelativePath)
	assert.Equal(t, core.OriginalRootModPath, doc.Originals[0].Root)
}

// TestCreateSnapshot_RecordsATrackedPathThatIsGone pins the honest
// treatment of a missing file: "tracked and absent" is a real state and
// different from "never tracked", so the row stays and says so.
func TestCreateSnapshot_RecordsATrackedPathThatIsGone(t *testing.T) {
	svc, game, _ := newSnapshotFixture(t)
	require.NoError(t, os.Remove(filepath.Join(game.ModPath, "Data", "shipped.esp")))

	_, err := svc.CreateSnapshot(context.Background(), game, "default", "after-loss")
	require.NoError(t, err)

	doc, err := svc.LoadSnapshot(context.Background(), "g1", "after-loss")
	require.NoError(t, err)
	require.Len(t, doc.DeployedFiles, 1)
	assert.True(t, doc.DeployedFiles[0].Missing)
	assert.Empty(t, doc.DeployedFiles[0].SHA256)
}

// TestCreateSnapshot_RefusesATakenName pins that a snapshot is never
// silently overwritten: it is the only copy of an arrangement the user
// asked lmm to remember.
func TestCreateSnapshot_RefusesATakenName(t *testing.T) {
	svc, game, _ := newSnapshotFixture(t)
	_, err := svc.CreateSnapshot(context.Background(), game, "default", "taken")
	require.NoError(t, err)

	_, err = svc.CreateSnapshot(context.Background(), game, "default", "taken")
	require.ErrorIs(t, err, core.ErrSnapshotExists)
}

func TestCreateSnapshot_RefusesANameThatIsNotAFileName(t *testing.T) {
	svc, game, _ := newSnapshotFixture(t)
	for _, bad := range []string{"", "  ", "a/b", `a\b`, "..", "../escape", ".hidden"} {
		_, err := svc.CreateSnapshot(context.Background(), game, "default", bad)
		require.ErrorIsf(t, err, core.ErrInvalidSnapshotName, "name %q must be refused", bad)
	}
}

// TestListSnapshots_NewestFirstAndSkipsTheOriginalsManifest pins the two
// things a listing has to get right: the order a user reads a backup list
// in, and not mistaking the originals store's own manifest for a snapshot.
func TestListSnapshots_NewestFirstAndSkipsTheOriginalsManifest(t *testing.T) {
	svc, game, dataDir := newSnapshotFixture(t)

	for _, name := range []string{"first", "second", "third"} {
		_, err := svc.CreateSnapshot(context.Background(), game, "default", name)
		require.NoError(t, err)
	}
	// The store's manifest sits in the same directory.
	assert.FileExists(t, filepath.Join(dataDir, "snapshots", "g1", "originals.json"))

	listing, err := svc.ListSnapshots(context.Background(), "g1")
	require.NoError(t, err)
	assert.Equal(t, "g1", listing.GameID)
	require.Len(t, listing.Snapshots, 3, "originals.json is the store's manifest, not a snapshot")
	assert.Empty(t, listing.Warnings)

	for i := 1; i < len(listing.Snapshots); i++ {
		assert.False(t, listing.Snapshots[i-1].CreatedAt.Before(listing.Snapshots[i].CreatedAt),
			"snapshots list newest first")
	}
	for _, row := range listing.Snapshots {
		assert.Equal(t, 1, row.Mods)
		assert.Equal(t, 1, row.Originals)
		assert.Positive(t, row.SizeBytes)
	}
}

// TestListSnapshots_AnUnreadableSnapshotIsAWarningNotAFailure: the other
// snapshots are still restorable, and the broken one is what the user
// needs telling about.
func TestListSnapshots_AnUnreadableSnapshotIsAWarningNotAFailure(t *testing.T) {
	svc, game, dataDir := newSnapshotFixture(t)
	_, err := svc.CreateSnapshot(context.Background(), game, "default", "good")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "snapshots", "g1", "broken.json"), []byte("{not json"), 0600))

	listing, err := svc.ListSnapshots(context.Background(), "g1")
	require.NoError(t, err)
	require.Len(t, listing.Snapshots, 1)
	assert.Equal(t, "good", listing.Snapshots[0].Name)
	require.Len(t, listing.Warnings, 1)
	assert.Contains(t, listing.Warnings[0], "broken.json")
}

func TestListSnapshots_AGameWithNoSnapshotsIsNotAnError(t *testing.T) {
	svc, _ := newOriginalsService(t)
	listing, err := svc.ListSnapshots(context.Background(), "never-touched")
	require.NoError(t, err)
	assert.Equal(t, "never-touched", listing.GameID)
	assert.Empty(t, listing.Snapshots)
}

// TestDeleteSnapshot_LeavesTheOriginalsAlone pins the rule that makes
// deleting safe: the stored originals are the ONLY copy of files lmm
// replaced, shared by every snapshot of the game. "I no longer need this
// arrangement recorded" must never mean "throw away the stock content".
func TestDeleteSnapshot_LeavesTheOriginalsAlone(t *testing.T) {
	svc, game, dataDir := newSnapshotFixture(t)
	_, err := svc.CreateSnapshot(context.Background(), game, "default", "doomed")
	require.NoError(t, err)

	result, err := svc.DeleteSnapshot(context.Background(), "g1", "doomed")
	require.NoError(t, err)
	assert.Equal(t, "doomed", result.Name)
	assert.True(t, result.Deleted)
	assert.NoFileExists(t, filepath.Join(dataDir, "snapshots", "g1", "doomed.json"))

	assert.Equal(t, "as shipped",
		readStoredOriginal(t, dataDir, "g1", "mod_path", "Data/shipped.esp"))
	assert.NotEmpty(t, readOriginalsManifest(t, dataDir, "g1"))
}

func TestDeleteSnapshot_UnknownNameIsTyped(t *testing.T) {
	svc, _, _ := newSnapshotFixture(t)
	_, err := svc.DeleteSnapshot(context.Background(), "g1", "never-existed")
	require.ErrorIs(t, err, core.ErrSnapshotNotFound)
}

func TestLoadSnapshot_UnknownNameIsTyped(t *testing.T) {
	svc, _, _ := newSnapshotFixture(t)
	_, err := svc.LoadSnapshot(context.Background(), "g1", "never-existed")
	require.ErrorIs(t, err, core.ErrSnapshotNotFound)
}

// TestCreateSnapshot_CapturesTheProfileOverrideOriginalToo pins that the
// second capture trigger reaches the document as well - a restore has to
// put back an overridden game INI, not just a replaced mod file.
func TestCreateSnapshot_CapturesTheProfileOverrideOriginalToo(t *testing.T) {
	svc, game, _ := newSnapshotFixture(t)

	shipped := filepath.Join(game.InstallPath, "game.ini")
	require.NoError(t, os.WriteFile(shipped, []byte("shipped ini"), 0644))

	pm := svc.NewProfileManager()
	profile, err := pm.Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"game.ini": []byte("overridden ini")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	_, err = svc.CreateSnapshot(context.Background(), game, "default", "with-override")
	require.NoError(t, err)

	doc, err := svc.LoadSnapshot(context.Background(), "g1", "with-override")
	require.NoError(t, err)
	require.Len(t, doc.Originals, 2)
	roots := map[core.OriginalRoot]string{}
	for _, o := range doc.Originals {
		roots[o.Root] = o.RelativePath
	}
	assert.Equal(t, "Data/shipped.esp", roots[core.OriginalRootModPath])
	assert.Equal(t, "game.ini", roots[core.OriginalRootInstallPath])
}
