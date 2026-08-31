package serve

// Task 8 flow 2: uninstall (docs/plans/2026-08-30-serve-impl.md Task 8).
// PlanUninstall -> a confirm page showing the files, the merged artifact and
// the #226 options -> the Apply as a job. Every assertion here is on the end
// state: the database row, the profile's load order, the deployed tree and
// the cache entry.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_ModUninstall_EntryPostRendersTheConfirmPage is the RED test for
// the Plan half: the /mods row's uninstall form must answer with the plan,
// not with a mutation.
func TestServer_ModUninstall_EntryPostRendersTheConfirmPage(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	deployFixtureProfile(t, s, game)

	rec := postForm(s, "/mods/fake/m1/uninstall", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Uninstall")
	assert.Contains(t, body, "Mod One")
	assert.Contains(t, body, deployFixtureFile, "the confirm page must list the files that would be removed")
	assert.Contains(t, body, `name="keep_cache"`, "#226: the confirm form offers the keep-cache option")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)

	// Nothing has happened yet: a plan is a preview.
	_, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err)
	assert.FileExists(t, deployedFixturePath(game))
}

// TestServer_ModUninstall_ConfirmRunsTheJobAndRemovesEverything is the Apply
// half: the confirm form's plan_id is redeemed, the job runs, and the mod is
// gone from the database, the profile and the game directory.
func TestServer_ModUninstall_ConfirmRunsTheJobAndRemovesEverything(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	deployFixtureProfile(t, s, game)
	pid := confirmPlanID(t, s, game, "uninstall", nil)

	rec := postForm(s, "/mods/fake/m1/uninstall", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})

	require.Equal(t, http.StatusSeeOther, rec.Code)
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	assert.NoFileExists(t, deployedFixturePath(game))

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "the profile's load order must lose the ref too")

	assert.False(t, svc.GetGameCache(game).Exists(game.ID, "fake", "m1", "1.0"),
		"without keep_cache the cache entry is deleted")
}

// TestServer_ModUninstall_KeepCacheOptionIsHonoured pins #226: the confirm
// form's checkbox maps to UninstallOptions.KeepCache, and the cache entry
// survives when it is ticked.
func TestServer_ModUninstall_KeepCacheOptionIsHonoured(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	deployFixtureProfile(t, s, game)
	pid := confirmPlanID(t, s, game, "uninstall", nil)

	rec := postForm(s, "/mods/fake/m1/uninstall", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid, "keep_cache": "1",
	})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	assert.True(t, svc.GetGameCache(game).Exists(game.ID, "fake", "m1", "1.0"),
		"keep_cache must leave the cache entry in place")
}

// TestServer_ModUninstall_SyncFallback_MutatesIdentically is the no-JS
// fallback: the Apply runs inline and the result page renders, reaching the
// same end state as the job path.
func TestServer_ModUninstall_SyncFallback_MutatesIdentically(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	deployFixtureProfile(t, s, game)
	pid := confirmPlanID(t, s, game, "uninstall", nil)

	rec := postForm(s, "/mods/fake/m1/uninstall?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Done.")

	_, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	assert.NoFileExists(t, deployedFixturePath(game))
}

// TestServer_ModUninstall_UsedPlan_RendersAFreshConfirmPage is the plan
// store's single-use rule seen from the browser: a double-submitted confirm
// form re-plans and asks again rather than failing or applying twice.
func TestServer_ModUninstall_UsedPlan_RendersAFreshConfirmPage(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	deployFixtureProfile(t, s, game)
	pid := confirmPlanID(t, s, game, "uninstall", nil)

	// Consume the plan without applying it, so the second submission finds
	// exactly the state a re-submitted form finds.
	_, err := s.plans.Take(planID(pid))
	require.NoError(t, err)

	rec := postForm(s, "/mods/fake/m1/uninstall", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "freshly computed plan")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)

	_, err = svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err, "a re-plan must not have applied anything")
}

// TestServer_ModUninstall_WithoutCSRF_IsRefused pins the CSRF rule on the
// uninstall route, entry and confirm submissions alike.
func TestServer_ModUninstall_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	deployFixtureProfile(t, s, game)

	rec := postFormWithoutCSRF(s, "/mods/fake/m1/uninstall", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusForbidden, rec.Code)

	_, err := svc.GetInstalledMod(t.Context(), "fake", "m1", game.ID, "default")
	require.NoError(t, err)
}
