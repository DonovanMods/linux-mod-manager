package db_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #629: a deployed path under the game's install_path (domain.InstallPathKey)
// is a row of its own, distinct from the mod_path row of the same relative
// path, and every reader hands it back in the form it was written.
func TestDeployedFiles_InstallPathRowsAreTheirOwnFiles(t *testing.T) {
	d, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := t.Context()

	root := domain.InstallPathKey("d3d11.dll")
	require.NoError(t, d.SaveDeployedFile(ctx, "g", "default", "d3d11.dll", "src", "data-mod"))
	require.NoError(t, d.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "default", RelativePath: root, SourceID: "src", ModID: "enb",
		ModPath: "/games/g/Data", Fingerprint: &db.FileFingerprint{Checksum: "c", Size: 1},
	}))

	owner, err := d.GetFileOwner(ctx, "g", "default", root)
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, "enb", owner.ModID)
	owner, err = d.GetFileOwner(ctx, "g", "default", "d3d11.dll")
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, "data-mod", owner.ModID)

	owned, err := d.AnyProfileOwnsFile(ctx, "g", domain.InstallPathKey("other.dll"))
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
	assert.Equal(t, []string{root}, paths)

	listed, err := d.ListDeployedFiles(ctx, "g", "default")
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.Equal(t, "d3d11.dll", listed[0].RelativePath, "mod_path rows list first")
	assert.Equal(t, root, listed[1].RelativePath)

	records, err := d.DeployedPathRecords(ctx, "g")
	require.NoError(t, err)
	assert.Len(t, records[root], 1)
	assert.Len(t, records["d3d11.dll"], 1)

	conflicts, err := d.CheckFileConflicts(ctx, "g", "default", []string{root})
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, root, conflicts[0].RelativePath)
	assert.Equal(t, "enb", conflicts[0].ModID)
	conflicts, err = d.CheckFileConflicts(ctx, "g", "default", []string{"d3d11.dll", root})
	require.NoError(t, err)
	assert.Len(t, conflicts, 2)

	forMod, err := d.DeployedFileRecordsForMod(ctx, "g", "default", "src", "enb")
	require.NoError(t, err)
	require.Len(t, forMod, 1)
	assert.Equal(t, root, forMod[0].RelativePath)

	// keepPaths names a root file by its key.
	require.NoError(t, d.SaveDeployedFile(ctx, "g", "default", domain.InstallPathKey("enbseries/a.fx"), "src", "enb"))
	require.NoError(t, d.DeleteDeployedFilesExcept(ctx, "g", "default", "src", "enb", nil, []string{root}))
	paths, err = d.GetDeployedFilesForMod(ctx, "g", "default", "src", "enb")
	require.NoError(t, err)
	assert.Equal(t, []string{root}, paths)

	require.NoError(t, d.DeleteDeployedFile(ctx, "g", "default", root))
	owner, err = d.GetFileOwner(ctx, "g", "default", "d3d11.dll")
	require.NoError(t, err)
	require.NotNil(t, owner, "deleting the root row leaves the mod_path one")
	owner, err = d.GetFileOwner(ctx, "g", "default", root)
	require.NoError(t, err)
	assert.Nil(t, owner)
}
