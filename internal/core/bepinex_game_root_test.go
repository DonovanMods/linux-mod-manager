package core_test

// "Deploys into its game root" is a fact about DIRECTORIES, not about how
// games.yaml spells them (#413 final review F2). Steam's ~/.steam/steam is
// a symlink to ~/.local/share/Steam on most Linux installs, so the same
// install is routinely written two ways - and a lexical comparison made a
// game-root BepInEx game "off root": the derivation fell back to
// generic-files, a loose plugin landed in the game directory where BepInEx
// never loads it, and an explicit `adapter: bepinex` was refused for a
// layout that would have been right.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdapterName_TheGameRootIsTheSameDirectoryHoweverItIsSpelled writes
// each spelling into games.yaml, so `~`, a relative mod_path and `..` reach
// the derivation exactly as a user's file delivers them.
func TestAdapterName_TheGameRootIsTheSameDirectoryHoweverItIsSpelled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	real := filepath.Join(home, "games", "real")
	other := filepath.Join(home, "games", "other")
	bepinexInstall(t, real, "", domain.LoaderBootstrapNative, time.Time{})
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.Symlink(real, filepath.Join(home, "link")))
	require.NoError(t, os.Symlink(other, filepath.Join(home, "elsewhere")))

	cases := []struct {
		name, install, mod, want string
	}{
		{"a symlinked install path", "~/link", real, "bepinex"},
		{"a symlinked mod path", real, "~/link", "bepinex"},
		{"a symlink on each side", filepath.Join(home, "link"), "~/link/", "bepinex"},
		{"a trailing slash", real + "/", real, "bepinex"},
		{"~ on both sides", "~/games/real", "~/games/real", "bepinex"},
		{"~ with the absolute path", "~/games/real", real + "/", "bepinex"},
		{"a relative mod path", real, ".", "bepinex"},
		{"a relative mod path with ..", real, "BepInEx/..", "bepinex"},
		{".. in the install path", filepath.Join(home, "games", "other") + "/../real", real, "bepinex"},
		{"a mod path inside the game", real, "BepInEx/plugins", "generic-files"},
		{"a symlink to another directory", real, "~/elsewhere", "generic-files"},
		{"a mod path that does not exist", real, "~/games/missing", "generic-files"},
		{"a dangling symlink", real, "~/dangling", "generic-files"},
	}
	require.NoError(t, os.Symlink(filepath.Join(home, "gone"), filepath.Join(home, "dangling")))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFlowsTestService(t)
			yaml := "games:\n  valheim:\n    name: Valheim\n    install_path: \"" + tc.install + "\"\n    mod_path: \"" + tc.mod + "\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(svc.ConfigDir(), "games.yaml"), []byte(yaml), 0o644))
			reloaded, err := svc.ReloadGames()
			require.NoError(t, err)
			require.True(t, reloaded)
			game, err := svc.GetGame("valheim")
			require.NoError(t, err)

			assert.Equal(t, tc.want, svc.AdapterName(game), "install %q, mod %q", game.InstallPath, game.ModPath)

			explicit := *game
			explicit.Adapter = "bepinex"
			_, err = svc.AdapterFor(&explicit)
			if tc.want == "bepinex" {
				assert.NoError(t, err, "an explicit bepinex adapter is not refused for a layout that is right")
				assert.Empty(t, svc.AdapterConfigWarning(game.ID), "and nothing is flagged")
			} else {
				assert.Error(t, err)
			}
		})
	}
}
