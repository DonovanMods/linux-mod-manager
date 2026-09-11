package core_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanImportArchive_BepInEx_MixedRootStillRequiresTheLoader pins the
// #424 re-review's finding A: a root that holds BepInEx/ beside a sibling
// is unrecognised as a LAYOUT (it deploys verbatim, with a warning, on a
// game that has the loader) but it still unmistakably NEEDS the loader, so
// on a game with no declaration and no BepInEx on disk it is refused with
// #359's setup steps - exactly as the pure BepInEx/ archive is.
func TestPlanImportArchive_BepInEx_MixedRootStillRequiresTheLoader(t *testing.T) {
	svc, game := newBepInExGameRootService(t)

	archivePath := filepath.Join(t.TempDir(), "Mixed-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/patchers/Pre.dll": "assembly",
		"Jotunn/Jotunn.dll":        "assembly",
	})

	_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.Error(t, err, "a mixed BepInEx root on an undeclared game must not deploy silently")

	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)
	assert.Equal(t, "bepinex", loaderErr.Kind)
	assert.Equal(t, game.ID, loaderErr.GameID)
	require.NotEmpty(t, loaderErr.Setup)
}
