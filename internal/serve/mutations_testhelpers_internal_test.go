package serve

// Shared fixtures for Task 8's mutation tests (package serve, so they can
// reach the CSRF token, the plan store and the job registry directly).

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// formValues is one HTML form submission's fields, in the shape a test
// writes them; postForm adds the CSRF token itself.
type formValues map[string]string

// newMutationFixtureServer is newDeployFixtureServer plus the Service, which
// every mutation test needs for its end-state assertions (the DB row, the
// profile, the deployed tree).
func newMutationFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	svc, game := newDeployFixtureService(t)
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), svc, game
}

// seedFixtureModEnabled re-saves the fixture's seeded mod with the given
// enabled state - the full-row upsert SaveInstalledMod performs is the only
// way to reach the flag from outside package core.
func seedFixtureModEnabled(t *testing.T, svc *core.Service, game *domain.Game, enabled bool) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      enabled,
	}))
}

// formRequest builds a browser-shaped form POST the middleware chain will
// admit: the server's Host, a urlencoded body, and (unless withoutCSRF) the
// process CSRF token in the hidden-field form a rendered page carries.
func formRequest(s *Server, target string, values formValues, withCSRF bool) *http.Request {
	form := url.Values{}
	for k, v := range values {
		form.Set(k, v)
	}
	if withCSRF {
		form.Set(csrfFormField, s.csrf.token)
	}
	req := httptest.NewRequest(http.MethodPost, "http://"+internalTestAddr+target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// postForm runs one form POST against the server's full handler chain.
func postForm(s *Server, target string, values formValues) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, formRequest(s, target, values, true))
	return rec
}

// postFormWithoutCSRF is postForm with the token left off, for the refusal
// tests every mutation route carries.
func postFormWithoutCSRF(s *Server, target string, values formValues) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, formRequest(s, target, values, false))
	return rec
}

// awaitRedirectedJob resolves the job a 303 redirect points at and blocks
// until its Apply has returned, so the caller's end-state assertions never
// race the goroutine.
func awaitRedirectedJob(t *testing.T, s *Server, rec *httptest.ResponseRecorder) *job {
	t.Helper()
	loc := rec.Header().Get("Location")
	require.True(t, strings.HasPrefix(loc, "/jobs/"), "expected a redirect to a job page, got %q (body: %s)", loc, rec.Body.String())

	j, ok := s.jobs.job(jobID(strings.TrimPrefix(loc, "/jobs/")))
	require.True(t, ok, "the redirect names a job the registry does not hold: %q", loc)

	select {
	case <-j.done():
	case <-time.After(30 * time.Second):
		t.Fatal("job did not finish")
	}
	return j
}

// deployFixtureProfile runs a REAL deploy of the fixture profile, so the
// deployed_files rows a conflict check reads and the game-dir file an
// uninstall removes both genuinely exist. It goes through the plan/apply
// pair (never DeployProfile, the Ruling-10 convenience wrapper) for the same
// reason serve itself does.
func deployFixtureProfile(t *testing.T, s *Server, game *domain.Game) {
	t.Helper()
	plan, err := s.svc.PlanDeploy(t.Context(), game, "default", core.DeployOptions{})
	require.NoError(t, err)
	_, err = s.svc.ApplyDeploy(t.Context(), game, plan, core.DeployOptions{}, nil)
	require.NoError(t, err)
}

// planIDPattern matches the confirm page's hidden plan_id field, whose value
// is newPlanID's 16 random bytes in hex.
var planIDPattern = regexp.MustCompile(`name="plan_id" value="([0-9a-f]{32})"`)

// confirmPlanID drives one flow's ENTRY submission and returns the plan_id
// its confirm page was rendered with - the handle a test's confirm
// submission then redeems, exactly as a browser would.
func confirmPlanID(t *testing.T, s *Server, game *domain.Game, kind, modID string, extra formValues) string {
	t.Helper()
	values := formValues{"game": game.ID, "profile": "default"}
	for k, v := range extra {
		values[k] = v
	}
	rec := postForm(s, "/mods/fake/"+modID+"/"+kind, values)
	require.Equal(t, http.StatusOK, rec.Code, "entry submission did not render a confirm page: %s", rec.Body.String())

	match := planIDPattern.FindStringSubmatch(rec.Body.String())
	require.Len(t, match, 2, "confirm page carried no plan_id")
	return match[1]
}

// postFormMulti is postForm for a form carrying REPEATED fields - the file
// checkboxes #225's install confirm page renders, which a browser submits
// as one "file" field per ticked box.
func postFormMulti(s *Server, target string, values url.Values) *httptest.ResponseRecorder {
	values.Set(csrfFormField, s.csrf.token)
	req := httptest.NewRequest(http.MethodPost, "http://"+internalTestAddr+target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// hiddenFieldPattern extracts one hidden input's value from a rendered
// form, so a test can submit exactly what the page offered rather than a
// hand-built approximation of it.
func hiddenField(t *testing.T, body, name string) string {
	t.Helper()
	re := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`)
	match := re.FindStringSubmatch(body)
	require.Len(t, match, 2, "no hidden %q field in the rendered page", name)
	return match[1]
}

// getPage runs one GET against the server's full handler chain - what a
// browser does after following a mutation's redirect.
func getPage(s *Server, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+internalTestAddr+target, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// newLiveMutationFixtureServer is newMutationFixtureServer bound to a
// WILDCARD address, for the tests that drive the server over real TCP
// through httptest.NewServer (which picks its own ephemeral port): a
// concrete bind would pin a port the test cannot know in advance and 403
// every request. In-process helpers (postForm) still work against it, since
// a wildcard bind admits any IP-literal Host.
func newLiveMutationFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	svc, game := newDeployFixtureService(t)
	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: ":0"}), svc, game
}
