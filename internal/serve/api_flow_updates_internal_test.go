package serve

// The updates batch over /api/v1 (#74): the whole selection becomes ONE
// plan and ONE job. Three mods are updatable and two are named, so every
// test's real assertion is about the third: its version, its profile ref
// and its deployed file must all be exactly where they started.
//
// Ported from the deleted mutations_updates_internal_test.go - the ticked
// checkbox set the /updates table submitted is the "mods" member the SPA
// sends, and the end-state assertions are unchanged.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updatesBatchBody is the two-mod selection most tests here plan, as the
// domain.ModKey strings updatesPlanRequest takes.
const updatesBatchBody = `{"mods":["` + fixtureSourceID + `:u1","` + fixtureSourceID + `:u2"]}`

// assertUntouchedThirdMod is the control: u3 was never named, so nothing
// about it may have moved.
func assertUntouchedThirdMod(t *testing.T, s *Server, game *domain.Game) {
	t.Helper()
	installed, err := s.svc.GetInstalledMod(t.Context(), fixtureSourceID, "u3", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateFromVersion, installed.Version, "an unselected mod must not be updated")
	assert.Equal(t, updatableContent("u3", updateFromVersion), deployedContent(t, game, updatableModFile("u3")),
		"an unselected mod's deployed file must not be replaced")
}

// TestFlowUpdates_PlanNamesOnlyTheSelectionAndUpdatesNothing is the plan
// half: one document for the whole selection, naming exactly the mods asked
// for and applying nothing.
func TestFlowUpdates_PlanNamesOnlyTheSelectionAndUpdatesNothing(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	_, raw := planFlow(t, s, game, "updates", updatesBatchBody)
	body := string(raw)
	assert.Contains(t, body, "U1")
	assert.Contains(t, body, "U2")
	assert.NotContains(t, body, "U3", "an unselected mod must not appear in the plan")

	for _, modID := range updatableModIDs {
		installed, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, modID, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateFromVersion, installed.Version, "a plan must update nothing")
	}
}

// TestFlowUpdates_JobAppliesOnlyTheSelectedMods is #74's whole point: the
// batch job moves the two selected mods - DB row, profile ref and deployed
// file - and leaves the third exactly as it was.
func TestFlowUpdates_JobAppliesOnlyTheSelectedMods(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	j := runFlow(t, s, game, "updates", updatesBatchBody, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	for _, modID := range []string{"u1", "u2"} {
		installed, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, modID, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateToVersion, installed.Version, "%s was selected and must be updated", modID)
		assert.Equal(t, updatableContent(modID, updateToVersion), deployedContent(t, game, updatableModFile(modID)),
			"%s's deployed file must now resolve to the new version", modID)
		ref := profile.FindRef(fixtureSourceID, modID)
		require.NotNil(t, ref)
		assert.Equal(t, updateToVersion, ref.Version, "%s's profile ref must follow the update", modID)
	}
	assertUntouchedThirdMod(t, s, game)
}

// TestFlowUpdates_EmptySelectionIsRefusedAtTheBoundary pins
// updatesPlanRequest.validate: a batch of nothing is bad input, refused
// before any core call, and it changes nothing.
func TestFlowUpdates_EmptySelectionIsRefusedAtTheBoundary(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)

	rec := doAPI(s, "POST", scoped("/api/v1/plans/updates", game), `{"mods":[]}`)

	require.Equal(t, 400, rec.Code, rec.Body.String())
	assertUntouchedThirdMod(t, s, game)
}

// TestFlowUpdates_LockedSelection_IsRefusedWithoutStoppingTheBatch is the
// lock rule under a batch: one locked mod must not cost the rest of the
// selection its update, which is exactly what cmd/lmm's bulk loop does. The
// refusal is reported on the stored result rather than swallowed.
func TestFlowUpdates_LockedSelection_IsRefusedWithoutStoppingTheBatch(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	_, err := svc.SetModLock(t.Context(), fixtureSourceID, "u3", game.ID, "default", updateFromVersion)
	require.NoError(t, err)

	j := runFlow(t, s, game, "updates",
		`{"mods":["`+fixtureSourceID+`:u1","`+fixtureSourceID+`:u3"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "a refused item must not fail the whole batch")

	updated, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateToVersion, updated.Version, "the unlocked mod still gets its update")

	locked, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u3", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateFromVersion, locked.Version, "the locked mod is left where the lock put it")

	result, ok := j.status().Result.(*updatesBatchResult)
	require.True(t, ok, "the stored result must be the batch document")
	require.Len(t, result.Failed, 1, "the refusal must be reported, not swallowed")
	assert.Contains(t, result.Failed[0].Mod, "u3")
}
