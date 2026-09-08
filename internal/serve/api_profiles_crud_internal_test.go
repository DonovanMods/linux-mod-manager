package serve

// The profiles modal's CRUD routes (#332): create, delete, set-default,
// rename and export. Every one answers the core document its `lmm profile
// ...  --json` twin emits, and every one asserts the END STATE it left
// behind, not just the document it returned.

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireEncodesLikeInternal asserts got byte-matches core.EncodeJSON of
// want - the package-internal twin of serve_test's requireEncodesLike, so
// an internal test can pin "the response IS the core document, framed the
// way --json frames it".
func requireEncodesLikeInternal(t *testing.T, got []byte, want any) {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, core.EncodeJSON(&buf, want))
	require.Equal(t, buf.String(), string(got),
		"the response must byte-match core.EncodeJSON of the equivalent core call")
}

// TestAPIProfileCreate_CreatesTheProfileAndAnswersTheDocument
func TestAPIProfileCreate_CreatesTheProfileAndAnswersTheDocument(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles", game), `{"name":"survival"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := decodeProfileResult(t, rec.Body.Bytes())
	assert.Equal(t, "survival", got.Profile.Name)
	assert.Equal(t, game.ID, got.Profile.GameID)
	assert.Empty(t, got.Profile.Mods, "a new profile is empty")

	created, err := svc.NewProfileManager().Get(t.Context(), game.ID, "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", created.Name)
}

// TestAPIProfileCreate_ExistingNameAnswers409: a collision with state the
// caller could not have known about is neither bad input nor a 404.
func TestAPIProfileCreate_ExistingNameAnswers409(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles", game), `{"name":"`+activeProfile+`"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	var env apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, env.Error, "already exists")
}

// TestAPIProfileCreate_BadInput covers the request-body refusals.
func TestAPIProfileCreate_BadInput(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"name":""}`, `{"name":"a","nope":1}`, `{"name":"../escape"}`} {
		t.Run(body, func(t *testing.T) {
			s, _, game := newProfilesFixtureServer(t)
			rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles", game), body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// TestAPIProfileDelete_ReportsThePreDeletionProfile: the document names
// what was deleted, mods included, and the profile is really gone.
func TestAPIProfileDelete_ReportsThePreDeletionProfile(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodDelete, scoped("/api/v1/profiles/"+switchTargetProfile, game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := decodeProfileResult(t, rec.Body.Bytes())
	assert.Equal(t, switchTargetProfile, got.Profile.Name)
	require.Len(t, got.Profile.Mods, 2, "the pre-deletion document must carry the mods it held")

	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, switchTargetProfile)
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}

// TestAPIProfileDelete_UnknownProfileAnswers404
func TestAPIProfileDelete_UnknownProfileAnswers404(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodDelete, scoped("/api/v1/profiles/ghost", game), "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAPIProfileSetDefault_MovesTheDefaultFlag: the answered document is
// the profile as persisted, and the previous default lost the flag.
func TestAPIProfileSetDefault_MovesTheDefaultFlag(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles/"+switchTargetProfile+"/set-default", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := decodeProfileResult(t, rec.Body.Bytes())
	assert.Equal(t, switchTargetProfile, got.Profile.Name)
	assert.True(t, got.Profile.IsDefault)

	pm := svc.NewProfileManager()
	def, err := pm.GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, switchTargetProfile, def.Name)

	previous, err := pm.Get(t.Context(), game.ID, activeProfile)
	require.NoError(t, err)
	assert.False(t, previous.IsDefault, "the previous default must have lost the flag")
}

// TestAPIProfileSetDefault_UnknownProfileAnswers404
func TestAPIProfileSetDefault_UnknownProfileAnswers404(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles/ghost/set-default", game), "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAPIProfileRename_MovesTheProfileAndItsRows is the end-state test the
// route's core seam earns: the new name resolves in the config dir AND in
// the DB, and the old one in neither.
func TestAPIProfileRename_MovesTheProfileAndItsRows(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles/"+activeProfile+"/rename", game), `{"name":"survival"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := decodeProfileResult(t, rec.Body.Bytes())
	assert.Equal(t, "survival", got.Profile.Name)
	require.Len(t, got.Profile.Mods, 2)

	ctx := t.Context()
	pm := svc.NewProfileManager()
	renamed, err := pm.Get(ctx, game.ID, "survival")
	require.NoError(t, err)
	assert.True(t, renamed.IsDefault, "the renamed default profile stays the default")

	_, err = pm.Get(ctx, game.ID, activeProfile)
	require.ErrorIs(t, err, domain.ErrProfileNotFound)

	mods, err := svc.GetInstalledMods(ctx, game.ID, "survival")
	require.NoError(t, err)
	assert.Len(t, mods, 2, "the install rows must have moved with the profile")

	stale, err := svc.GetInstalledMods(ctx, game.ID, activeProfile)
	require.NoError(t, err)
	assert.Empty(t, stale)
}

// TestAPIProfileRename_Refusals: an occupied target is 409, an unknown
// source is 404, a missing name is 400 - and nothing moves in any of them.
func TestAPIProfileRename_Refusals(t *testing.T) {
	tests := []struct {
		name   string
		target string
		body   string
		status int
	}{
		{"occupied target", activeProfile, `{"name":"` + switchTargetProfile + `"}`, http.StatusConflict},
		{"unknown source", "ghost", `{"name":"survival"}`, http.StatusNotFound},
		{"missing name", activeProfile, `{}`, http.StatusBadRequest},
		{"invalid name", activeProfile, `{"name":"../escape"}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, svc, game := newProfilesFixtureServer(t)
			rec := doAPI(s, http.MethodPost, scoped("/api/v1/profiles/"+tc.target+"/rename", game), tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())

			var env apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error)

			_, err := svc.NewProfileManager().Get(t.Context(), game.ID, activeProfile)
			require.NoError(t, err, "a refused rename must leave the source profile in place")
		})
	}
}

// TestAPIProfileExport_ServesTheExportedDocumentAsAnAttachment
func TestAPIProfileExport_ServesTheExportedDocumentAsAnAttachment(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/profiles/"+activeProfile+"/export", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `attachment; filename="`+activeProfile+`.json"`, rec.Header().Get("Content-Disposition"))

	var got domain.ExportedProfile
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got, json.RejectUnknownMembers(true)),
		"the export must decode into domain.ExportedProfile with no unknown members")
	assert.Equal(t, activeProfile, got.Name)
	require.Len(t, got.Mods, 2)
	assert.NotEmpty(t, got.Mods[0].FileIDs, "an export backfills each ref's file ids from its install row")

	// The bytes are exactly what the CLI's own --json export emits.
	want, err := svc.ExportProfile(t.Context(), game.ID, activeProfile)
	require.NoError(t, err)
	requireEncodesLikeInternal(t, rec.Body.Bytes(), want)
}

// TestAPIProfileExport_UnknownProfileAnswers404
func TestAPIProfileExport_UnknownProfileAnswers404(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/profiles/ghost/export", game), "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAPIProfileCRUD_RefusesWithoutCSRF: every state-changing route in this
// file is behind the same token, DELETE included.
func TestAPIProfileCRUD_RefusesWithoutCSRF(t *testing.T) {
	tests := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, "/api/v1/profiles", `{"name":"survival"}`},
		{http.MethodDelete, "/api/v1/profiles/" + switchTargetProfile, ""},
		{http.MethodPost, "/api/v1/profiles/" + switchTargetProfile + "/set-default", ""},
		{http.MethodPost, "/api/v1/profiles/" + activeProfile + "/rename", `{"name":"survival"}`},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			s, svc, game := newProfilesFixtureServer(t)
			rec := doAPIWithoutCSRF(s, tc.method, scoped(tc.target, game), tc.body)
			require.Equal(t, http.StatusForbidden, rec.Code)

			names, err := svc.NewProfileManager().ListNames(t.Context(), game.ID)
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{activeProfile, switchTargetProfile, applyTargetProfile}, names,
				"a refused request must not have changed the profile set")
		})
	}
}

// TestAPIProfileCRUD_UnknownGameAnswers404: the game half of the selection
// still governs, even though the profile is in the path.
func TestAPIProfileCRUD_UnknownGameAnswers404(t *testing.T) {
	s, _, _ := newProfilesFixtureServer(t)

	for _, target := range []string{
		"/api/v1/profiles?game=ghost",
		"/api/v1/profiles/" + activeProfile + "/export?game=ghost",
	} {
		method := http.MethodPost
		body := `{"name":"survival"}`
		if target != "/api/v1/profiles?game=ghost" {
			method, body = http.MethodGet, ""
		}
		rec := doAPI(s, method, target, body)
		require.Equal(t, http.StatusNotFound, rec.Code, target+": "+rec.Body.String())
	}
}
