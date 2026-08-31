package serve

// Shared fixtures for the package-internal (package serve) tests that need
// to reach unexported server state - the plan store, the job registry, the
// CSRF token, the SSE clock seam. testhelpers_test.go's richer fixtures
// live in package serve_test and are not visible from here.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/require"
)

// internalTestAddr is the bind address every internal-test Server is
// constructed with; requests must carry it as their Host or hostCheck
// rejects them before any handler runs.
const internalTestAddr = "127.0.0.1:7420"

// deployFixtureFile is the one cache-resident file the seeded mod deploys,
// and therefore the file a successful ApplyDeploy must leave under the
// game's ModPath - the end-state assertion that proves the happy path ran a
// REAL mutation rather than merely returning a document.
const deployFixtureFile = "Mods/one.pak"

// newDeployFixtureServer builds a Server over a Service seeded so that
// `POST /api/v1/plans/deploy` has real work to do: one game ("g1", the
// configured default), one "default" profile, and one enabled installed mod
// whose files are already in the cache - so ApplyDeploy links them into the
// game directory without ever touching a source or the network.
func newDeployFixtureServer(t *testing.T) (*Server, *domain.Game) {
	t.Helper()
	svc, game := newDeployFixtureService(t)
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), game
}

// newLiveFixtureServer is newDeployFixtureServer for the tests that drive
// the server over real TCP through httptest.NewServer, which binds its own
// ephemeral port. It binds a WILDCARD address so allowedHostsFor returns
// nil and hostCheck admits the "127.0.0.1:<random>" Host the test server
// hands out - a concrete bind would pin a port the test cannot know in
// advance and 403 every request.
func newLiveFixtureServer(t *testing.T) (*Server, *domain.Game) {
	t.Helper()
	svc, game := newDeployFixtureService(t)
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: ":0"}), game
}

// newDeployFixtureService is newDeployFixtureServer's Service half, for the
// tests that want to construct the Server themselves.
func newDeployFixtureService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	sandboxEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fixtureSource{}
	svc.RegisterSource(src)

	ctx := t.Context()
	game := &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		SourceIDs:   map[string]string{fixtureSourceID: ""},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	mod := domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID}
	require.NoError(t, svc.GetGameCache(game).Store(
		game.ID, mod.SourceID, mod.ID, mod.Version, deployFixtureFile, []byte("pak bytes")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          mod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	// The DB row says the mod is installed; the profile's load order is what
	// a deploy actually walks, so both halves have to be seeded (the same
	// pairing internal/core's own deploy fixtures use).
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}))

	return svc, game
}

// fixtureSourceID is the id the seeded mod's source is registered under.
const fixtureSourceID = "fake"

// fixtureSource is a minimal source.ModSource registered on the fixture
// Service so the seeded game maps a real source, as a configured game
// always does. It serves the one seeded mod from memory and supports no
// download at all - every fixture mod's files are already in the cache, so
// a deploy that reached the network would be a bug this double turns into a
// visible failure rather than a silent live call.
type fixtureSource struct{}

func (*fixtureSource) ID() string      { return fixtureSourceID }
func (*fixtureSource) Name() string    { return "Fixture Source" }
func (*fixtureSource) AuthURL() string { return "" }

func (*fixtureSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (*fixtureSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}

func (*fixtureSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	if modID != "m1" {
		return nil, domain.ErrModNotFound
	}
	return &domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: "g1"}, nil
}

func (*fixtureSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (*fixtureSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}

func (*fixtureSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}

func (*fixtureSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

var _ source.ModSource = (*fixtureSource)(nil)

// deployedFixturePath is where deployFixtureFile lands once a deploy has
// actually run.
func deployedFixturePath(game *domain.Game) string {
	return filepath.Join(game.ModPath, filepath.FromSlash(deployFixtureFile))
}

// apiRequest builds a request the middleware chain will admit: the server's
// Host, and - for a state-changing method - the process CSRF token in the
// header form an API caller uses.
func apiRequest(s *Server, method, target, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://"+internalTestAddr+target, reader)
	if unsafeMethod(method) {
		req.Header.Set(csrfHeaderName, s.csrf.token)
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// doAPI runs one request against the server's full handler chain and
// returns the recorder.
func doAPI(s *Server, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, apiRequest(s, method, target, body))
	return rec
}
