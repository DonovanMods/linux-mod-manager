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
