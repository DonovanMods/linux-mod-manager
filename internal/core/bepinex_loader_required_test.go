package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanImportArchive_BepInEx_RefusesAGameWithNoLoader is #359's
// precondition at the earliest point the requirement is knowable: the
// archive is unmistakably a BepInEx plugin, the game declares no loader, and
// a plan that said "1 file" would be promising a DLL that will never be
// loaded by anything.
//
// It is a PLAN-time refusal, not a DependencyResolver one: the resolver
// orders mods within a profile, and the loader is not in the profile (spike
// §2). And it is a typed error with Details(), so both frontends render the
// setup instructions rather than each inventing them.
func TestPlanImportArchive_BepInEx_RefusesAGameWithNoLoader(t *testing.T) {
	svc, game := newBepInExGameRootService(t)

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/plugins/SkinwalkerMod.dll": "assembly",
		"manifest.json":                     "{}",
	})

	_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.Error(t, err)

	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)
	assert.Equal(t, "bepinex", loaderErr.Kind)
	assert.Equal(t, game.ID, loaderErr.GameID)
	require.NotEmpty(t, loaderErr.Setup, "the error carries the setup steps both frontends render")
	assert.Contains(t, err.Error(), "BepInEx")
}

// The same archive into a game that DOES declare the loader plans normally -
// the refusal is about the game's configuration, not about the archive.
func TestPlanImportArchive_BepInEx_DeclaredGamePlansNormally(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/plugins/SkinwalkerMod.dll": "assembly",
		"manifest.json":                     "{}",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join("BepInEx", "plugins", "SkinwalkerMod.dll")}, plan.Files)
}

// TestImportArchive_BepInEx_RefusesBeforeDeployingAnything: the ingest makes
// the same refusal, because a caller can reach it without planning first,
// and it must land before the deploy rather than after it.
func TestImportArchive_BepInEx_RefusesBeforeDeployingAnything(t *testing.T) {
	svc, game := newBepInExGameRootService(t)

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/plugins/SkinwalkerMod.dll": "assembly",
		"manifest.json":                     "{}",
	})

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)

	_, statErr := os.Lstat(filepath.Join(game.InstallPath, "BepInEx"))
	assert.True(t, os.IsNotExist(statErr), "nothing is deployed by a refused import")

	mods, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, mods, "and nothing is recorded")
}

// A shape lmm does NOT recognise infers no requirement at all: `plugins/` is
// an ordinary directory name, so an undeclared game importing one is not
// making a BepInEx claim and must not be told to install a loader.
func TestPlanImportArchive_BepInEx_NoRequirementFromAnAmbiguousShape(t *testing.T) {
	svc, game := newBepInExGameRootService(t)

	archivePath := filepath.Join(t.TempDir(), "Patcher-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"patchers/HookGen/HookGenPatcher.dll": "assembly",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join("patchers", "HookGen", "HookGenPatcher.dll")}, plan.Files)
}
