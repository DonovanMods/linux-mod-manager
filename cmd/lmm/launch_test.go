package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliLauncher is core's Launcher seam with nothing real behind it (#501):
// no test here starts a program. runs records each systemd-run.
type cliLauncher struct {
	runs    [][]string
	noSteam bool // neither steam nor xdg-open: the game cannot launch
}

func (f *cliLauncher) launcher() *core.Launcher {
	paths := map[string]string{"systemd-run": "/usr/bin/systemd-run", "steam": "/usr/bin/steam"}
	if f.noSteam {
		delete(paths, "steam")
	}
	env := map[string]string{"DISPLAY": ":0"}
	return &core.Launcher{
		LookPath: func(file string) (string, error) {
			if p, ok := paths[file]; ok {
				return p, nil
			}
			return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
		},
		Getenv:     func(k string) string { return env[k] },
		Environ:    func() []string { return []string{"DISPLAY=:0"} },
		ManagerEnv: func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			f.runs = append(f.runs, append([]string{name}, args...))
			return nil, nil
		},
		Detach:     func([]string, []string, string) error { return errors.New("detach not expected") },
		SteamApp:   func(string) (core.SteamApp, bool) { return core.SteamApp{AppID: "892970"}, true },
		UnitSuffix: func() string { return "abcd1234" },
	}
}

// setupLaunchTest is setupDoDeployTest with the fake launcher installed and
// launch's flags reset.
func setupLaunchTest(t *testing.T) (*core.Service, *domain.Game, *cliLauncher) {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	game.Name = "Valheim"
	game.InstallPath = filepath.Dir(game.ModPath)
	f := &cliLauncher{}
	svc.SetLauncher(f.launcher())

	oldProfile, oldDeploy, oldStrict, oldDryRun := launchProfile, launchDeploy, launchStrict, launchDryRun
	launchProfile, launchDeploy, launchStrict, launchDryRun = "", false, false, false
	t.Cleanup(func() {
		launchProfile, launchDeploy, launchStrict, launchDryRun = oldProfile, oldDeploy, oldStrict, oldDryRun
	})
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return svc, game, f
}

func TestLaunchCmd_Structure(t *testing.T) {
	assert.Equal(t, "launch", launchCmd.Use)
	for _, name := range []string{"profile", "deploy", "strict", "dry-run"} {
		assert.NotNil(t, launchCmd.Flags().Lookup(name), name)
	}
	found := false
	for _, c := range rootCmd.Commands() {
		found = found || c == launchCmd
	}
	assert.True(t, found, "launch is a top-level command")
}

func TestDoLaunch_StartsTheGame(t *testing.T) {
	svc, game, f := setupLaunchTest(t)

	out := captureStdout(t, func() error { return doLaunch(context.Background(), svc, game) })
	require.Len(t, f.runs, 1)
	assert.Equal(t, "systemd-run", f.runs[0][0])
	assert.Contains(t, out, "Asked Steam to start Valheim (profile default).")
	assert.Contains(t, out, "lmm-launch-g1-abcd1234")
}

func TestDoLaunch_DryRunPrintsTheCommandAndRunsNothing(t *testing.T) {
	svc, game, f := setupLaunchTest(t)
	launchDryRun = true

	out := captureStdout(t, func() error { return doLaunch(context.Background(), svc, game) })
	assert.Empty(t, f.runs)
	assert.Contains(t, out, "Would launch Valheim (profile default) via steam:\n")
	assert.Contains(t, out, "systemd-run --user --collect '--service-type=exec' '--unit=lmm-launch-g1-abcd1234' '--description=lmm: Valheim' '--setenv=DISPLAY=:0' -- /usr/bin/steam 'steam://rungameid/892970'\n")
}

// Undeployed changes warn and launch; --strict refuses with the drift as
// the error.
func TestDoLaunch_UndeployedChanges(t *testing.T) {
	svc, game, f := setupLaunchTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	stdout, stderr, err := captureStdoutAndStderr(t, func() error { return doLaunch(context.Background(), svc, game) })
	require.NoError(t, err)
	assert.Contains(t, stderr, "Warning: launching anyway: 1 enabled mod(s) in profile \"default\" of g1 are not deployed; deploy first (`lmm launch --deploy`)")
	assert.Contains(t, stdout, "Asked Steam to start")
	assert.Len(t, f.runs, 1)

	launchStrict = true
	err = doLaunch(context.Background(), svc, game)
	var drift *core.LaunchDriftError
	require.ErrorAs(t, err, &drift)
	assert.Equal(t, 1, drift.Undeployed)
	assert.Len(t, f.runs, 1, "--strict starts nothing")
}

// --deploy deploys before the launch, so even --strict then has nothing to
// refuse.
func TestDoLaunch_DeployFirst(t *testing.T) {
	svc, game, f := setupLaunchTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	launchDeploy, launchStrict = true, true

	out := captureStdout(t, func() error { return doLaunch(context.Background(), svc, game) })
	assert.Contains(t, out, "Deployed 1 mod(s) to profile default.")
	_, err := os.Lstat(filepath.Join(game.ModPath, "a.esp"))
	require.NoError(t, err, "the mod is deployed before the launch")
	assert.Len(t, f.runs, 1)
}

// --deploy checks the launch first: a game that cannot launch is not
// deployed for nothing.
func TestDoLaunch_DeployIsSkippedWhenTheLaunchCannotHappen(t *testing.T) {
	svc, game, f := setupLaunchTest(t)
	f.noSteam = true
	svc.SetLauncher(f.launcher())
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	launchDeploy = true

	err := doLaunch(context.Background(), svc, game)
	var unconfigured *core.LaunchUnconfiguredError
	require.ErrorAs(t, err, &unconfigured)
	_, statErr := os.Lstat(filepath.Join(game.ModPath, "a.esp"))
	assert.True(t, os.IsNotExist(statErr), "nothing was deployed")
	assert.Empty(t, f.runs)
}

// --deploy --strict refuses BEFORE deploying when the drift is one a
// deploy cannot clear - a disabled mod still deployed - and the refusal
// names the fix that does, not --deploy.
func TestDoLaunch_DeployStrictRefusesDriftADeployCannotClear(t *testing.T) {
	svc, game, f := setupLaunchTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "off", SourceID: "src", Name: "Off", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: false, Deployed: true,
	}))
	launchDeploy, launchStrict = true, true

	var err error
	out := captureStdout(t, func() error { err = doLaunch(context.Background(), svc, game); return nil })
	var drift *core.LaunchDriftError
	require.ErrorAs(t, err, &drift)
	assert.False(t, drift.DeployClears)
	assert.Equal(t, 1, drift.StillDeployed)
	assert.Contains(t, err.Error(), "lmm profile apply --game g1")
	assert.NotContains(t, err.Error(), "--deploy")
	assert.NotContains(t, out, "Deployed")
	_, statErr := os.Lstat(filepath.Join(game.ModPath, "a.esp"))
	assert.True(t, os.IsNotExist(statErr), "nothing was deployed")
	assert.Empty(t, f.runs)
}

// A dry run with --deploy previews the launch the deploy makes possible,
// so --strict has no drift to refuse.
func TestDoLaunch_DeployDryRunStrict(t *testing.T) {
	svc, game, f := setupLaunchTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	launchDeploy, launchStrict, launchDryRun = true, true, true

	out := captureStdout(t, func() error { return doLaunch(context.Background(), svc, game) })
	assert.Contains(t, out, "Would deploy the profile first.")
	assert.Contains(t, out, "Would launch Valheim")
	assert.Empty(t, f.runs)
}

// Only the active profile launches.
func TestDoLaunch_InactiveProfile(t *testing.T) {
	svc, game, f := setupLaunchTest(t)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "other")
	require.NoError(t, err)
	launchProfile = "other"

	err = doLaunch(context.Background(), svc, game)
	require.ErrorIs(t, err, core.ErrProfileNotActive)
	assert.Empty(t, f.runs)
}

// `lmm launch --json`'s documents: a launch, and a dry run.
func TestJSONGolden_Launch(t *testing.T) {
	t.Run("launch", func(t *testing.T) {
		svc, game, _ := setupLaunchTest(t)
		withJSONOutput(t)
		out := captureStdout(t, func() error { return doLaunch(context.Background(), svc, game) })
		assertJSONCLIGolden(t, "launch", out)
	})
	t.Run("dry_run", func(t *testing.T) {
		svc, game, _ := setupLaunchTest(t)
		withJSONOutput(t)
		launchDryRun = true
		out := captureStdout(t, func() error { return doLaunch(context.Background(), svc, game) })
		assertJSONCLIGolden(t, "launch_dry_run", out)
	})
}

// The drift refusal's counts reach the --json envelope as data (#501).
func TestReportError_JSON_LaunchDriftError(t *testing.T) {
	withJSONOutput(t)

	err := &core.LaunchDriftError{GameID: "valheim", Profile: "default", Undeployed: 2, DeployClears: true}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"2 enabled mod(s) in profile \\\"default\\\" of valheim are not deployed; deploy first (`lmm launch --deploy`); or launch anyway\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"valheim\",\n"+
		"    \"profile\": \"default\",\n"+
		"    \"undeployed\": 2,\n"+
		"    \"deploy_clears\": true\n"+
		"  }\n"+
		"}\n", out)
}
