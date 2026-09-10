package serve

// api_snapshots_internal_test.go covers the Snapshots card's three
// single-step routes (#350): the listing, a create, and a delete. Every
// assertion is semantic - the status, the decoded core document, and what
// the CALL LEFT BEHIND - never a byte-golden of an /api/v1 response.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decodeSnapshotListing decodes GET /api/v1/snapshots' body.
func decodeSnapshotListing(t *testing.T, body []byte) core.SnapshotListing {
	t.Helper()
	var listing core.SnapshotListing
	require.NoError(t, json.Unmarshal(body, &listing))
	return listing
}

func TestAPISnapshots_EmptyListingIsAWellFormedDocument(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/snapshots", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	listing := decodeSnapshotListing(t, rec.Body.Bytes())
	assert.Equal(t, game.ID, listing.GameID)
	assert.Empty(t, listing.Snapshots, "a game with no snapshots answers an empty list, not a 404")
}

func TestAPISnapshotCreate_RecordsOneAndAnswersTheCoreDocument(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"name":"before-tweaks"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.SnapshotResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, "before-tweaks", result.Name)
	assert.Equal(t, game.ID, result.GameID)
	assert.Equal(t, "default", result.Profile)
	assert.Equal(t, 1, result.Mods)
	assert.Equal(t, 1, result.DeployedFiles)
	assert.NotEmpty(t, result.Path)

	// And it really landed - the listing sees it.
	rec = doAPI(s, http.MethodGet, scoped("/api/v1/snapshots", game), "")
	require.Equal(t, http.StatusOK, rec.Code)
	listing := decodeSnapshotListing(t, rec.Body.Bytes())
	require.Len(t, listing.Snapshots, 1)
	assert.Equal(t, "before-tweaks", listing.Snapshots[0].Name)
}

// TestAPISnapshotCreate_EmptyNameGetsTheSharedDefault pins that the card's
// "Snapshot now" button needs no text input: core owns the default name, so
// the two frontends name the same gesture the same way.
func TestAPISnapshotCreate_EmptyNameGetsTheSharedDefault(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/snapshots", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.SnapshotResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Regexp(t, `^\d{8}-\d{6}$`, result.Name)
}

// TestAPISnapshotCreate_TakenNameIs409: the request was well-formed and
// refused on state, which is what 409 means - and the refusal matters,
// because a snapshot is the only copy of an arrangement the user asked lmm
// to remember.
func TestAPISnapshotCreate_TakenNameIs409(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"name":"taken"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = doAPI(s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"name":"taken"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "already exists")
}

func TestAPISnapshotCreate_IllegalNameIs400(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	for _, name := range []string{"a/b", "..", ".hidden"} {
		rec := doAPI(s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"name":"`+name+`"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "name %q: %s", name, rec.Body.String())
	}
}

func TestAPISnapshotCreate_RejectsUnknownMembers(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"nam":"typo"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestAPISnapshotDelete_RemovesItAndAnswersTheCoreDocument(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"name":"doomed"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = doAPI(s, http.MethodDelete, scoped("/api/v1/snapshots/doomed", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.SnapshotDeleteResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, "doomed", result.Name)
	assert.True(t, result.Deleted)

	rec = doAPI(s, http.MethodGet, scoped("/api/v1/snapshots", game), "")
	assert.Empty(t, decodeSnapshotListing(t, rec.Body.Bytes()).Snapshots)
}

func TestAPISnapshotDelete_UnknownNameIs404(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodDelete, scoped("/api/v1/snapshots/never-existed", game), "")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAPISnapshots_MutationsRefuseAMissingCSRFToken pins that both writes
// go through the same guard every other state-changing route does.
func TestAPISnapshots_MutationsRefuseAMissingCSRFToken(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/snapshots", game), `{"name":"x"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = doAPIWithoutCSRF(s, http.MethodDelete, scoped("/api/v1/snapshots/x", game), "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}
