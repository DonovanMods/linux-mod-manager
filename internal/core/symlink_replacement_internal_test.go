package core

import (
	"io/fs"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
)

func TestSymlinkReplacementState_PreservesMixedAndUnknownOwnership(t *testing.T) {
	symlink, copyMethod := domain.LinkSymlink, domain.LinkCopy

	assert.NotNil(t, symlinkReplacementState([]db.DeployedFileState{
		{Profile: "a", LinkMethod: &symlink},
		{Profile: "b", LinkMethod: &symlink},
	}, 0), "every profile agrees that a non-link replaced lmm's link")

	assert.Nil(t, symlinkReplacementState([]db.DeployedFileState{
		{Profile: "a", LinkMethod: &symlink},
		{Profile: "b", LinkMethod: &copyMethod},
	}, 0), "another profile's copy may be the regular file on disk")

	assert.Nil(t, symlinkReplacementState([]db.DeployedFileState{
		{Profile: "a", LinkMethod: &symlink},
		{Profile: "residual"},
	}, 0), "a residual row with no installed method keeps provenance unknown")

	assert.Nil(t, symlinkReplacementState([]db.DeployedFileState{
		{Profile: "a", LinkMethod: &symlink},
	}, fs.ModeSymlink), "a live link is not the replacement state")
}
