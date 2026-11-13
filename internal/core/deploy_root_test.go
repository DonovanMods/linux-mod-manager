package core_test

// deploy_root_test.go drives #629's game-root deploys through the real
// flows: a file the game's adapter routes to adapter.RootInstallPath
// deploys under the install_path, a stock file it lands on is preserved
// first and put back when lmm's file goes, and every removal, verify and
// conflict report finds it there and names the root.

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rootTestAdapter is the bethesda adapter's root rule in miniature: a
// top-level d3d11.dll and the enbseries/ folder belong at the game root.
type rootTestAdapter struct{ adapter.Generic }

const rootTestAdapterID = "root-test"

func (rootTestAdapter) ID() string { return rootTestAdapterID }

func (rootTestAdapter) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	out := make([]adapter.DeployRoot, len(files))
	for i, f := range files {
		if f == "d3d11.dll" || strings.HasPrefix(f, "enbseries/") {
			out[i] = adapter.RootInstallPath
		}
	}
	return out
}

func (rootTestAdapter) RootForModType(*domain.Game, string) (adapter.DeployRoot, bool) {
	return adapter.RootModPath, false
}

// newRootGame is a Bethesda-shaped game (mod_path = install_path/Data)
// using rootTestAdapter, saved so every flow resolves it.
func newRootGame(t *testing.T, svc *core.Service, method domain.LinkMethod) *domain.Game {
	t.Helper()
	svc.RegisterAdapter(rootTestAdapter{})
	install := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Game", InstallPath: install, ModPath: filepath.Join(install, "Data"),
		LinkMethod: method, LinkMethodExplicit: true, Adapter: rootTestAdapterID,
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return game
}

var enbFiles = map[string][]byte{
	"d3d11.dll":            []byte("the ENB's d3d11"),
	"enbseries/effect.fx":  []byte("shader"),
	"textures/sky/sky.dds": []byte("a texture"),
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func assertAbsent(t *testing.T, path, msg string) {
	t.Helper()
	_, err := os.Lstat(path)
	assert.True(t, os.IsNotExist(err), "%s: %s should not exist (%v)", msg, path, err)
}

var rootMethods = []domain.LinkMethod{domain.LinkSymlink, domain.LinkHardlink, domain.LinkCopy}

// The most important behaviour (#629): a game that already ships a
// d3d11.dll beside its executable gets it back. The deploy preserves the
// stock file under the originals store's install_path root before the
// mod's goes over it, and a purge puts it back - under every link method.
func TestDeployRoot_StockFileIsPreservedAndPurgeRestoresIt(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			svc, dataDir := newOriginalsService(t)
			game := newRootGame(t, svc, method)
			stock := filepath.Join(game.InstallPath, "d3d11.dll")
			require.NoError(t, os.WriteFile(stock, []byte("the game's own d3d11"), 0o755))

			seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
			seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")

			_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)

			assert.Equal(t, "the ENB's d3d11", readFileString(t, stock), "the mod's d3d11.dll is at the game root")
			assert.Equal(t, "shader", readFileString(t, filepath.Join(game.InstallPath, "enbseries", "effect.fx")))
			assert.Equal(t, "a texture", readFileString(t, filepath.Join(game.ModPath, "textures", "sky", "sky.dds")))
			assertAbsent(t, filepath.Join(game.ModPath, "d3d11.dll"), "nothing routed to the root lands under Data")
			assertAbsent(t, filepath.Join(game.ModPath, "enbseries"), "nothing routed to the root lands under Data")

			rows := readOriginalsManifest(t, dataDir, "g1")
			require.Len(t, rows, 1, "the stock d3d11.dll, and only it, was preserved")
			assert.Equal(t, "install_path", rows[0].Root)
			assert.Equal(t, "d3d11.dll", rows[0].RelativePath)
			assert.Equal(t, "deploy", rows[0].Op)
			assert.Equal(t, "enb", rows[0].ModID)
			assert.Equal(t, "the game's own d3d11", readStoredOriginal(t, dataDir, "g1", "install_path", "d3d11.dll"))

			plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			_, err = svc.ApplyPurge(context.Background(), game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)

			info, err := os.Lstat(stock)
			require.NoError(t, err, "purge puts the game's own d3d11.dll back")
			assert.True(t, info.Mode().IsRegular())
			assert.Equal(t, "the game's own d3d11", readFileString(t, stock))
			assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "with the mode it had")
			assertAbsent(t, filepath.Join(game.InstallPath, "enbseries"), "the folder the mod's removal emptied goes")
			assertAbsent(t, filepath.Join(game.ModPath, "textures"), "the Data content goes too")
			assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"), "the original was handed back")
		})
	}
}

// A game with no d3d11.dll of its own: the mod's is deployed, nothing is
// preserved, and removing the mod leaves no file there at all.
func TestDeployRoot_NoStockFileLeavesNothingBehind(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			svc, dataDir := newOriginalsService(t)
			game := newRootGame(t, svc, method)
			seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
			seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")

			_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, "the ENB's d3d11", readFileString(t, filepath.Join(game.InstallPath, "d3d11.dll")))
			assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"), "there was nothing of the game's to preserve")

			res, err := svc.UninstallMod(context.Background(), game, "default", "src", "enb", core.UninstallOptions{KeepCache: true})
			require.NoError(t, err)
			assert.Empty(t, res.Warnings)
			assertAbsent(t, filepath.Join(game.InstallPath, "d3d11.dll"), "uninstall removes the game-root file")
			assertAbsent(t, filepath.Join(game.InstallPath, "enbseries"), "and the folder it emptied")
			assertAbsent(t, filepath.Join(game.ModPath, "textures"), "and the Data content")
		})
	}
}

// A game-root file is never written outside the install_path: a folder
// under the game root that is a link to somewhere else is refused, and
// nothing lands where it points.
func TestDeployRoot_RefusesAGameRootFolderThatLeavesThroughALink(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(game.InstallPath, "enbseries")))

	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")

	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	entries, rerr := os.ReadDir(elsewhere)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "nothing was written through the link")

	var said []string
	if err != nil {
		said = append(said, err.Error())
	}
	if res != nil {
		said = append(said, res.Warnings...)
		for _, sk := range res.Skipped {
			said = append(said, sk.Reason)
		}
	}
	assert.Contains(t, strings.Join(said, "\n"), "install_path", "the refusal names the install_path")
	assertAbsent(t, filepath.Join(game.InstallPath, "d3d11.dll"), "the failed mod is rolled back")
}

// verify, conflicts and `lmm mod files` see a game-root file under its own
// root and say so: its path with root "install_path", which a frontend
// shows as "<game root>/d3d11.dll" - never a bare "d3d11.dll", which would
// be the Data file of that name.
func TestDeployRoot_ReportsNameTheGameRoot(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkCopy)
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedNamedInstalledMod(t, svc, game, "src", "enb2", "Other ENB", "1.0", true, map[string][]byte{"d3d11.dll": []byte("another d3d11")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb2", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	conflicts, err := svc.GetProfileConflicts(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, "d3d11.dll", conflicts[0].Path)
	assert.Equal(t, domain.RootInstallPath, conflicts[0].Root)

	files, err := svc.ModFiles(context.Background(), game, "default", "src", "enb")
	require.NoError(t, err)
	var paths []string
	for _, f := range files.Files {
		paths = append(paths, domain.DisplayDeployPath(f.Root, f.Path))
		assert.True(t, f.Deployed, f.Path)
	}
	assert.ElementsMatch(t, []string{"textures/sky/sky.dds", "<game root>/enbseries/effect.fx"}, paths,
		"enb2 won d3d11.dll, so enb records only these")

	// The user replaces the deployed file with their own: verify says so,
	// naming the root.
	tuned := filepath.Join(game.InstallPath, "enbseries", "effect.fx")
	require.NoError(t, os.Remove(tuned))
	require.NoError(t, os.WriteFile(tuned, []byte("hand-tuned"), 0o644))
	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	result := report.Result
	var modified []core.VerifyFinding
	for _, f := range result.Findings {
		if f.Status == core.VerifyStatusDeployedModified {
			modified = append(modified, f)
		}
	}
	require.Len(t, modified, 1, "%+v", result.Findings)
	assert.Equal(t, "enbseries/effect.fx", modified[0].FileID)
	assert.Equal(t, domain.RootInstallPath, modified[0].Root)
	assert.Contains(t, modified[0].Note, "<game root>/enbseries/effect.fx", "prose names the root")
	for _, f := range result.Findings {
		assert.NotContains(t, f.Note, "\x00", "no internal key reaches a finding")
		assert.NotContains(t, f.FileID, "\x00")
	}
}

// verify --fix converges: a game-root file the mod no longer ships is
// removed, reported under its root, and the stock file it had replaced
// goes back.
func TestDeployRoot_VerifyFixConvergesAGameRootFileNoLongerShipped(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	stock := filepath.Join(game.InstallPath, "d3d11.dll")
	require.NoError(t, os.WriteFile(stock, []byte("stock"), 0o644))
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, readOriginalsManifest(t, dataDir, "g1"), 1)

	// The cache entry stops shipping d3d11.dll (a re-extract of a newer
	// layout, say); verify --fix converges.
	cached := svc.GetGameCache(game).GetFilePath(cache.Key{GameID: "g1", SourceID: "src", ModID: "enb", Version: "1.0"}, "d3d11.dll")
	require.NoError(t, os.Remove(cached))

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	var fixed []string
	for _, f := range report.Result.Findings {
		if f.Status == "fixed_stale_deployment" {
			fixed = append(fixed, domain.DisplayDeployPath(f.Root, f.FileID))
		}
	}
	assert.Equal(t, []string{"<game root>/d3d11.dll"}, fixed)
	info, err := os.Lstat(stock)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the stock file is back")
	assert.Equal(t, "stock", readFileString(t, stock))
	assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"))
}

// A snapshot records a game-root file with its root, so the document never
// reads as the Data file of the same name.
func TestDeployRoot_SnapshotRecordsTheRoot(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	res, err := svc.CreateSnapshot(context.Background(), game, "default", "before")
	require.NoError(t, err)
	data, err := os.ReadFile(res.Path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `\u0000`)
	var doc struct {
		DeployedFiles []struct {
			RelativePath string  `json:"relative_path"`
			Root         *string `json:"root"`
		} `json:"deployed_files"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	roots := map[string]string{}
	for _, f := range doc.DeployedFiles {
		root := "(none)"
		if f.Root != nil {
			root = *f.Root
		}
		roots[f.RelativePath] = root
	}
	assert.Equal(t, map[string]string{
		"d3d11.dll":            "install_path",
		"enbseries/effect.fx":  "install_path",
		"textures/sky/sky.dds": "(none)",
	}, roots, "a game-root file names its root; a Data file carries none")
	_ = dataDir
}

// A deploy made before the game's adapter routed a file to the game root
// put it under the mod_path; the uninstall that follows the adapter change
// still finds it there, by its record.
func TestDeployRoot_UninstallFindsAFileWhereAnEarlierRoutingPutIt(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	game.Adapter = ""
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(game.ModPath, "d3d11.dll"))
	require.NoError(t, err, "the generic adapter deploys everything under Data")

	game.Adapter = rootTestAdapterID
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "enb", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(game.ModPath, "d3d11.dll"), "the uninstall removes the file where it was deployed")
	assertAbsent(t, filepath.Join(game.ModPath, "enbseries"), "")
	assertAbsent(t, filepath.Join(game.InstallPath, "d3d11.dll"), "and writes nothing at the root")
}

// Disabling a mod takes its game-root files down and gives the stock file
// back; enabling it deploys them at the root again, preserving it again.
func TestDeployRoot_DisableAndEnable(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	stock := filepath.Join(game.InstallPath, "d3d11.dll")
	require.NoError(t, os.WriteFile(stock, []byte("stock"), 0o644))
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	_, err = svc.DisableMod(context.Background(), game, "default", "src", "enb")
	require.NoError(t, err)
	assert.Equal(t, "stock", readFileString(t, stock))
	assertAbsent(t, filepath.Join(game.InstallPath, "enbseries"), "disable removes the game-root folder it emptied")
	assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"))

	_, err = svc.EnableMod(context.Background(), game, "default", "src", "enb")
	require.NoError(t, err)
	assert.Equal(t, "the ENB's d3d11", readFileString(t, stock))
	require.Len(t, readOriginalsManifest(t, dataDir, "g1"), 1, "the stock file is preserved again")
}

// Switching to a profile without the mod converges its game-root files
// away; switching back deploys them again.
func TestDeployRoot_ProfileSwitch(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	stock := filepath.Join(game.InstallPath, "d3d11.dll")
	require.NoError(t, os.WriteFile(stock, []byte("stock"), 0o644))
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	_, err := svc.NewProfileManager().Create(context.Background(), "g1", "plain")
	require.NoError(t, err)
	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "plain")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "stock", readFileString(t, stock), "the switch put the game's own file back")
	assertAbsent(t, filepath.Join(game.InstallPath, "enbseries"), "")

	plan, err = svc.PlanProfileSwitch(context.Background(), game, "default")
	require.NoError(t, err)
	_, err = svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "the ENB's d3d11", readFileString(t, stock), "switching back deploys it at the root again")
}

// writeRootProfile writes profile "default" listing src:enb, with overrides.
func writeRootProfile(t *testing.T, svc *core.Service, overrides map[string]string) {
	t.Helper()
	text := "name: default\ngame_id: g1\nmods:\n    - source_id: src\n      mod_id: enb\n      version: \"1.0\"\n"
	if len(overrides) > 0 {
		text += "overrides:\n"
		for k, v := range overrides {
			text += "    " + k + ": " + yamlQuote(v) + "\n"
		}
	}
	text += "is_default: true\n"
	dir := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "default.yaml"), []byte(text), 0o644))
}

// #630 on the second root: a profile override on a mod file deployed under
// the game root breaks that file's link exactly as it does under the mod
// path - a regular copy of its own, the shared cache untouched - verify
// calls it overridden, and once the override is gone the next deploy puts
// the plain link back.
func TestDeployRoot_OverrideOnAGameRootModFile(t *testing.T) {
	svc, dataDir := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	writeRootProfile(t, svc, map[string]string{"enbseries/effect.fx": "tuned\n"})

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(game.InstallPath, "enbseries", "effect.fx")
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "an overridden copy, not the link")
	assert.Equal(t, "tuned\n", readFileString(t, deployed))
	cached := svc.GetGameCache(game).GetFilePath(cache.Key{GameID: "g1", SourceID: "src", ModID: "enb", Version: "1.0"}, "enbseries/effect.fx")
	assert.Equal(t, "shader", readFileString(t, cached), "the shared cache entry is never written")
	assert.Empty(t, readOriginalsManifest(t, dataDir, "g1"), "the mod's own file is no original to keep")

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	var overridden []string
	for _, f := range report.Result.Findings {
		assert.NotEqual(t, core.VerifyStatusDeployedModified, f.Status, "%+v", f)
		if f.Status == core.VerifyStatusDeployedOverridden {
			overridden = append(overridden, domain.DisplayDeployPath(f.Root, f.FileID))
		}
	}
	assert.Equal(t, []string{"<game root>/enbseries/effect.fx"}, overridden)

	writeRootProfile(t, svc, nil)
	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	target, err := os.Readlink(deployed)
	require.NoError(t, err, "the plain link is back")
	assert.Equal(t, cached, target)
}

// `lmm deploy --purge` moves a file an earlier routing put under Data/ to
// the game root: the purge pass finds it by its record, the deploy puts it
// at the root.
func TestDeployRoot_DeployPurgeMovesAFileAnEarlierRoutingPutInData(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	game.Adapter = ""
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	game.Adapter = rootTestAdapterID
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{Purge: true}, nil)
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(game.ModPath, "d3d11.dll"), "the purge pass took the Data copy down")
	assertAbsent(t, filepath.Join(game.ModPath, "enbseries"), "")
	assert.Equal(t, "the ENB's d3d11", readFileString(t, filepath.Join(game.InstallPath, "d3d11.dll")))
	files, err := svc.ModFiles(context.Background(), game, "default", "src", "enb")
	require.NoError(t, err)
	var paths []string
	for _, f := range files.Files {
		paths = append(paths, domain.DisplayDeployPath(f.Root, f.Path))
	}
	assert.ElementsMatch(t, []string{"<game root>/d3d11.dll", "<game root>/enbseries/effect.fx", "textures/sky/sky.dds"}, paths)
}

// The bethesda adapter's ENB preset (#629 review, the blocker): a top-level
// enbseries.ini takes the whole mod to the game root, and the mod's Data/
// folder - whatever its case - lands in the mod_path, each file resolving
// to the cache member it came from, under every link method; verify finds
// nothing wrong, and a purge takes it all down.
func TestDeployRoot_ENBPresetWithADataFolder(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			svc, _ := newOriginalsService(t)
			svc.RegisterAdapter(bethesda.New())
			install := t.TempDir()
			game := &domain.Game{ID: "skyrim-se", Name: "Skyrim", InstallPath: install, ModPath: filepath.Join(install, "Data"),
				LinkMethod: method, LinkMethodExplicit: true, Adapter: bethesda.ID}
			require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
			require.NoError(t, svc.SaveGame(context.Background(), game))
			files := map[string][]byte{
				"enbseries.ini":         []byte("[GLOBAL]"),
				"enbseries/effect.fx":   []byte("shader"),
				"Data/Weather.esp":      []byte("weather plugin"),
				"data/textures/sky.dds": []byte("lowercase data folder"),
				"Data/meshes/cloud.nif": []byte("cloud"),
			}
			seedNamedInstalledMod(t, svc, game, "src", "preset", "ENB Preset", "1.0", true, files)
			seedProfileWithMod(t, svc, game.ID, "default", "src", "preset", "1.0")

			res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, 1, res.Deployed)
			assert.Empty(t, res.Skipped)
			assert.Empty(t, res.Warnings)

			for path, want := range map[string]string{
				filepath.Join(install, "enbseries.ini"):            "[GLOBAL]",
				filepath.Join(install, "enbseries", "effect.fx"):   "shader",
				filepath.Join(game.ModPath, "Weather.esp"):         "weather plugin",
				filepath.Join(game.ModPath, "textures", "sky.dds"): "lowercase data folder",
				filepath.Join(game.ModPath, "meshes", "cloud.nif"): "cloud",
			} {
				assert.Equal(t, want, readFileString(t, path), path)
			}
			assertAbsent(t, filepath.Join(install, "data"), "a lowercase data/ folder is the game's Data/, never one beside it")
			assertAbsent(t, filepath.Join(game.ModPath, "Data"), "nor Data/Data/")

			report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
			require.NoError(t, err)
			for _, f := range report.Result.Findings {
				switch f.Status {
				case core.VerifyStatusDeployedModified, core.VerifyStatusDeployedBlocked, "stale_deployment", "loader_plugin_unlinked":
					t.Errorf("verify found the deploy wrong: %+v", f)
				}
			}
			assert.Zero(t, report.Result.Issues, "%+v", report.Result.Findings)

			plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			_, err = svc.ApplyPurge(context.Background(), game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			for _, path := range []string{
				filepath.Join(install, "enbseries.ini"), filepath.Join(install, "enbseries"),
				filepath.Join(game.ModPath, "Weather.esp"), filepath.Join(game.ModPath, "textures"), filepath.Join(game.ModPath, "meshes"),
			} {
				assertAbsent(t, path, "the purge takes it down")
			}
		})
	}
}

// A mod_path folder literally named "<game root>" (#629 review) is its own
// file, never the game-root file its display spelling matches: both deploy,
// both are recorded, and a verify --fix that takes the game-root one down
// leaves the other.
func TestDeployRoot_AFolderNamedGameRootIsItsOwnFile(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkSymlink)
	seedNamedInstalledMod(t, svc, game, "src", "odd", "Odd Mod", "1.0", true, map[string][]byte{
		"d3d11.dll":             []byte("root d3d11"),
		"<game root>/d3d11.dll": []byte("data-side d3d11"),
	})
	seedProfileWithMod(t, svc, "g1", "default", "src", "odd", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "root d3d11", readFileString(t, filepath.Join(game.InstallPath, "d3d11.dll")))
	assert.Equal(t, "data-side d3d11", readFileString(t, filepath.Join(game.ModPath, "<game root>", "d3d11.dll")))

	files, err := svc.ModFiles(context.Background(), game, "default", "src", "odd")
	require.NoError(t, err)
	got := map[domain.DeployPath]bool{}
	for _, f := range files.Files {
		got[domain.DeployPath{Root: f.Root, Rel: f.Path}] = true
	}
	assert.Equal(t, map[domain.DeployPath]bool{domain.GameRootFile("d3d11.dll"): true, domain.ModPathFile("<game root>/d3d11.dll"): true}, got)

	cached := svc.GetGameCache(game).GetFilePath(cache.Key{GameID: "g1", SourceID: "src", ModID: "odd", Version: "1.0"}, "d3d11.dll")
	require.NoError(t, os.Remove(cached))
	_, err = svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(game.InstallPath, "d3d11.dll"), "the game-root file no longer shipped goes")
	assert.Equal(t, "data-side d3d11", readFileString(t, filepath.Join(game.ModPath, "<game root>", "d3d11.dll")), "the folder's file stays")
}

// No result a frontend is handed carries an internal key (#629 review): no
// NUL byte anywhere in the JSON of a deploy plan and result, a verify, a
// purge plan and result, conflicts, mod files or a snapshot of a game with
// game-root files.
func TestDeployRoot_NoOutputCarriesANul(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newRootGame(t, svc, domain.LinkCopy)
	seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
	seedNamedInstalledMod(t, svc, game, "src", "enb2", "Other ENB", "1.0", true, map[string][]byte{"d3d11.dll": []byte("another d3d11")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "enb2", "1.0")
	ctx := context.Background()
	// A folder under the game root that is a link, so a deploy has a
	// refusal to report too.
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(game.InstallPath, "enbseries")))

	var docs []any
	plan, err := svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
	require.NoError(t, err)
	docs = append(docs, plan)
	res, _ := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	docs = append(docs, res)
	require.NoError(t, os.Remove(filepath.Join(game.InstallPath, "enbseries")))
	res, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	docs = append(docs, res)
	tuned := filepath.Join(game.InstallPath, "enbseries", "effect.fx")
	require.NoError(t, os.Remove(tuned))
	require.NoError(t, os.WriteFile(tuned, []byte("mine"), 0o644))
	report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	docs = append(docs, report)
	conflicts, err := svc.GetProfileConflicts(ctx, game, "default")
	require.NoError(t, err)
	docs = append(docs, conflicts)
	files, err := svc.ModFiles(ctx, game, "default", "src", "enb")
	require.NoError(t, err)
	docs = append(docs, files)
	snap, err := svc.CreateSnapshot(ctx, game, "default", "s")
	require.NoError(t, err)
	docs = append(docs, snap)
	purge, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	docs = append(docs, purge)
	purged, err := svc.ApplyPurge(ctx, game, purge, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	docs = append(docs, purged)
	for i, d := range docs {
		b, err := json.Marshal(d)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `\u0000`, "document %d: %s", i, b)
	}
	snapDoc, err := os.ReadFile(snap.Path)
	require.NoError(t, err)
	assert.NotContains(t, string(snapDoc), `\u0000`)
}

// A plain deploy after the game's adapter starts routing files to the game
// root moves them there (#629 review): the deploy's undeploy-before-redeploy
// step finds the Data/ copy by its record. Every link method.
func TestDeployRoot_APlainDeployMovesFilesAnEarlierRoutingPutInData(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			svc, _ := newOriginalsService(t)
			game := newRootGame(t, svc, method)
			game.Adapter = ""
			require.NoError(t, svc.SaveGame(context.Background(), game))
			seedNamedInstalledMod(t, svc, game, "src", "enb", "ENB", "1.0", true, enbFiles)
			seedProfileWithMod(t, svc, "g1", "default", "src", "enb", "1.0")
			_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			require.FileExists(t, filepath.Join(game.ModPath, "d3d11.dll"))

			game.Adapter = rootTestAdapterID
			require.NoError(t, svc.SaveGame(context.Background(), game))
			_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			assertAbsent(t, filepath.Join(game.ModPath, "d3d11.dll"), "the Data copy is gone")
			assertAbsent(t, filepath.Join(game.ModPath, "enbseries"), "")
			assert.Equal(t, "the ENB's d3d11", readFileString(t, filepath.Join(game.InstallPath, "d3d11.dll")))
			assert.Equal(t, "shader", readFileString(t, filepath.Join(game.InstallPath, "enbseries", "effect.fx")))
		})
	}
}
