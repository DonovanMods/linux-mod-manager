package serve

// #376's HTTP half: `lmm serve` outlives the games.yaml load NewService
// performs, so every request has to pick up what the CLI wrote in another
// process. The core seam is core.Service.ReloadGames (stat-gated re-read);
// wrap calls it once per request, which is what these assert against the
// real handlers.

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newReloadServer builds a Server over a Service with one game, and hands
// back the config dir so a test can edit games.yaml behind the server's
// back exactly as a concurrent `lmm game add`/`lmm game edit` would.
func newReloadServer(t *testing.T) (*Server, string) {
	t.Helper()
	sandboxEnv(t)
	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	dir := t.TempDir()
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "g1", Name: "Fixture Game", InstallPath: dir, ModPath: dir,
		LinkMethod: domain.LinkSymlink,
	}))
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), configDir
}

// gameIDsFromListing decodes GET /api/v1/games into its ids.
func gameIDsFromListing(t *testing.T, body []byte) []string {
	t.Helper()
	var entries []core.GameListEntry
	require.NoError(t, json.Unmarshal(body, &entries))
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// TestAPIGames_SeesAGameTheCLIAddedWhileServeRan is the issue's own
// reproduction: `lmm game add` writes games.yaml in another process, and
// GET /api/v1/games used to keep answering the startup snapshot forever.
func TestAPIGames_SeesAGameTheCLIAddedWhileServeRan(t *testing.T) {
	s, configDir := newReloadServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"g1"}, gameIDsFromListing(t, rec.Body.Bytes()))

	appendGameToYAML(t, configDir, "testgame", t.TempDir())

	rec = doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"g1", "testgame"}, gameIDsFromListing(t, rec.Body.Bytes()),
		"a game added behind the server's back must appear without a restart")
}

// TestAPIStatus_SeesASourceMapTheCLIEdited is the edit direction: `lmm
// game edit --source` rewrites the same file, and a scoped endpoint used
// to answer with the source map it loaded at startup.
func TestAPIStatus_SeesASourceMapTheCLIEdited(t *testing.T) {
	s, configDir := newReloadServer(t)

	game, err := s.svc.GetGame("g1")
	require.NoError(t, err)
	require.Empty(t, game.SourceIDs)

	writeGameWithSource(t, configDir, "g1", game.InstallPath, "curseforge", "skyrimspecialedition")

	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var entries []core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, map[string]string{"curseforge": "skyrimspecialedition"}, entries[0].SourceIDs,
		"an edited source map must be visible without a restart")
}

// TestAPIGames_DropsAGameRemovedFromGamesYAML is the delete direction the
// issue calls sharper: a game the user removed must stop being offered,
// so a later serve-side write cannot resurrect it.
func TestAPIGames_DropsAGameRemovedFromGamesYAML(t *testing.T) {
	s, configDir := newReloadServer(t)

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte("games: {}\n"), 0o644))

	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String(), "a removed game must not survive in the served snapshot")
}

// TestAPIGames_SurvivesAGamesYAMLEditedIntoGarbage pins the failure
// direction: a bad hand-edit mid-session must not empty the chooser.
func TestAPIGames_SurvivesAGamesYAMLEditedIntoGarbage(t *testing.T) {
	s, configDir := newReloadServer(t)

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"),
		[]byte("games:\n  g1:\n    link_method: teleport\n"), 0o644))

	rec := doAPI(s, http.MethodGet, "/api/v1/games", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"g1"}, gameIDsFromListing(t, rec.Body.Bytes()),
		"the last good game set must still be served")
}

// appendGameToYAML adds one game to games.yaml the way a second process
// would: read, append, write. The indent is taken from the block the file
// already has rather than assumed - what lmm writes is four spaces deep,
// and a two-space guess silently produces a parse error instead of a new
// game.
func appendGameToYAML(t *testing.T, configDir, id, dir string) {
	t.Helper()
	path := filepath.Join(configDir, "games.yaml")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	indent := gameBlockIndent(t, string(body))
	entry := indent + id + ":\n" +
		indent + indent + "name: " + id + "\n" +
		indent + indent + "install_path: " + dir + "\n" +
		indent + indent + "mod_path: " + dir + "\n"
	require.NoError(t, os.WriteFile(path, append(body, entry...), 0o644))
}

// gameBlockIndent returns the leading whitespace of the first game key
// under "games:" in body.
func gameBlockIndent(t *testing.T, body string) string {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "games:" {
			continue
		}
		require.Greater(t, len(lines), i+1, "games.yaml has no game under games:")
		next := lines[i+1]
		return next[:len(next)-len(strings.TrimLeft(next, " "))]
	}
	t.Fatalf("no games: key in %q", body)
	return ""
}

// writeGameWithSource rewrites games.yaml with a single game carrying one
// source mapping - `lmm game edit --source`'s result.
func writeGameWithSource(t *testing.T, configDir, id, dir, sourceID, sourceGameID string) {
	t.Helper()
	body := "games:\n  " + id + ":\n    name: Fixture Game\n    install_path: " + dir +
		"\n    mod_path: " + dir + "\n    sources:\n      " + sourceID + ": " + sourceGameID + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte(body), 0o644))
}
