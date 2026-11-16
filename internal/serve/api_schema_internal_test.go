package serve

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

// TestAPI_SchemaGateSparesTheRoutesThatNeverReadTheDatabase pins the
// gate's scope (#648 review): the job registry's routes, the activity
// stream and lmm's own release status read nothing from the database, so
// they keep answering once the gate is latched - the activity stream does
// not drop and reopen forever, a job that was running when the migration
// landed can still be followed after a reload, and the release status can
// still say a newer binary is installed (the remedy, ready). Every route
// that reads or writes the database stays refused, health included: its
// verify reads the installed mods.
func TestAPI_SchemaGateSparesTheRoutesThatNeverReadTheDatabase(t *testing.T) {
	s, _, game, path := newSchemaFixtureServer(t)

	// A job that finished around the migration.
	id, err := s.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) { return nil, nil })
	require.NoError(t, err)
	awaitJob(t, s, id)

	migrateUnderServer(t, path)
	require.ErrorIs(t, s.svc.CheckSchema(t.Context()), core.ErrSchemaTooNew, "the gate is latched")

	for _, req := range []struct {
		method, target string
		want           int
	}{
		{http.MethodGet, "/api/v1/jobs", http.StatusOK},
		{http.MethodGet, "/api/v1/jobs/" + string(id), http.StatusOK},
		{http.MethodGet, "/api/v1/jobs/" + string(id) + "/events", http.StatusOK},
		{http.MethodGet, "/api/v1/jobs/no-such-job", http.StatusNotFound},
		{http.MethodGet, "/api/v1/updates/lmm", http.StatusOK},
		// No release checker in this fixture: the route's own 404, not
		// the gate's 503.
		{http.MethodPost, "/api/v1/updates/lmm/notice", http.StatusNotFound},
	} {
		rec := doAPI(s, req.method, req.target, "")
		assert.Equal(t, req.want, rec.Code, "%s %s is spared: %s", req.method, req.target, rec.Body.String())
	}

	// The activity stream stays open: it answers 200 and streams until
	// the client goes away.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, apiRequest(s, http.MethodGet, "/api/v1/events", "").WithContext(ctx))
	assert.Equal(t, http.StatusOK, rec.Code, "the activity stream is spared: %s", rec.Body.String())
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	for _, target := range []string{
		scoped("/api/v1/health", game),
		scoped("/api/v1/status", game),
		scoped("/api/v1/conflicts", game),
		"/api/v1/profiles?" + gameParam + "=" + game.ID,
	} {
		rec := doAPI(s, http.MethodGet, target, "")
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "GET %s reads the database: %s", target, rec.Body.String())
		var env schemaEnvelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.Equal(t, core.SchemaTooNewCode, env.Details.Code, "GET %s", target)
	}
	// And a job start, whose Apply would write.
	rec = doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"x"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}
