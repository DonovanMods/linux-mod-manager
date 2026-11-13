// The #629 review's repro: a whole-mod game-root route of a mod with a
// Data/ folder deploys that folder's files into the mod_path, from the
// cache member that holds them.
package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wholeRootAdapter struct{ adapter.Generic }

func (wholeRootAdapter) ID() string { return "whole-root" }
func (wholeRootAdapter) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	out := make([]adapter.DeployRoot, len(files))
	for i := range out {
		out[i] = adapter.RootInstallPath
	}
	return out
}
func (wholeRootAdapter) RootForModType(*domain.Game, string) (adapter.DeployRoot, bool) {
	return adapter.RootModPath, false
}

func TestDeployRoot_WholeModRouteWithDataFolder(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			svc, _ := newOriginalsService(t)
			svc.RegisterAdapter(wholeRootAdapter{})
			install := t.TempDir()
			game := &domain.Game{ID: "g1", Name: "Game", InstallPath: install, ModPath: filepath.Join(install, "Data"),
				LinkMethod: method, LinkMethodExplicit: true, Adapter: "whole-root"}
			require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
			require.NoError(t, svc.SaveGame(context.Background(), game))
			seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, map[string][]byte{
				"enbseries.ini":   []byte("ini"),
				"Data/plugin.esp": []byte("esp from Data/"),
			})
			seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
			res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			t.Logf("deploy err: %v res=%+v", err, res)
			_ = filepath.Walk(install, func(p string, info os.FileInfo, _ error) error {
				l, _ := os.Readlink(p)
				t.Logf("  %s %v -> %s", p, info.Mode(), l)
				return nil
			})
			files, ferr := svc.ModFiles(context.Background(), game, "default", "src", "enb")
			t.Logf("modfiles %+v %v", files, ferr)
			got, rerr := os.ReadFile(filepath.Join(game.ModPath, "plugin.esp"))
			t.Logf("Data/plugin.esp read: %q err=%v", got, rerr)
			assert.Equal(t, "esp from Data/", string(got))
		})
	}
}
