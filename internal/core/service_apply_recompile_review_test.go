package core_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestApplyRecompile_RetainedSourceStatError_SurfacesActionably pins the
// #196 review finding: os.Stat's error on the retained source path was
// treated as "missing" unconditionally, masking a genuine permission/I/O
// problem behind misleading "retained source is missing" / redownload-
// fallback text. A non-ENOENT stat error must surface as its own
// actionable failure instead.
//
// A real unreadable-parent fixture would ALSO break ApplyRecompile's own
// prepareStaging seed step (which reads the same version directory before
// this check ever runs), so this exercises the extracted pure classifier
// directly - core.ClassifyRetainedSourceStatError - rather than trying to
// force a filesystem-level permission error through the full call.
func TestClassifyRetainedSourceStatError(t *testing.T) {
	t.Run("nil error: present", func(t *testing.T) {
		missing, err := core.ClassifyRetainedSourceStatError(nil)
		require.False(t, missing)
		require.NoError(t, err)
	})

	t.Run("not-exist: missing, no error", func(t *testing.T) {
		notExist := &os.PathError{Op: "stat", Path: "/x", Err: os.ErrNotExist}
		missing, err := core.ClassifyRetainedSourceStatError(notExist)
		require.True(t, missing)
		require.NoError(t, err)
	})

	t.Run("permission denied: not missing, actionable error", func(t *testing.T) {
		permErr := &os.PathError{Op: "stat", Path: "/x", Err: os.ErrPermission}
		missing, err := core.ClassifyRetainedSourceStatError(permErr)
		require.False(t, missing, "a permission error must never be folded into 'missing'")
		require.Error(t, err)
		require.ErrorIs(t, err, permErr)
	})
}

// TestApplyRecompile_RetainedSourceStatError_Integration proves the
// classifier is actually wired into ApplyRecompile: a stat error that is
// NOT "not exist" must abort with an actionable error, WITHOUT ever
// treating the entry as eligible for the local-mod-fails-loud or
// redownload-fallback text (which would misrepresent a permission/I/O
// problem as "the file is gone").
//
// Simulated here by replacing the retained source with a directory (a
// real, portable way to make a SECOND os.Stat-adjacent operation fail
// without touching filesystem permissions): os.Stat itself still succeeds
// on a directory, so this proves the narrower regression - that a
// genuinely present (if wrong-shaped) entry is never silently redownloaded
// - while the classifier unit tests above cover the permission-error path
// directly.
func TestApplyRecompile_RetainedSourceIsDirectory_DoesNotSilentlyRedownload(t *testing.T) {
	fx := seedCompiledInstalledMod(t, domain.LinkCopy, "fake-compiler", "0000000000000000000000000000000000dead")

	gameCache := fx.svc.GetGameCache(fx.game)
	retainedPath := gameCache.GetFilePath(fx.game.ID, "fake-compiler", "bear-mount", "3.3", cache.RetainedSourceName("exmodz-file-id"))
	require.NoError(t, os.Remove(retainedPath))
	require.NoError(t, os.Mkdir(retainedPath, 0o755))

	compiler := &redownloadCompilerSource{
		fakeCompilerSource: &fakeCompilerSource{},
		downloadBody:       "should-never-be-fetched",
		files:              []domain.DownloadableFile{{ID: "exmodz-file-id", FileName: "Bear_Mount.exmodz"}},
	}
	compiler.start(t)
	fx.svc.RegisterSource(compiler)

	mod, err := fx.svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	_, err = fx.svc.ApplyRecompile(context.Background(), fx.game, "default", *mod, nil)
	require.Error(t, err, "a present-but-unusable retained source must fail loud, not silently redownload or succeed")
}

// TestApplyRecompile_RedownloadedFileName_SanitizedAgainstTraversal pins
// the #196 review finding: a source-controlled DownloadableFile.FileName
// joined verbatim into the staging path can traverse outside the intended
// staging directory (e.g. "../evil.exmodz"). The fix must sanitize via
// filepath.Base before joining, mirroring the existing convention used
// elsewhere in this package for exactly this concern (importer.go's
// filepath.Base(archivePath), service.go's filepath.Base(localPath)
// fallback).
func TestApplyRecompile_RedownloadedFileName_SanitizedAgainstTraversal(t *testing.T) {
	dataDir := t.TempDir()
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	const modID, version, fileID = "bear-mount", "3.3", "exmodz-file-id"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, "Bear_Mount_P.pak", []byte("stale-compiled-bytes")))
	versionDir := gameCache.ModPath(game.ID, "fake-compiler", modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{"Bear_Mount_P.pak"}))
	require.NoError(t, cache.MarkBaseIndexHash(versionDir, fileID, "0000000000000000000000000000000000dead"))
	// Deliberately NO retained source stored - forces the redownload path,
	// which is where the vulnerable filepath.Join(tempDir, match.FileName)
	// lives.

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   domain.LinkCopy,
		FileIDs:      []string{fileID},
	}
	require.NoError(t, svc.SaveInstalledMod(im))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &im.Mod, "default"))
	pm := svc.NewProfileManager()
	_, cerr := pm.Create(game.ID, "default")
	require.NoError(t, cerr)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	compiler := &redownloadCompilerSource{
		fakeCompilerSource: &fakeCompilerSource{},
		downloadBody:       "redownloaded-exmodz-bytes",
		files:              []domain.DownloadableFile{{ID: fileID, FileName: "../evil-traversal.exmodz"}},
	}
	compiler.start(t)
	svc.RegisterSource(compiler)

	mod, err := svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	_, err = svc.ApplyRecompile(context.Background(), game, "default", *mod, nil)
	require.NoError(t, err)

	// newStagingDir("lmm-recompile-*") creates its scratch dir directly
	// under dataDir/downloads (Service.stagingRoot) - an UNSANITIZED
	// filepath.Join(tempDir, "../evil-traversal.exmodz") climbs exactly one
	// level out of that scratch dir, landing at dataDir/downloads/
	// evil-traversal.exmodz. That parent is never removed (only tempDir
	// itself is), so an escaped write would persist right here.
	escapedPath := filepath.Join(dataDir, "downloads", "evil-traversal.exmodz")
	_, statErr := os.Stat(escapedPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), fmt.Sprintf("a traversal filename must never write outside the staging tempDir (found %s)", escapedPath))
}
