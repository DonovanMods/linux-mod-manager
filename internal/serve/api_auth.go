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
// into an error, and never echoed in a response.
//
// Since #79 the write itself is encrypted at rest, and no report this file
// answers with can carry one back: a STORED credential appears as
// key_fingerprint (the first 8 hex of its SHA-256) with no masked form at
// all, and app.MaskAPIKey is applied only to a key read from the
// environment - one lmm holds in the clear regardless. (The fingerprint is
// computed over the key, so the storage layer does decrypt each row to
// build it; nothing above db.TokenInfo ever sees the result - review 2.)
// A key file that is missing or unusable is not a per-row condition, so it
// fails this route with the storage layer's own message naming the file and
// the remedy, rather than 200 with every source marked unreadable. A validator's own
// error text is passed through as the 400's message, so a source must not
// echo the key into it - the built-ins do not.
package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

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
	s.writeAuthStatus(w, r.Context(), http.StatusOK, false)
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
// A stored key takes effect IMMEDIATELY: rekeySource swaps a freshly
// constructed source carrying the new credential into the registry, so the
// next search or install uses it with no restart (see rekeySource for the
// one case that still needs one).
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
	s.writeAuthStatus(w, ctx, http.StatusOK, s.rekeySource(ctx, sourceID))
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
	s.writeAuthStatus(w, ctx, http.StatusOK, s.rekeySource(ctx, sourceID))
}

// rekeyGrace bounds how long a credential write waits for the mutation
// gate before giving up on the live swap. core's beginOp serialises
// mutations by BLOCKING, so a re-key attempted during a long install would
// otherwise hold the HTTP request open for the length of that install.
// Five seconds is far longer than an idle server ever needs and far
// shorter than a download.
const rekeyGrace = 5 * time.Second

// rekeySource makes a credential change take effect on the running
// service, best-effort.
//
// The credential itself is already stored (or deleted) by the time this
// runs - that write is the durable effect and the response document
// reflects it either way. What this adds is the live swap: a freshly
// constructed source carrying the new key, put into the registry under the
// mutation gate (app.RekeySource), so nothing has to restart.
//
// A failure is logged, not surfaced as an HTTP error. The two ways it can
// fail are a source that cannot be rebuilt (a definition file deleted
// underneath us) and the gate not coming free within rekeyGrace, and in
// BOTH the honest answer to the caller is the one it already has - the key
// is stored - with the same "picked up at the next start" behaviour lmm had
// before #333. Turning either into an HTTP failure would report a
// credential write that did happen as one that did not.
//
// It DOES reach the caller now, as a fact rather than a failure: this
// returns true when the swap did not take effect, which the response
// document carries as AuthStatusReport.RestartRequired (#334, Unit 7 review
// Minor #6). Before that the response said "authenticated" and the only
// trace of the miss was this WARN, so a user whose next search still used
// the old key had nothing to go on.
//
// A source that was never swapped BECAUSE there was nothing to swap - not
// registered, or not auth-capable, both of which RekeySource reports as
// (false, nil) - needs no restart: nothing about how any request behaves
// depended on it.
func (s *Server) rekeySource(ctx context.Context, sourceID string) (restartRequired bool) {
	ctx, cancel := context.WithTimeout(ctx, rekeyGrace)
	defer cancel()

	switch swapped, err := app.RekeySource(ctx, s.svc, sourceID); {
	case err != nil:
		s.log.Warn("serve: credential stored, but the running source was not re-keyed; restart lmm serve to pick it up",
			"source", sourceID, "err", err)
		return true
	case swapped:
		s.log.Debug("serve: re-keyed the running source", "source", sourceID)
	}
	return false
}

// writeAuthStatus assembles and writes app.AuthStatusReport, the one
// document every route in this file answers with. restartRequired is
// rekeySource's own verdict, stamped onto the report so a caller that just
// changed a credential learns whether the running process actually picked
// it up (#334); a plain read passes false.
func (s *Server) writeAuthStatus(w http.ResponseWriter, ctx context.Context, status int, restartRequired bool) {
	report, err := app.AuthStatus(ctx, s.svc)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	report.RestartRequired = restartRequired
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
