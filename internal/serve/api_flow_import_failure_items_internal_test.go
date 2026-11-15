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

// #660 (owner ruling, replacing all-or-nothing): a collection import whose
// items fail installs what it can and leaves the rest to finish. Its job
// SUCCEEDS - its tally carries the items left to do, which the web UI
// shows as needing attention - and the result lists them, each with its
// action; GET /api/v1/profiles/{name}/todos is the same list.
func TestAPIFlow_ProfileImportNexus_FailuresAreTodosOnASucceededJob(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	// A Premium account installs; the double knows neither required item,
	// so both fail.
	seedNexusCollection(t, svc, game, source.TierPremium)

	planID, _ := planFlow(t, s, game, "profile_import", `{"nexus_collection":"abcdef"}`)
	j := startFlowJob(t, s, planID, `{"install":true}`)
	status := j.status()
	require.Equal(t, jobSucceeded, status.State, "%v", status.Error)
	require.NotNil(t, status.Tally)
	assert.Equal(t, 2, status.Tally.Todos)
	assert.Equal(t, 2, status.Tally.Total)
	assert.Equal(t, 2, status.Tally.Failed)
	assert.Equal(t, game.ID, status.Game, "the job names the game its plan was made for")
	assert.NotEmpty(t, status.Profile, "and the profile")
	assert.Equal(t, "Three Mods", status.Collection, "and the collection it imports")

	doc, ok := status.Result.(*core.ProfileImportResult)
	require.True(t, ok, "%T", status.Result)
	require.Len(t, doc.Todos, 2)
	assert.Equal(t, "10", doc.Todos[0].ModID)
	assert.Equal(t, "11", doc.Todos[1].ModID)
	assert.NotEmpty(t, doc.Todos[0].Action)

	rec := doAPI(s, http.MethodGet, "/api/v1/profiles/Three%20Mods/todos?game="+game.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.CollectionTodoReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	assert.Equal(t, doc.Todos, report.Todos)
	assert.Equal(t, 2, report.Total)
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
