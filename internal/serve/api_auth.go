// api_auth.go is the Setup surface's credential half
// (docs/plans/2026-08-31-serve-spa-design.md §Scope: "all admin tools:
// game add/detect, `auth login`, custom-source management"): read the
// per-source authentication state, store a key, remove one.
//
// Every response - success or failure - is app.AuthStatusReport, the exact
// document `lmm auth status --json` emits. A write answers with the report
// RE-READ rather than an acknowledgement, so the SPA never has to follow a
// mutation with a second request to learn what changed (and so a login
// that also cleared an orphaned-token row shows that in the same
// response).
//
// SECRET HANDLING. The submitted key appears in exactly two places: the
// call to the source's own validator, and the DB write. It is never
// logged (requestLogging records method/path/status/duration and no body -
// pinned by TestAuthRequestLoggingNeverCarriesTheKey), never interpolated
// into an error, and never echoed in a response: the report carries only
// app.MaskAPIKey's masked form. A validator's own error text is passed
// through as the 400's message, so a source must not echo the key into
// it - the built-ins do not.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// authKeyRequest is POST /api/v1/auth/{source}'s body: the API key to
// store, and nothing else. The source is named in the PATH, never in the
// body, for the same reason the profile routes name a profile there:
// "authenticate THIS source" must not be able to retarget another one.
type authKeyRequest struct {
	APIKey string `json:"api_key"`
}

// validate implements the same one-check contract the other request bodies
// follow. What makes a key VALID is the source's own business (a live
// validator where one exists, first use otherwise); only emptiness is
// refused here, because an empty key stored is a source that silently
// stops working.
func (r *authKeyRequest) validate() error {
	if r.APIKey == "" {
		return errors.New(`"api_key" is required`)
	}
	return nil
}

// handleAPIAuth answers GET /api/v1/auth with app.AuthStatusReport: every
// registered auth-capable source's state (stored key, environment
// variable, or neither) plus any stored token belonging to none of them.
func (s *Server) handleAPIAuth(w http.ResponseWriter, r *http.Request) {
	s.writeAuthStatus(w, r.Context(), http.StatusOK)
}

// handleAPIAuthLogin answers POST /api/v1/auth/{source}: validate the key
// live where the source implements source.KeyValidator, store it, and
// answer with the re-read report.
//
// The validator gate is absolute: when a source HAS one, a key that does
// not pass it is never stored. A rejected key is 400 carrying the
// validator's own message; a validator that could not reach its service is
// 502, because nothing was learned about the key and calling it invalid
// would be a lie.
//
// NOTE (documented limitation, not a bug being hidden): storing a key does
// not re-key the ALREADY-REGISTERED source object in this process. Source
// implementations set their key through a plain unsynchronised field write
// (e.g. custom.API.SetAPIKey), so calling it from a request handler while
// another request is mid-search would be a data race. The stored key is
// therefore picked up at the next `lmm serve` start, exactly as it is by
// the next CLI invocation. A concurrency-safe re-key belongs in the source
// layer.
func (s *Server) handleAPIAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req authKeyRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	sourceID := r.PathValue("source")
	ctx := r.Context()

	// app owns the live check: it is the layer that may name
	// source.KeyValidator (internal/serve may not - boundary_test.go), and
	// it is the same seam `lmm auth login` validates through, so the two
	// frontends cannot disagree about when a key was really checked.
	if _, err := app.ValidateSourceKey(ctx, s.svc, sourceID, req.APIKey); err != nil {
		if errors.Is(err, app.ErrSourceNotAuthCapable) {
			s.writeAPIError(w, http.StatusNotFound, err)
			return
		}
		// The validator's message, never the key: the envelope is what
		// the SPA renders next to the field.
		s.writeAPIError(w, authValidationStatus(err), fmt.Errorf("invalid API key: %w", err))
		return
	}

	if err := s.svc.SaveSourceToken(ctx, sourceID, req.APIKey); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, errors.New("storing the credential failed"))
		return
	}
	s.writeAuthStatus(w, ctx, http.StatusOK)
}

// handleAPIAuthLogout answers DELETE /api/v1/auth/{source} with the re-read
// report.
//
// Unlike the login route it accepts any source with a STORED token, not
// only a registered auth-capable one - mirroring `lmm auth logout`, and for
// the same reason: AuthStatusReport's own "orphaned" rows are tokens whose
// source no longer declares auth (or is no longer registered at all), and
// a surface that lists them with "remove this" must be able to remove
// them. A name that is neither auth-capable nor holds a token is 404.
func (s *Server) handleAPIAuthLogout(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source")
	ctx := r.Context()

	if !app.IsAuthCapableSource(s.svc, sourceID) {
		token, err := s.svc.GetSourceToken(ctx, sourceID)
		if err != nil {
			s.writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
		if token == nil {
			s.writeAPIError(w, http.StatusNotFound, fmt.Errorf("no stored credentials for %q and it is not a registered auth-capable source", sourceID))
			return
		}
	}

	if err := s.svc.DeleteSourceToken(ctx, sourceID); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeAuthStatus(w, ctx, http.StatusOK)
}

// writeAuthStatus assembles and writes app.AuthStatusReport, the one
// document every route in this file answers with.
func (s *Server) writeAuthStatus(w http.ResponseWriter, ctx context.Context, status int) {
	report, err := app.AuthStatus(ctx, s.svc)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, status, report)
}

// authValidationStatus classifies a source.KeyValidator failure.
//
// A transport-level failure - the request never got an answer - is 502:
// the key was not rejected, the check simply did not happen, and calling
// it invalid would be a claim nothing supports. Everything else is the
// source's own verdict on the key, which is the caller's problem (400).
// domain.ErrAuthRequired is listed explicitly because a source that wraps
// it is stating that verdict in the clearest available form.
func authValidationStatus(err error) int {
	if errors.Is(err, domain.ErrAuthRequired) {
		return http.StatusBadRequest
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return http.StatusBadGateway
	}
	return http.StatusBadRequest
}
