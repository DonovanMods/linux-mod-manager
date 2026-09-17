package core_test

// #451: a deploy now records the mod_path it deployed under, so a mod_path
// changed behind lmm's back - a games.yaml hand edit with files deployed -
// is detected: every game document and `lmm verify` say where the files
// are and how to resolve it, every deploy refuses until it is resolved (a
// deploy here would take over the records and strand the files for good),
// and `lmm purge` removes the files from where they were deployed.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handEditModPath rewrites gameID's mod_path in games.yaml the way a user
// with an editor would, and has svc read the file again.
func handEditModPath(t *testing.T, svc *core.Service, from, to string) {
	t.Helper()
	path := filepath.Join(svc.ConfigDir(), "games.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), from)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), from, to)), 0o644))
	reloaded, err := svc.ReloadGames()
	require.NoError(t, err)
	require.True(t, reloaded)
}

// movedState is ledgerState whose mod_path was then hand-edited from mods
// to mods2: mod k's two files are live under the old one.
func movedState(t *testing.T, method domain.LinkMethod) (f *legacyFixture, oldPath, newPath string) {
	t.Helper()
	f = ledgerState(t, method)
	oldPath = f.game.ModPath
	newPath = oldPath + "2"
	handEditModPath(t, f.svc, oldPath, newPath)
	game, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	require.Equal(t, newPath, game.ModPath)
	f.game = game
	return f, oldPath, newPath
}

// assertMovedSentence checks a mod_path report names both directories and
// both ways out.
func assertMovedSentence(t *testing.T, text, oldPath, newPath string) {
	t.Helper()
	assert.Contains(t, text, "2 under "+oldPath+" (profile default)")
	assert.Contains(t, text, "mod_path is now "+newPath)
	assert.Contains(t, text, "`lmm game edit sky --mod-path "+oldPath+"`")
	assert.Contains(t, text, "`lmm purge --game sky --profile default`")
}

func TestModPathMoved_EveryReportSaysWhereTheFilesAre(t *testing.T) {
	ctx := context.Background()
	f, oldPath, newPath := movedState(t, domain.LinkCopy)

	problem, err := f.svc.ModPathProblem(ctx, f.game)
	require.NoError(t, err)
	require.NotNil(t, problem)
	assert.Equal(t, []core.DeployedUnder{{ModPath: oldPath, Files: 2, Profiles: []string{"default"}}}, problem.DeployedUnder)
	assert.Equal(t, 2, problem.DeployedFiles)
	assertMovedSentence(t, problem.Error(), oldPath, newPath)

	entries, err := f.svc.ListGameEntries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assertMovedSentence(t, entries[0].ModPathError, oldPath, newPath)

	status, err := f.svc.GameStatus(ctx, f.game)
	require.NoError(t, err)
	assertMovedSentence(t, status.ModPathError, oldPath, newPath)

	report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	var row *core.VerifyFinding
	for i := range report.Result.Findings {
		if report.Result.Findings[i].Status == "mod_path_missing" {
			row = &report.Result.Findings[i]
		}
	}
	require.NotNil(t, row, "%+v", report.Result.Findings)
	assertMovedSentence(t, row.Note, oldPath, newPath)
}

func TestModPathMoved_EveryDeployRefusesAndStrandsNothing(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkSymlink} {
		t.Run(method.String(), func(t *testing.T) {
			f, oldPath, newPath := movedState(t, method)
			var moved *core.ModPathMissingError

			_, err := f.svc.PlanDeploy(ctx, f.game, "default", core.DeployOptions{})
			require.ErrorAs(t, err, &moved)
			assertMovedSentence(t, err.Error(), oldPath, newPath)

			_, err = f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
			require.ErrorAs(t, err, &moved)
			_, err = f.svc.PlanRelinkMod(ctx, f.game, "default", "local", "k", "", "")
			require.ErrorAs(t, err, &moved)
			// #451 F3: `lmm profile apply`'s Plan refuses too - it used to
			// pass clean and let its own Apply half-run instead (see
			// TestModPathMoved_ProfileApplyRefusesBeforeHalfApplying).
			_, err = f.svc.PlanProfileApply(ctx, f.game, "default")
			require.ErrorAs(t, err, &moved)

			// The installer itself refuses too, for any path that reaches it
			// without a plan.
			mod, err := f.svc.GetInstalledMod(ctx, "local", "k", "sky", "default")
			require.NoError(t, err)
			require.ErrorAs(t, f.svc.GetInstallerForTest(f.game).Install(ctx, f.game, &mod.Mod, "default"), &moved)

			// Removals run, and keep the records under the old mod_path.
			_, err = f.svc.DisableMod(ctx, f.game, "default", "local", "k")
			require.NoError(t, err)

			assert.Equal(t, "mod k", readLive(t, filepath.Join(oldPath, "Data", "k.esp")))
			assert.NoDirExists(t, filepath.Join(newPath, "Data"))
			assert.ElementsMatch(t, []string{"Data/k.esp", "Data/k2.esp"}, f.recorded(t, "default", "k"))
			problem, err := f.svc.ModPathProblem(ctx, f.game)
			require.NoError(t, err)
			require.NotNil(t, problem)
			assert.Equal(t, 2, problem.DeployedFiles)
		})
	}
}

func TestModPathMoved_PurgeRemovesTheFilesFromTheOldModPath(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink, domain.LinkSymlink} {
		t.Run(method.String(), func(t *testing.T) {
			f, oldPath, newPath := movedState(t, method)

			problem, err := f.svc.ModPathProblem(ctx, f.game)
			require.NoError(t, err)
			for _, command := range refusalCommand.FindAllStringSubmatch(problem.Error(), -1) {
				if strings.HasPrefix(command[1], "lmm purge ") {
					runRefusalCommand(t, f.svc, "sky", command[1])
				}
			}

			assert.NoFileExists(t, filepath.Join(oldPath, "Data", "k.esp"))
			assert.NoFileExists(t, filepath.Join(oldPath, "Data", "k2.esp"))
			assert.NoDirExists(t, filepath.Join(oldPath, "Data"), "the directories the purge emptied go too")
			assert.Empty(t, f.recorded(t, "default", "k"))
			problem, err = f.svc.ModPathProblem(ctx, f.game)
			require.NoError(t, err)
			assert.Nil(t, problem)

			// And the deploy it names puts the profile under the new one.
			runRefusalCommand(t, f.svc, "sky", "lmm deploy --game sky")
			assert.Equal(t, "mod k", readLive(t, filepath.Join(newPath, "Data", "k.esp")))
		})
	}
}

func TestModPathMoved_PurgeKeepsAFileTheUserChangedUnderTheOldModPath(t *testing.T) {
	ctx := context.Background()
	f, oldPath, _ := movedState(t, domain.LinkCopy)
	changed := filepath.Join(oldPath, "Data", "k.esp")
	require.NoError(t, os.WriteFile(changed, []byte("USER FILE"), 0o644))

	plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)

	assert.Equal(t, "USER FILE", readLive(t, changed))
	assert.NoFileExists(t, filepath.Join(oldPath, "Data", "k2.esp"))
	require.Len(t, result.Kept, 1)
	assert.Equal(t, core.PurgeKeptPath{Path: "Data/k.esp", Reason: core.PurgeKeptUserFile, Note: "its content changed after lmm deployed it", ModPath: oldPath}, result.Kept[0])
	problem, err := f.svc.ModPathProblem(ctx, f.game)
	require.NoError(t, err)
	assert.Nil(t, problem, "the kept file is no longer tracked")
}

func TestModPathMoved_ARecordedOnlyPurgeRemovesFromTheOldModPath(t *testing.T) {
	f, oldPath, _ := movedState(t, domain.LinkCopy)
	f.profile(t, "default", false, "k")
	f.profile(t, "other", true)

	_, result := f.purge(t, "default")

	assert.Equal(t, 2, result.RemovedPaths)
	assert.NoFileExists(t, filepath.Join(oldPath, "Data", "k.esp"))
	assert.Empty(t, f.recorded(t, "default", "k"))
}

func TestModPathMoved_SettingItBackIsAllowedAndResolvesIt(t *testing.T) {
	ctx := context.Background()
	f, oldPath, _ := movedState(t, domain.LinkCopy)

	entry, err := f.svc.EditGame(ctx, "sky", core.GameEdit{ModPath: &oldPath})
	require.NoError(t, err)
	assert.Empty(t, entry.ModPathError)
	game, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	_, err = f.svc.PlanDeploy(ctx, game, "default", core.DeployOptions{})
	require.NoError(t, err)
}

func TestModPathMoved_AMoveElsewhereIsRefusedNamingWhereTheFilesAre(t *testing.T) {
	ctx := context.Background()
	f, oldPath, _ := movedState(t, domain.LinkCopy)
	third := filepath.Join(f.game.InstallPath, "mods3")

	_, err := f.svc.EditGame(ctx, "sky", core.GameEdit{ModPath: &third})
	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, []string{oldPath}, inUse.DeployedUnder)
	assert.Equal(t, 2, inUse.DeployedFiles)
	assert.Contains(t, err.Error(), "deployed under "+oldPath+" (")

	// Nor can a whole-game save move it.
	moved := *f.game
	moved.ModPath = third
	require.ErrorAs(t, f.svc.SaveGame(ctx, &moved), &inUse)
}

// TestSaveGame_RefusesAModPathMoveUnderADeployment closes the #451 comment
// on Service.SaveGame: it moves nothing a deployment stands on.
func TestSaveGame_RefusesAModPathMoveUnderADeployment(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	moved := *f.game
	moved.ModPath = f.game.ModPath + "-elsewhere"

	err := f.svc.SaveGame(ctx, &moved)
	var inUse *core.GameModPathInUseError
	require.ErrorAs(t, err, &inUse)
	game, err := f.svc.GetGame("sky")
	require.NoError(t, err)
	assert.Equal(t, f.game.ModPath, game.ModPath)

	// A save that keeps the mod_path is not a move.
	same := *f.game
	same.Name = "Renamed"
	require.NoError(t, f.svc.SaveGame(ctx, &same))
}

// TestModPathMoved_ProfileApplyRefusesBeforeHalfApplying is #451 finding F3:
// `lmm profile apply` passed a clean Plan in the moved state and then
// half-applied it - its disable loop ran (undeploying and disabling mod k)
// and only the later enable/install loop's Installer.Install call hit the
// backstop, leaving k disabled with its files stranded rather than the
// refusal happening before anything moved. PlanProfileApply now refuses
// outright, and - the harder case - so does an ApplyProfileApply given a
// plan computed BEFORE the move: the disable loop must never run.
func TestModPathMoved_ProfileApplyRefusesBeforeHalfApplying(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	oldPath := f.game.ModPath
	newPath := oldPath + "2"

	// Drop mod k from the profile document, so the plan has something to
	// disable - the state the half-apply bug actually reached.
	f.profile(t, "default", true)

	plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err, "the plan computed before the move must still succeed")
	require.Len(t, plan.ToDisable, 1, "k is installed and enabled but no longer listed")

	handEditModPath(t, f.svc, oldPath, newPath)
	game, err := f.svc.GetGame("sky")
	require.NoError(t, err)

	var moved *core.ModPathMissingError

	// A fresh plan, computed after the move, refuses outright.
	_, err = f.svc.PlanProfileApply(ctx, game, "default")
	require.ErrorAs(t, err, &moved)

	// The plan computed BEFORE the move refuses too - before disabling
	// anything: k's row is still enabled and deployed, and its files are
	// still live under the OLD mod_path.
	result, err := f.svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.ErrorAs(t, err, &moved)
	require.NotNil(t, result)
	assert.Zero(t, result.Disabled, "the disable loop must never have run")

	mod, err := f.svc.GetInstalledMod(ctx, "local", "k", "sky", "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled, "k must still be enabled")
	assert.True(t, mod.Deployed, "k must still claim its deployment")
	assert.Equal(t, "mod k", readLive(t, filepath.Join(oldPath, "Data", "k.esp")))
	assert.ElementsMatch(t, []string{"Data/k.esp", "Data/k2.esp"}, f.recorded(t, "default", "k"))
}

// TestModPathMoved_EveryDryRunPlanRefuses is #451 finding F3's other half:
// every deploy-direction flow's Plan must refuse a moved mod_path, not just
// deploy/install/relink - a dry run must not promise what its own Apply
// would refuse. `lmm profile apply` is covered by
// TestModPathMoved_ProfileApplyRefusesBeforeHalfApplying above (and its own
// case in TestModPathMoved_EveryDeployRefusesAndStrandsNothing); this covers
// the remaining Plan functions that read a game's mod_path and preview a
// deploy: profile switch, profile import, adopt and snapshot restore.
func TestModPathMoved_EveryDryRunPlanRefuses(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		plan func(t *testing.T, f *legacyFixture) error
	}{
		{
			name: "profile switch",
			plan: func(t *testing.T, f *legacyFixture) error {
				f.profile(t, "other", false)
				_, err := f.svc.PlanProfileSwitch(ctx, f.game, "other")
				return err
			},
		},
		{
			name: "profile import",
			plan: func(t *testing.T, f *legacyFixture) error {
				_, err := f.svc.PlanImport(ctx, f.game, []byte("name: default\ngame_id: sky\nmods: []\n"))
				return err
			},
		},
		{
			name: "adopt",
			plan: func(t *testing.T, f *legacyFixture) error {
				_, err := f.svc.PlanAdopt(ctx, f.game, "default", core.AdoptOptions{SkipMatch: true})
				return err
			},
		},
		{
			name: "snapshot restore",
			plan: func(t *testing.T, f *legacyFixture) error {
				_, err := f.svc.CreateSnapshot(ctx, f.game, "default", "known-good")
				require.NoError(t, err)
				_, err = f.svc.PlanSnapshotRestore(ctx, f.game, "known-good")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, newPath := movedState(t, domain.LinkCopy)
			// The new mod_path exists (a user who repointed lmm there
			// usually made the directory too), so the only way any of these
			// can refuse is the #451 moved check - not adopt's unrelated
			// "mod_path does not exist" stat check.
			require.NoError(t, os.MkdirAll(newPath, 0o755))

			var moved *core.ModPathMissingError
			require.ErrorAs(t, tc.plan(t, f), &moved)
			assert.NotEmpty(t, moved.DeployedUnder, "must be the MOVED reason, not some other ModPathMissingError")
		})
	}
}
