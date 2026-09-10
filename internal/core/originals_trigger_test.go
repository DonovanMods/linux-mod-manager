package core_test

// originals_trigger_test.go drives the originals store (#350) through the
// REAL flows rather than through the store's own API: each test performs
// the write that replaces a file and then reads the manifest lmm wrote,
// which is the contract a restore depends on.
//
// The manifest is parsed here with a local struct on purpose. It is on-disk
// state a future lmm has to keep reading, so a test that decodes it by
// shape - rather than through the type that produced it - is what notices
// a key being renamed.

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// originalRow mirrors one internal/core.OriginalFile manifest row.
type originalRow struct {
	Root         string `json:"root"`
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Op           string `json:"op"`
	SourceID     string `json:"source_id"`
	ModID        string `json:"mod_id"`
	Profile      string `json:"profile"`
}

// newOriginalsService returns a Service whose data directory the test can
// read the manifest out of - the one thing newFlowsTestService's anonymous
// t.TempDir() does not expose.
func newOriginalsService(t *testing.T) (*core.Service, string) {
	t.Helper()
	// HOME and every XDG variable, sandboxed: the originals store and the
	// snapshot documents built on it resolve real paths, and nothing here
	// should be able to reach the developer's own config or data even by a
	// path this test does not itself name.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, dataDir
}

// readOriginalsManifest decodes <dataDir>/snapshots/<gameID>/_originals/manifest.json,
// treating an absent file as no rows (a game that never had anything
// replaced).
func readOriginalsManifest(t *testing.T, dataDir, gameID string) []originalRow {
	t.Helper()
	path := filepath.Join(dataDir, "snapshots", gameID, "_originals", "manifest.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var doc struct {
		Originals []originalRow `json:"originals"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc.Originals
}

func readStoredOriginal(t *testing.T, dataDir, gameID, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "snapshots", gameID, "_originals", "files", root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(data)
}

// TestDeploy_PreservesAStockFileItReplaces is the gap #350 exists to close.
// lmm's conflict detection only compares against files ANOTHER INSTALLED
// MOD owns, so a deploy landing on stock game content raises nothing at all
// and simply overwrites it. Those bytes are the one thing lmm cannot
// rebuild, so they are copied aside first.
func TestDeploy_PreservesAStockFileItReplaces(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	stock := filepath.Join(gameDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	rows := readOriginalsManifest(t, dataDir, "g1")
	require.Len(t, rows, 1)
	assert.Equal(t, "mod_path", rows[0].Root)
	assert.Equal(t, "Data/shipped.esp", rows[0].RelativePath)
	assert.Equal(t, "deploy", rows[0].Op)
	assert.Equal(t, "src", rows[0].SourceID, "the row names the mod whose file replaced it")
	assert.Equal(t, "m1", rows[0].ModID)
	assert.Equal(t, "default", rows[0].Profile)
	assert.Equal(t, int64(len("as the game shipped")), rows[0].Size)
	assert.NotEmpty(t, rows[0].SHA256)

	assert.Equal(t, "as the game shipped",
		readStoredOriginal(t, dataDir, "g1", "mod_path", "Data/shipped.esp"))
}

// TestDeploy_DoesNotPreserveAFileLmmAlreadyOwns pins the OTHER half of the
// rule, and the reason the mod-versus-mod Overwrite decision needs no
// capture of its own: the loser of that contest is a mod file, still in the
// cache, and lmm can put it back by deploying it again. Only content lmm
// has no copy of is worth storing - and storing a mod's file as an
// "original" would make a restore write the wrong bytes.
func TestDeploy_DoesNotPreserveAFileLmmAlreadyOwns(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	gameDir := t.TempDir()
	// Copy mode, so what the first deploy leaves behind is a REGULAR file -
	// the shape that would otherwise look like foreign content.
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkCopy}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true,
		map[string][]byte{"shared.esp": []byte("A's file")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true,
		map[string][]byte{"shared.esp": []byte("B's file")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")
	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"),
		"a file already attributed to a mod is reconstructible from the cache; only foreign content is stored")
}

// TestDeploy_DoesNotPreserveItsOwnSymlink pins the cheapest of the three
// filters: a symlink at the destination is lmm's own deployment (or another
// manager's link), never stock content.
func TestDeploy_DoesNotPreserveItsOwnSymlink(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"one.esp": []byte("1")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	for range 2 {
		_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
		require.NoError(t, err)
	}
	assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"))
}

// TestDeploy_PreservesAStockFileOnceOnly is the first-original-wins rule
// seen from the flow side: a redeploy must not record the mod's own file as
// the "original".
func TestDeploy_PreservesAStockFileOnceOnly(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkCopy}

	stock := filepath.Join(gameDir, "shipped.esp")
	require.NoError(t, os.WriteFile(stock, []byte("stock"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"shipped.esp": []byte("mod")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	for range 3 {
		_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
		require.NoError(t, err)
	}

	rows := readOriginalsManifest(t, dataDir, "g1")
	require.Len(t, rows, 1)
	assert.Equal(t, "stock", readStoredOriginal(t, dataDir, "g1", "mod_path", "shipped.esp"))
}

// TestDeploy_PreservesTheGameFileAProfileOverrideReplaces is the second
// trigger: an override is a write into the game's INSTALL directory, and
// the common case is an INI the game itself shipped.
func TestDeploy_PreservesTheGameFileAProfileOverrideReplaces(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	installDir, gameDir := t.TempDir(), t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: installDir, ModPath: gameDir,
		LinkMethod: domain.LinkSymlink,
	}

	shipped := filepath.Join(installDir, "Data", "game.ini")
	require.NoError(t, os.MkdirAll(filepath.Dir(shipped), 0755))
	require.NoError(t, os.WriteFile(shipped, []byte("[General]\nshipped=1"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"one.esp": []byte("1")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	// Add the override to the saved profile, which is what the deploy reads.
	pm := svc.NewProfileManager()
	profile, err := pm.Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{
		"Data/game.ini": []byte("[General]\noverridden=1"),
		"Data/new.ini":  []byte("brand new"),
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	// The override really landed...
	written, err := os.ReadFile(shipped)
	require.NoError(t, err)
	assert.Contains(t, string(written), "overridden=1")

	// ...and the file it replaced was preserved, under the INSTALL root.
	rows := readOriginalsManifest(t, dataDir, "g1")
	require.Len(t, rows, 1, "only the override that replaced something is recorded")
	assert.Equal(t, "install_path", rows[0].Root)
	assert.Equal(t, "Data/game.ini", rows[0].RelativePath)
	assert.Equal(t, "profile_override", rows[0].Op)
	assert.Empty(t, rows[0].SourceID, "an override belongs to no mod")
	assert.Equal(t, "default", rows[0].Profile)

	assert.Equal(t, "[General]\nshipped=1",
		readStoredOriginal(t, dataDir, "g1", "install_path", "Data/game.ini"))
}

// TestDeploy_PreservesAStockFileWhenModPathIsTheInstallDir is the
// compile-mode/BepInEx shape (#267's spike: mod_path IS install_path), and
// the case #350 names as "a compile-mode artifact replacing a stock file".
// It needs no format-specific code: a merged artifact deploys as an
// ordinary synthetic mod through the same Installer, so the general rule
// covers it.
func TestDeploy_PreservesAStockFileWhenModPathIsTheInstallDir(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: gameDir, ModPath: gameDir,
		LinkMethod: domain.LinkSymlink,
	}

	stock := filepath.Join(gameDir, "BepInEx", "core", "plugin.dll")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("stock plugin"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"BepInEx/core/plugin.dll": []byte("patched plugin")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	rows := readOriginalsManifest(t, dataDir, "g1")
	require.Len(t, rows, 1)
	assert.Equal(t, "BepInEx/core/plugin.dll", rows[0].RelativePath)
	assert.Equal(t, "stock plugin",
		readStoredOriginal(t, dataDir, "g1", "mod_path", "BepInEx/core/plugin.dll"))
}

// TestUndeploy_LeavesAFileLmmDoesNotOwnAlone is the data-loss path #350's
// originals work exposed, and the one that needed a fix rather than a
// backup.
//
// Installer.Uninstall undeploys every path the mod's CACHE ENTRY names,
// which is not the same as every path the mod actually put there. The copy
// and hardlink linkers remove whatever is at the path, so a deploy (which
// undeploys before it installs), a purge or an ordinary uninstall deleted
// stock game content sitting where one of the mod's files would go -
// silently, with no way back. The symlink linker refuses to remove a
// non-symlink, which is why the default link method never showed it.
func TestUndeploy_LeavesAFileLmmDoesNotOwnAlone(t *testing.T) {
	svc, _ := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	stock := filepath.Join(gameDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0644))

	// A mod whose file list NAMES that path, installed but never deployed.
	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	_, err := svc.PurgeProfile(context.Background(), game,
		"default", modsOf(t, svc, "g1", "default"), core.PurgeOptions{}, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(stock)
	require.NoError(t, err, "the stock file must still be there")
	assert.Equal(t, "as the game shipped", string(data))
}

// TestUndeploy_StillRemovesItsOwnCopiedFile pins the other half: the fix
// must not turn a copy-mode uninstall into a no-op. lmm's own copied file
// carries a deployed_files row, so it is removed exactly as before.
func TestUndeploy_StillRemovesItsOwnCopiedFile(t *testing.T) {
	svc, _ := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/mine.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(gameDir, "Data", "mine.esp"))

	_, err = svc.PurgeProfile(context.Background(), game,
		"default", modsOf(t, svc, "g1", "default"), core.PurgeOptions{}, nil)
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(gameDir, "Data", "mine.esp"),
		"lmm's own copied file carries an ownership row and is still removed")
}

// modsOf reads a profile's installed set, which PurgeProfile takes directly.
func modsOf(t *testing.T, svc *core.Service, gameID, profileName string) []domain.InstalledMod {
	t.Helper()
	mods, err := svc.GetInstalledMods(context.Background(), gameID, profileName)
	require.NoError(t, err)
	return mods
}

// TestReplace_LeavesAFileLmmDoesNotOwnAlone is review finding 4: the same
// "an undeploy deletes a file lmm did not put there" data loss survived in
// the UPDATE path after e262cbc4 fixed Uninstall.
//
// replaceWithCaches' obsolete-file loop iterates the OLD cache entry's raw
// ListFiles union and calls linker.Undeploy on every member the new side
// does not name - with no ownership guard. Under copy or hardlink that
// removes whatever is at the path, so `lmm update` deleted stock content
// sitting at a path the old cache entry named but lmm never deployed: the
// #210 narrowing case, and the "stale unclaimed member" case.
func TestReplace_LeavesAFileLmmDoesNotOwnAlone(t *testing.T) {
	modCache := cache.New(t.TempDir())
	gameDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	game := &domain.Game{ID: "g", ModPath: gameDir, LinkMethod: domain.LinkCopy}
	oldMod := &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g"}
	newMod := &domain.Mod{ID: "1", SourceID: "src", Version: "2.0", GameID: "g"}

	// v1's cache entry names Data/stock.esp; v2's does not, so the obsolete
	// loop will visit it. Nothing was ever deployed there - the file on
	// disk is the game's own.
	require.NoError(t, modCache.Store("g", "src", "1", "1.0", "Data/stock.esp", []byte("the mod's version")))
	require.NoError(t, modCache.Store("g", "src", "1", "1.0", "Data/mine.esp", []byte("v1")))
	require.NoError(t, modCache.Store("g", "src", "1", "2.0", "Data/mine.esp", []byte("v2")))

	stock := filepath.Join(gameDir, "Data", "stock.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0644))

	// lmm's own v1 deployment of the OTHER file, with its ownership row.
	mine := filepath.Join(gameDir, "Data", "mine.esp")
	require.NoError(t, os.WriteFile(mine, []byte("v1"), 0644))
	require.NoError(t, database.SaveDeployedFile(context.Background(), "g", "default", "Data/mine.esp", "src", "1"))

	inst := core.NewInstaller(modCache, linker.New(domain.LinkCopy), database)
	require.NoError(t, inst.Replace(context.Background(), game, oldMod, newMod, "default"))

	data, err := os.ReadFile(stock)
	require.NoError(t, err, "the stock file must still be there after an update")
	assert.Equal(t, "as the game shipped", string(data))

	data, err = os.ReadFile(mine)
	require.NoError(t, err)
	assert.Equal(t, "v2", string(data), "lmm's own file is still replaced by the new version")
}

// TestDeploy_AFailedCaptureIsVisibleAtDefaultVerbosity is review finding 5.
// A capture failure is the moment lmm is about to overwrite an
// irreplaceable file and could not preserve it - a full disk, a permission
// problem on the store, a manifest it cannot parse. It was reported with
// log.Warn only, and `lmm`'s default --log-level is "off" (cmd/lmm's
// newCLILogger returns a discarding handler for it), so the user learned
// about it at the restore that could not put the file back.
//
// It is now on both always-on channels: ServiceConfig.WarnWriter, and the
// deploy result's Warnings (which the CLI and the SPA surface
// unconditionally, as a WarningEvent).
func TestDeploy_AFailedCaptureIsVisibleAtDefaultVerbosity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	var warned bytes.Buffer
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
		WarnWriter: &warned,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	stock := filepath.Join(gameDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0644))

	// The store cannot be written: its own directory is a FILE.
	storeDir := filepath.Join(dataDir, "snapshots", "g1", "_originals")
	require.NoError(t, os.MkdirAll(filepath.Dir(storeDir), 0755))
	require.NoError(t, os.WriteFile(storeDir, []byte("not a directory"), 0644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	var warnings []string
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{},
		func(e core.Event) {
			if w, ok := e.(core.WarningEvent); ok {
				warnings = append(warnings, w.Message)
			}
		})
	require.NoError(t, err, "a failed capture is a warning, never a refusal")

	assert.Contains(t, strings.Join(result.Warnings, "\n"), "could not preserve",
		"the deploy result must name the file it could not preserve")
	assert.Contains(t, strings.Join(warnings, "\n"), "could not preserve",
		"and it must reach a live progress stream as a WarningEvent")
	assert.Contains(t, warned.String(), "could not preserve",
		"and the always-on user channel, since the CLI's default log level discards logs")
}

// TestUninstall_PutsBackTheFileItReplaced is the coordinator's ruling on
// the review's note 13. An ordinary uninstall or purge used to leave the
// HOLE where stock content had been: the original was in the store, but the
// only way to get it back was `lmm snapshot restore`, a whole-state
// operation nobody wants for one mod. "Undo what lmm did" has to include
// the file lmm displaced, so it goes back at the moment lmm's own file is
// removed - and the manifest row goes with it, because lmm no longer holds
// the only copy.
func TestUninstall_PutsBackTheFileItReplaced(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	stock := filepath.Join(gameDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0755))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	data, err := os.ReadFile(stock)
	require.NoError(t, err)
	require.Equal(t, "the mod's version", string(data), "the mod really did replace it")
	require.Len(t, readOriginalsManifest(t, dataDir, "g1"), 1)

	_, err = svc.PurgeProfile(context.Background(), game,
		"default", modsOf(t, svc, "g1", "default"), core.PurgeOptions{}, nil)
	require.NoError(t, err)

	data, err = os.ReadFile(stock)
	require.NoError(t, err, "the stock file must be BACK, not merely absent")
	assert.Equal(t, "as the game shipped", string(data))

	info, err := os.Stat(stock)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0755), info.Mode().Perm(), "and with the mode it had")

	assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"),
		"the row goes once the original is back in place: lmm no longer holds the only copy")
}

// TestDeploy_DoesNotPreserveAnotherProfilesOwnFile is #350 review minor 7.
// Ownership was asked profile-scoped (db.GetFileOwner takes game AND
// profile), so `lmm deploy -p B` over a copy/hardlink deployment made under
// profile A saw A's own regular file as foreign: it was stored as an
// "original", and a later restore would have written a mod's bytes back as
// if they were stock content. The capture decision is game-scoped now.
func TestDeploy_DoesNotPreserveAnotherProfilesOwnFile(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	gameDir := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	// Profile A deploys a real (copied) file and owns it.
	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shared.esp": []byte("A's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(gameDir, "Data", "shared.esp"))

	// Profile B deploys a DIFFERENT mod over the same path.
	seedInstalledModInProfile(t, svc, game, "other", "src", "m2", "Mod Two", "1.0",
		map[string][]byte{"Data/shared.esp": []byte("B's version")})
	_, err = svc.DeployProfile(context.Background(), game, "other", core.DeployOptions{}, nil)
	require.NoError(t, err)

	assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"),
		"another profile's own deployment is not stock content")
}

// TestRemoval_AFailedPutBackIsOnTheResult is #350 re-review finding N2.
// Ruling (a) made an uninstall and a purge into paths that WRITE stored
// originals back, so they became paths a put-back can fail on - a stored
// copy whose checksum no longer matches, which is precisely when the user
// most needs telling. The message reached ServiceConfig.WarnWriter, but
// only the deploy/install/update flows drained the pending list onto their
// result, so under `lmm serve` - where WarnWriter is the SERVER's stderr,
// not the job's event stream - a browser user saw nothing, and the entry was
// then surfaced late on whatever deploy ran next.
func TestRemoval_AFailedPutBackIsOnTheResult(t *testing.T) {
	t.Run("purge", func(t *testing.T) {
		svc, dataDir, warned, game := newFailedPutBackFixture(t)
		result, err := svc.PurgeProfile(context.Background(), game,
			"default", modsOf(t, svc, "g1", "default"), core.PurgeOptions{}, nil)
		require.NoError(t, err, "a failed put-back never fails the removal")
		assertPutBackFailureIsVisible(t, dataDir, warned, result.Warnings)
	})

	t.Run("uninstall", func(t *testing.T) {
		svc, dataDir, warned, game := newFailedPutBackFixture(t)
		result, err := svc.UninstallMod(context.Background(), game, "default", "src", "m1",
			core.UninstallOptions{})
		require.NoError(t, err, "a failed put-back never fails the removal")
		assertPutBackFailureIsVisible(t, dataDir, warned, result.Warnings)
	})
}

// newFailedPutBackFixture deploys a mod over the game's own
// Data/shipped.esp - so the original is captured - and then CORRUPTS the
// stored copy, which is what makes the put-back that follows fail its
// checksum check.
func newFailedPutBackFixture(t *testing.T) (*core.Service, string, *bytes.Buffer, *domain.Game) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	warned := &bytes.Buffer{}
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
		WarnWriter: warned,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir,
		LinkMethod: domain.LinkCopy, LinkMethodExplicit: true,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	stock := filepath.Join(gameDir, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0755))
	require.NoError(t, os.WriteFile(stock, []byte("as the game shipped"), 0755))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")

	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, readOriginalsManifest(t, dataDir, "g1"), 1, "the deploy captured the stock file")

	stored := filepath.Join(dataDir, "snapshots", "g1", "_originals", "files", "mod_path", "Data", "shipped.esp")
	require.NoError(t, os.WriteFile(stored, []byte("corrupted in the store"), 0600))

	warned.Reset()
	return svc, dataDir, warned, game
}

func assertPutBackFailureIsVisible(t *testing.T, dataDir string, warned *bytes.Buffer, warnings []string) {
	t.Helper()
	assert.Contains(t, strings.Join(warnings, "\n"), "could not put back",
		"the removal's result must name the file it could not put back - it is all `lmm serve` has")
	assert.Contains(t, warned.String(), "could not put back",
		"and the always-on user channel, since the CLI's default log level discards logs")
	assert.Len(t, readOriginalsManifest(t, dataDir, "g1"), 1,
		"the row survives a failed put-back: `lmm snapshot restore` can still do it")
}
