package core_test

// #528: a game's install_path could not be changed once it was added - not
// for a path mistyped at `lmm game add`, and not after a Steam library
// moved. These pin GameEdit.InstallPath and the policy the coordinator
// approved for it:
//
//   - the value is ~-expanded and must be an existing directory;
//   - a mod_path inside the install path follows it (a relative --mod-path
//     in the same edit resolves against the NEW one), and one outside it
//     stays where it is;
//   - a CORRECTION - nothing recorded against the old directories - just
//     writes;
//   - a MOVE - the old install directory is gone, and every deployed file
//     and backed-up original is at the new one - re-roots the ledger;
//   - anything else is a GameInstallPathInUseError naming the purge.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedInstallPathGame saves game g1 installed at <root>/A with mod_path
// <install>/<modRel> (both created) and a default profile, and returns it
// with root - where a test makes the directory it moves or corrects to.
func seedInstallPathGame(t *testing.T, svc *core.Service, modRel string) (*domain.Game, string) {
	t.Helper()
	root := t.TempDir()
	install := filepath.Join(root, "A")
	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: install, ModPath: filepath.Join(install, modRel),
		SourceIDs:  map[string]string{"nexusmods": "fixturegame"},
		LinkMethod: domain.LinkSymlink,
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(t.Context(), game.ID)
	require.NoError(t, err)
	return game, root
}

func editInstallPath(svc *core.Service, path string) (*core.GameListEntry, error) {
	return svc.EditGame(context.Background(), "g1", core.GameEdit{InstallPath: &path})
}

// assertPathsOnDisk checks games.yaml and the in-memory game agree on both
// paths.
func assertPathsOnDisk(t *testing.T, svc *core.Service, install, modPath string) {
	t.Helper()
	game, err := svc.GetGame("g1")
	require.NoError(t, err)
	assert.Equal(t, install, game.InstallPath)
	assert.Equal(t, modPath, game.ModPath)
	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, install, onDisk["g1"].InstallPath)
	assert.Equal(t, modPath, onDisk["g1"].ModPath)
}

// ledgerRoots is every mod_path g1's deployed-file records name.
func ledgerRoots(t *testing.T, svc *core.Service) []string {
	t.Helper()
	rows, err := svc.QueryForTest(t.Context(), `SELECT DISTINCT COALESCE(mod_path, '') FROM deployed_files WHERE game_id = 'g1'`)
	require.NoError(t, err)
	var roots []string
	for _, r := range rows {
		roots = append(roots, r[0])
	}
	return roots
}

func TestEditGame_InstallPath_CorrectionWithNothingDeployedMovesTheModPathToo(t *testing.T) {
	svc := newGameAddService(t)
	_, root := seedInstallPathGame(t, svc, "mods")
	fixed := filepath.Join(root, "B")
	require.NoError(t, os.Mkdir(fixed, 0o755))

	entry, err := editInstallPath(svc, fixed)
	require.NoError(t, err)
	assert.Equal(t, fixed, entry.InstallPath)
	assert.Equal(t, filepath.Join(fixed, "mods"), entry.ModPath, "a mod_path inside the install path follows it")
	assertPathsOnDisk(t, svc, fixed, filepath.Join(fixed, "mods"))
}

func TestEditGame_InstallPath_ExpandsTilde(t *testing.T) {
	svc := newGameAddService(t)
	seedInstallPathGame(t, svc, "mods")
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, "games", "g1"), 0o755))

	_, err := editInstallPath(svc, " ~/games/g1 ")
	require.NoError(t, err)
	assertPathsOnDisk(t, svc, filepath.Join(home, "games", "g1"), filepath.Join(home, "games", "g1", "mods"))
}

func TestEditGame_InstallPath_MustBeAnExistingDirectory(t *testing.T) {
	svc := newGameAddService(t)
	game, root := seedInstallPathGame(t, svc, "mods")
	file := filepath.Join(root, "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	for name, value := range map[string]string{
		"empty":    "  ",
		"missing":  filepath.Join(root, "nope"),
		"a file":   file,
		"relative": "A",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := editInstallPath(svc, value)
			var spec *core.GameSpecError
			require.ErrorAs(t, err, &spec)
			assert.Equal(t, "install_path", spec.Field)
			assertPathsOnDisk(t, svc, game.InstallPath, game.ModPath)
		})
	}
}

// TestEditGame_InstallPath_ARelativeModPathInTheSameEditResolvesAgainstTheNewOne:
// "Data" typed beside the corrected install path means <new>/Data.
func TestEditGame_InstallPath_ARelativeModPathInTheSameEditResolvesAgainstTheNewOne(t *testing.T) {
	svc := newGameAddService(t)
	_, root := seedInstallPathGame(t, svc, "mods")
	fixed := filepath.Join(root, "B")
	require.NoError(t, os.Mkdir(fixed, 0o755))
	modPath := "Data"

	_, err := svc.EditGame(t.Context(), "g1", core.GameEdit{InstallPath: &fixed, ModPath: &modPath})
	require.NoError(t, err)
	assertPathsOnDisk(t, svc, fixed, filepath.Join(fixed, "Data"))
}

// TestEditGame_InstallPath_AModPathOutsideItStaysPut: a mod_path somewhere
// else (a Proton prefix, Documents) is not part of the game folder, so it
// neither moves nor stops a change while files are deployed there.
func TestEditGame_InstallPath_AModPathOutsideItStaysPut(t *testing.T) {
	svc := newGameAddService(t)
	game, root := seedInstallPathGame(t, svc, "mods")
	outside := filepath.Join(root, "Documents", "My Games", "mods")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	game.ModPath = outside
	require.NoError(t, svc.SaveGame(t.Context(), game))
	deployOneFile(t, svc, game, "default", "m1")
	fixed := filepath.Join(root, "B")
	require.NoError(t, os.Mkdir(fixed, 0o755))

	_, err := editInstallPath(svc, fixed)
	require.NoError(t, err)
	assertPathsOnDisk(t, svc, fixed, outside)
	assert.Equal(t, []string{outside}, ledgerRoots(t, svc), "the ledger is untouched")
}

// TestEditGame_InstallPath_AMoveReRootsTheLedger is the Steam library that
// moved: the old folder is gone, and the deployed files came along with
// the new one, so lmm follows them instead of stranding them.
func TestEditGame_InstallPath_AMoveReRootsTheLedger(t *testing.T) {
	svc := newGameAddService(t)
	game, root := seedInstallPathGame(t, svc, "mods")
	deployOneFile(t, svc, game, "default", "m1")
	moved := filepath.Join(root, "B")
	require.NoError(t, os.Rename(game.InstallPath, moved))

	_, err := editInstallPath(svc, moved)
	require.NoError(t, err)
	newMods := filepath.Join(moved, "mods")
	assertPathsOnDisk(t, svc, moved, newMods)
	assert.Equal(t, []string{newMods}, ledgerRoots(t, svc))

	reloaded, err := svc.GetGame("g1")
	require.NoError(t, err)
	problem, err := svc.ModPathProblem(t.Context(), reloaded)
	require.NoError(t, err)
	assert.Nil(t, problem, "nothing is stranded")

	// And lmm manages the moved files: a purge removes them from the new
	// folder.
	mods, err := svc.GetInstalledMods(t.Context(), "g1", "default")
	require.NoError(t, err)
	_, err = svc.PurgeProfile(t.Context(), reloaded, "default", mods, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(newMods, "m1.dll"))
	assert.Empty(t, ledgerRoots(t, svc))
}

// TestEditGame_InstallPath_RefusedWhileTheOldFolderStillExists: with files
// deployed and the old folder still there, the edit is not a move - the
// files are where lmm recorded them, and changing the path would strand
// them.
func TestEditGame_InstallPath_RefusedWhileTheOldFolderStillExists(t *testing.T) {
	svc := newGameAddService(t)
	game, root := seedInstallPathGame(t, svc, "mods")
	deployOneFile(t, svc, game, "default", "m1")
	other := filepath.Join(root, "B")
	require.NoError(t, os.MkdirAll(filepath.Join(other, "mods"), 0o755))

	_, err := editInstallPath(svc, other)
	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, game.InstallPath, inUse.InstallPath)
	assert.Equal(t, other, inUse.NewInstallPath)
	assert.Equal(t, filepath.Join(other, "mods"), inUse.NewModPath)
	assert.True(t, inUse.OldInstallPathExists)
	assert.Equal(t, 1, inUse.DeployedFiles)
	assert.Equal(t, []core.ProfileDeployedFiles{{Profile: "default", DeployedFiles: 1}}, inUse.Profiles)
	assert.Contains(t, err.Error(), "`lmm purge --game g1 --profile default`")
	assert.Same(t, inUse, inUse.Details())

	assertPathsOnDisk(t, svc, game.InstallPath, game.ModPath)
	assert.Equal(t, []string{game.ModPath}, ledgerRoots(t, svc), "nothing re-rooted")
}

// TestEditGame_InstallPath_AMoveMissingFilesIsRefused: the old folder is
// gone, but the new one does not hold everything lmm recorded - so it is
// not (only) a move, and re-rooting would record files that are not there.
func TestEditGame_InstallPath_AMoveMissingFilesIsRefused(t *testing.T) {
	svc := newGameAddService(t)
	game, root := seedInstallPathGame(t, svc, "mods")
	deployOneFile(t, svc, game, "default", "m1")
	deployOneFile(t, svc, game, "default", "m2")
	moved := filepath.Join(root, "B")
	require.NoError(t, os.Rename(game.InstallPath, moved))
	require.NoError(t, os.Remove(filepath.Join(moved, "mods", "m2.dll")))

	_, err := editInstallPath(svc, moved)
	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.False(t, inUse.OldInstallPathExists)
	assert.Equal(t, 2, inUse.DeployedFiles)
	assert.Equal(t, 1, inUse.MissingFiles)
	assertPathsOnDisk(t, svc, game.InstallPath, game.ModPath)
	assert.Equal(t, []string{game.ModPath}, ledgerRoots(t, svc))
}

// TestEditGame_InstallPath_AModPathThatDoesNotFollowIsAModPathMove: naming
// a mod_path elsewhere in the same edit moves the deployed files' root out
// from under them, which is the mod_path refusal's case, not a move.
func TestEditGame_InstallPath_AModPathThatDoesNotFollowIsAModPathMove(t *testing.T) {
	svc := newGameAddService(t)
	game, root := seedInstallPathGame(t, svc, "mods")
	deployOneFile(t, svc, game, "default", "m1")
	moved := filepath.Join(root, "B")
	require.NoError(t, os.Rename(game.InstallPath, moved))
	elsewhere := "Data"

	_, err := svc.EditGame(t.Context(), "g1", core.GameEdit{InstallPath: &moved, ModPath: &elsewhere})
	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, filepath.Join(moved, "Data"), inUse.NewModPath)
	assertPathsOnDisk(t, svc, game.InstallPath, game.ModPath)
}

// seedOriginalsGame is seedInstallPathGame with a stock file under the
// mod_path that a deploy replaced and a stock game.ini a profile override
// replaced - one backed-up original under each root.
func seedOriginalsGame(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, _ := newOriginalsService(t)
	game, root := seedInstallPathGame(t, svc, "mods")

	stock := filepath.Join(game.ModPath, "Data", "shipped.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(stock), 0o755))
	require.NoError(t, os.WriteFile(stock, []byte("as shipped"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(game.InstallPath, "game.ini"), []byte("shipped ini"), 0o644))

	seedNamedInstalledMod(t, svc, game, "src", "m1", "Mod One", "1.0", true,
		map[string][]byte{"Data/shipped.esp": []byte("the mod's version")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "m1", "1.0")
	profile, err := svc.NewProfileManager().Get(t.Context(), "g1", "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"game.ini": []byte("overridden ini")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	_, err = svc.DeployProfile(t.Context(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return svc, game, root
}

// TestEditGame_InstallPath_AMoveCarriesTheOriginals: lmm's backups of the
// files it replaced restore under the NEW folder after a move - the
// mod_path one through a purge, the install_path one (a profile override's)
// through the originals store.
func TestEditGame_InstallPath_AMoveCarriesTheOriginals(t *testing.T) {
	svc, game, root := seedOriginalsGame(t)
	moved := filepath.Join(root, "B")
	require.NoError(t, os.Rename(game.InstallPath, moved))

	_, err := editInstallPath(svc, moved)
	require.NoError(t, err)
	reloaded, err := svc.GetGame("g1")
	require.NoError(t, err)

	mods, err := svc.GetInstalledMods(t.Context(), "g1", "default")
	require.NoError(t, err)
	_, err = svc.PurgeProfile(t.Context(), reloaded, "default", mods, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(moved, "mods", "Data", "shipped.esp"))
	require.NoError(t, err)
	assert.Equal(t, "as shipped", string(got))

	require.NoError(t, svc.RestoreOriginalsForTest(reloaded))
	got, err = os.ReadFile(filepath.Join(moved, "game.ini"))
	require.NoError(t, err)
	assert.Equal(t, "shipped ini", string(got))
}

// TestEditGame_InstallPath_OriginalsAloneBlockACorrection: a profile
// override's backup is recorded against the install path even after every
// deployed file is purged, so a "correction" away from a folder lmm wrote
// into is refused while the old folder is there.
func TestEditGame_InstallPath_OriginalsAloneBlockACorrection(t *testing.T) {
	svc, game, root := seedOriginalsGame(t)
	mods, err := svc.GetInstalledMods(t.Context(), "g1", "default")
	require.NoError(t, err)
	_, err = svc.PurgeProfile(t.Context(), game, "default", mods, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, ledgerRoots(t, svc))
	other := filepath.Join(root, "B")
	require.NoError(t, os.Mkdir(other, 0o755))

	_, err = editInstallPath(svc, other)
	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Zero(t, inUse.DeployedFiles)
	assert.Equal(t, 1, inUse.Originals)
	assert.True(t, inUse.OldInstallPathExists)
	assertPathsOnDisk(t, svc, game.InstallPath, game.ModPath)
}

// TestEditGame_InstallPath_AMoveMissingAnOriginalsTargetIsRefused: a
// backup whose file is not at the new folder is not a moved file.
func TestEditGame_InstallPath_AMoveMissingAnOriginalsTargetIsRefused(t *testing.T) {
	svc, game, root := seedOriginalsGame(t)
	moved := filepath.Join(root, "B")
	require.NoError(t, os.Rename(game.InstallPath, moved))
	require.NoError(t, os.Remove(filepath.Join(moved, "game.ini")))

	_, err := editInstallPath(svc, moved)
	var inUse *core.GameInstallPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, 2, inUse.Originals)
	assert.Equal(t, 1, inUse.MissingOriginals)
	assertPathsOnDisk(t, svc, game.InstallPath, game.ModPath)
}
