package serve

import (
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSchemaFixtureServer is a server over a Service whose database path the
// test knows, so a second handle can migrate it underneath the server.
func newSchemaFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game, string) {
	t.Helper()
	sandboxEnv(t)
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	ctx := t.Context()
	game := &domain.Game{ID: "g1", Name: "Fixture Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.CreateProfile(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))
	s := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr})
	return s, svc, game, filepath.Join(dataDir, "lmm.db")
}

// migrateUnderServer is #648's newer lmm: it records, through a database
// handle of its own, a migration the running server does not know, and
// returns the version it recorded.
func migrateUnderServer(t *testing.T, path string) int {
	t.Helper()
	other, err := db.New(path)
	require.NoError(t, err)
	version := other.SupportedSchemaVersion() + 1
	_, err = other.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version)
	require.NoError(t, err)
	require.NoError(t, other.Close())
	return version
}

// schemaEnvelope is the refusal's /api/v1 envelope: the remedy as the
// sentence, and the banner's data as details.
type schemaEnvelope struct {
	Error   string `json:"error"`
	Details struct {
		Code      string `json:"code"`
		Version   int    `json:"version"`
		Supported int    `json:"supported"`
	} `json:"details"`
}

// TestAPI_ANewerLMMMigratingTheDatabaseRefusesEveryRequest is #648 over
// /api/v1: a `lmm serve` started on an older binary kept writing after a
// newer CLI migrated the database, and failed deep inside an
// update-from-file. Every /api/v1 request - a read too, since a column a
// migration rebuilt can fail or be misread - now refuses with the open's
// refusal in the error envelope: the remedy as its sentence, the code and
// both versions as the details the SPA's restart banner shows. The SPA
// shell itself still loads, so there is a page to show the banner on.
func TestAPI_ANewerLMMMigratingTheDatabaseRefusesEveryRequest(t *testing.T) {
	s, svc, game, path := newSchemaFixtureServer(t)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/status", game), "")
	require.Equal(t, http.StatusOK, rec.Code, "before the migration the server answers: %s", rec.Body.String())

	newer := migrateUnderServer(t, path)

	for _, req := range []struct{ method, target, body string }{
		{http.MethodGet, scoped("/api/v1/status", game), ""},
		{http.MethodGet, scoped("/api/v1/mods", game), ""},
		{http.MethodGet, "/api/v1/games", ""},
		{http.MethodPost, scoped("/api/v1/plans/deploy", game), "{}"},
		{http.MethodPost, "/api/v1/profiles?" + gameParam + "=" + game.ID, `{"name":"second"}`},
	} {
		rec := doAPI(s, req.method, req.target, req.body)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "%s %s: %s", req.method, req.target, rec.Body.String())
		var env schemaEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), "%s %s: %s", req.method, req.target, rec.Body.String())
		assert.Contains(t, env.Error, "another, newer lmm upgraded the database", "%s %s", req.method, req.target)
		assert.Contains(t, env.Error, "restart lmm serve on the newer binary")
		assert.Equal(t, core.SchemaTooNewCode, env.Details.Code)
		assert.Equal(t, newer, env.Details.Version)
		assert.Equal(t, newer-1, env.Details.Supported)
	}

	// Nothing the refused requests asked for happened.
	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, "second")
	assert.Error(t, err, "a refused profile create writes nothing")

	// The shell is not an /api/v1 request: it loads, to carry the banner.
	shell := doAPI(s, http.MethodGet, "/", "")
	assert.Equal(t, http.StatusOK, shell.Code)
}

// TestAPI_ARunningJobsMutationRefusesTheSameWay: the gate is serve's, but
// the refusal is core's - a mutation that gets past the gate (a plan
// redeemed by a job the moment before the migration) is refused by
// beginOp with the same document, as the job's failure.
func TestAPI_ARunningJobsMutationRefusesTheSameWay(t *testing.T) {
	_, svc, game, path := newSchemaFixtureServer(t)
	migrateUnderServer(t, path)

	_, err := svc.CreateProfile(t.Context(), game.ID, "second")
	require.ErrorIs(t, err, core.ErrSchemaTooNew)
	env := newAPIErrorEnvelope(err)
	upgraded, ok := env.Details.(*core.SchemaUpgradedError)
	require.True(t, ok, "the job envelope's details are the refusal's: %#v", env.Details)
	assert.Equal(t, core.SchemaTooNewCode, upgraded.Code)
}
