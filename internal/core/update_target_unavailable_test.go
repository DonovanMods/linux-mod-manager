package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// TestApplyUpdate_AdvertisedFileGone_RefusesInsteadOfInstallingASameLabelFile
// is #505 end to end: the update names the Forge build by id, the source no
// longer lists that id, and the only 1.9.0 file left is the Fabric build.
// ApplyUpdate used to install the Fabric file by its label; it now fails with
// a typed error naming the mod and the file the user can pick instead, and
// leaves the installed mod exactly as it was.
func TestApplyUpdate_AdvertisedFileGone_RefusesInsteadOfInstallingASameLabelFile(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Big Pack", "1.0.0", []string{"forge-100"},
		map[string][]byte{"bigpack-forge-1.0.0.jar": []byte("old")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "fabric-190", Name: "bigpack-fabric-1.9.0", FileName: "bigpack-fabric-1.9.0.jar", Version: "1.9.0", IsPrimary: true, Category: "release"},
			{ID: "forge-100", Name: "bigpack-forge-1.0.0", FileName: "bigpack-forge-1.0.0.jar", Version: "1.0.0", Category: "release"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Big Pack", Version: "1.9.0", GameID: "g1"})
	mock.AddDownload("fabric-190", []byte("fabric"))

	upd := domain.Update{InstalledMod: *old, NewVersion: "1.9.0", FileIDReplacements: map[string]string{"forge-100": "forge-190"}}
	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default", upd)
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)

	var target *core.UpdateTargetUnavailableError
	require.True(t, errors.As(err, &target), "want *core.UpdateTargetUnavailableError, got %T: %v", err, err)
	assert.Equal(t, "src", target.SourceID)
	assert.Equal(t, "mod1", target.ModID)
	assert.Equal(t, "Big Pack", target.ModName)
	assert.Equal(t, "default", target.Profile)
	assert.Equal(t, "1.9.0", target.TargetVersion)
	assert.Equal(t, []string{"forge-190"}, target.MissingFileIDs)
	assert.True(t, target.Advertised)
	assert.Equal(t, []core.UpdateTargetCandidate{{ID: "fabric-190", Name: "bigpack-fabric-1.9.0", Version: "1.9.0", Category: "release"}}, target.Candidates)

	updated, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", updated.Version, "the installed mod is untouched")
	assert.Equal(t, []string{"forge-100"}, updated.FileIDs)
	_, statErr := os.Stat(filepath.Join(gameDir, "bigpack-fabric-1.9.0.jar"))
	assert.True(t, os.IsNotExist(statErr), "the Fabric file must not be deployed")
}
