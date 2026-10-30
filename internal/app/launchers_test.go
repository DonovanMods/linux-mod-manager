package app

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

// #501: until the composition root registers a launcher, core cannot
// launch anything; RegisterLaunchers is that registration. Resolving starts
// nothing, so this test runs no program.
func TestRegisterLaunchers_RegistersALauncher(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	game := &domain.Game{ID: "g", InstallPath: root, ModPath: root, Launch: &domain.GameLaunch{Command: []string{"/opt/game/run", "{game_id}"}}}

	_, _, err := svc.ResolveLaunch(context.Background(), game, "default")
	require.ErrorIs(t, err, core.ErrLaunchUnavailable)

	RegisterLaunchers(svc)
	info, argv, err := svc.ResolveLaunch(context.Background(), game, "default")
	require.NoError(t, err)
	assert.Equal(t, core.LaunchInfo{Method: "command"}, *info)
	assert.Equal(t, []string{"/opt/game/run", "g"}, argv)
}

// The launcher's Steam lookup is steam.AppIDForInstallPath: a game in a
// (sandboxed) Steam library resolves to Steam with no launch block. Which
// client opens the URL depends on what is on this machine's PATH, so the
// assertion only pins that the app id was found.
func TestRegisterLaunchers_FindsTheSteamAppID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")
	lib := filepath.Join(home, ".local", "share", "Steam", "steamapps")
	install := filepath.Join(lib, "common", "Valheim")
	require.NoError(t, os.MkdirAll(install, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lib, "appmanifest_892970.acf"),
		[]byte("\"AppState\"\n{\n\t\"appid\"\t\t\"892970\"\n\t\"installdir\"\t\t\"Valheim\"\n}\n"), 0o644))

	svc := newTestService(t)
	RegisterLaunchers(svc)
	info, argv, err := svc.ResolveLaunch(context.Background(), &domain.Game{ID: "valheim", InstallPath: install, ModPath: install}, "default")
	require.NoError(t, err)
	if info.Method == "steam" {
		assert.Equal(t, "steam://rungameid/892970", argv[len(argv)-1])
	} else {
		assert.True(t, strings.Contains(info.Reason, "892970"), info.Reason)
	}

	// The lookup is cached, but a game Steam installs afterwards is found.
	icarus := filepath.Join(lib, "common", "Icarus")
	require.NoError(t, os.MkdirAll(icarus, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lib, "appmanifest_1149460.acf"),
		[]byte("\"AppState\"\n{\n\t\"appid\"\t\t\"1149460\"\n\t\"installdir\"\t\t\"Icarus\"\n}\n"), 0o644))
	info, argv, err = svc.ResolveLaunch(context.Background(), &domain.Game{ID: "icarus", InstallPath: icarus, ModPath: icarus}, "default")
	require.NoError(t, err)
	if info.Method == "steam" {
		assert.Equal(t, "steam://rungameid/1149460", argv[len(argv)-1])
	} else {
		assert.True(t, strings.Contains(info.Reason, "1149460"), info.Reason)
	}
}

// #501: no built-in source's API key variable reaches a game Open's
// Service starts detached. The launcher is a fake - nothing starts.
func TestOpen_LaunchCarriesNoBuiltinSourceKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	svc, err := Open(t.Context(), Options{ConfigDir: t.TempDir(), DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	var keys []string
	for _, src := range svc.ListSources() {
		keys = append(keys, EnvKeyFor(src))
	}
	require.Contains(t, keys, "NEXUSMODS_API_KEY")
	environ := []string{"PATH=/usr/bin", "DISPLAY=:0"}
	for _, k := range keys {
		environ = append(environ, k+"=secret")
	}
	var got []string
	svc.SetLauncher(&core.Launcher{
		LookPath: func(file string) (string, error) {
			if file == "systemd-run" {
				return "", os.ErrNotExist
			}
			return "/usr/bin/" + file, nil
		},
		Getenv: func(k string) string {
			if k == "DISPLAY" {
				return ":0"
			}
			return ""
		},
		Environ:    func() []string { return environ },
		ManagerEnv: func(context.Context) (map[string]bool, error) { return nil, os.ErrNotExist },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("systemd-run is not on this fake PATH")
			return nil, nil
		},
		Detach:     func(_, env []string, _ string) error { got = env; return nil },
		UnitSuffix: func() string { return "x" },
	})
	root := t.TempDir()
	game := &domain.Game{ID: "g", Name: "G", InstallPath: root, ModPath: root, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"nexusmods": "g"}, Launch: &domain.GameLaunch{Command: []string{"/opt/game/run"}}}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), "g", "default")
	require.NoError(t, err)

	_, err = svc.Launch(t.Context(), game, core.LaunchOptions{AllowDetached: true})
	require.NoError(t, err)
	assert.Contains(t, got, "PATH=/usr/bin")
	for _, kv := range got {
		assert.NotContains(t, kv, "secret")
	}
}

// Open registers the launcher, so both frontends can launch.
func TestOpen_RegistersALauncher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	svc, err := Open(t.Context(), Options{ConfigDir: t.TempDir(), DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	root := t.TempDir()
	_, _, err = svc.ResolveLaunch(context.Background(), &domain.Game{ID: "g", InstallPath: root, ModPath: root}, "default")
	require.NoError(t, err)
}
