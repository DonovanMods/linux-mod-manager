package core

// Where adapter.RouteCopyOnce members are written, and what happens when
// one cannot be (#413, the #358/#359 review's F14 carry-in).
//
// #358 shipped this as seedBepInExConfig, called from inside
// Installer.Install's per-file loop and from replaceWithCaches. U1 shipped
// adapter.RouteCopyOnce with a flow-level pass of its own
// (Service.applyAdapterCopyOnce), which `lmm deploy` ran and no other flow
// did. U3 has to end with ONE mechanism whose reach is at least the one it
// replaced - every path that deploys a mod, verify --fix's re-deploy
// included - so the write lives where the old one did, on the Installer,
// and both entry points call the same seedCopyOnceFiles.
//
// These are INTERNAL tests because setAdapter is unexported: an Installer is
// a core primitive and its adapter comes from the Service's registry, never
// from a caller.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copyOnceRouter routes one fixed path copy-once and links everything else
// - the shape internal/adapter/bepinex has for BepInEx/config/**, stated
// here so this file tests the SEAM rather than one adapter's rules.
type copyOnceRouter struct{ adapter.Generic }

func (copyOnceRouter) ID() string { return "copy-once-router" }

func (copyOnceRouter) RouteFile(_ *domain.Game, rel string) adapter.FileRoute {
	if rel == "cfg/plugin.cfg" {
		return adapter.RouteCopyOnce
	}
	return adapter.RouteLink
}

// copyOnceFixture is a cache holding one mod version with a linked file and
// a copy-once file, plus the game they deploy into.
type copyOnceFixture struct {
	cache     *cache.Cache
	game      *domain.Game
	installer *Installer
}

func newCopyOnceFixture(t *testing.T, versions map[string]string) copyOnceFixture {
	t.Helper()
	c := cache.New(t.TempDir())
	for version, defaults := range versions {
		require.NoError(t, c.Store("g", "src", "m", version, "bin/plugin.dll", []byte("assembly "+version)))
		require.NoError(t, c.Store("g", "src", "m", version, "cfg/plugin.cfg", []byte(defaults)))
	}
	root := t.TempDir()
	game := &domain.Game{ID: "g", InstallPath: root, ModPath: root, LinkMethod: domain.LinkSymlink}
	inst := NewInstaller(c, linker.New(domain.LinkSymlink), nil)
	inst.setAdapter(copyOnceRouter{})
	return copyOnceFixture{cache: c, game: game, installer: inst}
}

func modAt(version string) *domain.Mod {
	return &domain.Mod{ID: "m", SourceID: "src", Version: version, Name: "Mod", GameID: "g"}
}

// TestInstallerSeedsCopyOnceFilesAsRealFiles is F14 (a) at the one place
// every deploy path goes through. The linked file is a symlink and the
// copy-once file is a REAL file, because the whole reason for the route is
// that the user hand-edits it after the game's first run and a symlink
// would push that edit back into the shared cache entry.
func TestInstallerSeedsCopyOnceFilesAsRealFiles(t *testing.T) {
	f := newCopyOnceFixture(t, map[string]string{"1.0": "volume=5\n"})
	require.NoError(t, f.installer.Install(context.Background(), f.game, modAt("1.0"), "default"))

	link, err := os.Lstat(filepath.Join(f.game.ModPath, "bin", "plugin.dll"))
	require.NoError(t, err)
	assert.NotZero(t, link.Mode()&os.ModeSymlink, "ordinary mod content is the linker's")

	seeded := filepath.Join(f.game.ModPath, "cfg", "plugin.cfg")
	info, err := os.Lstat(seeded)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "a copy-once member is a real file")
	body, err := os.ReadFile(seeded)
	require.NoError(t, err)
	assert.Equal(t, "volume=5\n", string(body))
}

// TestInstallerNeverOverwritesASeededFile: after the first deploy the file
// belongs to the user. A second install of the same version - which is what
// `lmm deploy` and verify --fix's repair both run - must leave their edit
// exactly where it is.
func TestInstallerNeverOverwritesASeededFile(t *testing.T) {
	f := newCopyOnceFixture(t, map[string]string{"1.0": "volume=5\n"})
	require.NoError(t, f.installer.Install(context.Background(), f.game, modAt("1.0"), "default"))

	seeded := filepath.Join(f.game.ModPath, "cfg", "plugin.cfg")
	require.NoError(t, os.WriteFile(seeded, []byte("volume=11\n"), 0o644))

	require.NoError(t, f.installer.Install(context.Background(), f.game, modAt("1.0"), "default"))
	body, err := os.ReadFile(seeded)
	require.NoError(t, err)
	assert.Equal(t, "volume=11\n", string(body), "the edit survives a re-deploy")
}

// TestInstallerReplaceKeepsTheUsersSeededFile is F14 (b), both halves in one
// pass: replacing 1.0 with 2.0 must not REMOVE the copy they edited (the
// obsolete-file loop skips a routed member, notLinkerOwned) and must not
// OVERWRITE it with the new version's default (copyOnce is a no-op for a
// file that is already there).
func TestInstallerReplaceKeepsTheUsersSeededFile(t *testing.T) {
	f := newCopyOnceFixture(t, map[string]string{"1.0": "volume=5\n", "2.0": "volume=7\n"})
	require.NoError(t, f.installer.Install(context.Background(), f.game, modAt("1.0"), "default"))

	seeded := filepath.Join(f.game.ModPath, "cfg", "plugin.cfg")
	require.NoError(t, os.WriteFile(seeded, []byte("volume=11\n"), 0o644))

	require.NoError(t, f.installer.Replace(context.Background(), f.game, modAt("1.0"), modAt("2.0"), "default"))

	body, err := os.ReadFile(seeded)
	require.NoError(t, err)
	assert.Equal(t, "volume=11\n", string(body),
		"an update carries a new default, not a new answer for the user")

	// And the update itself happened: the linked half points at 2.0.
	target, err := os.Readlink(filepath.Join(f.game.ModPath, "bin", "plugin.dll"))
	require.NoError(t, err)
	assert.Contains(t, target, filepath.Join("m", "2.0"))
}

// TestInstallerFailsTheWholeInstallWhenASeedCannotBeWritten pins the
// failure semantics F14 asked to be decided explicitly.
//
// The decision is the one #358 already made and this unit preserves: a seed
// failure FAILS THE INSTALL. A mod whose shipped defaults could not be
// written is not installed, and reporting success would leave a plugin
// running against configuration nobody chose.
//
// It is seeded BEFORE anything is linked, though, which is the one change:
// the old code interleaved the two, so a failure on the third file left the
// first two deployed and rolled them back. Failing first means there is
// nothing to roll back, and the game directory is untouched.
func TestInstallerFailsTheWholeInstallWhenASeedCannotBeWritten(t *testing.T) {
	f := newCopyOnceFixture(t, map[string]string{"1.0": "volume=5\n"})
	// A regular FILE where the seed's directory has to be: MkdirAll cannot
	// create through it.
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "cfg"), []byte("not a directory"), 0o644))

	err := f.installer.Install(context.Background(), f.game, modAt("1.0"), "default")
	require.Error(t, err)

	_, statErr := os.Lstat(filepath.Join(f.game.ModPath, "bin", "plugin.dll"))
	assert.True(t, os.IsNotExist(statErr), "a failed install deploys nothing")
}

// TestCopyOnceFilesAreNeverDeployables is the routing half: a copy-once
// member is not the linker's, so it never reaches deployableFiles and
// therefore never gets a deployed_files row, an undeploy, or a convergence
// sweep. That standing is what makes "it is the user's file now" true.
func TestCopyOnceFilesAreNeverDeployables(t *testing.T) {
	f := newCopyOnceFixture(t, map[string]string{"1.0": "volume=5\n"})
	files, err := deployableFiles(f.cache, copyOnceRouter{}, f.game, "src", "m", "1.0")
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join("bin", "plugin.dll")}, files)
	assert.Equal(t, []string{filepath.Join("cfg", "plugin.cfg")},
		adapterCopyOnceFiles(copyOnceRouter{}, f.game, []string{
			filepath.Join("bin", "plugin.dll"), filepath.Join("cfg", "plugin.cfg"),
		}))
}

// TestInstallerUninstallLeavesASeededFileAlone is the other half of the
// standing a copy-once member inherits from a profile override: it is never
// entered into deployed_files, and it is never removed by an uninstall.
//
// Asserted against a db-less Installer on purpose. That is the shape where
// foreignFile cannot answer, and under a copy or hardlink deploy nothing
// else would have stopped the removal - the symlink linker's refusal to
// unlink a regular file is an accident of the default, not a guarantee.
func TestInstallerUninstallLeavesASeededFile(t *testing.T) {
	f := newCopyOnceFixture(t, map[string]string{"1.0": "volume=5\n"})
	require.NoError(t, f.installer.Install(context.Background(), f.game, modAt("1.0"), "default"))

	seeded := filepath.Join(f.game.ModPath, "cfg", "plugin.cfg")
	require.NoError(t, os.WriteFile(seeded, []byte("volume=11\n"), 0o644))

	require.NoError(t, f.installer.Uninstall(context.Background(), f.game, modAt("1.0"), "default"))

	_, err := os.Lstat(filepath.Join(f.game.ModPath, "bin", "plugin.dll"))
	assert.True(t, os.IsNotExist(err), "the linked half goes")
	body, err := os.ReadFile(seeded)
	require.NoError(t, err)
	assert.Equal(t, "volume=11\n", string(body), "the user's file stays")
}
