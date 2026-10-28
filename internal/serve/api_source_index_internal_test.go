package serve

// api_source_index_internal_test.go covers the index surface's four
// settings-class routes (#410): the status and the refresh of one game's
// index, the listing of every index on disk, and the prune - plus the two
// typed failures they and the search answer with. Every assertion is
// semantic (status, decoded core document, what the call left behind).

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const indexSourceID = "ts"

// indexFixtureSource is a Thunderstore-shaped double: a local index per
// community, an inventory of them on "disk", a slug-shaped identifier, and
// a loader requirement on every package.
type indexFixtureSource struct {
	fixtureSource

	cached     map[string]source.CachedIndex
	removed    []string
	refreshErr error
	searchErr  error
	forced     bool
}

func (*indexFixtureSource) ID() string   { return indexSourceID }
func (*indexFixtureSource) Name() string { return "Thunderstore" }

// IgnoresGameIdentifier is false: this source needs its community.
func (*indexFixtureSource) IgnoresGameIdentifier() bool { return false }

func (*indexFixtureSource) ValidateGameIdentifier(id string) error {
	if id == "" || strings.ToLower(id) != id || strings.ContainsAny(id, " _/") {
		return fmt.Errorf("%q is not a community slug: %w", id, source.ErrGameIdentifierInvalid)
	}
	return nil
}

func (s *indexFixtureSource) Search(_ context.Context, q source.SearchQuery) (source.SearchResult, error) {
	if s.searchErr != nil {
		return source.SearchResult{}, s.searchErr
	}
	return source.SearchResult{Mods: []domain.Mod{{ID: "A-Pack", SourceID: indexSourceID, Name: "Pack", Version: "1.0.0", GameID: q.GameID}}, TotalCount: 1}, nil
}

func (*indexFixtureSource) GetMod(_ context.Context, community, modID string) (*domain.Mod, error) {
	return &domain.Mod{ID: modID, SourceID: indexSourceID, Name: "Pack", Version: "1.0.0", GameID: community}, nil
}

func (*indexFixtureSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{ID: "1.0.0", Name: "Pack 1.0.0", FileName: "A-Pack-1.0.0.zip", Version: "1.0.0", IsPrimary: true, Category: "MAIN", Size: 10}}, nil
}

func (*indexFixtureSource) LoaderRequirement(context.Context, *domain.Mod) (string, string, string, bool, error) {
	return domain.LoaderKindBepInEx, "5.4.2100", "BepInEx-BepInExPack-5.4.2100", true, nil
}

func (s *indexFixtureSource) IndexStatus(_ context.Context, id string) (source.IndexStatus, error) {
	ci, ok := s.cached[id]
	if !ok {
		return source.IndexStatus{GameID: id}, nil
	}
	return source.IndexStatus{GameID: id, Present: ci.Present, Packages: ci.Packages, FetchedAt: ci.FetchedAt, Bytes: ci.Bytes}, nil
}

func (s *indexFixtureSource) RefreshIndex(_ context.Context, id string, force bool, progress source.IndexProgressFunc) (source.IndexStatus, error) {
	s.forced = force
	if s.refreshErr != nil {
		st, _ := s.IndexStatus(context.Background(), id)
		return st, s.refreshErr
	}
	progress(source.FetchPhaseStarted, "fetching", 0)
	s.cached[id] = source.CachedIndex{GameID: id, Present: true, Packages: 12, Bytes: 4096, FetchedAt: time.Now(), Removable: true}
	progress(source.FetchPhaseDone, "done", 0)
	st, _ := s.IndexStatus(context.Background(), id)
	return st, nil
}

func (s *indexFixtureSource) CachedIndexes(context.Context) ([]source.CachedIndex, error) {
	out := make([]source.CachedIndex, 0, len(s.cached))
	for _, ci := range s.cached {
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID < out[j].GameID })
	return out, nil
}

func (s *indexFixtureSource) RemoveIndex(_ context.Context, id string, _ time.Time) (int64, error) {
	ci, ok := s.cached[id]
	if !ok {
		return 0, nil
	}
	delete(s.cached, id)
	s.removed = append(s.removed, id)
	return ci.Bytes, nil
}

// newIndexFixtureServer is the deploy fixture plus the index source and a
// second game ("lc") that maps it to "lethal-company", which is cached,
// beside an index nobody uses.
func newIndexFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game, *indexFixtureSource) {
	t.Helper()
	s, svc, _ := newFlowFixtureServer(t)
	src := &indexFixtureSource{cached: map[string]source.CachedIndex{
		"lethal-company":  {GameID: "lethal-company", Present: true, Packages: 50707, Bytes: 239075328, FetchedAt: time.Now().Add(-time.Hour), Removable: true},
		"content-warning": {GameID: "content-warning", Present: true, Packages: 900, Bytes: 4096000, FetchedAt: time.Now().Add(-time.Hour), Removable: true},
	}}
	svc.RegisterSource(src)
	game := &domain.Game{
		ID: "lc", Name: "Lethal Company", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{indexSourceID: "lethal-company"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err := svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	return s, svc, game, src
}

func indexPath(game string) string {
	return "/api/v1/sources/" + indexSourceID + "/index?game=" + game
}

func decodeIndexEnvelope(t *testing.T, body []byte) apiErrorEnvelope {
	t.Helper()
	var env struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return apiErrorEnvelope{Error: env.Error, Details: env.Details}
}

func TestAPISourceIndex_StatusIsTheCoreDocument(t *testing.T) {
	s, _, game, _ := newIndexFixtureServer(t)

	rec := doAPI(s, http.MethodGet, indexPath(game.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var status core.IndexStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, indexSourceID, status.Source)
	assert.Equal(t, "lethal-company", status.Game)
	assert.True(t, status.Present)
	assert.Equal(t, 50707, status.Packages)
}

func TestAPISourceIndex_Refusals(t *testing.T) {
	s, svc, game, _ := newIndexFixtureServer(t)
	require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
		ID: "bad", Name: "Bad", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{indexSourceID: "Lethal_Company"},
	}))

	tests := []struct {
		name, target string
		want         int
		details      map[string]any
	}{
		{"unknown source", "/api/v1/sources/nope/index?game=" + game.ID, http.StatusNotFound, nil},
		{"a source with no index", "/api/v1/sources/" + fixtureSourceID + "/index?game=" + game.ID, http.StatusNotFound, nil},
		{"no game named", "/api/v1/sources/" + indexSourceID + "/index", http.StatusBadRequest, nil},
		{"unknown game", indexPath("nope"), http.StatusNotFound, nil},
		{"a game that does not map the source", indexPath("g1"), http.StatusBadRequest,
			map[string]any{"game_id": "g1", "source": indexSourceID, "value": ""}},
		{"a malformed mapping", indexPath("bad"), http.StatusBadRequest,
			map[string]any{"game_id": "bad", "source": indexSourceID, "value": "Lethal_Company"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				rec := doAPI(s, method, tt.target, "")
				assert.Equal(t, tt.want, rec.Code, "%s: %s", method, rec.Body.String())
				if tt.details != nil {
					assert.Equal(t, tt.details, decodeIndexEnvelope(t, rec.Body.Bytes()).Details, method)
				}
			}
		})
	}
}

func TestAPISourceIndexRefresh_RebuildsAndAnswersTheReport(t *testing.T) {
	s, _, game, src := newIndexFixtureServer(t)
	delete(src.cached, "lethal-company")

	rec := doAPI(s, http.MethodPost, indexPath(game.ID), `{"refresh":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.IndexReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	assert.Equal(t, core.IndexStatusBuilt, report.Status)
	assert.Equal(t, 12, report.Packages)
	assert.True(t, src.forced)

	// And it is on "disk" now: the status says so.
	rec = doAPI(s, http.MethodGet, indexPath(game.ID), "")
	var status core.IndexStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.True(t, status.Present)
}

func TestAPISourceIndexRefresh_RejectsAnUnknownMember(t *testing.T) {
	s, _, game, _ := newIndexFixtureServer(t)
	rec := doAPI(s, http.MethodPost, indexPath(game.ID), `{"refersh":true}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestAPISourceIndexRefresh_NeedsTheCSRFToken(t *testing.T) {
	s, _, game, src := newIndexFixtureServer(t)
	rec := doAPIWithoutCSRF(s, http.MethodPost, indexPath(game.ID), `{"refresh":true}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, src.forced)
}

// TestAPISourceIndexRefresh_NoIndexAndNoUpstreamIs502 is design §4.3's
// status for ErrIndexUnavailable: the failure is upstream of lmm, and the
// envelope's details say which index and why - and when lmm will ask again.
func TestAPISourceIndexRefresh_NoIndexAndNoUpstreamIs502(t *testing.T) {
	s, _, game, src := newIndexFixtureServer(t)
	delete(src.cached, "lethal-company")
	until := time.Date(2026, 9, 16, 12, 5, 0, 0, time.UTC)
	src.refreshErr = fmt.Errorf("fetching: %w", &source.RetryLaterError{Source: "Thunderstore", Until: until, Reason: "suspended after repeated failures"})

	rec := doAPI(s, http.MethodPost, indexPath(game.ID), `{"refresh":true}`)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	env := decodeIndexEnvelope(t, rec.Body.Bytes())
	assert.Contains(t, env.Error, "not asking Thunderstore again until")
	assert.Equal(t, map[string]any{
		"source": indexSourceID, "game": "lethal-company",
		"reason": "suspended after repeated failures", "retry_at": "2026-09-16T12:05:00Z",
	}, env.Details)
}

func TestAPIIndexes_ListsEveryIndex(t *testing.T) {
	s, _, _, _ := newIndexFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/indexes", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listing core.SourceIndexListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listing))
	require.Len(t, listing.Indexes, 2)
	assert.Equal(t, "content-warning", listing.Indexes[0].Game)
	assert.Empty(t, listing.Indexes[0].MappedBy)
	assert.Equal(t, []string{"lc"}, listing.Indexes[1].MappedBy)
}

// TestAPIIndexesPrune_PreviewThenConfirm is the web's two-step prune: the
// preview removes nothing, and the confirmed run - bound to the preview's
// keys - removes exactly what it showed.
func TestAPIIndexesPrune_PreviewThenConfirm(t *testing.T) {
	s, _, _, src := newIndexFixtureServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/indexes/prune", `{"dry_run":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var preview core.IndexPruneReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &preview))
	assert.True(t, preview.DryRun)
	assert.Equal(t, 1, preview.Removed)
	assert.Empty(t, src.removed)

	body, err := json.Marshal(map[string]any{"only": preview.RemovalKeys()})
	require.NoError(t, err)
	rec = doAPI(s, http.MethodPost, "/api/v1/indexes/prune", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.IndexPruneReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	assert.False(t, report.DryRun)
	assert.Equal(t, []string{"content-warning"}, src.removed)
	assert.Equal(t, int64(4096000), report.FreedBytes)
}

func TestAPIIndexesPrune_AllTakesTheIndexesInUse(t *testing.T) {
	s, _, _, src := newIndexFixtureServer(t)
	rec := doAPI(s, http.MethodPost, "/api/v1/indexes/prune", `{"all":true,"dry_run":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var preview core.IndexPruneReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &preview))

	body, err := json.Marshal(map[string]any{"all": true, "only": preview.RemovalKeys()})
	require.NoError(t, err)
	rec = doAPI(s, http.MethodPost, "/api/v1/indexes/prune", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	sort.Strings(src.removed)
	assert.Equal(t, []string{"content-warning", "lethal-company"}, src.removed)
}

// TestAPIIndexesPrune_ARemovalMustNameWhatItRemoves is T3 review F11: an
// empty body was a real, unbound prune - and a form-encoded POST carrying
// the token has an empty body by the time the handler reads it, because
// the CSRF check's ParseForm consumed it. A removal now has to name the
// indexes it confirms (only, from a preview); anything else is a 400 with
// nothing removed.
func TestAPIIndexesPrune_ARemovalMustNameWhatItRemoves(t *testing.T) {
	for name, body := range map[string]string{
		"an empty body":       "",
		"an empty object":     `{}`,
		"all, unconfirmed":    `{"all":true}`,
		"only, null":          `{"only":null}`,
		"dry_run false, bare": `{"dry_run":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _, src := newIndexFixtureServer(t)
			rec := doAPI(s, http.MethodPost, "/api/v1/indexes/prune", body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "only")
			assert.Empty(t, src.removed)
		})
	}

	t.Run("a form-encoded POST with the token", func(t *testing.T) {
		s, _, _, src := newIndexFixtureServer(t)
		req := httptest.NewRequest(http.MethodPost, "http://"+internalTestAddr+"/api/v1/indexes/prune",
			strings.NewReader(url.Values{csrfFormField: {s.csrf.token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Empty(t, src.removed)
	})

	t.Run("a preview and an empty confirmation still answer", func(t *testing.T) {
		s, _, _, src := newIndexFixtureServer(t)
		assert.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/indexes/prune", `{"dry_run":true}`).Code)
		assert.Equal(t, http.StatusOK, doAPI(s, http.MethodPost, "/api/v1/indexes/prune", `{"only":[]}`).Code)
		assert.Empty(t, src.removed)
	})
}

func TestAPIIndexesPrune_NeedsTheCSRFToken(t *testing.T) {
	s, _, _, src := newIndexFixtureServer(t)
	rec := doAPIWithoutCSRF(s, http.MethodPost, "/api/v1/indexes/prune", `{"all":true}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, src.removed)
}

// TestAPISearch_IndexFailuresAreTyped: the search page meets the same two
// failures, and answers them with the same statuses.
func TestAPISearch_IndexFailuresAreTyped(t *testing.T) {
	s, svc, game, src := newIndexFixtureServer(t)

	src.searchErr = fmt.Errorf("source %q: the lethal-company index could not be built: %w: %w", indexSourceID, errors.New("rate limited by Thunderstore (HTTP 429) after 3 attempts"), source.ErrIndexUnavailable)
	rec := doAPI(s, http.MethodGet, "/api/v1/search?q=x&source="+indexSourceID+"&game="+game.ID+"&profile=default", "")
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.Equal(t, "rate limited by Thunderstore (HTTP 429) after 3 attempts", decodeIndexEnvelope(t, rec.Body.Bytes()).Details.(map[string]any)["reason"])

	src.searchErr = nil
	bad := *game
	bad.SourceIDs = map[string]string{indexSourceID: ""}
	require.NoError(t, svc.SaveGame(t.Context(), &bad))
	rec = doAPI(s, http.MethodGet, "/api/v1/search?q=x&source="+indexSourceID+"&game="+game.ID+"&profile=default", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestAPIPlanInstall_ALoaderRequirementIs409WithTheSetupSteps is #423's
// server half (T2 review F1): the refusal is the user's game needing a
// loader, not the server failing, and the setup steps travel as data.
func TestAPIPlanInstall_ALoaderRequirementIs409WithTheSetupSteps(t *testing.T) {
	s, _, game, _ := newIndexFixtureServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/plans/install?game="+game.ID+"&profile=default",
		`{"source_id":"`+indexSourceID+`","mod_id":"A-Pack"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	details, ok := decodeIndexEnvelope(t, rec.Body.Bytes()).Details.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "5.4.2100", details["version"])
	setup, ok := details["setup"].([]any)
	require.True(t, ok)
	assert.Len(t, setup, 3)
	assert.Contains(t, setup[0], "5.4.2100")
}
