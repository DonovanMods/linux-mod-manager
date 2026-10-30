package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// battleNetClient is where a prefix's Battle.net client lives.
const battleNetClient = "drive_c/Program Files (x86)/Battle.net/Battle.net Launcher.exe"

// bnetPrefix makes a prefix with a Battle.net client in it. A Proton one is
// laid out as umu-launcher makes it: pfx -> . and a version file.
func bnetPrefix(t *testing.T, proton bool) string {
	t.Helper()
	prefix := filepath.Join(t.TempDir(), "battlenet")
	exe := filepath.Join(prefix, filepath.FromSlash(battleNetClient))
	require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
	require.NoError(t, os.WriteFile(exe, nil, 0o644))
	if proton {
		require.NoError(t, os.WriteFile(filepath.Join(prefix, "version"), []byte("GE-Proton11-11\n"), 0o644))
		require.NoError(t, os.Symlink(".", filepath.Join(prefix, "pfx")))
	}
	return prefix
}

// bnetFixture is launchFixture for a Battle.net game, with the battlenet
// method registered the way app.RegisterLaunchers registers it.
func bnetFixture(t *testing.T, bn *domain.BattleNetLaunch) (*core.Service, *fakeLauncher, *domain.Game) {
	t.Helper()
	g := launchGame("wow")
	g.Name = "World of Warcraft"
	g.Launch = &domain.GameLaunch{Via: domain.LaunchViaBattleNet, BattleNet: bn}
	svc, f := launchFixture(t, g)
	svc.RegisterLaunchMethod(core.BattleNetLaunchMethod())
	f.path["umu-run"] = "/usr/bin/umu-run"
	f.path["wine"] = "/usr/bin/wine"
	return svc, f, mustGame(t, svc, "wow")
}

// #545: the owner's setup. A Proton prefix runs Battle.net through umu-run,
// as omarchy-launch-battlenet does: the prefix, umu's game id, the Proton
// and the verb go into the unit's environment (systemd-run --setenv), and
// the argv asks the client to launch the game by its launch code. Nothing
// starts - the fake launcher only records.
func TestLaunch_BattleNetThroughUmu(t *testing.T) {
	prefix := bnetPrefix(t, true)
	svc, f, game := bnetFixture(t, &domain.BattleNetLaunch{Prefix: prefix, Product: "WoW", Proton: "GE-Proton"})

	res, err := svc.Launch(context.Background(), game, core.LaunchOptions{})
	require.NoError(t, err)

	exe := filepath.Join(prefix, filepath.FromSlash(battleNetClient))
	assert.Equal(t, "battlenet", res.Method)
	assert.True(t, res.Requested, "Battle.net only accepts the request")
	assert.Equal(t, []string{"/usr/bin/umu-run", exe, "--exec=launch WoW"}, res.Argv)
	require.Len(t, f.runs, 1)
	for _, kv := range []string{"WINEPREFIX=" + prefix, "GAMEID=umu-battlenet", "PROTONPATH=GE-Proton", "PROTON_VERB=run"} {
		assert.Contains(t, f.runs[0], "--setenv="+kv)
	}
	assert.Equal(t, []string{"--", "/usr/bin/umu-run", exe, "--exec=launch WoW"}, f.runs[0][len(f.runs[0])-4:])
}

// No launch code opens the client; no proton leaves umu its default.
func TestLaunch_BattleNetWithoutALaunchCodeOpensTheClient(t *testing.T) {
	prefix := bnetPrefix(t, true)
	svc, f, game := bnetFixture(t, &domain.BattleNetLaunch{Prefix: prefix})

	res, err := svc.Launch(context.Background(), game, core.LaunchOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/bin/umu-run", filepath.Join(prefix, filepath.FromSlash(battleNetClient))}, res.Argv)
	assert.NotContains(t, res.Command, "--setenv=PROTONPATH=")
	assert.Empty(t, f.runs, "a dry run starts nothing")
}

// A Proton prefix without umu-run is refused, saying why - never run with
// a plain wine, which would rewrite a Proton prefix. runner: wine, set by
// the user, is how they ask for that.
func TestLaunch_BattleNetProtonPrefixNeedsUmu(t *testing.T) {
	prefix := bnetPrefix(t, true)
	svc, f, game := bnetFixture(t, &domain.BattleNetLaunch{Prefix: prefix})
	delete(f.path, "umu-run")

	_, err := svc.Launch(context.Background(), game, core.LaunchOptions{})
	var unconfigured *core.LaunchUnconfiguredError
	require.ErrorAs(t, err, &unconfigured)
	assert.Contains(t, unconfigured.Reason, "umu-run")
	assert.Empty(t, f.runs)

	svc, f, game = bnetFixture(t, &domain.BattleNetLaunch{Prefix: prefix, Runner: domain.BattleNetRunnerWine})
	delete(f.path, "umu-run")
	res, err := svc.Launch(context.Background(), game, core.LaunchOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/wine", res.Argv[0])
}

// A plain Wine prefix runs with wine and WINEPREFIX alone.
func TestLaunch_BattleNetThroughWine(t *testing.T) {
	prefix := bnetPrefix(t, false)
	svc, f, game := bnetFixture(t, &domain.BattleNetLaunch{Prefix: prefix})

	res, err := svc.Launch(context.Background(), game, core.LaunchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/bin/wine", filepath.Join(prefix, filepath.FromSlash(battleNetClient))}, res.Argv)
	require.Len(t, f.runs, 1)
	assert.Contains(t, f.runs[0], "--setenv=WINEPREFIX="+prefix)
	assert.NotContains(t, f.runs[0], "--setenv=GAMEID=umu-battlenet")

	delete(f.path, "wine")
	_, err = svc.Launch(context.Background(), game, core.LaunchOptions{})
	var unconfigured *core.LaunchUnconfiguredError
	require.ErrorAs(t, err, &unconfigured)
	assert.Contains(t, unconfigured.Reason, "wine")
}

// Steam's compatdata/<id>/pfx: umu's WINEPREFIX is the compatdata folder,
// the one holding pfx and version.
func TestLaunch_BattleNetInASteamCompatdataPrefix(t *testing.T) {
	compat := filepath.Join(t.TempDir(), "compatdata", "2000000001")
	prefix := filepath.Join(compat, "pfx")
	exe := filepath.Join(prefix, filepath.FromSlash(battleNetClient))
	require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
	require.NoError(t, os.WriteFile(exe, nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(compat, "version"), []byte("9.0-300\n"), 0o644))
	svc, _, game := bnetFixture(t, &domain.BattleNetLaunch{Prefix: prefix})

	res, err := svc.Launch(context.Background(), game, core.LaunchOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/umu-run", res.Argv[0])
	assert.Contains(t, res.Command, "--setenv=WINEPREFIX="+compat)
}

// A prefix with no Battle.net client in it says where it looked; a ~/
// prefix is the user's home.
func TestLaunch_BattleNetClientMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc, _, game := bnetFixture(t, &domain.BattleNetLaunch{Prefix: "~/Games/none"})

	info, argv, err := svc.ResolveLaunch(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Nil(t, argv)
	assert.Contains(t, info.Reason, filepath.Join(home, "Games", "none"))
	assert.Contains(t, info.Reason, "Battle.net")
}

// Registering a method again replaces it, so a Service the composition root
// registers twice still names each method once.
func TestRegisterLaunchMethod_ReplacesByName(t *testing.T) {
	g := launchGame("g")
	g.Launch = &domain.GameLaunch{Via: "nope"}
	svc, _ := launchFixture(t, g)
	svc.RegisterLaunchMethod(core.BattleNetLaunchMethod())
	svc.RegisterLaunchMethod(core.BattleNetLaunchMethod())

	info, _, err := svc.ResolveLaunch(context.Background(), mustGame(t, svc, "g"), "default")
	require.NoError(t, err)
	assert.Contains(t, info.Reason, "(known: battlenet, command, steam)")
}
