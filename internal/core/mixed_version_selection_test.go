package core_test

// #647: an install selection whose files belong to more than one version of
// the mod is refused. The row records one version while the cache tree would
// hold files of both, so update and rollback would reason about a version
// the tree does not match. A file with no version (a source that reports
// none) is compatible with every version and never makes a selection mixed.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateInstallFileSelection_MixedVersions is the rule itself.
func TestValidateInstallFileSelection_MixedVersions(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1"}

	main1 := domain.DownloadableFile{ID: "m1", FileName: "main-1.0.zip", Version: "1.0"}
	opt1 := domain.DownloadableFile{ID: "o1", FileName: "patch-1.0.zip", Version: "1.0"}
	main2 := domain.DownloadableFile{ID: "m2", FileName: "main-2.0.zip", Version: "2.0"}
	main3 := domain.DownloadableFile{ID: "m3", Name: "Main 3", Version: "3.0"}
	bare := domain.DownloadableFile{ID: "b", FileName: "extras.zip"}

	ok := []struct {
		name  string
		files []domain.DownloadableFile
	}{
		{"one version, several files", []domain.DownloadableFile{main1, opt1}},
		{"a file with no version joins any version", []domain.DownloadableFile{main2, bare}},
		{"no versions at all", []domain.DownloadableFile{bare, {ID: "b2", FileName: "more.zip"}}},
		{"a single file", []domain.DownloadableFile{main1}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, svc.ValidateInstallFileSelection(game, tc.files))
		})
	}

	t.Run("two versions refused, naming each version's files", func(t *testing.T) {
		err := svc.ValidateInstallFileSelection(game, []domain.DownloadableFile{main1, main2, opt1, bare})
		var mixed *core.MixedVersionSelectionError
		require.True(t, errors.As(err, &mixed), "want *core.MixedVersionSelectionError, got %v", err)
		assert.Equal(t, "these files belong to versions 1.0 (main-1.0.zip, patch-1.0.zip) and 2.0 (main-2.0.zip); pick one version", err.Error())
		assert.Equal(t, []core.MixedVersion{
			{Version: "1.0", Files: []core.MixedVersionFile{{ID: "m1", FileName: "main-1.0.zip"}, {ID: "o1", FileName: "patch-1.0.zip"}}},
			{Version: "2.0", Files: []core.MixedVersionFile{{ID: "m2", FileName: "main-2.0.zip"}}},
		}, mixed.Versions, "a file with no version belongs to no version, so it is not named")
	})

	t.Run("three versions", func(t *testing.T) {
		err := svc.ValidateInstallFileSelection(game, []domain.DownloadableFile{main3, main1, main2})
		require.Error(t, err)
		assert.Equal(t, "these files belong to versions 3.0 (Main 3), 1.0 (main-1.0.zip) and 2.0 (main-2.0.zip); pick one version", err.Error())
	})
}

// TestApplyInstall_StrictPath_TargetFileIDs_MixedVersions_Refused pins the
// --file shape: TargetFileIDs naming files of 2.0 and 1.0 are refused before
// any download, and nothing is recorded.
func TestApplyInstall_StrictPath_TargetFileIDs_MixedVersions_Refused(t *testing.T) {
	svc, game, mock := setupInterplayService(t, false)
	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Empty(t, plan.Dependencies, "must take the STRICT path")

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{TargetFileIDs: []string{"root-main-2", "root-opt-1"}}, nil)
	var mixed *core.MixedVersionSelectionError
	require.True(t, errors.As(err, &mixed), "want *core.MixedVersionSelectionError, got %v", err)
	assert.Equal(t, "these files belong to versions 2.0 (root-2.0.zip) and 1.0 (root-patch-1.0.zip); pick one version", err.Error())
	assertNothingInstalled(t, svc, game, mock)
}

// TestApplyInstall_StrictPath_CallerSuppliedMixedVersionFiles_Refused is
// ApplyInstall's own re-check: a caller that writes a mixed selection into
// plan.Files, with no pins at all, is refused too - Apply never trusts the
// selection it is handed.
func TestApplyInstall_StrictPath_CallerSuppliedMixedVersionFiles_Refused(t *testing.T) {
	svc, game, mock := setupInterplayService(t, false)
	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)

	plan.Files = interplayRootFiles()
	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	var mixed *core.MixedVersionSelectionError
	require.True(t, errors.As(err, &mixed), "want *core.MixedVersionSelectionError, got %v", err)
	assertNothingInstalled(t, svc, game, mock)
}

// TestApplyInstall_BatchPath_TargetFileIDs_MixedVersions_Refused is the
// dependencies-present path: the primary's pins are refused up front, so no
// dependency installs either.
func TestApplyInstall_BatchPath_TargetFileIDs_MixedVersions_Refused(t *testing.T) {
	svc, game, mock := setupInterplayService(t, true)
	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Dependencies, "must take the BATCH path")

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{TargetFileIDs: []string{"root-opt-1", "root-main-2"}}, nil)
	var mixed *core.MixedVersionSelectionError
	require.True(t, errors.As(err, &mixed), "want *core.MixedVersionSelectionError, got %v", err)
	assertNothingInstalled(t, svc, game, mock)
	_, err = svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "no dependency installs either")
}

// TestApplyInstall_UnversionedFileJoinsAVersion installs a versioned file
// with one whose source reports no version: compatible, so it installs, and
// the row records the versioned file's version.
func TestApplyInstall_UnversionedFileJoinsAVersion(t *testing.T) {
	svc, game, mock := setupInterplayService(t, false)
	mock.files["root"] = append(interplayRootFiles(), domain.DownloadableFile{ID: "root-extra", Name: "Extras", FileName: "extras.zip", Category: "OPTIONAL"})
	stageInterplayDownload(t, mock, "root-extra", "root-extra.esp")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{TargetFileIDs: []string{"root-main-2", "root-extra"}}, nil)
	require.NoError(t, err)

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"root-main-2", "root-extra"}, got.FileIDs)
	assert.Equal(t, "2.0", got.Version)
}

// assertNothingInstalled is a refused install's end state: no download, no
// deployed file, no row.
func assertNothingInstalled(t *testing.T, svc *core.Service, game *domain.Game, mock *perModMultiFileSource) {
	t.Helper()
	assert.Equal(t, 0, mock.DownloadCount(), "a refused selection downloads nothing")
	entries, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be deployed")
	_, err = svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "no row may be written")
}
