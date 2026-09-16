package core_test

// #427: a game whose mod_path no longer exists was shown by `game show` and
// `status` with no warning, `lmm import` just failed, and the only repair
// was a hand edit of games.yaml. These pin the flag on every game document
// and the command that repairs it (SetGameModPath, `lmm game edit
// --mod-path`, PUT /api/v1/games/{id}'s "mod_path").

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

// seedModPathGame saves a game whose mod_path is <install>/mods, the
// directory `lmm game add` defaults to, created only when exists is true.
func seedModPathGame(t *testing.T, svc *core.Service, exists bool) *domain.Game {
	t.Helper()
	root := t.TempDir()
	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: root, ModPath: filepath.Join(root, "mods"),
		SourceIDs: map[string]string{"nexusmods": "fixturegame"},
	}
	if exists {
		require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(t.Context(), game.ID)
	require.NoError(t, err)
	return game
}

// deployOneFile installs one mod file into profile's deployment of game -
// a real deployed_files row, relative to the game's current mod_path.
func deployOneFile(t *testing.T, svc *core.Service, game *domain.Game, profile, modID string) {
	t.Helper()
	seedInstalledModUnderProfile(t, svc, game, profile, "src", modID, "Mod "+modID, "1.0", true,
		map[string][]byte{modID + ".dll": []byte("x")})
	require.NoError(t, svc.GetInstallerForTest(game).Install(context.Background(), game,
		&domain.Mod{ID: modID, SourceID: "src", Version: "1.0", GameID: game.ID}, profile))
}

// seedStaleModPathGame is the owner's #427 case: an install that still
// exists, and a mod_path under it that lmm deployed into and that has since
// gone - so lmm's own records point at files that are not there.
func seedStaleModPathGame(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()
	game := seedModPathGame(t, svc, true)
	deployOneFile(t, svc, game, "default", "m1")
	require.NoError(t, os.RemoveAll(game.ModPath))
	return game
}

func modPathProblem(t *testing.T, svc *core.Service, game *domain.Game) *core.ModPathMissingError {
	t.Helper()
	problem, err := svc.ModPathProblem(t.Context(), game)
	require.NoError(t, err)
	return problem
}

func TestGameDocuments_FlagAMissingModPath(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedStaleModPathGame(t, svc)

	entries, err := svc.ListGameEntries(t.Context())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	flag := entries[0].ModPathError
	assert.Contains(t, flag, game.ModPath+" does not exist, but lmm recorded 1 deployed file(s) under it")
	assert.Contains(t, flag, "lmm deploy --game g1", "the flag names the command that puts the files back")
	assert.Contains(t, flag, "lmm game edit g1 --mod-path <path>", "and the one that points lmm elsewhere")

	detail, err := svc.GameDetail(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, flag, detail.ModPathError, "game show carries the same sentence")

	status, err := svc.GameStatus(t.Context(), game)
	require.NoError(t, err)
	assert.Equal(t, flag, status.ModPathError, "status --game carries the same sentence")

	report, err := svc.Status(t.Context())
	require.NoError(t, err)
	require.Len(t, report.Games, 1)
	assert.Equal(t, flag, report.Games[0].ModPathError, "the status summary carries the same sentence")
}

func TestGameDocuments_SayNothingAboutAModPathThatExists(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, true)

	entries, err := svc.ListGameEntries(t.Context())
	require.NoError(t, err)
	assert.Empty(t, entries[0].ModPathError)
	assert.Nil(t, modPathProblem(t, svc, game))
}

// TestGameDocuments_AFreshGameIsNotFlagged (#427 review F3): a game nobody
// has deployed to yet has no mod directory - `lmm game add` and detection
// both leave it to the first deploy, which creates it - so every freshly
// added or detected game read as broken until then. Nothing is wrong with
// it, and no document says otherwise.
func TestGameDocuments_AFreshGameIsNotFlagged(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, false)
	// A deeper mod_path, as the curated UE entries have
	// (Meteorite/Content/Paks/~mods): only the install path exists.
	deep := *game
	deep.ID, deep.ModPath = "g2", filepath.Join(game.InstallPath, "Game", "Content", "Paks", "~mods")
	require.NoError(t, svc.SaveGame(t.Context(), &deep))

	assert.Nil(t, modPathProblem(t, svc, game))
	assert.Nil(t, modPathProblem(t, svc, &deep))

	entries, err := svc.ListGameEntries(t.Context())
	require.NoError(t, err)
	for _, entry := range entries {
		assert.Empty(t, entry.ModPathError, entry.ID)
	}
	detail, err := svc.GameDetail(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Empty(t, detail.ModPathError)
	status, err := svc.GameStatus(t.Context(), game)
	require.NoError(t, err)
	assert.Empty(t, status.ModPathError)
	report, err := svc.Status(t.Context())
	require.NoError(t, err)
	for _, summary := range report.Games {
		assert.Empty(t, summary.ModPathError, summary.Game.ID)
	}

	listing, err := svc.GameDetectListing(t.Context(), []domain.DetectedGame{{
		Slug: game.ID, Name: game.Name, InstallPath: game.InstallPath, ModPath: game.ModPath, Known: true,
	}}, nil, core.GameDetectListingOptions{})
	require.NoError(t, err)
	require.Len(t, listing.Games, 1)
	assert.True(t, listing.Games[0].AlreadyConfigured)
	assert.Empty(t, listing.Games[0].ModPathError, "a configured game exactly as the catalog says is not 'needs repair'")
}

// TestModPathProblem_ADeployCannotCreateIt: an absent mod_path is only the
// ordinary pre-deploy state when a deploy could create it - inside an
// install that is still there.
func TestModPathProblem_ADeployCannotCreateIt(t *testing.T) {
	svc := newFlowsTestService(t)

	t.Run("the install path is gone", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "Game")
		game := &domain.Game{ID: "g1", InstallPath: root, ModPath: filepath.Join(root, "mods")}
		problem := modPathProblem(t, svc, game)
		require.NotNil(t, problem)
		assert.True(t, problem.InstallPathMissing)
		assert.Equal(t, root, problem.InstallPath)
		assert.Contains(t, problem.Error(), "mod_path "+game.ModPath+" does not exist, and neither does the install path "+root)
	})

	t.Run("the mod_path is outside the install path", func(t *testing.T) {
		root := t.TempDir()
		elsewhere := filepath.Join(t.TempDir(), "old-library", "Game", "mods")
		game := &domain.Game{ID: "g1", InstallPath: root, ModPath: elsewhere}
		problem := modPathProblem(t, svc, game)
		require.NotNil(t, problem)
		assert.True(t, problem.OutsideInstallPath)
		assert.Contains(t, problem.Error(), "does not exist, and is outside the install path "+root)
		assert.Contains(t, problem.Error(), "lmm game edit g1 --mod-path <path>")
	})

	t.Run("a symlinked spelling of the install path is still inside it", func(t *testing.T) {
		root := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		require.NoError(t, os.Symlink(root, link))
		game := &domain.Game{ID: "g1", InstallPath: link, ModPath: filepath.Join(root, "Content", "mods")}
		assert.Nil(t, modPathProblem(t, svc, game))
	})
}

// TestModPathProblem_ABepInExGameIsPointedAtItsRoot: a BepInEx layout is
// relative to the game root, so for a game that has BepInEx the repair is
// not "some path" - it is the install path, and the flag says so exactly.
func TestModPathProblem_ABepInExGameIsPointedAtItsRoot(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedStaleModPathGame(t, svc)
	game.ID = "human-host"
	game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	deployOneFile(t, svc, game, "default", "m1")
	require.NoError(t, os.RemoveAll(game.ModPath))

	problem := modPathProblem(t, svc, game)
	require.NotNil(t, problem)
	assert.Equal(t, game.InstallPath, problem.SuggestedModPath)
	assert.Equal(t, "mod_path "+game.ModPath+" does not exist, but lmm recorded 1 deployed file(s) under it; "+
		"a BepInEx game deploys into its root: purge them, then run `lmm game edit human-host --mod-path "+game.InstallPath+
		"`, which names the purge each profile needs", problem.Error())
}

func TestModPathProblem_AFileIsNotAModPath(t *testing.T) {
	svc := newFlowsTestService(t)
	root := t.TempDir()
	file := filepath.Join(root, "mods")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	problem := modPathProblem(t, svc, &domain.Game{ID: "g1", InstallPath: root, ModPath: file})
	require.NotNil(t, problem, "a file is flagged whether or not anything was deployed")
	assert.Contains(t, problem.Error(), file+" is not a directory")
}

func TestScanLocal_AMissingModPathIsTheTypedRefusal(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, false)

	_, err := svc.ScanLocal(t.Context(), game, core.ScanOptions{ProfileName: "default"})
	var missing *core.ModPathMissingError
	require.ErrorAs(t, err, &missing, "the import error carries the repair, not just 'does not exist'")
	assert.Equal(t, game.ID, missing.GameID)
	assert.Contains(t, err.Error(), "lmm game edit g1 --mod-path")
	assert.Same(t, missing, missing.Details(), "the --json envelope and the web UI get the fields")
}

func TestSetGameModPath_RepairsTheGame(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, false)

	entry, err := svc.SetGameModPath(t.Context(), game.ID, game.InstallPath)
	require.NoError(t, err)
	assert.Equal(t, game.InstallPath, entry.ModPath)
	assert.Empty(t, entry.ModPathError, "the returned row is re-read after the write")

	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, game.InstallPath, onDisk[game.ID].ModPath)
	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, game.InstallPath, reloaded.ModPath)
	assert.Equal(t, game.SourceIDs, reloaded.SourceIDs, "only the mod_path changes")
}

// TestSetGameModPath_ResolvesLikeGameAdd: the loader's rules - "~/" is the
// home directory, a relative value is relative to install_path - and the
// value written is the resolved absolute path (#363).
func TestSetGameModPath_ResolvesLikeGameAdd(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, false)
	require.NoError(t, os.MkdirAll(filepath.Join(game.InstallPath, "Data"), 0o755))

	entry, err := svc.SetGameModPath(t.Context(), game.ID, " Data ")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(game.InstallPath, "Data"), entry.ModPath)

	home := t.TempDir()
	t.Setenv("HOME", home)
	entry, err = svc.SetGameModPath(t.Context(), game.ID, "~/mods")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "mods"), entry.ModPath)
}

func TestSetGameModPath_RefusesBadInput(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, false)
	file := filepath.Join(game.InstallPath, "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	for name, value := range map[string]string{"empty": "  ", "a file": file} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.SetGameModPath(t.Context(), game.ID, value)
			var specErr *core.GameSpecError
			require.ErrorAs(t, err, &specErr)
			assert.Equal(t, "mod_path", specErr.Field, "the wire key PUT /api/v1/games/{id} takes")

			reloaded, err := svc.GetGame(game.ID)
			require.NoError(t, err)
			assert.Equal(t, game.ModPath, reloaded.ModPath, "a refused edit writes nothing")
		})
	}

	_, err := svc.SetGameModPath(t.Context(), "no-such-game", game.InstallPath)
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
}

// TestSetGameModPath_RefusesWhileFilesAreDeployed: lmm records a deployed
// file relative to the mod_path it was deployed under, so moving the
// mod_path under a live deployment would leave every file behind, live and
// unrecorded - and a later purge would look for them in the wrong place.
// The purge has to come first, and the refusal says so.
func TestSetGameModPath_RefusesWhileFilesAreDeployed(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, true)
	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{"plugin.dll": []byte("x")})
	require.NoError(t, svc.GetInstallerForTest(game).Install(context.Background(), game,
		&domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	_, err := svc.SetGameModPath(t.Context(), game.ID, game.InstallPath)
	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, 1, inUse.DeployedFiles)
	assert.Equal(t, game.ModPath, inUse.ModPath)
	assert.Contains(t, err.Error(), "lmm purge --game g1", "the refusal names the step that clears it")
	assert.Same(t, inUse, inUse.Details())

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, game.ModPath, reloaded.ModPath)
}

// TestSetGameModPath_RefusesToCreateAnAdapterRefusal: `adapter: bepinex`
// needs the game root (#413 re-review P-b), so moving such a game's mod_path
// off the root would leave a game every flow refuses. A game that is
// already refused can still be repaired - which is the point of the edit.
func TestSetGameModPath_RefusesToCreateAnAdapterRefusal(t *testing.T) {
	svc := newFlowsTestService(t)
	root := t.TempDir()
	game := &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: root, ModPath: root, Adapter: "bepinex",
		SourceIDs: map[string]string{"nexusmods": "valheim"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	plugins := filepath.Join(root, "BepInEx", "plugins")
	require.NoError(t, os.MkdirAll(plugins, 0o755))

	_, err := svc.SetGameModPath(t.Context(), game.ID, plugins)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "mod_path", specErr.Field)
	assert.Contains(t, err.Error(), "not its install path")

	// The refused state (bepinex off the root) is exactly what the edit
	// repairs.
	refused := *game
	refused.ModPath = plugins
	require.NoError(t, svc.SaveGame(t.Context(), &refused))
	_, err = svc.AdapterFor(&refused)
	require.Error(t, err, "fixture: bepinex off the root is refused")

	entry, err := svc.SetGameModPath(t.Context(), game.ID, root)
	require.NoError(t, err)
	assert.Empty(t, entry.AdapterError)
	assert.Equal(t, "bepinex", entry.EffectiveAdapter)
}

func TestGameModPathInUseError_Wire(t *testing.T) {
	err := &core.GameModPathInUseError{GameID: "g1", ModPath: "/games/g1/mods", DeployedFiles: 3}
	assert.Equal(t, "3 file(s) are deployed under /games/g1/mods, and lmm records each one relative to the mod_path; run `lmm purge --game g1` first, then change the mod_path, then run `lmm deploy --game g1`", err.Error())
}

// TestVerify_AMissingModPathUnderDeployedModsIsAWarning: a game whose
// mod_path has gone while lmm has mods to deploy there says so - with the
// repair - rather than leaving the user to infer it from the rows the
// missing directory causes. A never-deployed game's absent directory is
// ordinary (a deploy creates it), so an empty profile says nothing.
func TestVerify_AMissingModPathUnderDeployedModsIsAWarning(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedModPathGame(t, svc, false)

	report, err := svc.VerifyReport(t.Context(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(report.Result, "mod_path_missing"), "nothing installed, nothing to say")

	// Enabled but never deployed: the first deploy creates the directory.
	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{"plugin.dll": []byte("x")})
	report, err = svc.VerifyReport(t.Context(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(report.Result, "mod_path_missing"), "%v", findingStatuses(report.Result))

	// Deployed, then the directory went away: lmm's own files are gone.
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.GetInstallerForTest(game).Install(context.Background(), game,
		&domain.Mod{ID: "m1", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))
	require.NoError(t, os.RemoveAll(game.ModPath))
	report, err = svc.VerifyReport(t.Context(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	row := findingWithStatus(report.Result, "mod_path_missing")
	require.NotNil(t, row, "%v", findingStatuses(report.Result))
	assert.Equal(t, modPathProblem(t, svc, game).Error(), row.Note)
	assert.False(t, row.Fixable)
	assert.GreaterOrEqual(t, report.Result.Warnings, 1)
}

// TestVerifyMemo_SeesTheModPathItReports (#427 review F9): the memo's
// fingerprint read an absent mod directory as an empty tree, and did not
// include the mod_path at all, so a repaired mod_path went on being
// reported from the memo - and a moved one went on reading clean.
func TestVerifyMemo_SeesTheModPathItReports(t *testing.T) {
	svc := newFlowsTestService(t)
	game := seedStaleModPathGame(t, svc)
	memo := core.VerifyOptions{Tier: core.VerifyLocal}

	report, err := svc.VerifyReport(t.Context(), game, "default", memo, nil)
	require.NoError(t, err)
	require.NotNil(t, findingWithStatus(report.Result, "mod_path_missing"), "fixture: the row is memoised")

	// Recreated by hand, empty: the tree walk sees nothing new.
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	report, err = svc.VerifyReport(t.Context(), game, "default", memo, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(report.Result, "mod_path_missing"), "the repaired mod_path is not reported from the memo")

	// The game read with another mod_path (an out-of-process games.yaml
	// edit): a different question.
	moved := *game
	moved.ModPath = filepath.Join(game.InstallPath, "elsewhere")
	report, err = svc.VerifyReport(t.Context(), &moved, "default", memo, nil)
	require.NoError(t, err)
	assert.NotNil(t, findingWithStatus(report.Result, "mod_path_missing"), "the moved mod_path is not answered from the old one's memo")
}
