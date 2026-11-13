package db_test

import (
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #629: a file under the game's install_path is a row of its own, distinct
// from the mod_path row of the same relative path, and every reader hands
// it back with its root.
func TestDeployedFiles_InstallPathRowsAreTheirOwnFiles(t *testing.T) {
	d, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := t.Context()

	root := domain.GameRootFile("d3d11.dll")
	data := domain.ModPathFile("d3d11.dll")
	require.NoError(t, d.SaveDeployedFile(ctx, "g", "default", "d3d11.dll", "src", "data-mod"))
	require.NoError(t, d.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "default", Path: root, SourceID: "src", ModID: "enb",
		ModPath: "/games/g/Data", Fingerprint: &db.FileFingerprint{Checksum: "c", Size: 1},
	}))

	owner, err := d.GetFileOwner(ctx, "g", "default", root)
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, "enb", owner.ModID)
	owner, err = d.GetFileOwner(ctx, "g", "default", data)
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, "data-mod", owner.ModID)

	owned, err := d.AnyProfileOwnsFile(ctx, "g", domain.GameRootFile("other.dll"))
	require.NoError(t, err)
	assert.False(t, owned)
	owned, err = d.AnyProfileOwnsFile(ctx, "g", root)
	require.NoError(t, err)
	assert.True(t, owned)

	states, err := d.DeployedFileStates(ctx, "g", root)
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, "enb", states[0].ModID)
	assert.Equal(t, "c", states[0].Fingerprint.Checksum)

	paths, err := d.GetDeployedFilesForMod(ctx, "g", "default", "src", "enb")
	require.NoError(t, err)
	assert.Equal(t, []domain.DeployPath{root}, paths)

	listed, err := d.ListDeployedFiles(ctx, "g", "default")
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.Equal(t, data, listed[0].Path, "mod_path rows list first")
	assert.Equal(t, root, listed[1].Path)

	records, err := d.DeployedPathRecords(ctx, "g")
	require.NoError(t, err)
	assert.Len(t, records[root], 1)
	assert.Len(t, records[data], 1)

	conflicts, err := d.CheckFileConflicts(ctx, "g", "default", []domain.DeployPath{root})
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, root, conflicts[0].Path)
	assert.Equal(t, "enb", conflicts[0].ModID)
	conflicts, err = d.CheckFileConflicts(ctx, "g", "default", []domain.DeployPath{data, root})
	require.NoError(t, err)
	assert.Len(t, conflicts, 2)

	forMod, err := d.DeployedFileRecordsForMod(ctx, "g", "default", "src", "enb")
	require.NoError(t, err)
	require.Len(t, forMod, 1)
	assert.Equal(t, root, forMod[0].Path)

	// keepPaths names a root file by its root and path.
	require.NoError(t, d.RecordDeployedFile(ctx, db.DeployedFileRecord{GameID: "g", Profile: "default", Path: domain.GameRootFile("enbseries/a.fx"), SourceID: "src", ModID: "enb"}))
	require.NoError(t, d.DeleteDeployedFilesExcept(ctx, "g", "default", "src", "enb", nil, []domain.DeployPath{root}))
	paths, err = d.GetDeployedFilesForMod(ctx, "g", "default", "src", "enb")
	require.NoError(t, err)
	assert.Equal(t, []domain.DeployPath{root}, paths)

	require.NoError(t, d.DeleteDeployedFile(ctx, "g", "default", root))
	owner, err = d.GetFileOwner(ctx, "g", "default", data)
	require.NoError(t, err)
	require.NotNil(t, owner, "deleting the root row leaves the mod_path one")
	owner, err = d.GetFileOwner(ctx, "g", "default", root)
	require.NoError(t, err)
	assert.Nil(t, owner)
}

// DeleteDeployedFilesExcept keeps thousands of paths across both roots in
// one statement (#629 review): one NOT IN per root, not one clause per path,
// which SQLite refuses past an expression depth of 1000.
func TestDeleteDeployedFilesExcept_ManyKeptPathsAcrossBothRoots(t *testing.T) {
	d, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := t.Context()

	var keep []domain.DeployPath
	for i := range 6000 {
		p := domain.ModPathFile(fmt.Sprintf("textures/%04d.dds", i))
		if i%2 == 1 {
			p = domain.GameRootFile(fmt.Sprintf("enbseries/%04d.fx", i))
		}
		require.NoError(t, d.RecordDeployedFile(ctx, db.DeployedFileRecord{GameID: "g", Profile: "p", Path: p, SourceID: "s", ModID: "m"}))
		if i < 5000 {
			keep = append(keep, p)
		}
	}
	require.NoError(t, d.DeleteDeployedFilesExcept(ctx, "g", "p", "s", "m", nil, keep))
	left, err := d.GetDeployedFilesForMod(ctx, "g", "p", "s", "m")
	require.NoError(t, err)
	assert.Len(t, left, 5000, "the 5000 kept paths stay, the other 1000 go")
	assert.ElementsMatch(t, keep, left)
}
