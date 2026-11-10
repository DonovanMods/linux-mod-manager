package serve

// #590 through the endpoint the Updates card reads: a GET /api/v1/updates
// carrying the SPA's CSRF token that finds a gone mod of a source with
// unstable IDs, and exactly one successor for it, re-links it in an
// automatic "catalog_relink" job - the tray shows it - and the checks after
// it say what moved. Without the token the check is read-only, and while
// another job runs the pass steps aside.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	relinkGoneID = "dLs3nvmWj5uOPnXxezGe"
	relinkNewID  = "8m1z04vNo2CCdKcD2C0H"
)

// unstableFixtureSource is a source with unstable IDs whose catalog has
// dropped relinkGoneID and lists Bear Mount (by Donovan) as relinkNewID.
type unstableFixtureSource struct{ fixtureSource }

func (*unstableFixtureSource) ID() string        { return "gonecat" }
func (*unstableFixtureSource) Name() string      { return "Gone Catalog" }
func (*unstableFixtureSource) UnstableIDs() bool { return true }

func (*unstableFixtureSource) moved() domain.Mod {
	return domain.Mod{ID: relinkNewID, SourceID: "gonecat", Name: "Bear Mount", Author: "Donovan", Version: "1.0", GameID: "g1"}
}

func (s *unstableFixtureSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{Mods: []domain.Mod{s.moved()}}, nil
}

func (s *unstableFixtureSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	if modID != relinkNewID {
		return nil, domain.ErrModNotFound
	}
	m := s.moved()
	return &m, nil
}

func (*unstableFixtureSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var errs []error
	for _, im := range installed {
		if im.ID == relinkGoneID {
			errs = append(errs, &source.ModNotFoundError{ModID: im.ID, Err: fmt.Errorf("%s: %w", im.ID, domain.ErrModNotFound)})
		}
	}
	if len(errs) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("source %q: %d update check(s) failed: %w", "gonecat", len(errs), errors.Join(errs...))
}

// newAutoRelinkFixtureServer installs Bear Mount under relinkGoneID, with
// its files in the cache, on a game that maps the unstable source.
func newAutoRelinkFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	ctx := t.Context()
	svc.RegisterSource(&unstableFixtureSource{})
	if game.SourceIDs == nil {
		game.SourceIDs = map[string]string{}
	}
	game.SourceIDs["gonecat"] = game.ID
	require.NoError(t, svc.SaveGame(ctx, game))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: relinkGoneID, SourceID: "gonecat", Name: "Bear Mount", Author: "Donovan", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	require.NoError(t, svc.NewProfileManager().UpsertMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: "gonecat", ModID: relinkGoneID, Version: "1.0"}))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "gonecat", relinkGoneID, "1.0", "bear.pak", []byte("pak")))
	return s, svc, game
}

// relinkJobs is every catalog_relink job the registry holds.
func relinkJobs(s *Server) []jobSummary {
	var out []jobSummary
	for _, j := range s.jobs.list() {
		if j.Kind == "catalog_relink" {
			out = append(out, j)
		}
	}
	return out
}

func hasInstalled(t *testing.T, svc *core.Service, game *domain.Game, modID string) bool {
	t.Helper()
	_, err := svc.GetInstalledMod(t.Context(), "gonecat", modID, game.ID, "default")
	return err == nil
}

const relinkMessage = "Bear Mount moved to a new ID in Gone Catalog; re-linked (" + relinkGoneID + " -> " + relinkNewID + ")"

func TestAPIFlow_AutoRelink_ACheckReLinksTheMovedMod(t *testing.T) {
	s, svc, game := newAutoRelinkFixtureServer(t)

	report := getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoRelink, "a check that finds a moved mod of an unstable source re-links it")
	assert.Equal(t, []string{"gonecat:" + relinkGoneID}, report.AutoRelink.Mods)
	require.NotEmpty(t, report.AutoRelink.JobID)

	j := awaitJob(t, s, jobID(report.AutoRelink.JobID))
	status := j.status()
	require.Equal(t, jobSucceeded, status.State, "job failed: %+v", status.Error)
	assert.Equal(t, "catalog_relink", status.Kind)
	assert.Equal(t, jobOriginAuto, status.Origin, "the tray says no one clicked for it")
	require.NotNil(t, status.Tally)
	assert.Equal(t, 1, status.Tally.Applied)
	assert.Equal(t, []string{relinkMessage}, status.Tally.Notes)

	assert.True(t, hasInstalled(t, svc, game, relinkNewID))
	assert.False(t, hasInstalled(t, svc, game, relinkGoneID))

	after := getUpdatesReport(t, s, game)
	assert.Empty(t, after.CatalogMissing, "the moved mod is checked under its new ID")
	require.NotNil(t, after.AutoRelink, "the card goes on saying what moved")
	assert.Empty(t, after.AutoRelink.JobID)
	require.Len(t, after.AutoRelink.Relinked, 1)
	assert.Equal(t, relinkMessage, after.AutoRelink.Relinked[0].Message)
	assert.Len(t, relinkJobs(s), 1)

	refreshed := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game)+"&refresh=1", "")
	require.Equal(t, http.StatusOK, refreshed.Code, refreshed.Body.String())
	var fresh core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(refreshed.Body.Bytes(), &fresh))
	assert.Nil(t, fresh.AutoRelink, "the user's explicit check starts the record afresh")
}

// A GET needs no CSRF token, so any site could make a browser send one;
// without the SPA's token the check is read-only, as for #587's pass.
func TestAPIFlow_AutoRelink_NeverWithoutTheCSRFToken(t *testing.T) {
	s, svc, game := newAutoRelinkFixtureServer(t)

	req := apiRequest(s, http.MethodGet, scoped("/api/v1/updates", game), "")
	req.Header.Del(csrfHeaderName)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))

	assert.Nil(t, report.AutoRelink)
	require.Len(t, report.CatalogMissing, 1)
	assert.Len(t, report.CatalogMissing[0].Successors, 1, "the suggestion is still made")
	assert.Empty(t, relinkJobs(s))
	assert.True(t, hasInstalled(t, svc, game, relinkGoneID))
}

// A check made while a job runs never queues a re-link behind it; the
// next check re-links.
func TestAPIFlow_AutoRelink_StepsAsideWhileAJobRuns(t *testing.T) {
	s, svc, game := newAutoRelinkFixtureServer(t)

	release := make(chan struct{})
	busy, err := s.jobs.Start("deploy", func(context.Context, core.EventSink) (any, error) {
		<-release
		return nil, nil
	})
	require.NoError(t, err)

	report := getUpdatesReport(t, s, game)
	assert.Nil(t, report.AutoRelink)
	assert.Empty(t, relinkJobs(s), "nothing is queued behind the running job")
	assert.True(t, hasInstalled(t, svc, game, relinkGoneID))

	close(release)
	awaitJob(t, s, busy)

	report = getUpdatesReport(t, s, game)
	require.NotNil(t, report.AutoRelink)
	awaitJob(t, s, jobID(report.AutoRelink.JobID))
	assert.True(t, hasInstalled(t, svc, game, relinkNewID))
}
