package serve

// #563: GET /api/v1/sources/{id}/version-types?game=... answers the
// core.GameVersionTypesReport the game editor's CurseForge picker renders,
// and each way it cannot is a status the SPA can tell apart.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// versionTypesFixtureSource is a source.GameVersionTypeLister registered as
// "versioned".
type versionTypesFixtureSource struct {
	namedFixtureSource
	types   []source.GameVersionType
	listErr error
}

func (v *versionTypesFixtureSource) ListGameVersionTypes(context.Context, string) ([]source.GameVersionType, error) {
	return v.types, v.listErr
}

var _ source.GameVersionTypeLister = (*versionTypesFixtureSource)(nil)

func newVersionTypesServer(t *testing.T, listErr error) *Server {
	t.Helper()
	s := newGamesServer(t)
	s.svc.RegisterSource(&versionTypesFixtureSource{
		namedFixtureSource: namedFixtureSource{id: "versioned", name: "Versioned"},
		types: []source.GameVersionType{
			{ID: 517, Name: "WoW Retail", Slug: "game"},
			{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"},
		},
		listErr: listErr,
	})
	require.NoError(t, s.svc.SaveGame(t.Context(), &domain.Game{
		ID: "wow-classic", Name: "WoW Classic", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"versioned": "1", "nexusmods": "x"},
	}))
	return s
}

func TestAPIVersionTypes_ListsTheGamesTypes(t *testing.T) {
	s := newVersionTypesServer(t, nil)

	rec := doAPI(s, http.MethodGet, "/api/v1/sources/versioned/version-types?game=wow-classic", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var report core.GameVersionTypesReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report, json.RejectUnknownMembers(true)))
	assert.Equal(t, core.GameVersionTypesReport{
		GameID: "wow-classic", SourceID: "versioned",
		Types: []core.GameVersionType{
			{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"},
			{ID: 517, Name: "WoW Retail", Slug: "game"},
		},
	}, report)
}

func TestAPIVersionTypes_Statuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listErr error
		target  string
		want    int
	}{
		{"unknown source", nil, "/api/v1/sources/nope/version-types?game=wow-classic", http.StatusNotFound},
		{"no game", nil, "/api/v1/sources/versioned/version-types", http.StatusBadRequest},
		{"unknown game", nil, "/api/v1/sources/versioned/version-types?game=nope", http.StatusNotFound},
		{"source publishes none", nil, "/api/v1/sources/nexusmods/version-types?game=wow-classic", http.StatusNotFound},
		{"no key", domain.ErrAuthRequired, "/api/v1/sources/versioned/version-types?game=wow-classic", http.StatusUnauthorized},
		{"upstream failure", assert.AnError, "/api/v1/sources/versioned/version-types?game=wow-classic", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newVersionTypesServer(t, tc.listErr)
			rec := doAPI(s, http.MethodGet, tc.target, "")
			assert.Equal(t, tc.want, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// A game that does not map the source is its configuration, not a missing
// thing, when the source does publish types.
func TestAPIVersionTypes_UnmappedGameIsBadRequest(t *testing.T) {
	s := newVersionTypesServer(t, nil)
	require.NoError(t, s.svc.SaveGame(t.Context(), &domain.Game{
		ID: "other", Name: "Other", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"nexusmods": "y"},
	}))

	rec := doAPI(s, http.MethodGet, "/api/v1/sources/versioned/version-types?game=other", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
}
