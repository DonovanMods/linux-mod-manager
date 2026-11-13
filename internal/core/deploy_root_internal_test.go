package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rootLinker never writes or removes anything outside the install_path
// (#629): not a destination that is not beneath it, and not one reached
// through a folder that is a link to somewhere else.
func TestRootLinker_Containment(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkSymlink, domain.LinkHardlink, domain.LinkCopy} {
		t.Run(method.String(), func(t *testing.T) {
			install, outside := t.TempDir(), t.TempDir()
			src := filepath.Join(t.TempDir(), "d3d11.dll")
			require.NoError(t, os.WriteFile(src, []byte("mod"), 0o644))
			rl := rootLinker{inner: linker.New(method), root: install}

			err := rl.Deploy(src, filepath.Join(outside, "d3d11.dll"))
			require.ErrorIs(t, err, errOutsideInstallPath)

			require.NoError(t, os.Symlink(outside, filepath.Join(install, "enbseries")))
			err = rl.Deploy(src, filepath.Join(install, "enbseries", "a.fx"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "leaves the game's install_path")
			entries, _ := os.ReadDir(outside)
			assert.Empty(t, entries)

			victim := filepath.Join(outside, "a.fx")
			require.NoError(t, os.WriteFile(victim, []byte("not lmm's"), 0o644))
			require.Error(t, rl.Undeploy(filepath.Join(install, "enbseries", "a.fx")), "a removal through the link is refused")
			require.Error(t, rl.removeOwnCopy(filepath.Join(install, "enbseries", "a.fx")))
			assert.FileExists(t, victim)

			// Inside the root, it deploys and removes as the method does.
			dst := filepath.Join(install, "sub", "d3d11.dll")
			require.NoError(t, rl.Deploy(src, dst))
			got, err := os.ReadFile(dst)
			require.NoError(t, err)
			assert.Equal(t, "mod", string(got))
			require.NoError(t, rl.Deploy(src, dst), "a redeploy replaces lmm's own file")
			require.NoError(t, rl.Undeploy(dst))
			_, err = os.Lstat(dst)
			assert.True(t, os.IsNotExist(err))
			require.NoError(t, rl.Undeploy(dst), "an absent file is already undeployed")
			require.NoError(t, rl.Undeploy(filepath.Join(install, "nowhere", "x")), "so is one whose folder is absent")
		})
	}
}

// Under symlink, a regular file at a game-root path is not lmm's link, and
// an Undeploy refuses it as the symlink linker does.
func TestRootLinker_SymlinkUndeployRefusesARegularFile(t *testing.T) {
	install := t.TempDir()
	stock := filepath.Join(install, "d3d11.dll")
	require.NoError(t, os.WriteFile(stock, []byte("stock"), 0o644))
	rl := rootLinker{inner: linker.New(domain.LinkSymlink), root: install}
	require.Error(t, rl.Undeploy(stock))
	assert.FileExists(t, stock)
}

// A game-root route whose path lands inside the mod_path - a whole-mod
// route's Data/ folder - is that mod_path file: one place, one name.
func TestGameRootKey_InsideTheModPathIsTheModPathFile(t *testing.T) {
	install := t.TempDir()
	game := &domain.Game{InstallPath: install, ModPath: filepath.Join(install, "Data")}
	assert.Equal(t, filepath.Join("textures", "a.dds"), gameRootKey(game, filepath.Join("Data", "textures", "a.dds")))
	assert.Equal(t, domain.InstallPathKey("d3d11.dll"), gameRootKey(game, "d3d11.dll"))
}
