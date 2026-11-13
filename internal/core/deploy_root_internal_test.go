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
			assert.Contains(t, err.Error(), "install_path")
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
// route's Data/ folder - is that mod_path file: one place, one name. The
// mod_path's folder is matched case-insensitively: an archive's data/ is the
// game's Data/.
func TestGameRootPath_InsideTheModPathIsTheModPathFile(t *testing.T) {
	install := t.TempDir()
	game := &domain.Game{InstallPath: install, ModPath: filepath.Join(install, "Data")}
	assert.Equal(t, domain.ModPathFile(filepath.Join("textures", "a.dds")), gameRootPath(game, filepath.Join("Data", "textures", "a.dds")))
	assert.Equal(t, domain.ModPathFile("Weather.esp"), gameRootPath(game, filepath.Join("data", "Weather.esp")))
	assert.Equal(t, domain.GameRootFile("d3d11.dll"), gameRootPath(game, "d3d11.dll"))
	assert.Equal(t, domain.GameRootFile("Database.txt"), gameRootPath(game, "Database.txt"), "a name merely starting with Data is not the folder")
	assert.Equal(t, domain.GameRootFile("Data"), gameRootPath(game, "Data"), "the folder itself is not a file under it")
}

// A folder under the game root that is a link - even one that stays inside
// the install_path, into Data/ - is refused: a game-root file is only ever
// written where its record says it is.
func TestRootLinker_RefusesALinkInsideTheRoot(t *testing.T) {
	install := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(install, "Data", "enbseries"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(install, "Data", "enbseries"), filepath.Join(install, "enbseries")))
	src := filepath.Join(t.TempDir(), "a.fx")
	require.NoError(t, os.WriteFile(src, []byte("fx"), 0o644))
	rl := rootLinker{inner: linker.New(domain.LinkSymlink), root: install}
	err := rl.Deploy(src, filepath.Join(install, "enbseries", "a.fx"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a link")
	_, err = os.Lstat(filepath.Join(install, "Data", "enbseries", "a.fx"))
	assert.True(t, os.IsNotExist(err), "nothing lands in Data/ under a root name")
}

// An original under the install_path goes back through the contained walk
// (#629 review): lmm's link at the path is replaced, never written
// through, and a folder on the way that is a link is refused.
func TestRestoreContained(t *testing.T) {
	install, outside, cacheDir := t.TempDir(), t.TempDir(), t.TempDir()
	stored := filepath.Join(t.TempDir(), "stored")
	require.NoError(t, os.WriteFile(stored, []byte("stock"), 0o600))
	cached := filepath.Join(cacheDir, "d3d11.dll")
	require.NoError(t, os.WriteFile(cached, []byte("mod"), 0o644))
	dst := filepath.Join(install, "d3d11.dll")
	require.NoError(t, os.Symlink(cached, dst))

	row := OriginalFile{Root: OriginalRootInstallPath, RelativePath: "d3d11.dll", Mode: 0o755}
	base, ok := baseOf(dst, row.RelativePath)
	require.True(t, ok)
	assert.Equal(t, filepath.Clean(install), base)
	require.NoError(t, restoreContained(row, stored, base, dst))
	info, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	got, _ := os.ReadFile(cached)
	assert.Equal(t, "mod", string(got), "the cache entry is never written through")

	require.NoError(t, os.Symlink(outside, filepath.Join(install, "enbseries")))
	linked := filepath.Join(install, "enbseries", "a.fx")
	row = OriginalFile{Root: OriginalRootInstallPath, RelativePath: "enbseries/a.fx"}
	require.Error(t, restoreContained(row, stored, install, linked))
	entries, _ := os.ReadDir(outside)
	assert.Empty(t, entries)
}

// The two rules restoreContained holds an original to (#629 re-review): a
// profile override's original follows a folder linked inside the
// install_path, as the override write did, and refuses one leaving it; a
// game-root mod file's original refuses both.
func TestRestoreContained_OverrideOriginalsFollowAnInRootLink(t *testing.T) {
	install, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(install, "realcfg"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(install, "realcfg"), filepath.Join(install, "cfg")))
	require.NoError(t, os.Symlink(outside, filepath.Join(install, "away")))
	stored := filepath.Join(t.TempDir(), "stored")
	require.NoError(t, os.WriteFile(stored, []byte("shipped"), 0o600))

	override := OriginalFile{Root: OriginalRootInstallPath, RelativePath: "cfg/game.ini", Op: OriginalOpProfileOverride, Mode: 0o640}
	require.NoError(t, restoreContained(override, stored, install, filepath.Join(install, "cfg", "game.ini")))
	got, err := os.ReadFile(filepath.Join(install, "realcfg", "game.ini"))
	require.NoError(t, err)
	assert.Equal(t, "shipped", string(got))
	info, err := os.Stat(filepath.Join(install, "realcfg", "game.ini"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())

	escaping := OriginalFile{Root: OriginalRootInstallPath, RelativePath: "away/game.ini", Op: OriginalOpProfileOverride}
	require.Error(t, restoreContained(escaping, stored, install, filepath.Join(install, "away", "game.ini")))
	entries, _ := os.ReadDir(outside)
	assert.Empty(t, entries, "a link leaving the install_path is refused even for an override")

	deployed := OriginalFile{Root: OriginalRootInstallPath, RelativePath: "cfg/d3d11.dll", Op: OriginalOpDeploy}
	require.Error(t, restoreContained(deployed, stored, install, filepath.Join(install, "cfg", "d3d11.dll")),
		"a game-root mod file's original keeps the strict rule")
}

// A purge plan built without PlanPurge's typed set reads its Remove
// entries back from their display spelling (#629 re-review), never a
// "<game root>/x" entry as a mod_path folder of that name.
func TestPurgePlan_RemoveKeysFallbackParsesTheDisplaySpelling(t *testing.T) {
	plan := &PurgePlan{Remove: []string{"<game root>/d3d11.dll", "textures/a.dds"}}
	assert.Equal(t, map[domain.DeployPath]bool{
		domain.GameRootFile("d3d11.dll"):     true,
		domain.ModPathFile("textures/a.dds"): true,
	}, plan.removeKeys())
}
