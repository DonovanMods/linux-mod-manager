package serve

// #546: a profile switch and a profile apply deploy the profile, so they
// end with the game adapter's launch advice exactly as a deploy does - on
// the stored job result, which the SPA's readout reads, and as a warning
// frame on the job's stream, which the tray renders.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adviceFixtureAdapter advises one launch argument while any mod is
// deployed - a stand-in for d2r, which only Battle.net's settings can pass.
type adviceFixtureAdapter struct{}

func (adviceFixtureAdapter) ID() string    { return "advice-fixture" }
func (adviceFixtureAdapter) Label() string { return "Advice fixture" }
func (adviceFixtureAdapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

func (adviceFixtureAdapter) LaunchAdvice(_ *domain.Game, mods []adapter.DeployedMod, _ string) adapter.Advice {
	if len(mods) == 0 {
		return adapter.Advice{}
	}
	return adapter.Advice{Args: "-fixture", Lines: []string{adviceFixtureLine}}
}

const adviceFixtureLine = "start the game with -fixture"

var _ adapter.LaunchAdvisor = adviceFixtureAdapter{}

// newAdviceFixtureServer is the profiles fixture with its game on the
// advice adapter.
func newAdviceFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newProfilesFixtureServer(t)
	svc.RegisterAdapter(adviceFixtureAdapter{})
	game.Adapter = adviceFixtureAdapter{}.ID()
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return s, svc, game
}

// warningFrames is every warning message on the job's event stream.
func warningFrames(t *testing.T, s *Server, j *job) []string {
	t.Helper()
	rec := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.id)+"/events", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var out []string
	for _, frame := range parseSSE(t, rec.Body.String()) {
		if frame.Event != "warning" {
			continue
		}
		var payload struct {
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame.Data), &payload))
		out = append(out, payload.Data.Message)
	}
	return out
}

func TestFlowSwitch_CarriesTheLaunchAdvice(t *testing.T) {
	s, _, game := newAdviceFixtureServer(t)

	j := runFlow(t, s, game, "switch", `{"profile":"`+switchTargetProfile+`"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.SwitchResult)
	require.True(t, ok)
	assert.Contains(t, result.Warnings, adviceFixtureLine)
	assert.Contains(t, warningFrames(t, s, j), adviceFixtureLine)
}

func TestFlowProfileApply_CarriesTheLaunchAdvice(t *testing.T) {
	s, svc, game := newAdviceFixtureServer(t)
	makeApplyTargetActive(t, svc, game)

	j := runFlow(t, s, game, "profile_apply", `{"profile":"`+applyTargetProfile+`"}`, "")
	// The fixture's unresolvable entry fails the job (#470); what it did
	// deploy is still advised, on the envelope's result.
	status := j.status()
	require.NotNil(t, status.Error)
	result, ok := status.Error.Details.(*core.ProfileApplyResult)
	require.True(t, ok, "got %T", status.Error.Details)
	assert.Contains(t, result.Warnings, adviceFixtureLine)
	assert.Contains(t, warningFrames(t, s, j), adviceFixtureLine)
}
