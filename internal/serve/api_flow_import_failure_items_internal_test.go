package serve

// #655: a collection import that fails says which mods failed and why, one
// entry per mod, beside the engine's joined text - in the job's status
// document, the jobs index and the activity stream's job_done frame alike.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIFlow_ProfileImportNexus_FailureListsEveryFailedMod(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	// A Premium account installs; the double knows neither required item,
	// so both refuse and the all-or-nothing import writes nothing.
	seedNexusCollection(t, svc, game, source.TierPremium)

	planID, _ := planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef"}`)
	j := startFlowJob(t, s, planID, `{"install":true}`)
	status := j.status()
	require.Equal(t, jobFailed, status.State)
	require.NotNil(t, status.Error)
	assert.Contains(t, status.Error.Error, "nexusmods:10:", "the joined text is kept")
	require.Len(t, status.Error.Items, 2)
	assert.Equal(t, "nexusmods", status.Error.Items[0].SourceID)
	assert.Equal(t, "10", status.Error.Items[0].ModID)
	assert.Equal(t, "11", status.Error.Items[1].ModID)
	assert.NotEmpty(t, status.Error.Items[0].Reason)
	assert.False(t, status.EndedAt.IsZero(), "a failed job says when it ended")
	assert.Equal(t, game.ID, status.Game, "the job names the game its plan was made for")
	assert.NotEmpty(t, status.Profile, "and the profile")
	assert.Equal(t, "Three Mods", status.Collection, "and the collection it imports")

	// The jobs index carries the same envelope, and ended_at.
	rec := doAPI(s, http.MethodGet, "/api/v1/jobs", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var index struct {
		Jobs []struct {
			State      string `json:"state"`
			EndedAt    string `json:"ended_at"`
			Game       string `json:"game"`
			Collection string `json:"collection"`
			Error      struct {
				Items []core.ItemFailure `json:"items"`
			} `json:"error"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &index))
	require.Len(t, index.Jobs, 1)
	assert.Equal(t, "failed", index.Jobs[0].State)
	assert.NotEmpty(t, index.Jobs[0].EndedAt)
	assert.Equal(t, game.ID, index.Jobs[0].Game)
	assert.Equal(t, "Three Mods", index.Jobs[0].Collection)
	assert.Len(t, index.Jobs[0].Error.Items, 2)
}

// TestAPIFlow_AJobNamesItsGameButNoCollection: a job that imports no
// collection names its game only.
func TestAPIFlow_AJobNamesItsGameButNoCollection(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	j := runFlow(t, s, game, "deploy", "", "")
	status := j.status()
	assert.Equal(t, game.ID, status.Game)
	assert.Empty(t, status.Collection)
}
