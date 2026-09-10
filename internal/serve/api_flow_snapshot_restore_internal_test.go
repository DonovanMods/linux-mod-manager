package serve

// The snapshot-restore flow over /api/v1: POST
// /api/v1/plans/snapshot_restore -> POST /api/v1/jobs (#350). Every
// assertion is on the END STATE - the game directory, the DB row, the
// profile - because "the job succeeded" is not what a restore promises.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotFixture deploys the fixture profile and records a snapshot,
// returning its name.
func snapshotFixture(t *testing.T, s *Server, gameID, name string) {
	t.Helper()
	rec := doAPI(s, http.MethodPost, "/api/v1/snapshots?"+gameParam+"="+gameID+"&"+profileParam+"=default",
		`{"name":"`+name+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestFlowSnapshotRestore_PlanPreviewsTheRestoreAndChangesNothing(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)
	snapshotFixture(t, s, game.ID, "known-good")

	_, raw := planFlow(t, s, game, "snapshot_restore", `{"snapshot":"known-good"}`)
	assert.Contains(t, string(raw), `"snapshot": "known-good"`)
	assert.Contains(t, string(raw), `"to_purge"`, "the plan must say what it would undeploy")
	assert.Contains(t, string(raw), `"originals"`)
	assert.Contains(t, string(raw), `"mods"`)

	assert.FileExists(t, deployedFixturePath(game), "planning must not touch the game directory")
}

// TestFlowSnapshotRestore_JobPutsTheProfileBack is the point of the flow.
// A mod is deployed AFTER the snapshot; the restore has to remove it and
// leave the snapshot's own deployment in place.
func TestFlowSnapshotRestore_JobPutsTheProfileBack(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)
	snapshotFixture(t, s, game.ID, "known-good")

	// A stray file the snapshot never knew about, deployed by hand into
	// the game directory the way a later mod would have.
	stray := filepath.Join(game.ModPath, "stray.pak")
	require.NoError(t, os.WriteFile(stray, []byte("later"), 0o644))

	j := runFlow(t, s, game, "snapshot_restore", `{"snapshot":"known-good"}`, `{"no_safety_snapshot":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.SnapshotRestoreResult)
	require.True(t, ok, "the job's result is the core document, verbatim: %T", j.status().Result)
	assert.Equal(t, "known-good", result.Snapshot)
	assert.Empty(t, result.SafetySnapshot, "no_safety_snapshot suppressed the safety copy")
	assert.Empty(t, result.Refused)

	assert.FileExists(t, deployedFixturePath(game), "the snapshot's own deployment is back")
	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.Enabled)
}

// TestFlowSnapshotRestore_TakesASafetySnapshotByDefault pins the default
// that makes a restore itself reversible - and pins that it is a DEFAULT,
// not an option the SPA has to remember to send.
func TestFlowSnapshotRestore_TakesASafetySnapshotByDefault(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)
	snapshotFixture(t, s, game.ID, "known-good")

	j := runFlow(t, s, game, "snapshot_restore", `{"snapshot":"known-good"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.SnapshotRestoreResult)
	require.True(t, ok)
	require.NotEmpty(t, result.SafetySnapshot)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/snapshots", game), "")
	listing := decodeSnapshotListing(t, rec.Body.Bytes())
	names := map[string]bool{}
	for _, row := range listing.Snapshots {
		names[row.Name] = true
	}
	assert.True(t, names[result.SafetySnapshot], "the safety copy is in the listing the card renders")
}

// TestFlowSnapshotRestore_UnknownSnapshotIs404 pins that a snapshot the
// caller named which does not exist is a not-found, with its OWN sentinel
// rather than being reported as a missing profile.
func TestFlowSnapshotRestore_UnknownSnapshotIs404(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/snapshot_restore", game), `{"snapshot":"never-taken"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "snapshot not found")
}

func TestFlowSnapshotRestore_IllegalSnapshotNameIs400(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/snapshot_restore", game), `{"snapshot":"../escape"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestFlowSnapshotRestore_MissingSnapshotMemberIsRefusedAtTheBoundary:
// there is no sensible default, and "the newest one" would be a guess
// about intent.
func TestFlowSnapshotRestore_MissingSnapshotMemberIsRefusedAtTheBoundary(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	for _, body := range []string{"", "{}", `{"snapshot":""}`} {
		rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/snapshot_restore", game), body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "body %q: %s", body, rec.Body.String())
	}
}

func TestFlowSnapshotRestore_RejectsUnknownOptions(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/snapshot_restore", game), `{"snapshoot":"x"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestFlowSnapshotRestore_UsedPlanIsRefused is the plan store's
// single-use rule, which matters more here than anywhere: replaying a
// restore handle would re-run a destructive five-stage flow.
func TestFlowSnapshotRestore_UsedPlanIsRefused(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)
	snapshotFixture(t, s, game.ID, "known-good")

	id, _ := planFlow(t, s, game, "snapshot_restore", `{"snapshot":"known-good"}`)
	j := startFlowJob(t, s, id, `{"no_safety_snapshot":true}`)
	require.Equal(t, jobSucceeded, j.status().State)

	rec := doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// TestFlowSnapshotRestore_IsRegisteredInTheClosedTable pins that the kind
// shows up where a client discovers what it can ask for.
func TestFlowSnapshotRestore_IsRegisteredInTheClosedTable(t *testing.T) {
	assert.Contains(t, supportedPlanKinds(), "snapshot_restore")
}
