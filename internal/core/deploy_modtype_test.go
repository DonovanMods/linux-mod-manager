package core_test

// #607: a mod's STATED type (ModReference.ModType, copied onto its
// installed row) routes the whole mod through the game's adapter
// (adapter.RootForModType), so a collection's "dinput" mod - a script
// extender - deploys under the install_path, files and folders alike, and
// a type the adapter does not know refuses the deploy rather than putting
// the files where the game never reads them.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBethesdaGame is a Skyrim-shaped game (mod_path = install_path/Data)
// under the bethesda adapter.
func newBethesdaGame(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	svc.RegisterAdapter(bethesda.New())
	install := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: install, ModPath: filepath.Join(install, "Data"),
		LinkMethod: domain.LinkSymlink, Adapter: "bethesda",
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return game
}

// typedMod seeds an installed mod whose row states modType.
func typedMod(t *testing.T, svc *core.Service, game *domain.Game, modType string) {
	t.Helper()
	seedNamedInstalledMod(t, svc, game, "src", "ext", "Extender", "1.0", true, map[string][]byte{
		"extender_readme.txt":       []byte("readme"),
		"Data/Scripts/extender.pex": []byte("script"),
	})
	row, err := svc.GetInstalledMod(context.Background(), "src", "ext", game.ID, "default")
	require.NoError(t, err)
	row.ModType = modType
	require.NoError(t, svc.SaveInstalledMod(context.Background(), row))
	seedProfileWithMod(t, svc, game.ID, "default", "src", "ext", "1.0")
}

func TestDeployModType_StatedTypeRoutesTheWholeMod(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newBethesdaGame(t, svc)
	typedMod(t, svc, game, "dinput")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "readme", readFileString(t, filepath.Join(game.InstallPath, "extender_readme.txt")),
		"a dinput mod's top-level file is the game root's, whatever its name")
	assert.Equal(t, "script", readFileString(t, filepath.Join(game.ModPath, "Scripts", "extender.pex")),
		"and its Data/ folder is the mod_path")
	assertAbsent(t, filepath.Join(game.ModPath, "extender_readme.txt"), "nothing of it lands loose under Data")

	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "ext", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(game.InstallPath, "extender_readme.txt"), "uninstall finds it at the root")
	assertAbsent(t, filepath.Join(game.ModPath, "Scripts", "extender.pex"), "and under Data")
}

func TestDeployModType_UntypedModRoutesByFile(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newBethesdaGame(t, svc)
	typedMod(t, svc, game, "")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "readme", readFileString(t, filepath.Join(game.ModPath, "extender_readme.txt")),
		"without a stated type, the adapter's per-file rules apply, as before")
}

func TestDeployModType_UnknownTypeRefusesTheDeploy(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newBethesdaGame(t, svc)
	typedMod(t, svc, game, "witcher3menumodroot")

	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, res.Skipped, 1, "an unknown stated type is refused, never deployed under Data")
	assert.Contains(t, res.Skipped[0].Reason, "witcher3menumodroot")
	assert.Zero(t, res.Deployed)
	assertAbsent(t, filepath.Join(game.ModPath, "extender_readme.txt"), "nothing was deployed")
	assertAbsent(t, filepath.Join(game.InstallPath, "extender_readme.txt"), "nothing was deployed")
}
