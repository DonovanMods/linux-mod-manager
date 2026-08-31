package serve

// Task 9 flow 1: the updates batch (#74) - the /updates checkbox set
// becomes ONE plan, ONE confirm page and ONE job
// (docs/plans/2026-08-30-serve-impl.md Task 9). Three mods are updatable
// and two are ticked, so every test's real assertion is about the third:
// its version, its profile ref and its deployed file must all be exactly
// where they started.

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updatesBatchForm is the two-mod selection every test in this file
// submits, in the shape the /updates checkbox table sends it.
func updatesBatchForm(gameID string, extra url.Values) url.Values {
	values := url.Values{
		"game":    {gameID},
		"profile": {"default"},
		"mod":     {"fake:u1", "fake:u2"},
	}
	for key, vs := range extra {
		values[key] = vs
	}
	return values
}

// assertUntouchedThirdMod is the control: u3 was never ticked, so nothing
// about it may have moved.
func assertUntouchedThirdMod(t *testing.T, s *Server, game *domain.Game) {
	t.Helper()
	installed, err := s.svc.GetInstalledMod(t.Context(), "fake", "u3", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateFromVersion, installed.Version, "an unticked mod must not be updated")
	assert.Equal(t, updatableContent("u3", updateFromVersion), deployedContent(t, game, updatableModFile("u3")),
		"an unticked mod's deployed file must not be replaced")
}

// TestServer_UpdatesApply_EntryPostRendersTheBatchPlan is the plan half:
// one confirm page for the whole selection, naming exactly the ticked mods
// and applying nothing.
func TestServer_UpdatesApply_EntryPostRendersTheBatchPlan(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	rec := postFormMulti(s, "/updates/apply", updatesBatchForm(game.ID, nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "U1")
	assert.Contains(t, body, "U2")
	assert.NotContains(t, body, "U3", "an unticked mod must not appear in the plan")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)
	assert.Contains(t, body, `name="mod" value="fake:u1"`,
		"the selection must survive an Update plan, so it is carried as hidden fields")

	for _, modID := range updatableModIDs {
		installed, err := svc.GetInstalledMod(t.Context(), "fake", modID, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateFromVersion, installed.Version, "a plan must update nothing")
	}
}

// TestServer_UpdatesApply_ConfirmAppliesOnlyTheCheckedMods is #74's whole
// point: the batch job moves the two ticked mods - DB row, profile ref and
// deployed file - and leaves the third exactly as it was.
func TestServer_UpdatesApply_ConfirmAppliesOnlyTheCheckedMods(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	entry := postFormMulti(s, "/updates/apply", updatesBatchForm(game.ID, nil))
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postFormMulti(s, "/updates/apply", updatesBatchForm(game.ID, url.Values{
		"confirm": {"1"}, "plan_id": {hiddenField(t, entry.Body.String(), "plan_id")},
	}))

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	for _, modID := range []string{"u1", "u2"} {
		installed, err := svc.GetInstalledMod(t.Context(), "fake", modID, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateToVersion, installed.Version, "%s was ticked and must be updated", modID)
		assert.Equal(t, updatableContent(modID, updateToVersion), deployedContent(t, game, updatableModFile(modID)),
			"%s's deployed file must now resolve to the new version", modID)
		ref := profile.FindRef("fake", modID)
		require.NotNil(t, ref)
		assert.Equal(t, updateToVersion, ref.Version, "%s's profile ref must follow the update", modID)
	}
	assertUntouchedThirdMod(t, s, game)
}

// TestServer_UpdatesApply_SyncFallback_MutatesIdentically is the no-JS
// path: the same batch, run inline, reaching the same end state.
func TestServer_UpdatesApply_SyncFallback_MutatesIdentically(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	entry := postFormMulti(s, "/updates/apply", updatesBatchForm(game.ID, nil))
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postFormMulti(s, "/updates/apply?sync=1", updatesBatchForm(game.ID, url.Values{
		"confirm": {"1"}, "plan_id": {hiddenField(t, entry.Body.String(), "plan_id")},
	}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Done.")

	for _, modID := range []string{"u1", "u2"} {
		installed, err := svc.GetInstalledMod(t.Context(), "fake", modID, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateToVersion, installed.Version)
	}
	assertUntouchedThirdMod(t, s, game)
}

// TestServer_UpdatesApply_EmptySelection_RendersAFriendlyNoOp: submitting
// the table with nothing ticked is an ordinary thing to do by accident, so
// it says so on a normal page rather than failing or planning a batch of
// nothing.
func TestServer_UpdatesApply_EmptySelection_RendersAFriendlyNoOp(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	rec := postForm(s, "/updates/apply", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "No mods were selected")
	assert.NotContains(t, body, `name="plan_id"`, "nothing was planned, so there is nothing to confirm")

	for _, modID := range updatableModIDs {
		installed, err := svc.GetInstalledMod(t.Context(), "fake", modID, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateFromVersion, installed.Version)
	}
}

// TestServer_UpdatesApply_WithoutCSRF_IsRefused pins the CSRF rule on the
// batch route, and that a refused request updated nothing.
func TestServer_UpdatesApply_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	rec := postFormWithoutCSRF(s, "/updates/apply", formValues{
		"game": game.ID, "profile": "default", "mod": "fake:u1", "confirm": "1",
	})

	require.Equal(t, http.StatusForbidden, rec.Code)
	installed, err := svc.GetInstalledMod(t.Context(), "fake", "u1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateFromVersion, installed.Version)
}

// TestServer_Updates_PageOffersTheBatchForm proves Task 4's disabled shell
// is live: the checkboxes and the submit button are enabled, and the
// "coming in this release" note is gone.
func TestServer_Updates_PageOffersTheBatchForm(t *testing.T) {
	s, _, _ := newUpdatesFixtureServer(t)

	rec := getPage(s, "/updates")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `action="/updates/apply"`)
	assert.Contains(t, body, `name="mod" value="fake:u1"`)
	assert.NotContains(t, body, "disabled")
	assert.NotContains(t, body, "coming in this release")
}

// TestServer_UpdatesApply_LockedSelection_IsRefusedWithoutStoppingTheBatch
// pins the two halves of the batch's own contract about a mod it cannot
// update. The confirm page says so before anything runs, and the run itself
// records the refusal and carries on: one locked mod must not cost the rest
// of the selection its update, which is exactly what cmd/lmm's bulk loop
// does.
func TestServer_UpdatesApply_LockedSelection_IsRefusedWithoutStoppingTheBatch(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	_, err := svc.SetModLock(t.Context(), "fake", "u3", game.ID, "default", updateFromVersion)
	require.NoError(t, err)

	selection := url.Values{
		"game": {game.ID}, "profile": {"default"}, "mod": {"fake:u1", "fake:u3"},
	}
	entry := postFormMulti(s, "/updates/apply", selection)
	require.Equal(t, http.StatusOK, entry.Code, entry.Body.String())
	assert.Contains(t, entry.Body.String(), "Locked, so the update would be refused")

	confirm := url.Values{"confirm": {"1"}, "plan_id": {hiddenField(t, entry.Body.String(), "plan_id")}}
	for key, vs := range selection {
		confirm[key] = vs
	}
	rec := postFormMulti(s, "/updates/apply", confirm)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "a refused item must not fail the whole batch")

	updated, err := svc.GetInstalledMod(t.Context(), "fake", "u1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateToVersion, updated.Version, "the unlocked mod still gets its update")

	locked, err := svc.GetInstalledMod(t.Context(), "fake", "u3", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateFromVersion, locked.Version, "the locked mod is left where the lock put it")

	page := getPage(s, "/jobs/"+string(j.status().ID))
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "Not updated", "the refusal must be reported, not swallowed")
}

// TestServer_UpdatesApply_UpdatePlanKeepsTheSelection is why the confirm
// page carries the ticked set as hidden fields: re-planning from the form
// must compute the SAME batch, not an empty one.
func TestServer_UpdatesApply_UpdatePlanKeepsTheSelection(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	first := postFormMulti(s, "/updates/apply", updatesBatchForm(game.ID, nil))
	require.Equal(t, http.StatusOK, first.Code)
	firstPlanID := hiddenField(t, first.Body.String(), "plan_id")

	// No confirm flag: the "Update plan" button, submitting the page's own
	// fields plus a changed option.
	updated := postFormMulti(s, "/updates/apply", updatesBatchForm(game.ID, url.Values{
		"plan_id": {firstPlanID}, "skip_hooks": {"1"},
	}))

	require.Equal(t, http.StatusOK, updated.Code, "no confirm flag means re-plan, not apply")
	body := updated.Body.String()
	assert.Contains(t, body, "U1", "the re-plan must still be about the ticked mods")
	assert.Contains(t, body, "U2")
	assert.NotContains(t, body, "U3")
	assert.NotEqual(t, firstPlanID, hiddenField(t, body, "plan_id"), "a re-plan issues a fresh handle")

	assertUntouchedThirdMod(t, s, game)
	for _, modID := range []string{"u1", "u2"} {
		installed, err := svc.GetInstalledMod(t.Context(), "fake", modID, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateFromVersion, installed.Version, "Update plan must apply nothing")
	}
}
