package core_test

// #376: `lmm serve` is a long-running process over the same games.yaml the
// CLI writes. NewService loads that file once, so a game the CLI added,
// edited or removed while the server ran stayed invisible to every /api/v1
// answer - and, in the delete direction, a game the user had removed was
// still plannable and got written back by the next serve-side SaveGame.
//
// ReloadGames is the seam that closes it: a stat-gated re-read of
// games.yaml into the in-memory snapshot every other query answers from.
// These are the core half; internal/serve/api_games_reload_internal_test.go
// is the HTTP half.

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newReloadService builds a Service over a config dir the test owns, so it
// can write games.yaml behind the Service's back the way a concurrent
// `lmm game add` would.
func newReloadService(t *testing.T) (*core.Service, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir,
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, configDir
}

// writeGamesYAML replaces games.yaml wholesale, as an editor or a CLI
// write would.
func writeGamesYAML(t *testing.T, configDir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte(body), 0o644))
}

// gamesYAMLWith returns a games.yaml naming exactly the given ids, each
// with an install path that exists.
func gamesYAMLWith(t *testing.T, ids ...string) string {
	t.Helper()
	body := "games:\n"
	for _, id := range ids {
		dir := t.TempDir()
		body += "  " + id + ":\n    name: " + id + "\n    install_path: " + dir + "\n    mod_path: " + dir + "\n"
	}
	return body
}

// TestReloadGames_SeesAGameAddedBehindTheServicesBack is the issue's
// headline: a game written into games.yaml after NewService loaded it.
func TestReloadGames_SeesAGameAddedBehindTheServicesBack(t *testing.T) {
	svc, configDir := newReloadService(t)
	writeGamesYAML(t, configDir, gamesYAMLWith(t, "one"))
	reloaded, err := svc.ReloadGames()
	require.NoError(t, err)
	require.True(t, reloaded, "the first read of a games.yaml that did not exist at Open is a change")

	writeGamesYAML(t, configDir, gamesYAMLWith(t, "one", "two"))

	reloaded, err = svc.ReloadGames()
	require.NoError(t, err)
	assert.True(t, reloaded, "a games.yaml whose size/mtime moved must be re-read")

	ids := make([]string, 0, 2)
	for _, g := range svc.ListGames() {
		ids = append(ids, g.ID)
	}
	assert.Equal(t, []string{"one", "two"}, ids)

	got, err := svc.GetGame("two")
	require.NoError(t, err, "GetGame must resolve the new game too, not just ListGames")
	assert.Equal(t, "two", got.ID)
}

// TestReloadGames_DropsAGameRemovedBehindTheServicesBack is the sharper
// direction the issue names: a removed game must stop being plannable, so
// the next SaveGame cannot resurrect it.
func TestReloadGames_DropsAGameRemovedBehindTheServicesBack(t *testing.T) {
	svc, configDir := newReloadService(t)
	writeGamesYAML(t, configDir, gamesYAMLWith(t, "one", "two"))
	_, err := svc.ReloadGames()
	require.NoError(t, err)
	require.Len(t, svc.ListGames(), 2)

	writeGamesYAML(t, configDir, gamesYAMLWith(t, "one"))
	_, err = svc.ReloadGames()
	require.NoError(t, err)

	require.Len(t, svc.ListGames(), 1)
	_, err = svc.GetGame("two")
	assert.Error(t, err, "a game removed from games.yaml must no longer resolve")
}

// TestReloadGames_IsANoOpWhenTheFileHasNotMoved pins the stat gate: an
// unchanged games.yaml is not re-parsed on every request.
func TestReloadGames_IsANoOpWhenTheFileHasNotMoved(t *testing.T) {
	svc, configDir := newReloadService(t)
	writeGamesYAML(t, configDir, gamesYAMLWith(t, "one"))
	_, err := svc.ReloadGames()
	require.NoError(t, err)

	reloaded, err := svc.ReloadGames()
	require.NoError(t, err)
	assert.False(t, reloaded, "an unchanged games.yaml must not be re-read")
}

// TestReloadGames_KeepsTheLastGoodSetWhenTheFileGoesBad pins the failure
// direction: a games.yaml edited into something unparsable must not empty
// the running server's game set. The error is reported so the caller can
// log it; the snapshot is left alone.
func TestReloadGames_KeepsTheLastGoodSetWhenTheFileGoesBad(t *testing.T) {
	svc, configDir := newReloadService(t)
	writeGamesYAML(t, configDir, gamesYAMLWith(t, "one"))
	_, err := svc.ReloadGames()
	require.NoError(t, err)

	writeGamesYAML(t, configDir, "games:\n  one:\n    link_method: teleport\n")
	reloaded, err := svc.ReloadGames()
	require.Error(t, err)
	assert.False(t, reloaded)
	assert.Len(t, svc.ListGames(), 1, "the last good game set must survive a bad edit")
}

// TestReloadGames_DoesNotUndoAServeSideSave pins the interleaving that
// matters: SaveGame writes the file AND the snapshot, so a reload right
// after it must agree with what was just written rather than fight it.
func TestReloadGames_DoesNotUndoAServeSideSave(t *testing.T) {
	svc, configDir := newReloadService(t)
	writeGamesYAML(t, configDir, gamesYAMLWith(t, "one"))
	_, err := svc.ReloadGames()
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
		ID: "web", Name: "Web Added", InstallPath: dir, ModPath: dir,
		LinkMethod: domain.LinkSymlink,
	}))

	_, err = svc.ReloadGames()
	require.NoError(t, err)

	ids := make([]string, 0, 2)
	for _, g := range svc.ListGames() {
		ids = append(ids, g.ID)
	}
	assert.Equal(t, []string{"one", "web"}, ids)
}
