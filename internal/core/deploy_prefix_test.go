package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/modtypes"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bg3Mods is where Baldur's Gate 3 loads .pak mods from, under the prefix's
// Windows user directory (#636).
const bg3Mods = "AppData/Local/Larian Studios/Baldur's Gate 3/Mods"

// newPrefixDir is a Proton-shaped prefix with a steamuser user directory.
func newPrefixDir(t *testing.T) string {
	t.Helper()
	prefix := filepath.Join(t.TempDir(), "pfx")
	require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
	return prefix
}

// newBG3Game is a Baldur's Gate 3 game (mod_path = install_path/Data) on
// the baldurs-gate-3 adapter, its prefix explicit - or none, for prefix
// "". It returns the game as GetGame hands it out: with its prefix
// resolved.
func newBG3Game(t *testing.T, svc *core.Service, method domain.LinkMethod, prefix string) *domain.Game {
	t.Helper()
	svc.RegisterAdapter(modtypes.BaldursGate3())
	install := t.TempDir()
	game := &domain.Game{
		ID: "bg3", Name: "Baldur's Gate 3", InstallPath: install, ModPath: filepath.Join(install, "Data"),
		LinkMethod: method, LinkMethodExplicit: true, Adapter: modtypes.BaldursGate3ID, Prefix: prefix,
	}
	if prefix != "" {
		game.PrefixUser = "steamuser"
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	got, err := svc.GetGame("bg3")
	require.NoError(t, err)
	return got
}

// modsDir is the game's Mods folder in prefix.
func modsDir(prefix string) string {
	return filepath.Join(prefix, "drive_c", "users", "steamuser", filepath.FromSlash(bg3Mods))
}

var bg3PakMod = map[string][]byte{
	"MyMod-1.2/MyMod.pak": []byte("the mod's pak"),
	"MyMod-1.2/info.json": []byte(`{"name":"MyMod"}`),
}

// The issue itself (#636): a .pak mod deploys into the Mods folder in the
// game's prefix, flat (its wrapper folder dropped), not under Data/; Vortex's
// info.json is not deployed; and an uninstall takes the pak away again but
// leaves the game's Mods folder - under every link method.
func TestPrefixDeploy_PakGoesToTheModsFolderInThePrefix(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			svc, _ := newOriginalsService(t)
			prefix := newPrefixDir(t)
			game := newBG3Game(t, svc, method, prefix)
			require.NotNil(t, game.PrefixRoot, "GetGame resolves the prefix for a game that deploys into it")

			files := map[string][]byte{"Public/MyLoose/x.lsx": []byte("loose")}
			for k, v := range bg3PakMod {
				files[k] = v
			}
			seedNamedInstalledMod(t, svc, game, "src", "pak", "MyMod", "1.0", true, files)
			seedProfileWithMod(t, svc, "bg3", "default", "src", "pak", "1.0")

			res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			assert.Empty(t, res.Skipped)

			pak := filepath.Join(modsDir(prefix), "MyMod.pak")
			assert.Equal(t, "the mod's pak", readFileString(t, pak), "the pak is in the Mods folder, flat")
			assert.Equal(t, "loose", readFileString(t, filepath.Join(game.ModPath, "Public", "MyLoose", "x.lsx")), "loose files stay in Data")
			assertAbsent(t, filepath.Join(game.ModPath, "MyMod-1.2"), "nothing of the pak lands under Data")
			assertAbsent(t, filepath.Join(modsDir(prefix), "MyMod-1.2"), "no folder is made inside Mods")
			assertAbsent(t, filepath.Join(modsDir(prefix), "info.json"), "Vortex's info.json is not deployed")

			joined := strings.Join(res.Warnings, "\n")
			assert.Contains(t, joined, "modsettings.lsx", "the deploy says what lmm does not do yet")

			report, err := svc.ModFiles(context.Background(), game, "default", "src", "pak")
			require.NoError(t, err)
			var roots []domain.DeployRoot
			for _, f := range report.Files {
				roots = append(roots, f.Root)
			}
			assert.Contains(t, roots, domain.RootPrefix, "lmm mod files names the prefix root")

			_, err = svc.UninstallMod(context.Background(), game, "default", "src", "pak", core.UninstallOptions{KeepCache: true})
			require.NoError(t, err)
			assertAbsent(t, pak, "uninstall removes the pak from the prefix")
			info, err := os.Stat(modsDir(prefix))
			require.NoError(t, err, "the game's Mods folder stays")
			assert.True(t, info.IsDir())
			assertAbsent(t, filepath.Join(game.ModPath, "Public"), "and the Data content goes")
		})
	}
}

// A pak of the user's own with the mod's name is kept before the mod's goes
// over it, under the originals store's prefix root, and a purge puts it back.
func TestPrefixDeploy_UsersSameNamePakIsPreservedAndPurgeRestoresIt(t *testing.T) {
	for _, method := range rootMethods {
		t.Run(method.String(), func(t *testing.T) {
			svc, dataDir := newOriginalsService(t)
			prefix := newPrefixDir(t)
			game := newBG3Game(t, svc, method, prefix)
			require.NoError(t, os.MkdirAll(modsDir(prefix), 0o755))
			users := filepath.Join(modsDir(prefix), "MyMod.pak")
			require.NoError(t, os.WriteFile(users, []byte("the user's own pak"), 0o640))

			seedNamedInstalledMod(t, svc, game, "src", "pak", "MyMod", "1.0", true, bg3PakMod)
			seedProfileWithMod(t, svc, "bg3", "default", "src", "pak", "1.0")
			_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, "the mod's pak", readFileString(t, users))

			rows := readOriginalsManifest(t, dataDir, "bg3")
			require.Len(t, rows, 1)
			assert.Equal(t, "prefix", rows[0].Root)
			assert.Equal(t, bg3Mods+"/MyMod.pak", rows[0].RelativePath)
			assert.Equal(t, "the user's own pak", readStoredOriginal(t, dataDir, "bg3", "prefix", bg3Mods+"/MyMod.pak"))

			plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			_, err = svc.ApplyPurge(context.Background(), game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)

			info, err := os.Lstat(users)
			require.NoError(t, err, "purge puts the user's pak back")
			assert.True(t, info.Mode().IsRegular())
			assert.Equal(t, "the user's own pak", readFileString(t, users))
			assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
			assert.Empty(t, readOriginalsManifest(t, dataDir, "bg3"))
		})
	}
}

// A prefix lmm cannot resolve is never replaced by another directory: the
// deploy is refused with the typed error naming what to set, and nothing
// lands under Data.
func TestPrefixDeploy_UnresolvedPrefixIsRefused(t *testing.T) {
	svc, _ := newOriginalsService(t)
	game := newBG3Game(t, svc, domain.LinkSymlink, "")
	assert.Nil(t, game.PrefixRoot)
	seedNamedInstalledMod(t, svc, game, "src", "pak", "MyMod", "1.0", true, bg3PakMod)
	seedProfileWithMod(t, svc, "bg3", "default", "src", "pak", "1.0")

	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	var refused *core.PrefixUnresolvedError
	var said []string
	if err != nil {
		said = append(said, err.Error())
		if errors.As(err, &refused) {
			said = append(said, "typed")
		}
	}
	if res != nil {
		for _, sk := range res.Skipped {
			said = append(said, sk.Reason)
		}
	}
	joined := strings.Join(said, "\n")
	assert.Contains(t, joined, "prefix", "the refusal names the prefix")
	assert.Contains(t, joined, "lmm game edit bg3 --prefix", "and how to set it")
	assertAbsent(t, filepath.Join(game.ModPath, "MyMod-1.2"), "nothing falls back to Data")
}

// Files recorded in the prefix are neither removed nor forgotten while the
// prefix does not resolve: the uninstall is refused, and once the prefix is
// back the pak goes.
func TestPrefixDeploy_RecordedFilesRefuseRemovalWhileThePrefixIsGone(t *testing.T) {
	svc, _ := newOriginalsService(t)
	prefix := newPrefixDir(t)
	game := newBG3Game(t, svc, domain.LinkCopy, prefix)
	seedNamedInstalledMod(t, svc, game, "src", "pak", "MyMod", "1.0", true, bg3PakMod)
	seedProfileWithMod(t, svc, "bg3", "default", "src", "pak", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	pak := filepath.Join(modsDir(prefix), "MyMod.pak")
	require.FileExists(t, pak)

	moved := prefix + ".away"
	require.NoError(t, os.Rename(prefix, moved))
	gone, err := svc.GetGame("bg3")
	require.NoError(t, err)
	assert.Nil(t, gone.PrefixRoot)
	_, err = svc.UninstallMod(context.Background(), gone, "default", "src", "pak", core.UninstallOptions{KeepCache: true})
	var refused *core.PrefixUnresolvedError
	require.ErrorAs(t, err, &refused)

	_, err = svc.PlanPurge(context.Background(), gone, "default", core.PurgeOptions{})
	require.ErrorAs(t, err, &refused, "a purge is refused too")

	require.NoError(t, os.Rename(moved, prefix))
	back, err := svc.GetGame("bg3")
	require.NoError(t, err)
	_, err = svc.UninstallMod(context.Background(), back, "default", "src", "pak", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	assertAbsent(t, pak, "the record was kept, so the pak is found and removed")
}

// Wine matches names case-insensitively: a Mods folder the game created as
// "mods" is the one lmm writes into, not a second "Mods" beside it.
func TestPrefixDeploy_MatchesFoldersCaseInsensitively(t *testing.T) {
	svc, _ := newOriginalsService(t)
	prefix := newPrefixDir(t)
	lower := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "larian studios", "Baldur's Gate 3", "mods")
	require.NoError(t, os.MkdirAll(lower, 0o755))
	game := newBG3Game(t, svc, domain.LinkSymlink, prefix)
	seedNamedInstalledMod(t, svc, game, "src", "pak", "MyMod", "1.0", true, bg3PakMod)
	seedProfileWithMod(t, svc, "bg3", "default", "src", "pak", "1.0")

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "the mod's pak", readFileString(t, filepath.Join(lower, "MyMod.pak")))
	assertAbsent(t, filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Larian Studios"), "no second folder")

	vr, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Zero(t, vr.Result.Issues, "verify finds the pak where it is: %+v", vr.Result.Findings)

	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "pak", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(lower, "MyMod.pak"), "and removes it there")
}

// Nothing is written out of the prefix: a folder on the way to Mods that
// is a link to somewhere else is refused.
func TestPrefixDeploy_RefusesAFolderThatLeavesThroughALink(t *testing.T) {
	svc, _ := newOriginalsService(t)
	prefix := newPrefixDir(t)
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Larian Studios")))
	game := newBG3Game(t, svc, domain.LinkSymlink, prefix)
	seedNamedInstalledMod(t, svc, game, "src", "pak", "MyMod", "1.0", true, bg3PakMod)
	seedProfileWithMod(t, svc, "bg3", "default", "src", "pak", "1.0")

	_, _ = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was written through the link")
}

// A BG3 game deployed under the generic adapter has its paks in Data/; the
// switch to baldurs-gate-3 warns that they move, and the next deploy moves
// them into the prefix with no reinstall.
func TestPrefixDeploy_SwitchingToTheAdapterMovesPaksOutOfData(t *testing.T) {
	svc, _ := newOriginalsService(t)
	prefix := newPrefixDir(t)
	svc.RegisterAdapter(modtypes.BaldursGate3())
	install := t.TempDir()
	game := &domain.Game{ID: "bg3", Name: "Baldur's Gate 3", InstallPath: install, ModPath: filepath.Join(install, "Data"),
		LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true, Prefix: prefix, PrefixUser: "steamuser"}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedNamedInstalledMod(t, svc, game, "src", "pak", "MyMod", "1.0", true, map[string][]byte{"MyMod.pak": []byte("pak")})
	seedProfileWithMod(t, svc, "bg3", "default", "src", "pak", "1.0")
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(game.ModPath, "MyMod.pak"))

	warning, err := svc.AdapterSwitchWarning(context.Background(), game, modtypes.BaldursGate3ID)
	require.NoError(t, err)
	assert.Contains(t, warning, "prefix", "the switch says the paks move")

	game.Adapter = modtypes.BaldursGate3ID
	require.NoError(t, svc.SaveGame(context.Background(), game))
	switched, err := svc.GetGame("bg3")
	require.NoError(t, err)
	_, err = svc.DeployProfile(context.Background(), switched, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(game.ModPath, "MyMod.pak"), "the pak leaves Data")
	assert.Equal(t, "pak", readFileString(t, filepath.Join(modsDir(prefix), "MyMod.pak")))

	// And back: a game switched away still has its prefix resolved while
	// files are recorded there, so the next deploy takes them out of it.
	game.Adapter = ""
	require.NoError(t, svc.SaveGame(context.Background(), game))
	back, err := svc.GetGame("bg3")
	require.NoError(t, err)
	require.NotNil(t, back.PrefixRoot, "the recorded prefix files keep the prefix resolved")
	_, err = svc.DeployProfile(context.Background(), back, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assertAbsent(t, filepath.Join(modsDir(prefix), "MyMod.pak"), "the pak leaves the prefix")
	assert.Equal(t, "pak", readFileString(t, filepath.Join(game.ModPath, "MyMod.pak")))
	gone, err := svc.GetGame("bg3")
	require.NoError(t, err)
	assert.Nil(t, gone.PrefixRoot, "with nothing left there, a generic game resolves no prefix")
}
