package serve

// The rollback flow over /api/v1: PlanRollback -> POST /api/v1/jobs
// (#330's new "rollback" plan kind, kind_rollback.go). Assertions are on the
// end state - the database row and the deployed tree - the same convention
// every other flow test in this file's siblings follows.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rollbackFixtureNewVersion is the version rollbackReadyFlowFixtureServer
// advances the fixture mod to; the base fixture's own "1.0" (deployFixtureFile
// content "pak bytes", testhelpers_internal_test.go) becomes its
// PreviousVersion.
const rollbackFixtureNewVersion = "2.0"

// rollbackReadyFlowFixtureServer is newFlowFixtureServer plus a second
// cache entry for the fixture mod, advanced to rollbackFixtureNewVersion and
// deployed - the precondition ApplyRollback's own guards check (an old
// version's cache entry survives, mirroring
// internal/core/rollback_test.go's seedRollbackReadyMod, whose test-only
// helpers - GetInstallerForTest, ApplyModUpdateForTest - are package
// core_test-only and unreachable from here). Built entirely from public
// Service calls: a second Store, a plain SaveInstalledMod carrying both
// versions, and a REAL deploy through Plan/ApplyDeploy (deployFixtureProfile)
// so the "new" content is genuinely on disk before rollback ever runs.
func rollbackReadyFlowFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)

	require.NoError(t, svc.GetGameCache(game).Store(
		game.ID, fixtureSourceID, "m1", rollbackFixtureNewVersion, deployFixtureFile, []byte("new pak bytes")))

	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:             domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: rollbackFixtureNewVersion, GameID: game.ID},
		ProfileName:     "default",
		UpdatePolicy:    domain.UpdateNotify,
		Enabled:         true,
		LinkMethod:      domain.LinkSymlink,
		PreviousVersion: "1.0",
	}))
	require.NoError(t, svc.NewProfileManager().UpsertMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "m1", Version: rollbackFixtureNewVersion}))

	deployFixtureProfile(t, s, game)
	assert.Equal(t, "new pak bytes", deployedContent(t, game, deployFixtureFile))

	return s, svc, game
}

// rollbackPlanBody names the fixture's one installed mod.
const rollbackPlanBody = `{"source_id":"` + fixtureSourceID + `","mod_id":"m1"}`

// TestFlowRollback_PlanNamesFromAndToVersions is the Plan half: the plan
// names the version being rolled back FROM and the one it would land on,
// and nothing has moved yet.
func TestFlowRollback_PlanNamesFromAndToVersions(t *testing.T) {
	s, svc, game := rollbackReadyFlowFixtureServer(t)

	_, raw := planFlow(t, s, game, "rollback", rollbackPlanBody)
	assert.Contains(t, string(raw), `"from_version": "2.0"`)
	assert.Contains(t, string(raw), `"to_version": "1.0"`)

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, rollbackFixtureNewVersion, mod.Version, "planning must not move anything")
}

// TestFlowRollback_JobRestoresThePreviousVersion is the Apply half: the DB
// row's version swaps back and the previous content is genuinely on disk
// again.
func TestFlowRollback_JobRestoresThePreviousVersion(t *testing.T) {
	s, svc, game := rollbackReadyFlowFixtureServer(t)

	j := runFlow(t, s, game, "rollback", rollbackPlanBody, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version)
	assert.Equal(t, rollbackFixtureNewVersion, mod.PreviousVersion,
		"a rollback swaps the pair, it does not clear it")
	assert.Equal(t, "pak bytes", deployedContent(t, game, deployFixtureFile),
		"the game directory must carry the RESTORED version's content")
}

// TestFlowRollback_NoPreviousVersion_PlanRefuses proves a mod that was
// never updated (the plain fixture mod, no PreviousVersion) has nothing to
// plan - PlanRollback's own error, surfaced through the plan endpoint as an
// ordinary 500 (planRollbackKind wraps no sentinel of its own).
func TestFlowRollback_NoPreviousVersion_PlanRefuses(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, "POST", scoped("/api/v1/plans/rollback", game), rollbackPlanBody)
	assert.NotEqual(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "no previous version")
}
