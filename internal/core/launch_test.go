package core_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLauncher is the Launcher seam with nothing real behind it (#501): no
// test in this file starts a process. Every Run and Detach is recorded.
type fakeLauncher struct {
	path    map[string]string // LookPath answers; a missing name is not found
	env     map[string]string // lmm's own environment
	manager map[string]bool   // the user manager's variable names
	noUser  bool              // ManagerEnv fails: no user manager
	steam   *core.SteamApp    // SteamApp's answer for any install path
	runErr  error
	scans   int // SteamApp calls

	runs     [][]string // name + args of each Run
	detached [][]string // argv of each Detach
	detEnv   [][]string
	detDirs  []string
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{
		path: map[string]string{
			"systemd-run": "/usr/bin/systemd-run",
			"steam":       "/usr/bin/steam",
			"xdg-open":    "/usr/bin/xdg-open",
			"flatpak":     "/usr/bin/flatpak",
		},
		env:     map[string]string{"DISPLAY": ":0", "XDG_RUNTIME_DIR": "/run/user/1000"},
		manager: map[string]bool{},
	}
}

func (f *fakeLauncher) launcher() *core.Launcher {
	return &core.Launcher{
		LookPath: func(file string) (string, error) {
			if p, ok := f.path[file]; ok {
				return p, nil
			}
			return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
		},
		Getenv: func(key string) string { return f.env[key] },
		Environ: func() []string {
			var kv []string
			for k, v := range f.env {
				kv = append(kv, k+"="+v)
			}
			slices.Sort(kv)
			return kv
		},
		ManagerEnv: func(context.Context) (map[string]bool, error) {
			if f.noUser {
				return nil, errors.New("Failed to connect to bus")
			}
			return f.manager, nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			f.runs = append(f.runs, append([]string{name}, args...))
			return nil, f.runErr
		},
		Detach: func(argv, env []string, dir string) error {
			f.detached = append(f.detached, argv)
			f.detDirs = append(f.detDirs, dir)
			f.detEnv = append(f.detEnv, env)
			return nil
		},
		SteamApp: func(string) (core.SteamApp, bool) {
			f.scans++
			if f.steam == nil {
				return core.SteamApp{}, false
			}
			return *f.steam, true
		},
		UnitSuffix: func() string { return "abcd1234" },
	}
}

// launchFixture is a service with one game (and its "default" profile)
// and the fake launcher installed.
func launchFixture(t *testing.T, game *domain.Game) (*core.Service, *fakeLauncher) {
	t.Helper()
	svc := newFlowsTestService(t)
	if game.InstallPath == "" {
		game.InstallPath = t.TempDir()
	}
	if game.ModPath == "" {
		game.ModPath = game.InstallPath
	}
	if game.SourceIDs == nil {
		game.SourceIDs = map[string]string{"src": game.ID}
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	f := newFakeLauncher()
	svc.SetLauncher(f.launcher())
	return svc, f
}

func launchGame(id string) *domain.Game {
	return &domain.Game{ID: id, Name: "Valheim", LinkMethod: domain.LinkSymlink}
}

// The ordinary case: a Steam game, started as a transient systemd user
// service so it lives outside lmm's cgroup, with lmm's display variables
// forwarded.
func TestLaunch_SteamGameRunsUnderSystemdRun(t *testing.T) {
	svc, f := launchFixture(t, launchGame("valheim"))
	f.steam = &core.SteamApp{AppID: "892970"}

	res, err := svc.Launch(context.Background(), mustGame(t, svc, "valheim"), core.LaunchOptions{})
	require.NoError(t, err)

	require.Len(t, f.runs, 1)
	assert.Equal(t, []string{"systemd-run", "--user", "--collect", "--service-type=exec", "--unit=lmm-launch-valheim-abcd1234", "--description=lmm: Valheim",
		"--setenv=DISPLAY=:0", "--setenv=XDG_RUNTIME_DIR=/run/user/1000",
		"--", "/usr/bin/steam", "steam://rungameid/892970"}, f.runs[0])
	assert.Equal(t, "steam", res.Method)
	assert.Equal(t, "default", res.Profile)
	assert.Equal(t, "valheim", res.Game)
	assert.Equal(t, []string{"/usr/bin/steam", "steam://rungameid/892970"}, res.Argv)
	assert.Equal(t, f.runs[0], res.Command)
	assert.Equal(t, "lmm-launch-valheim-abcd1234", res.Unit)
	assert.True(t, res.Requested, "Steam only accepts the request; it does not mean the game runs")
	assert.False(t, res.DryRun)
}

func mustGame(t *testing.T, svc *core.Service, id string) *domain.Game {
	t.Helper()
	g, err := svc.GetGame(id)
	require.NoError(t, err)
	return g
}

// Every variable of the five lmm forwards is passed when lmm has it, and
// only then.
func TestLaunch_ForwardsTheSessionVariablesItHas(t *testing.T) {
	svc, f := launchFixture(t, launchGame("valheim"))
	f.steam = &core.SteamApp{AppID: "892970"}
	f.env = map[string]string{
		"DISPLAY": ":1", "WAYLAND_DISPLAY": "wayland-1", "XDG_RUNTIME_DIR": "/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus", "XDG_CURRENT_DESKTOP": "Hyprland",
		"XAUTHORITY": "/run/user/1000/xauth_x", "HOME": "/home/u", "NEXUSMODS_API_KEY": "secret",
	}

	_, err := svc.Launch(context.Background(), mustGame(t, svc, "valheim"), core.LaunchOptions{})
	require.NoError(t, err)
	require.Len(t, f.runs, 1)
	var setenv []string
	for _, a := range f.runs[0] {
		if strings.HasPrefix(a, "--setenv=") {
			setenv = append(setenv, a)
		}
	}
	assert.Equal(t, []string{
		"--setenv=DISPLAY=:1", "--setenv=WAYLAND_DISPLAY=wayland-1", "--setenv=XDG_RUNTIME_DIR=/run/user/1000",
		"--setenv=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "--setenv=XDG_CURRENT_DESKTOP=Hyprland",
		"--setenv=XAUTHORITY=/run/user/1000/xauth_x",
	}, setenv)
}

// The app id comes from launch.steam_app_id, then the steamworkshop
// source, then the Steam library scan.
func TestLaunch_SteamAppIDPrecedence(t *testing.T) {
	cases := map[string]struct {
		launch  *domain.GameLaunch
		sources map[string]string
		want    string
	}{
		"override":  {&domain.GameLaunch{SteamAppID: "1"}, map[string]string{"steamworkshop": "2"}, "1"},
		"workshop":  {nil, map[string]string{"steamworkshop": "2"}, "2"},
		"discovery": {nil, nil, "3"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := launchGame("g")
			g.Launch, g.SourceIDs = tc.launch, tc.sources
			svc, f := launchFixture(t, g)
			f.steam = &core.SteamApp{AppID: "3"}

			info, argv, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
			require.NoError(t, err)
			assert.Equal(t, core.LaunchInfo{Method: "steam"}, *info)
			assert.Equal(t, []string{"/usr/bin/steam", "steam://rungameid/" + tc.want}, argv)
		})
	}
}

// A game in the Flatpak Steam's library goes through the Flatpak; with no
// steam binary, xdg-open hands the URL to whatever handles it.
func TestLaunch_SteamClientChoice(t *testing.T) {
	t.Run("flatpak", func(t *testing.T) {
		svc, f := launchFixture(t, launchGame("g"))
		f.steam = &core.SteamApp{AppID: "7", Flatpak: true}
		_, argv, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
		require.NoError(t, err)
		assert.Equal(t, []string{"/usr/bin/flatpak", "run", "com.valvesoftware.Steam", "steam://rungameid/7"}, argv)
	})
	t.Run("xdg-open", func(t *testing.T) {
		svc, f := launchFixture(t, launchGame("g"))
		f.steam = &core.SteamApp{AppID: "7"}
		delete(f.path, "steam")
		_, argv, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
		require.NoError(t, err)
		assert.Equal(t, []string{"/usr/bin/xdg-open", "steam://rungameid/7"}, argv)
	})
	t.Run("neither", func(t *testing.T) {
		svc, f := launchFixture(t, launchGame("g"))
		f.steam = &core.SteamApp{AppID: "7"}
		delete(f.path, "steam")
		delete(f.path, "xdg-open")
		info, argv, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
		require.NoError(t, err)
		assert.Nil(t, argv)
		assert.Empty(t, info.Method)
		assert.Contains(t, info.Reason, "xdg-open")
	})
}

// launch.command wins over Steam, its placeholders are expanded, "~" in
// its program is the home directory, and the LMM_* variables a hook gets
// are set for it.
func TestLaunch_CommandFirstWithPlaceholders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	g := launchGame("valheim")
	g.SourceIDs = map[string]string{"steamworkshop": "892970"}
	g.Launch = &domain.GameLaunch{Command: []string{"~/bin/run.sh", "--profile={profile}", "{game_id}", "{install_path}", "{mod_path}", "{steam_app_id}"}}
	svc, f := launchFixture(t, g)
	game := mustGame(t, svc, "valheim")

	res, err := svc.Launch(context.Background(), game, core.LaunchOptions{})
	require.NoError(t, err)
	assert.Equal(t, "command", res.Method)
	assert.False(t, res.Requested)
	want := []string{filepath.Join(home, "bin", "run.sh"), "--profile=default", "valheim", game.InstallPath, game.ModPath, "892970"}
	assert.Equal(t, want, res.Argv)
	require.Len(t, f.runs, 1)
	run := strings.Join(f.runs[0], "\x00")
	for _, v := range []string{"LMM_GAME_ID=valheim", "LMM_GAME_PATH=" + game.InstallPath, "LMM_MOD_PATH=" + game.ModPath, "LMM_PROFILE=default"} {
		assert.Contains(t, run, "--setenv="+v)
	}
	assert.Equal(t, want, f.runs[0][len(f.runs[0])-len(want):], "the game's argv follows --")
}

// A bare program name is looked up on lmm's PATH, since the user
// manager's PATH may not be the shell's; one that is not there refuses.
func TestLaunch_CommandProgramLookup(t *testing.T) {
	g := launchGame("wow")
	g.Launch = &domain.GameLaunch{Command: []string{"lutris", "lutris:rungame/wow"}}
	svc, f := launchFixture(t, g)
	f.path["lutris"] = "/usr/bin/lutris"
	_, argv, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "wow"), "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/bin/lutris", "lutris:rungame/wow"}, argv)

	delete(f.path, "lutris")
	_, err = svc.Launch(context.Background(), mustGame(t, svc, "wow"), core.LaunchOptions{})
	var unconfigured *core.LaunchUnconfiguredError
	require.ErrorAs(t, err, &unconfigured)
	assert.Contains(t, err.Error(), "lutris")
	assert.Empty(t, f.runs)
}

// via: picks one method and skips the order; one this build does not know
// names the ones it does.
func TestLaunch_Via(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Via: "steam", Command: []string{"/bin/true"}}
	svc, f := launchFixture(t, g)
	f.steam = &core.SteamApp{AppID: "5"}
	info, _, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
	require.NoError(t, err)
	assert.Equal(t, "steam", info.Method)

	g2 := launchGame("g2")
	g2.Launch = &domain.GameLaunch{Via: "heroic"}
	svc2, _ := launchFixture(t, g2)
	info, argv, err := svc2.ResolveLaunch(context.Background(), mustGame(t, svc2, "g2"), "default")
	require.NoError(t, err)
	assert.Nil(t, argv)
	assert.Contains(t, info.Reason, "heroic")
	assert.Contains(t, info.Reason, "command, steam")
}

// {steam_app_id} with no app id to put there refuses rather than running
// the command with a hole in it.
func TestLaunch_SteamAppIDPlaceholderWithoutAnID(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Command: []string{"/bin/run", "{steam_app_id}"}}
	svc, _ := launchFixture(t, g)
	info, argv, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
	require.NoError(t, err)
	assert.Nil(t, argv)
	assert.Contains(t, info.Reason, "{steam_app_id}")
}

// Neither a command nor an app id: the refusal names launch.command.
func TestLaunch_Unconfigured(t *testing.T) {
	svc, f := launchFixture(t, launchGame("g"))
	_, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{})
	var unconfigured *core.LaunchUnconfiguredError
	require.ErrorAs(t, err, &unconfigured)
	assert.Equal(t, "g", unconfigured.GameID)
	assert.Contains(t, err.Error(), "launch.command")
	assert.Empty(t, f.runs)

	info, _, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
	require.NoError(t, err)
	assert.Empty(t, info.Method)
	assert.Equal(t, unconfigured.Reason, info.Reason)
}

// The drift check counts the mods whose enabled flag disagrees with what
// is deployed, never an external one. Without Force it refuses with the
// count as data; with Force it launches and says so.
func TestLaunch_Drift(t *testing.T) {
	svc, f := launchFixture(t, launchGame("g"))
	f.steam = &core.SteamApp{AppID: "5"}
	ctx := context.Background()
	for _, m := range []struct {
		id                          string
		enabled, deployed, external bool
	}{
		{"in-sync", true, true, false},
		{"off-in-sync", false, false, false},
		{"enabled-not-deployed", true, false, false},
		{"disabled-still-deployed", false, true, false},
		{"external", false, true, true},
	} {
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: m.id, SourceID: "src", Name: m.id, Version: "1", GameID: "g"},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
			Enabled: m.enabled, Deployed: m.deployed, External: m.external,
		}))
	}
	game := mustGame(t, svc, "g")

	_, err := svc.Launch(ctx, game, core.LaunchOptions{})
	var drift *core.LaunchDriftError
	require.ErrorAs(t, err, &drift)
	assert.Equal(t, &core.LaunchDriftError{GameID: "g", Profile: "default", Undeployed: 2, StillDeployed: 1}, drift)
	assert.False(t, drift.DeployClears, "a deploy leaves a disabled mod deployed")
	assert.Contains(t, err.Error(), "lmm profile apply --game g")
	assert.NotContains(t, err.Error(), "--deploy")
	assert.Same(t, drift, drift.Details())
	assert.Contains(t, err.Error(), "1 enabled mod(s)")
	assert.Contains(t, err.Error(), "1 disabled mod(s)")
	assert.Empty(t, f.runs)

	res, err := svc.Launch(ctx, game, core.LaunchOptions{Force: true})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Undeployed)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "1 disabled mod(s) in profile \"default\" of g are still deployed")
	assert.Len(t, f.runs, 1)
}

// Only the active profile launches: another one is refused with the
// switch hint, as `lmm deploy --profile` refuses it.
func TestLaunch_InactiveProfileRefused(t *testing.T) {
	svc, f := launchFixture(t, launchGame("g"))
	f.steam = &core.SteamApp{AppID: "5"}
	_, err := svc.NewProfileManager().Create(context.Background(), "g", "other")
	require.NoError(t, err)

	_, err = svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{Profile: "other"})
	require.ErrorIs(t, err, core.ErrProfileNotActive)
	assert.Contains(t, err.Error(), "lmm profile switch")
	assert.Empty(t, f.runs)

	res, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{Profile: "default"})
	require.NoError(t, err)
	assert.Equal(t, "default", res.Profile)
}

// No display in lmm's environment or the user manager's: the game would
// start with nowhere to draw, so lmm refuses and names the fix. One the
// manager has is enough - the service gets it from there.
func TestLaunch_Display(t *testing.T) {
	svc, f := launchFixture(t, launchGame("g"))
	f.steam = &core.SteamApp{AppID: "5"}
	f.env = map[string]string{}

	_, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{})
	var envErr *core.LaunchEnvironmentError
	require.ErrorAs(t, err, &envErr)
	assert.Contains(t, err.Error(), "dbus-update-activation-environment --systemd DISPLAY WAYLAND_DISPLAY")
	assert.Empty(t, f.runs)

	f.manager = map[string]bool{"WAYLAND_DISPLAY": true}
	_, err = svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{})
	require.NoError(t, err)
	require.Len(t, f.runs, 1)
	assert.NotContains(t, strings.Join(f.runs[0], " "), "WAYLAND_DISPLAY", "lmm forwards only what it has itself")
}

// With no systemd user manager (or no systemd-run), the CLI may start the
// game detached; lmm serve may not, since the child would sit in its
// service's cgroup.
func TestLaunch_NoSystemd(t *testing.T) {
	for name, breakIt := range map[string]func(*fakeLauncher){
		"no systemd-run": func(f *fakeLauncher) { delete(f.path, "systemd-run") },
		"no user bus":    func(f *fakeLauncher) { f.noUser = true },
	} {
		t.Run(name, func(t *testing.T) {
			svc, f := launchFixture(t, launchGame("g"))
			f.steam = &core.SteamApp{AppID: "5"}
			breakIt(f)

			_, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{})
			require.ErrorIs(t, err, core.ErrSystemdUnavailable)
			assert.Empty(t, f.runs)
			assert.Empty(t, f.detached)

			res, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{AllowDetached: true})
			require.NoError(t, err)
			assert.Empty(t, f.runs)
			require.Len(t, f.detached, 1)
			assert.Equal(t, []string{"/usr/bin/steam", "steam://rungameid/5"}, f.detached[0])
			assert.Empty(t, res.Unit)
			assert.Equal(t, res.Argv, res.Command)
		})
	}
}

// A detached command still gets the LMM_* variables, on top of lmm's own
// environment (which the real Detach inherits).
func TestLaunch_DetachedCommandEnvironment(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Command: []string{"/bin/run"}}
	svc, f := launchFixture(t, g)
	delete(f.path, "systemd-run")
	_, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{AllowDetached: true})
	require.NoError(t, err)
	require.Len(t, f.detEnv, 1)
	assert.Contains(t, f.detEnv[0], "LMM_GAME_ID=g")
	assert.Contains(t, f.detEnv[0], "LMM_PROFILE=default")
}

// A dry run resolves everything and runs nothing.
func TestLaunch_DryRun(t *testing.T) {
	svc, f := launchFixture(t, launchGame("valheim"))
	f.steam = &core.SteamApp{AppID: "892970"}

	res, err := svc.Launch(context.Background(), mustGame(t, svc, "valheim"), core.LaunchOptions{DryRun: true})
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	assert.Empty(t, f.runs)
	assert.Equal(t, "systemd-run", res.Command[0])
	assert.Equal(t, []string{"--", "/usr/bin/steam", "steam://rungameid/892970"}, res.Command[len(res.Command)-3:])
}

// A failing systemd-run is the launch failing, with what it said.
func TestLaunch_RunFailure(t *testing.T) {
	svc, f := launchFixture(t, launchGame("g"))
	f.steam = &core.SteamApp{AppID: "5"}
	f.runErr = errors.New("exit status 1: Unit already exists")
	_, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unit already exists")
	// Type=exec makes systemd-run fail when the program cannot start, so
	// the error says which program and which unit to look at.
	assert.Contains(t, err.Error(), "/usr/bin/steam")
	assert.Contains(t, err.Error(), "lmm-launch-g-abcd1234")
	assert.Contains(t, err.Error(), "journalctl --user -u lmm-launch-g-abcd1234")
}

// A game id with characters a unit name cannot carry still makes a valid
// unit name.
func TestLaunch_UnitNameIsSanitised(t *testing.T) {
	svc, f := launchFixture(t, launchGame("my game+1"))
	f.steam = &core.SteamApp{AppID: "5"}
	res, err := svc.Launch(context.Background(), mustGame(t, svc, "my game+1"), core.LaunchOptions{})
	require.NoError(t, err)
	assert.Equal(t, "lmm-launch-my_game_1-abcd1234", res.Unit)
}

// With no launcher registered there is nothing to launch with - and
// nothing is guessed: GameStatus carries no launch member at all.
func TestLaunch_NoLauncher(t *testing.T) {
	svc := newFlowsTestService(t)
	game := launchGame("g")
	game.InstallPath, game.ModPath, game.SourceIDs = t.TempDir(), t.TempDir(), map[string]string{"src": "g"}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	_, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{})
	require.ErrorIs(t, err, core.ErrLaunchUnavailable)

	st, err := svc.GameStatus(context.Background(), mustGame(t, svc, "g"))
	require.NoError(t, err)
	assert.Equal(t, core.LaunchInfo{}, st.Launch)
}

// GameStatus says how the game would launch, or why it cannot - what the
// web UI's Play button reads.
func TestGameStatus_Launch(t *testing.T) {
	svc, f := launchFixture(t, launchGame("g"))
	st, err := svc.GameStatus(context.Background(), mustGame(t, svc, "g"))
	require.NoError(t, err)
	assert.Empty(t, st.Launch.Method)
	assert.Contains(t, st.Launch.Reason, "launch.command")

	f.steam = &core.SteamApp{AppID: "5"}
	st, err = svc.GameStatus(context.Background(), mustGame(t, svc, "g"))
	require.NoError(t, err)
	assert.Equal(t, core.LaunchInfo{Method: "steam"}, st.Launch)
}

// systemd expands $VAR in a service's arguments; lmm passes the argv as
// written, so every "$" in an argument reaches systemd-run doubled. The
// program itself is not expanded - systemd-run looks it up as written, and
// a doubled "$" there is a path that does not exist - so it goes as is.
// Argv keeps the user's own spelling.
func TestLaunch_DollarSignsSurviveSystemd(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Command: []string{"/opt/$game/sh", "-c", `exec "$0" "$@"`, "/opt/$game"}}
	svc, f := launchFixture(t, g)
	res, err := svc.Launch(context.Background(), mustGame(t, svc, "g"), core.LaunchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/$game/sh", "-c", `exec "$0" "$@"`, "/opt/$game"}, res.Argv)
	require.Len(t, f.runs, 1)
	assert.Equal(t, []string{"--", "/opt/$game/sh", "-c", `exec "$$0" "$$@"`, "/opt/$$game"}, f.runs[0][len(f.runs[0])-5:])
}

// A command starts in the game's install folder - what a Wine or EXE-style
// command expects - under systemd and detached alike.
func TestLaunch_CommandWorkingDirectory(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Command: []string{"/bin/run"}}
	svc, f := launchFixture(t, g)
	game := mustGame(t, svc, "g")
	_, err := svc.Launch(context.Background(), game, core.LaunchOptions{})
	require.NoError(t, err)
	require.Len(t, f.runs, 1)
	assert.Contains(t, f.runs[0], "--working-directory="+game.InstallPath)

	delete(f.path, "systemd-run")
	_, err = svc.Launch(context.Background(), game, core.LaunchOptions{AllowDetached: true})
	require.NoError(t, err)
	assert.Equal(t, []string{game.InstallPath}, f.detDirs)
}

// The Steam library scan runs only when an answer needs it: not for a
// command that uses no app id - GameStatus resolves on every read.
func TestLaunch_SteamScanOnlyWhenNeeded(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Command: []string{"/bin/run", "{profile}"}}
	svc, f := launchFixture(t, g)
	_, _, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
	require.NoError(t, err)
	assert.Zero(t, f.scans)
}

// launchKeySource is a source whose API key can come from the environment:
// from its own variable (source.EnvKeyProvider) when key is set, as
// NexusMods' NEXUSMODS_API_KEY does, else from LMM_<ID>_API_KEY.
type launchKeySource struct{ id string }

func (s launchKeySource) ID() string    { return s.id }
func (s launchKeySource) Name() string  { return s.id }
func (launchKeySource) AuthURL() string { return "" }
func (launchKeySource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (launchKeySource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (launchKeySource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, domain.ErrModNotFound
}
func (launchKeySource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (launchKeySource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (launchKeySource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (launchKeySource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// keyedSource is a launchKeySource with its own variable.
type keyedSource struct {
	launchKeySource
	key string
}

func (s keyedSource) EnvKey() string { return s.key }

var _ source.EnvKeyProvider = keyedSource{}

// A game started detached inherits lmm's environment - all of it a game
// needs - but never an API key: not a source's own variable, and not the
// LMM_<ID>_API_KEY any source could read. The systemd path forwards only
// its session variables, so no key reaches it either.
func TestLaunch_NoCredentialReachesTheGame(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Command: []string{"/bin/run"}}
	svc, f := launchFixture(t, g)
	svc.RegisterSource(keyedSource{launchKeySource{"nexusmods"}, "NEXUSMODS_API_KEY"})
	svc.RegisterSource(keyedSource{launchKeySource{"steamworkshop"}, "STEAM_WEB_API_KEY"})
	svc.RegisterSource(launchKeySource{"plain"})
	keys := map[string]string{
		"NEXUSMODS_API_KEY":      "nx-secret",
		"STEAM_WEB_API_KEY":      "steam-secret",
		"LMM_PLAIN_API_KEY":      "plain-secret",
		"LMM_MY_SRC_API_KEY":     "unregistered-secret",
		"LMM_CURSEFORGE_API_KEY": "cf-secret",
	}
	for k, v := range keys {
		f.env[k] = v
	}
	f.env["PATH"], f.env["HOME"], f.env["LANG"] = "/usr/bin", "/home/u", "en_US.UTF-8"
	f.env["STEAM_COMPAT_DATA_PATH"] = "/home/u/compat"

	game := mustGame(t, svc, "g")
	_, err := svc.Launch(context.Background(), game, core.LaunchOptions{})
	require.NoError(t, err)
	require.Len(t, f.runs, 1)
	for _, a := range f.runs[0] {
		for k, v := range keys {
			assert.NotContains(t, a, k)
			assert.NotContains(t, a, v)
		}
	}

	delete(f.path, "systemd-run")
	_, err = svc.Launch(context.Background(), game, core.LaunchOptions{AllowDetached: true})
	require.NoError(t, err)
	require.Len(t, f.detEnv, 1)
	env := f.detEnv[0]
	for k := range keys {
		for _, kv := range env {
			assert.False(t, strings.HasPrefix(kv, k+"="), "%s reached the game", k)
		}
	}
	for _, kv := range []string{"PATH=/usr/bin", "HOME=/home/u", "LANG=en_US.UTF-8", "DISPLAY=:0",
		"STEAM_COMPAT_DATA_PATH=/home/u/compat", "LMM_GAME_ID=g", "LMM_PROFILE=default"} {
		assert.Contains(t, env, kv)
	}
}

// The drift says what clears it: a deploy for enabled mods not yet
// deployed - the only kind --deploy answers - and something else for a
// disabled mod still deployed or a mod_path that needs attention.
func TestLaunchDriftError_Remedies(t *testing.T) {
	for name, tt := range map[string]struct {
		err           core.LaunchDriftError
		want, notWant []string
	}{
		"not deployed": {
			err:     core.LaunchDriftError{GameID: "g", Profile: "p", Undeployed: 2, DeployClears: true},
			want:    []string{"2 enabled mod(s) in profile \"p\" of g are not deployed", "lmm launch --deploy"},
			notWant: []string{"profile apply", "mod-path"},
		},
		"still deployed": {
			err:     core.LaunchDriftError{GameID: "g", Profile: "p", Undeployed: 1, StillDeployed: 1},
			want:    []string{"1 disabled mod(s) in profile \"p\" of g are still deployed", "lmm profile apply --game g"},
			notWant: []string{"--deploy", "not deployed"},
		},
		"mod path": {
			err:     core.LaunchDriftError{GameID: "g", Profile: "p", ModPathError: "mod_path /x does not exist; run `lmm game edit g --mod-path <path>`"},
			want:    []string{"mod_path /x does not exist", "lmm game edit g --mod-path"},
			notWant: []string{"--deploy", "profile apply"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			msg := tt.err.Error()
			for _, w := range tt.want {
				assert.Contains(t, msg, w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, msg, w)
			}
		})
	}
}

// A drift a deploy would clear says so on the wire.
func TestLaunch_DriftADeployClears(t *testing.T) {
	svc, f := launchFixture(t, launchGame("g"))
	f.steam = &core.SteamApp{AppID: "5"}
	ctx := context.Background()
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "a", SourceID: "src", Name: "a", Version: "1", GameID: "g"},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	_, err := svc.Launch(ctx, mustGame(t, svc, "g"), core.LaunchOptions{})
	var drift *core.LaunchDriftError
	require.ErrorAs(t, err, &drift)
	assert.Equal(t, &core.LaunchDriftError{GameID: "g", Profile: "default", Undeployed: 1, DeployClears: true}, drift)
}
