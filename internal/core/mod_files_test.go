package core_test

// Tests for Service.ModFiles (v2 Phase 3 Task 10, #303): the read-only
// query behind `lmm mod files`, replacing cmd/lmm's own direct
// GetGameCache/HasRetainedCompileSource call. Reuses newFlowsTestService/
// seedInstalledMod (flows_test.go) and installSeededMod (flows_test.go /
// uninstall_plan_test.go family), already in this core_test package.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_ModFiles_NotInstalled_ReturnsModNotFound(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	report, err := svc.ModFiles(context.Background(), game, "default", "src", "missing")
	require.Error(t, err)
	assert.Equal(t, "mod not found: missing", err.Error())
	assert.Nil(t, report)
}

// TestService_ModFiles_ReturnsTrackedFilesWithSizeAndDeployed guards the
// new per-file data ModFileEntry adds beyond the plain path list
// doModFiles historically printed: each tracked, still-deployed file
// reports its real on-disk size.
func TestService_ModFiles_ReturnsTrackedFilesWithSizeAndDeployed(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("hello")})
	installSeededMod(t, svc, game, "a")

	report, err := svc.ModFiles(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, "Mod A", report.Mod.Name)
	require.Len(t, report.Files, 1)
	assert.Equal(t, "a.esp", report.Files[0].Path)
	assert.True(t, report.Files[0].Deployed)
	assert.EqualValues(t, len("hello"), report.Files[0].Size)
	assert.False(t, report.MergedPakOnly)
}

// TestService_ModFiles_TrackedButRemovedFromDisk_ReportsNotDeployed guards
// Deployed's meaning: a tracked path whose file was removed out from under
// lmm reports Deployed false and Size 0, rather than erroring.
func TestService_ModFiles_TrackedButRemovedFromDisk_ReportsNotDeployed(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("hello")})
	installSeededMod(t, svc, game, "a")

	require.NoError(t, os.Remove(filepath.Join(game.ModPath, "a.esp")))

	report, err := svc.ModFiles(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	require.Len(t, report.Files, 1)
	assert.False(t, report.Files[0].Deployed)
	assert.EqualValues(t, 0, report.Files[0].Size)
}

// TestService_ModFiles_NoFilesTracked_NonCompile_ReportsPlainEmpty guards
// the ordinary "zero tracked files" case: MergedPakOnly stays false outside
// DeployCompile.
func TestService_ModFiles_NoFilesTracked_NonCompile_ReportsPlainEmpty(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	seedInstalledMod(t, svc, game, "src", "a", "1.0", true, nil)

	report, err := svc.ModFiles(context.Background(), game, "default", "src", "a")
	require.NoError(t, err)
	assert.Empty(t, report.Files)
	assert.False(t, report.MergedPakOnly)
}

// TestService_ModFiles_DeployCompile_RetainedSource_ReportsMergedPakOnly
// guards the #197 postsmoke case: a validated+retained compile-mode mod
// with zero files of its own reports MergedPakOnly instead of the plain
// "no files tracked" shape.
func TestService_ModFiles_DeployCompile_RetainedSource_ReportsMergedPakOnly(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink, DeployMode: domain.DeployCompile}

	const modID, version, fileID = "bear-mount", "1.0", "exmodz-file"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", modID, version, cache.RetainedSourceName(fileID), []byte("bear-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))

	report, err := svc.ModFiles(context.Background(), game, "default", "src", modID)
	require.NoError(t, err)
	assert.Empty(t, report.Files)
	assert.True(t, report.MergedPakOnly)
}
