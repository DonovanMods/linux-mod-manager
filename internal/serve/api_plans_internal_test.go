package serve

// Internal (package serve) tests for POST /api/v1/plans/{kind}
// (docs/plans/2026-08-30-serve-design.md §"/api/v1": "computes a Plan,
// stores it server-side, returns the plan document plus a plan_id").
// Internal because the assertions that matter are about server state the
// wire deliberately never shows: which object the plan store actually
// holds, and that the id on the wire redeems exactly it.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIPlanDeploy_ReturnsThePlanDocumentAndAPlanID is the headline: the
// response carries the core.DeployPlan document `lmm deploy --dry-run
// --json` would emit, plus the opaque handle the job endpoint redeems.
func TestAPIPlanDeploy_ReturnsThePlanDocumentAndAPlanID(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/plans/deploy", `{}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))

	// The plan member decodes into the frozen core type itself, unknown
	// members rejected: the wire document must BE core.DeployPlan, not a
	// serve-shaped lookalike.
	var got struct {
		PlanID string          `json:"plan_id"`
		Kind   string          `json:"kind"`
		Plan   core.DeployPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got, json.RejectUnknownMembers(true)))
	assert.NotEmpty(t, got.PlanID)
	assert.Equal(t, "deploy", got.Kind)
	assert.Equal(t, "default", got.Plan.Profile)
	assert.False(t, got.Plan.NoChanges)
	require.Len(t, got.Plan.Mods, 1)
	assert.Equal(t, "Mod One", got.Plan.Mods[0].Name)
	assert.Equal(t, []string{deployFixtureFile}, got.Plan.Mods[0].Link)
}

// TestAPIPlanDeploy_StoresThePlanObjectNotItsWireCopy is the reason the
// plan store exists at all (plans.go): the id redeems the very *core.
// DeployPlan PlanDeploy returned, unexported freshness snapshot intact.
func TestAPIPlanDeploy_StoresThePlanObjectNotItsWireCopy(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/plans/deploy", `{}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		PlanID planID `json:"plan_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	stored, err := s.plans.Take(got.PlanID)
	require.NoError(t, err)
	assert.Equal(t, "deploy", stored.Kind)
	pending, ok := stored.Plan.(*pendingDeploy)
	require.True(t, ok, "the deploy kind must store its own pending value, got %T", stored.Plan)
	require.IsType(t, &core.DeployPlan{}, pending.Plan)
	assert.Equal(t, "default", pending.Plan.Profile)
	assert.Equal(t, "g1", pending.Game.ID, "the pending value carries the game its Apply needs")
}

// TestAPIPlan_UnknownKind_400NamesTheSupportedKinds pins the closed table:
// a {kind} nobody registered is bad input, and the envelope's details list
// what IS accepted - generated from the registry, never hand-maintained.
func TestAPIPlan_UnknownKind_400NamesTheSupportedKinds(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/plans/teleport", `{}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))

	var envelope struct {
		Error   string `json:"error"`
		Details struct {
			SupportedKinds []string `json:"supported_kinds"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope, json.RejectUnknownMembers(true)))
	assert.Contains(t, envelope.Error, "teleport")
	assert.Equal(t, supportedPlanKinds(), envelope.Details.SupportedKinds)
	assert.Contains(t, envelope.Details.SupportedKinds, "deploy")
}

// TestAPIPlan_WithoutCSRFToken_403 covers the design's "CSRF token on every
// form and state-changing API call" for the new POST surface. The failure
// must carry the same {"error","details"} JSON envelope every other
// /api/v1 failure does (epic live review M1) - not a bare text/plain
// http.Error, the one shape that broke the README's unconditional claim
// that every API failure uses the envelope.
func TestAPIPlan_WithoutCSRFToken_403(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	req := apiRequest(s, http.MethodPost, "/api/v1/plans/deploy", `{}`)
	req.Header.Del(csrfHeaderName)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var envelope apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope, json.RejectUnknownMembers(true)))
	assert.Contains(t, envelope.Error, "CSRF")
}

// TestAPIPlan_CrossOrigin_403 is the Origin-check half of M1: an
// /api/v1 request whose Origin disagrees with its Host must also get the
// JSON envelope, not the bare text/plain http.Error every non-API route
// still uses.
func TestAPIPlan_CrossOrigin_403(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	req := apiRequest(s, http.MethodPost, "/api/v1/plans/deploy", `{}`)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var envelope apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope, json.RejectUnknownMembers(true)))
	assert.Contains(t, envelope.Error, "cross-origin")
}

// TestAPIPlanDeploy_RejectsUnusableOptions covers the options decoder: an
// unknown member and an unparsable enum are both bad input, caught BEFORE
// any core call runs.
func TestAPIPlanDeploy_RejectsUnusableOptions(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unknown member", `{"purrge": true}`},
		{"unparsable link method", `{"link_method": "teleport"}`},
		{"not an object", `["deploy"]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newDeployFixtureServer(t)

			rec := doAPI(s, http.MethodPost, "/api/v1/plans/deploy", tc.body)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
			assert.Equal(t, 0, s.plans.len(), "a rejected request must not store a plan")
		})
	}
}

// TestAPIPlanDeploy_HonoursItsOptions proves the decoded options actually
// reach PlanDeploy: restricting the deploy to a mod id nothing matches
// yields a plan with nothing to do.
func TestAPIPlanDeploy_HonoursItsOptions(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/plans/deploy", `{"link_method":"copy","mod_id":"m1","source_id":"fake"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		PlanID planID `json:"plan_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	stored, err := s.plans.Take(got.PlanID)
	require.NoError(t, err)
	pending := stored.Plan.(*pendingDeploy)
	require.NotNil(t, pending.Opts.LinkMethod)
	assert.Equal(t, domain.LinkCopy, *pending.Opts.LinkMethod)
	assert.Equal(t, "m1", pending.Opts.ModID)
}

// TestRegisterPlanKind_DuplicatePanics pins the registry's wiring
// contract: registering a kind twice is a programming error caught at
// startup, not a runtime condition a request can reach.
func TestRegisterPlanKind_DuplicatePanics(t *testing.T) {
	assert.Panics(t, func() { registerPlanKind(planKind{Name: "deploy"}) })
	assert.Contains(t, supportedPlanKinds(), "deploy", "a refused duplicate must not disturb the table")
}
