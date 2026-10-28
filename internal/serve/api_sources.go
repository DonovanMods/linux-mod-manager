// api_sources.go is the Setup surface's custom-source half
// (docs/plans/2026-08-31-serve-spa-design.md §Scope: "custom-source
// editor"): list every source the process knows about, read one
// definition's YAML, validate a draft, save it, delete it.
//
// Everything these five routes do lives in internal/app - the rules about
// what a definition may declare, where its file goes, which ids are not
// available, and when a source may be removed are the same rules `lmm
// source add`/`lmm source remove` enforce, because they are literally the
// same functions (app/source_edit.go). This file parses a request, picks a
// status code, and renders.
//
// NOT GAME-SCOPED. Like the game and auth routes A1 landed, none of these
// reads ?game=/?profile=: a source exists before any game maps it, and the
// editor's job is to show EVERY definition including the broken ones that
// registered nowhere. `lmm source list`'s game scoping (its -g/--all
// columns) has no counterpart here - the SPA already holds each game's
// configured source ids from GET /api/v1/games, so it can mark "in use"
// itself without a second scoping rule on the wire.
//
// The list document is `lmm source list --json`'s verbatim, and BOTH writes
// answer with it RE-READ rather than an acknowledgement - the same
// convention the auth routes follow, and for the same reason: one save can
// change more than one row (a definition that was an "error" row becomes a
// real source; a duplicate-id error row disappears), so an acknowledgement
// would leave the SPA to guess.
package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// yamlContentType is what GET /api/v1/sources/{id}/definition answers with.
// The definition is served as TEXT, not wrapped in JSON, because what the
// editor needs is the file's own bytes - comments, key order and all - and
// a round trip through a JSON string would be a re-encoding of exactly the
// thing being edited.
const yamlContentType = "text/yaml; charset=utf-8"

// sourceValidateRequest is POST /api/v1/sources/validate's body: the draft
// YAML, plus `lmm source validate --probe/--id`'s two flags.
type sourceValidateRequest struct {
	// YAML is the definition text as typed. It is validated as content,
	// never written anywhere - a draft has no file yet, and inventing a
	// temp one would put a path the user never created into the report.
	YAML string `json:"yaml"`
	// Probe runs the live smoke test (a directory scan, a manifest fetch,
	// an API call) after a successful validation.
	Probe bool `json:"probe,omitzero"`
	// ProbeID supplies the mod id to probe get_mod with, for an api
	// definition that declares no search endpoint.
	ProbeID string `json:"probe_id,omitzero"`
}

// validate implements the one-check contract the other request bodies follow.
func (r *sourceValidateRequest) validate() error {
	if r.YAML == "" {
		return errors.New(`"yaml" is required`)
	}
	return nil
}

// sourceSaveRequest is PUT /api/v1/sources/{id}'s body: the definition text
// to store. The id is named in the PATH and must equal the document's own
// id - "save THIS source" must not be able to create or overwrite a
// different one.
type sourceSaveRequest struct {
	YAML string `json:"yaml"`
}

// validate implements validatingOptions' one-check contract.
func (r *sourceSaveRequest) validate() error {
	if r.YAML == "" {
		return errors.New(`"yaml" is required`)
	}
	return nil
}

// handleAPISources answers GET /api/v1/sources with []app.SourceInfo - the
// full registry plus every definition that failed to load, construct, or
// collided with an id already taken. It is `lmm source list --json`'s
// document with no game scoping (see the file comment).
func (s *Server) handleAPISources(w http.ResponseWriter, r *http.Request) {
	s.writeSourceList(w, r, http.StatusOK)
}

// writeSourceList assembles and writes the source list, the one document
// three of this file's five routes answer with.
func (s *Server) writeSourceList(w http.ResponseWriter, r *http.Request, status int) {
	infos, err := app.SourceInfos(r.Context(), s.svc, nil, false)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, status, infos)
}

// handleAPISourceDefinition answers GET /api/v1/sources/{id}/definition with
// the raw YAML of a user-defined source's definition file.
//
// A built-in is 404, not 403: it has no definition file at all, so there is
// nothing at this URL - the same answer an id nobody defined gets, which is
// also the honest one for a caller that cannot tell the two apart anyway.
func (s *Server) handleAPISourceDefinition(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	if app.IsBuiltinSourceID(sourceID) {
		s.writeAPIError(w, http.StatusNotFound, errors.New("built-in sources have no definition file"))
		return
	}
	data, err := app.ReadSourceDefinition(s.svc.ConfigDir(), sourceID)
	if err != nil {
		s.writeAPIError(w, s.sourceErrorStatus(err), err)
		return
	}
	w.Header().Set("Content-Type", yamlContentType)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		s.log.Error("api response write failed", "path", r.URL.Path, "err", err)
	}
}

// handleAPISourceValidate answers POST /api/v1/sources/validate with
// app.SourceValidationReport - `lmm source validate --json`'s document,
// including on failure: an invalid definition is the envelope with the
// report as its "details", exactly as the CLI's sourceValidationError
// renders it, so a client parses one shape either way.
//
// A definition failure is 400 (the submitted text is wrong). A PROBE
// failure is 502: the definition is fine and the live call to the source's
// own service did not come back - this server is a proxy for that call and
// did not itself fail, the same classification the games-catalog route
// makes.
func (s *Server) handleAPISourceValidate(w http.ResponseWriter, r *http.Request) {
	var req sourceValidateRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	report, def, err := app.ValidateSourceContent([]byte(req.YAML))
	if err != nil {
		s.writeReportError(w, http.StatusBadRequest, err, report)
		return
	}
	if !req.Probe {
		s.writeJSON(w, http.StatusOK, report)
		return
	}

	summary, err := app.ProbeSource(r.Context(), s.svc, def, req.ProbeID)
	if err != nil {
		report.Probe = &app.SourceProbeResult{Error: err.Error()}
		s.writeReportError(w, http.StatusBadGateway, err, report)
		return
	}
	report.Probe = &app.SourceProbeResult{OK: true, Summary: summary}
	s.writeJSON(w, http.StatusOK, report)
}

// handleAPISourceSave answers PUT /api/v1/sources/{id}: validate the
// submitted definition, write it under <configDir>/sources, and swap the
// constructed source into the RUNNING registry - so an edit takes effect
// with no restart (app.SaveSourceDefinition owns all of it, including the
// mutation gate the swap takes).
//
// The path id must equal the document's id (400 otherwise) and must not
// name a built-in (409 - the id is already claimed by something this route
// cannot replace). An invalid or unconstructable definition is 400 with the
// validation report as the envelope's details, the same shape the validate
// route answers with, so the editor renders one failure surface.
func (s *Server) handleAPISourceSave(w http.ResponseWriter, r *http.Request) {
	var req sourceSaveRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	report, err := app.SaveSourceDefinition(r.Context(), s.svc, r.PathValue("id"), []byte(req.YAML))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, app.ErrBuiltinSourceID) {
			status = http.StatusConflict
		}
		s.writeReportError(w, status, err, report)
		return
	}
	s.writeSourceList(w, r, http.StatusOK)
}

// handleAPISourceDelete answers DELETE /api/v1/sources/{id}: unregister the
// source and remove its definition file, answering with the re-read list.
//
// A built-in and an id nothing defines are both 404 (there is no definition
// at this URL either way). A source configured games still map is 409, with
// core.SourceInUseError naming those games in the envelope's details so the
// SPA can say WHICH games to fix.
func (s *Server) handleAPISourceDelete(w http.ResponseWriter, r *http.Request) {
	if err := app.DeleteSourceDefinition(r.Context(), s.svc, r.PathValue("id")); err != nil {
		s.writeAPIError(w, s.sourceErrorStatus(err), err)
		return
	}
	s.writeSourceList(w, r, http.StatusOK)
}

// sourceErrorStatus classifies a source-definition failure that is NOT a
// validation failure: a missing definition (and a built-in, which has none)
// is 404, a source games still map is 409, anything else is this server's
// own problem.
func (s *Server) sourceErrorStatus(err error) int {
	var inUse *core.SourceInUseError
	switch {
	case errors.Is(err, app.ErrSourceDefinitionNotFound), errors.Is(err, app.ErrBuiltinSourceID):
		return http.StatusNotFound
	case errors.As(err, &inUse):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// writeReportError writes the {"error","details"} envelope with report as
// its details - the shape `lmm source validate --json` fails with
// (cmd/lmm's sourceValidationError), reproduced here because the error
// values app returns carry no Details() of their own: the report is
// assembled alongside them, not inside them.
func (s *Server) writeReportError(w http.ResponseWriter, status int, err error, report *app.SourceValidationReport) {
	s.log.Debug("api error", "status", status, "err", err)
	s.writeJSON(w, status, apiErrorEnvelope{Error: err.Error(), Details: report})
}
