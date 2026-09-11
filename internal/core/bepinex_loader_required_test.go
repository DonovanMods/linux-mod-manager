package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

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

// newBepInExDetectedService is the third fixture in the set, and it is the
// state the original #424 report was made from: a game that does NOT declare
// the loader but HAS BepInEx installed in its directory - a Valheim entry
// added before the catalog declared the loader (#416), on a machine where
// the user installed BepInEx by hand.
//
// The declaration is a statement of intent lmm asks for; an installation is
// a FACT lmm can read. Making the second answer the question too is what
// stops an archive extracting verbatim into a Steam install directory and
// reporting success.
func newBepInExDetectedService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := newBepInExGameRootService(t)
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Time{})
	return svc, game
}

// bepinexUndeclaredNoticeText is the one sentence a detected-but-undeclared
// game gets, named once so every assertion below reads the same string the
// user does.
func bepinexUndeclaredNoticeText(game *domain.Game) string {
	return "BepInEx found in " + game.InstallPath +
		"; declare it with `lmm game edit " + game.ID + " --loader bepinex`"
}

// TestPlanImportArchive_BepInEx_DetectedInstallWidensTheGate is #424's
// second half: BepInEx is in the game directory, so the ambiguous shapes are
// no longer ambiguous - a plugin folder is a plugin folder. The plan
// normalises AND says why, naming the command that makes it permanent.
func TestPlanImportArchive_BepInEx_DetectedInstallWidensTheGate(t *testing.T) {
	svc, game := newBepInExDetectedService(t)

	archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
	createImportTestZip(t, archivePath, jotunnMembers)

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{})
	require.NoError(t, err, "a detected install must not be refused as an undeclared game")
	assert.Equal(t, fromSlashAll(jotunnDeployed), plan.Files)
	require.NotEmpty(t, plan.Warnings)
	assert.Contains(t, plan.Warnings, bepinexUndeclaredNoticeText(game),
		"the gate fired on detection alone, so the user is told exactly how to declare it")
}

// The ingest makes the same call, and nothing reaches the game root.
func TestImportArchive_BepInEx_DetectedInstallWidensTheGate(t *testing.T) {
	svc, game := newBepInExDetectedService(t)

	archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
	createImportTestZip(t, archivePath, jotunnMembers)

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	for _, want := range jotunnDeployed {
		_, statErr := os.Lstat(filepath.Join(game.InstallPath, filepath.FromSlash(want)))
		assert.NoError(t, statErr, "%s must deploy under BepInEx/plugins/", want)
	}
	_, statErr := os.Lstat(filepath.Join(game.InstallPath, "Jotunn"))
	assert.True(t, os.IsNotExist(statErr), "and nothing may land in the game root")
}

// A detected install also answers #359's precondition: the game HAS the
// loader, so an unmistakable BepInEx archive is no longer refused for
// wanting one. The notice is the whole difference from a declared game.
func TestPlanImportArchive_BepInEx_DetectedInstallIsNotRefused(t *testing.T) {
	svc, game := newBepInExDetectedService(t)

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/plugins/SkinwalkerMod.dll": "assembly",
		"manifest.json":                     "{}",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join("BepInEx", "plugins", "SkinwalkerMod.dll")}, plan.Files)
	assert.Contains(t, plan.Warnings, bepinexUndeclaredNoticeText(game))
}

// The other way: a game that DECLARES the loader is told nothing, because
// there is nothing for it to do. A notice on every import would be noise on
// the configuration that is already right.
func TestPlanImportArchive_BepInEx_ADeclaredGameGetsNoNotice(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
	createImportTestZip(t, archivePath, jotunnMembers)

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Empty(t, plan.Warnings)
}

// And the third way, which is the one the widening must not break: a game
// with NO declaration and NO BepInEx on disk keeps the archive's own layout
// and is told nothing about a loader it does not have.
func TestPlanImportArchive_BepInEx_NoDeclarationAndNoInstallStaysUntouched(t *testing.T) {
	svc, game := newBepInExGameRootService(t)

	archivePath := filepath.Join(t.TempDir(), "MyMod-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Mods/MyMod/ModInfo.xml": "<xml/>",
		"Mods/MyMod/MyMod.dll":   "assembly",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, fromSlashAll([]string{"Mods/MyMod/ModInfo.xml", "Mods/MyMod/MyMod.dll"}), plan.Files)
	assert.Empty(t, plan.Warnings, "a game with no BepInEx is not asked to declare one")
}
