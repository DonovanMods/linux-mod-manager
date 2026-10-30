package serve

// httptest coverage for POST /api/v1/games/{id}/launch (#501, api_launch.go):
// the Play button's one call. Nothing here starts a process: the Service's
// Launcher is fakeLaunchRecorder's, whose Run only records the command line
// it was handed.

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"os/exec"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// launchAppID is the Steam app id the fake launcher finds for every game.
const launchAppID = "892970"

// fakeLaunchRecorder is a core.Launcher with nothing real behind it: every
// program is "found" at /usr/bin/<name>, the session has a display, and Run
// records its command line instead of running it.
type fakeLaunchRecorder struct {
	mu     sync.Mutex
	runs   [][]string
	noUser bool   // no systemd user manager answers
	appID  string // the Steam app id found; "" finds none
}

func (f *fakeLaunchRecorder) launcher() *core.Launcher {
	return &core.Launcher{
		LookPath: func(file string) (string, error) {
			switch file {
			case "systemd-run", "steam":
				return "/usr/bin/" + file, nil
			}
			return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
		},
		Getenv: func(key string) string {
			if key == "DISPLAY" {
				return ":0"
			}
			return ""
		},
		Environ: func() []string { return []string{"DISPLAY=:0"} },
		ManagerEnv: func(context.Context) (map[string]bool, error) {
			if f.noUser {
				return nil, errors.New("Failed to connect to bus")
			}
			return map[string]bool{}, nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.runs = append(f.runs, append([]string{name}, args...))
			return nil, nil
		},
		Detach: func([]string, []string, string) error {
			panic("serve must never start a game detached")
		},
		SteamApp: func(string) (core.SteamApp, bool) {
			return core.SteamApp{AppID: f.appID}, f.appID != ""
		},
		UnitSuffix: func() string { return "abcd1234" },
	}
}

func (f *fakeLaunchRecorder) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.runs...)
}

// newLaunchServer is the flow fixture with the fake launcher registered.
func newLaunchServer(t *testing.T) (*Server, *core.Service, *domain.Game, *fakeLaunchRecorder) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	f := &fakeLaunchRecorder{appID: launchAppID}
	svc.SetLauncher(f.launcher())
	return s, svc, game, f
}

func launchPath(game *domain.Game) string { return "/api/v1/games/" + game.ID + "/launch" }

// A deployed profile launches: the answer is the LaunchResult document, and
// the one command run is systemd-run over Steam's argv - built from the
// game alone.
func TestAPIGameLaunch_StartsTheGameAsASystemdUnit(t *testing.T) {
	s, _, game, f := newLaunchServer(t)
	deployFixtureProfile(t, s, game)

	rec := doAPI(s, http.MethodPost, launchPath(game), `{"profile":"default"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.LaunchResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, game.ID, result.Game)
	assert.Equal(t, "default", result.Profile)
	assert.Equal(t, "steam", result.Method)
	assert.True(t, result.Requested)
	assert.Equal(t, "lmm-launch-g1-abcd1234", result.Unit)
	assert.Empty(t, result.Warnings)

	runs := f.recorded()
	require.Len(t, runs, 1)
	assert.Equal(t, "systemd-run", runs[0][0])
	assert.Equal(t, []string{"--", "/usr/bin/steam", "steam://rungameid/" + launchAppID}, runs[0][len(runs[0])-3:])
	assert.Equal(t, result.Command, runs[0], "the document names the command that ran")
}

// An empty body is the active profile, launched without force.
func TestAPIGameLaunch_EmptyBodyLaunchesTheActiveProfile(t *testing.T) {
	s, _, game, f := newLaunchServer(t)
	deployFixtureProfile(t, s, game)

	rec := doAPI(s, http.MethodPost, launchPath(game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Len(t, f.recorded(), 1)
}

// Undeployed changes are a 409 whose details are LaunchDriftError's - what
// the Play button's Deploy & launch choice renders from - and nothing runs.
func TestAPIGameLaunch_DriftIs409WithItsDetails(t *testing.T) {
	s, _, game, f := newLaunchServer(t)

	rec := doAPI(s, http.MethodPost, launchPath(game), `{"profile":"default"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	var env struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, env.Error, "not deployed")
	assert.Equal(t, map[string]any{
		"game_id": game.ID, "profile": "default", "undeployed": float64(1), "deploy_clears": true,
	}, env.Details)
	assert.Empty(t, f.recorded())
}

// force launches anyway, with the drift as a warning.
func TestAPIGameLaunch_ForceLaunchesAnyway(t *testing.T) {
	s, _, game, f := newLaunchServer(t)

	rec := doAPI(s, http.MethodPost, launchPath(game), `{"profile":"default","force":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result core.LaunchResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, 1, result.Undeployed)
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "launching anyway")
	assert.Len(t, f.recorded(), 1)
}

// With no systemd user manager the web UI never starts the game as its own
// child (it would die with lmm-serve.service): the refusal is
// SystemdUnavailableError's, a 422 nothing will clear by retrying.
func TestAPIGameLaunch_NoSystemdIsRefused(t *testing.T) {
	s, _, game, f := newLaunchServer(t)
	f.noUser = true
	deployFixtureProfile(t, s, game)

	rec := doAPI(s, http.MethodPost, launchPath(game), `{}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "systemd user manager")
	assert.Empty(t, f.recorded())
}

// A game no method can launch is a 422 naming the fix.
func TestAPIGameLaunch_UnconfiguredIs422(t *testing.T) {
	s, _, game, f := newLaunchServer(t)
	f.appID = ""
	deployFixtureProfile(t, s, game)

	rec := doAPI(s, http.MethodPost, launchPath(game), `{}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "launch.command")
	assert.Empty(t, f.recorded())
}

// Only the active profile launches.
func TestAPIGameLaunch_InactiveProfileIs409(t *testing.T) {
	s, svc, game, f := newLaunchServer(t)
	deployFixtureProfile(t, s, game)
	_, err := svc.NewProfileManager().Create(t.Context(), game.ID, "other")
	require.NoError(t, err)

	rec := doAPI(s, http.MethodPost, launchPath(game), `{"profile":"other"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "lmm profile switch")
	assert.Empty(t, f.recorded())
}

func TestAPIGameLaunch_UnknownGameIs404(t *testing.T) {
	s, _, _, f := newLaunchServer(t)

	rec := doAPI(s, http.MethodPost, "/api/v1/games/nope/launch", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Empty(t, f.recorded())
}

// A Service with no Launcher cannot launch at all.
func TestAPIGameLaunch_NoLauncherIs501(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, launchPath(game), `{}`)
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
}

// The POST is a state change like any other: without the CSRF token it is
// refused before anything runs.
func TestAPIGameLaunch_RefusedWithoutCSRF(t *testing.T) {
	s, _, game, f := newLaunchServer(t)
	deployFixtureProfile(t, s, game)

	rec := doAPIWithoutCSRF(s, http.MethodPost, launchPath(game), `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Empty(t, f.recorded())
}

// The body carries a profile and force, and nothing else: a member that
// tries to name a program, its arguments or its environment is refused
// outright, so no request can choose what lmm runs (the command line comes
// only from games.yaml).
func TestAPIGameLaunch_BodyCannotInjectACommand(t *testing.T) {
	for name, body := range map[string]string{
		"argv":        `{"profile":"default","argv":["/bin/sh","-c","touch /tmp/pwned"]}`,
		"command":     `{"profile":"default","command":["/bin/sh"]}`,
		"env":         `{"profile":"default","env":{"LD_PRELOAD":"/tmp/x.so"}}`,
		"launch":      `{"launch":{"command":["/bin/sh"],"via":"command"}}`,
		"steam app":   `{"steam_app_id":"1"}`,
		"dry run":     `{"dry_run":true}`,
		"detached":    `{"allow_detached":true}`,
		"not an obj":  `["/bin/sh"]`,
		"wrong types": `{"profile":["/bin/sh"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, game, f := newLaunchServer(t)
			deployFixtureProfile(t, s, game)

			rec := doAPI(s, http.MethodPost, launchPath(game), body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Empty(t, f.recorded())
		})
	}
}

// GameStatus says how the game would launch once a Launcher is registered:
// the Play button's enabled state.
func TestAPIStatus_CarriesTheLaunchMethod(t *testing.T) {
	s, _, game, _ := newLaunchServer(t)

	rec := doAPI(s, http.MethodGet, "/api/v1/status?game="+game.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var status core.GameStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, core.LaunchInfo{Method: "steam"}, status.Launch)

	// A game lmm cannot launch says why: the disabled button's tooltip.
	s, svc, game := newFlowFixtureServer(t)
	svc.SetLauncher((&fakeLaunchRecorder{}).launcher())
	rec = doAPI(s, http.MethodGet, "/api/v1/status?game="+game.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	status = core.GameStatus{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Empty(t, status.Launch.Method)
	assert.Contains(t, status.Launch.Reason, "launch.command")
}
