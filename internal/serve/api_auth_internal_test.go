package serve

// httptest coverage for the Setup surface's credential routes
// (api_auth.go), including the two SECURITY pins this surface exists to
// keep: a submitted API key must never reach a log line, an error message,
// or a response body.
//
// Every source here is a fake. Nothing in this file may call NexusMods or
// CurseForge, and the API-key environment variables .envrc exports are
// overridden per-test (sandboxEnv/t.Setenv) so a developer's real key can
// never leak into an assertion or a stored token.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// theSubmittedKey is the secret every test in this file posts. It is
// deliberately a single distinctive token so an assertion that it is
// ABSENT from a log or an envelope cannot pass by accident.
const theSubmittedKey = "sk-live-CANARY-9f3b2a71"

// authFixtureSource is an auth-capable fake with no live validator - the
// "stored, validated on first use" shape (a custom source).
type authFixtureSource struct {
	fixtureSource
	id string
}

func (a *authFixtureSource) ID() string   { return a.id }
func (a *authFixtureSource) Name() string { return "Auth " + a.id }
func (a *authFixtureSource) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Auth: true}
}

// authValidatingSource adds a live source.KeyValidator whose verdict the
// test controls.
type authValidatingSource struct {
	authFixtureSource
	err error
}

func (a *authValidatingSource) ValidateKey(context.Context, string) error { return a.err }

// authlessSource declares no auth capability at all - the 404 case.
type authlessSource struct{ fixtureSource }

func (*authlessSource) ID() string   { return "authless" }
func (*authlessSource) Name() string { return "Authless" }
func (*authlessSource) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true}
}

var (
	_ source.ModSource    = (*authFixtureSource)(nil)
	_ source.KeyValidator = (*authValidatingSource)(nil)
	_ source.ModSource    = (*authlessSource)(nil)
)

// newAuthServer builds a Server with a captured log, so every test can
// assert on what the request logging actually wrote.
func newAuthServer(t *testing.T, sources ...source.ModSource) (*Server, *bytes.Buffer) {
	t.Helper()
	sandboxEnv(t)
	// .envrc exports real NEXUSMODS_API_KEY/CURSEFORGE_API_KEY; blank them
	// so app.AuthStatus can never report a developer's live credential.
	t.Setenv("NEXUSMODS_API_KEY", "")
	t.Setenv("CURSEFORGE_API_KEY", "")

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	for _, src := range sources {
		svc.RegisterSource(src)
	}

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(t.Context(), svc, log, Options{Addr: internalTestAddr}), &logs
}

// submittedKeyFingerprint recomputes what a status document is allowed to
// say about theSubmittedKey: the first 8 hex of its SHA-256 (#79). Spelled
// out here rather than imported, because internal/serve does not depend on
// the storage layer and its tests should not either.
func submittedKeyFingerprint() string {
	sum := sha256.Sum256([]byte(theSubmittedKey))
	return hex.EncodeToString(sum[:])[:8]
}

func decodeAuthReport(t *testing.T, body []byte) app.AuthStatusReport {
	t.Helper()
	var report app.AuthStatusReport
	require.NoError(t, json.Unmarshal(body, &report, json.RejectUnknownMembers(true)))
	return report
}

// TestAPIAuth_ReportsEveryAuthCapableSource pins the read: the exact
// document `lmm auth status --json` emits, with a row per registered
// auth-capable source and none for a source that declares no auth.
func TestAPIAuth_ReportsEveryAuthCapableSource(t *testing.T) {
	s, _ := newAuthServer(t, &authFixtureSource{id: "acme"}, &authlessSource{})

	rec := doAPI(s, http.MethodGet, "/api/v1/auth", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	report := decodeAuthReport(t, rec.Body.Bytes())
	require.Len(t, report.Sources, 1)
	assert.Equal(t, "acme", report.Sources[0].ID)
	assert.False(t, report.Sources[0].Authenticated)

	want, err := app.AuthStatus(t.Context(), s.svc)
	require.NoError(t, err)
	requireEncodesLikeInternal(t, rec.Body.Bytes(), want)
}

// TestAPIAuthLogin_ReportsFingerprintOnly drives a login end to end and
// asserts the END STATE - the token is in the DB - alongside the re-read
// report, which shows the source authenticated and identifies the stored
// key by FINGERPRINT only (#79: it is encrypted at rest and never reaches
// the wire, so no masked form exists for it). Named for what it asserts:
// it used to be ...StoresAndReportsMasked, which is the opposite of the
// assertion it now carries (review, Minor 9).
func TestAPIAuthLogin_ReportsFingerprintOnly(t *testing.T) {
	s, _ := newAuthServer(t, &authFixtureSource{id: "acme"})

	rec := doAPI(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+theSubmittedKey+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	report := decodeAuthReport(t, rec.Body.Bytes())
	require.Len(t, report.Sources, 1)
	assert.True(t, report.Sources[0].Authenticated)
	assert.Equal(t, "stored", report.Sources[0].Via)
	assert.Equal(t, submittedKeyFingerprint(), report.Sources[0].KeyFingerprint)
	assert.Empty(t, report.Sources[0].KeyMasked)
	assert.NotContains(t, rec.Body.String(), theSubmittedKey, "the response must never carry the key itself")

	token, err := s.svc.GetSourceToken(t.Context(), "acme")
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, theSubmittedKey, token.APIKey)
}

// TestAPIAuthLogin_ValidatorAcceptsThenStores pins that a source WITH a
// live validator stores only after it passes.
func TestAPIAuthLogin_ValidatorAcceptsThenStores(t *testing.T) {
	s, _ := newAuthServer(t, &authValidatingSource{authFixtureSource: authFixtureSource{id: "acme"}})

	rec := doAPI(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+theSubmittedKey+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	token, err := s.svc.GetSourceToken(t.Context(), "acme")
	require.NoError(t, err)
	require.NotNil(t, token)
}

// TestAPIAuthLogin_RejectedKeyIsNeverStored is the gate this route exists
// for: a validator that says no means nothing is written, and the 400
// carries the validator's own message.
func TestAPIAuthLogin_RejectedKeyIsNeverStored(t *testing.T) {
	s, _ := newAuthServer(t, &authValidatingSource{
		authFixtureSource: authFixtureSource{id: "acme"},
		err:               errors.New("key rejected by the source"),
	})

	rec := doAPI(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+theSubmittedKey+`"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "key rejected by the source")

	token, err := s.svc.GetSourceToken(t.Context(), "acme")
	require.NoError(t, err)
	assert.Nil(t, token, "a key a validator refused must never be stored")
}

// TestAPIAuthLogin_ValidatorTransportFailureIs502 pins the honest split: a
// validator that could not reach its service learned nothing about the
// key, so the answer is 502 (upstream), not 400 (your key is bad).
func TestAPIAuthLogin_ValidatorTransportFailureIs502(t *testing.T) {
	s, _ := newAuthServer(t, &authValidatingSource{
		authFixtureSource: authFixtureSource{id: "acme"},
		err:               &url.Error{Op: "Get", URL: "https://example.invalid", Err: errors.New("dial tcp: no such host")},
	})

	rec := doAPI(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+theSubmittedKey+`"}`)
	assert.Equal(t, http.StatusBadGateway, rec.Code, "body: %s", rec.Body.String())

	token, err := s.svc.GetSourceToken(t.Context(), "acme")
	require.NoError(t, err)
	assert.Nil(t, token, "a key no validator could check must never be stored")
}

// TestAPIAuthLogin_AuthRequiredVerdictIs400 pins the other validator
// verdict: a source wrapping domain.ErrAuthRequired is stating plainly
// that the key is not accepted.
func TestAPIAuthLogin_AuthRequiredVerdictIs400(t *testing.T) {
	s, _ := newAuthServer(t, &authValidatingSource{
		authFixtureSource: authFixtureSource{id: "acme"},
		err:               domain.ErrAuthRequired,
	})

	rec := doAPI(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+theSubmittedKey+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAPIAuthLogin_Refusals pins the input and identity failures: an empty
// key, an unknown source, a source that declares no auth, and a typo'd
// member.
func TestAPIAuthLogin_Refusals(t *testing.T) {
	tests := []struct {
		name, target, body string
		want               int
	}{
		{"empty key", "/api/v1/auth/acme", `{"api_key":""}`, http.StatusBadRequest},
		{"unknown member", "/api/v1/auth/acme", `{"apikey":"x"}`, http.StatusBadRequest},
		{"unknown source", "/api/v1/auth/nope", `{"api_key":"x"}`, http.StatusNotFound},
		{"source declares no auth", "/api/v1/auth/authless", `{"api_key":"x"}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newAuthServer(t, &authFixtureSource{id: "acme"}, &authlessSource{})
			rec := doAPI(s, http.MethodPost, tt.target, tt.body)
			assert.Equal(t, tt.want, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// TestAPIAuthLogin_RequiresCSRF pins that storing a credential is guarded
// like every other state-changing route.
func TestAPIAuthLogin_RequiresCSRF(t *testing.T) {
	s, _ := newAuthServer(t, &authFixtureSource{id: "acme"})
	rec := doAPIWithoutCSRF(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+theSubmittedKey+`"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAPIAuthLogout_RemovesTheStoredKey pins the delete and its re-read
// report.
func TestAPIAuthLogout_RemovesTheStoredKey(t *testing.T) {
	s, _ := newAuthServer(t, &authFixtureSource{id: "acme"})
	require.NoError(t, s.svc.SaveSourceToken(t.Context(), "acme", theSubmittedKey))

	rec := doAPI(s, http.MethodDelete, "/api/v1/auth/acme", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	report := decodeAuthReport(t, rec.Body.Bytes())
	require.Len(t, report.Sources, 1)
	assert.False(t, report.Sources[0].Authenticated)

	token, err := s.svc.GetSourceToken(t.Context(), "acme")
	require.NoError(t, err)
	assert.Nil(t, token)
}

// TestAPIAuthLogout_RemovesAnOrphanedToken pins the deliberate widening
// over "auth-capable only": AuthStatusReport lists orphaned tokens with a
// remove-this remedy, so this route must be able to remove one - exactly
// as `lmm auth logout` can. The source here is not registered at all.
func TestAPIAuthLogout_RemovesAnOrphanedToken(t *testing.T) {
	s, _ := newAuthServer(t, &authFixtureSource{id: "acme"})
	require.NoError(t, s.svc.SaveSourceToken(t.Context(), "ghost", theSubmittedKey))

	before := decodeAuthReport(t, doAPI(s, http.MethodGet, "/api/v1/auth", "").Body.Bytes())
	require.Len(t, before.Orphaned, 1)
	require.Equal(t, "ghost", before.Orphaned[0].ID)

	rec := doAPI(s, http.MethodDelete, "/api/v1/auth/ghost", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Empty(t, decodeAuthReport(t, rec.Body.Bytes()).Orphaned)
}

// TestAPIAuthLogout_UnknownSourceWithNoTokenIs404 pins the other half of
// that rule: a name that is neither auth-capable nor holds a token is a
// 404, not a silent success.
func TestAPIAuthLogout_UnknownSourceWithNoTokenIs404(t *testing.T) {
	s, _ := newAuthServer(t, &authFixtureSource{id: "acme"})
	rec := doAPI(s, http.MethodDelete, "/api/v1/auth/nope", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestAPIAuthLogout_RequiresCSRF pins the delete's guard.
func TestAPIAuthLogout_RequiresCSRF(t *testing.T) {
	s, _ := newAuthServer(t, &authFixtureSource{id: "acme"})
	rec := doAPIWithoutCSRF(s, http.MethodDelete, "/api/v1/auth/acme", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestAuthRequestLoggingNeverCarriesTheKey is this surface's SECURITY
// ratchet, and it covers both halves of the promise at once: across a
// successful login, a validator-rejected one, and a read, the submitted key
// must appear in NO log line and in NO response body.
//
// It is the request-logging middleware's shape that makes the log half
// true - it records method, path, status and duration, never the body - so
// this test is also the pin that stops a future "log the request body for
// debugging" change from silently exfiltrating every credential the Setup
// surface handles.
func TestAuthRequestLoggingNeverCarriesTheKey(t *testing.T) {
	s, logs := newAuthServer(t, &authFixtureSource{id: "acme"},
		&authValidatingSource{authFixtureSource: authFixtureSource{id: "picky"}, err: errors.New("key rejected")})

	bodies := []string{
		doAPI(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+theSubmittedKey+`"}`).Body.String(),
		doAPI(s, http.MethodPost, "/api/v1/auth/picky", `{"api_key":"`+theSubmittedKey+`"}`).Body.String(),
		doAPI(s, http.MethodGet, "/api/v1/auth", "").Body.String(),
		doAPI(s, http.MethodDelete, "/api/v1/auth/acme", "").Body.String(),
	}

	require.NotEmpty(t, logs.String(), "the requests must actually have been logged, or this proves nothing")
	assert.NotContains(t, logs.String(), theSubmittedKey, "the API key must never reach a log line")
	for i, body := range bodies {
		assert.NotContains(t, body, theSubmittedKey, "response %d must never echo the API key", i)
	}
	// The FINGERPRINT is what a response may carry for a stored key (#79) -
	// assert it is there, so this test cannot pass by the report having lost
	// the source entirely.
	assert.Contains(t, bodies[0], submittedKeyFingerprint())
}

// TestAuthRequestLoggingLogsNoBodyAtAll is the structural half of the pin
// above: for a POST whose body is a distinctive non-secret string, nothing
// of that body reaches the log either. Stated separately from the key so a
// future body-logging change fails HERE with an obvious message rather than
// only tripping the credential assertion.
func TestAuthRequestLoggingLogsNoBodyAtAll(t *testing.T) {
	s, logs := newAuthServer(t, &authFixtureSource{id: "acme"})
	marker := "BODY-MARKER-DO-NOT-LOG"

	doAPI(s, http.MethodPost, "/api/v1/auth/acme", `{"api_key":"`+marker+`"}`)

	require.Contains(t, logs.String(), "/api/v1/auth/acme", "the path IS logged")
	assert.NotContains(t, logs.String(), marker, "requestLogging must never log a request body")
	assert.False(t, strings.Contains(logs.String(), "api_key"), "no part of the body's shape may reach the log")
}
