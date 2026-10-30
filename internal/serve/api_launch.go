package serve

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// launchTimeout bounds a launch handleAPIGameLaunch starts: the drift check
// and the systemd-run that starts the game's unit, which returns once the
// unit has started rather than when the game exits.
const launchTimeout = 30 * time.Second

// gameLaunchRequest is POST /api/v1/games/{id}/launch's body (#501): which
// profile, and whether to launch past undeployed changes. That is ALL a
// request can say. What runs - the program, its arguments, its environment
// - comes only from games.yaml, and decodeAPIBody refuses any other member,
// so a body naming a command is a 400 rather than something ignored.
type gameLaunchRequest struct {
	// Profile is the profile to launch; empty is the active one. Any other
	// profile is refused (409): only the active one's mods are deployed.
	Profile string `json:"profile,omitempty"`
	// Force launches although the profile has undeployed changes - the
	// drift modal's "Launch anyway".
	Force bool `json:"force,omitzero"`
}

// handleAPIGameLaunch answers POST /api/v1/games/{id}/launch with the
// core.LaunchResult document `lmm launch --json` emits: the web UI's Play
// button.
//
// It is a single-step call rather than a job, like the mod settings: there
// is nothing to preview and nothing to stream, because core.Service.Launch
// returns as soon as the user manager has started the game's unit. Launch
// takes the mutation slot itself, so a launch cannot start the game while a
// deploy job is changing its files (409, ErrOperationInProgress).
//
// The launch runs on the server's own context (the job registry's root, as
// every job does), bounded by launchTimeout, never on the request's: Run
// starts systemd-run with exec.CommandContext, so a tab closed while the
// unit was starting would otherwise kill systemd-run part-way.
//
// AllowDetached stays false: a game this process started itself would sit
// in lmm-serve.service's cgroup and die with it, so a host with no systemd
// user manager is refused (422) rather than given that.
func (s *Server) handleAPIGameLaunch(w http.ResponseWriter, r *http.Request) {
	var req gameLaunchRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	game, err := s.svc.GetGame(r.PathValue("id"))
	if err != nil {
		s.writeAPIError(w, http.StatusNotFound, err)
		return
	}
	ctx, cancel := context.WithTimeout(s.jobs.rootCtx, launchTimeout)
	defer cancel()
	result, err := s.svc.Launch(ctx, game, core.LaunchOptions{Profile: req.Profile, Force: req.Force})
	if err != nil {
		s.writeAPIError(w, launchErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// launchErrorStatus classifies a Launch refusal. Undeployed changes
// (*core.LaunchDriftError, whose details the drift modal renders) and a
// profile that is not the active one are 409s: the request is fine, the
// state is not, and the user can change the state. A game no method can
// launch, no display, and no systemd user manager are 422s - retrying the
// same request changes nothing until the user changes their configuration
// or session, and each error's text names how. A Service with no Launcher
// registered cannot launch at all (501).
func launchErrorStatus(err error) int {
	var drift *core.LaunchDriftError
	var unconfigured *core.LaunchUnconfiguredError
	var environment *core.LaunchEnvironmentError
	switch {
	case errors.As(err, &drift), errors.Is(err, core.ErrProfileNotActive), errors.Is(err, core.ErrActiveProfileUnknown):
		return http.StatusConflict
	case errors.As(err, &unconfigured), errors.As(err, &environment), errors.Is(err, core.ErrSystemdUnavailable):
		return http.StatusUnprocessableEntity
	case errors.Is(err, core.ErrLaunchUnavailable):
		return http.StatusNotImplemented
	case errors.Is(err, domain.ErrGameNotFound), errors.Is(err, domain.ErrProfileNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
