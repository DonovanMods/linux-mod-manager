package core_test

// #590, the owner's widened scope: a source whose IDs can change for the
// same mod (source.UnstableIDSource - Icarus, whose catalog sync deletes
// and re-adds a mod under a new Firestore ID) has a gone mod re-linked
// automatically when exactly one successor is found. Anything less certain
// - several candidates, none, a locked mod, a source with stable IDs - stays
// the suggestion it was.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unstableCatalogSource is successorCatalogSource declaring unstable IDs.
type unstableCatalogSource struct{ *successorCatalogSource }

func (unstableCatalogSource) UnstableIDs() bool { return true }

// newUnstableService is newSuccessorService over a source that declares
// unstable IDs (unstable false registers the stable one), with the gone
// mod's files in the cache and the source mapped on the game.
func newUnstableService(t *testing.T, unstable bool, catalog ...domain.Mod) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, src := newSuccessorService(t, catalog...)
	if unstable {
		svc.RegisterSource(unstableCatalogSource{src})
	}
	game.SourceIDs = map[string]string{"gonecat": game.ID}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "gonecat", "old", "1.0", "old.pak", []byte("pak")))
	return svc, game
}

// autoRelink runs the check and then the automatic re-link over it, the
// way `lmm update` does.
func autoRelink(t *testing.T, svc *core.Service, game *domain.Game, profile string) *core.AutoRelinkResult {
	t.Helper()
	ctx := context.Background()
	installed, err := svc.GetInstalledMods(ctx, game.ID, profile)
	require.NoError(t, err)
	report, err := svc.CheckGameUpdateReport(ctx, game, profile, installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	result, err := svc.AutoRelink(ctx, game, profile, installed, report.CatalogMissing, nil)
	require.NoError(t, err)
	return result
}

func installedIDs(t *testing.T, svc *core.Service, game *domain.Game) []string {
	t.Helper()
	var ids []string
	for _, m := range installedFor(t, svc, game) {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestAutoRelink_UniqueSuccessorOfAnUnstableSourceIsReLinked(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", "1.0"))

	result := autoRelink(t, svc, game, "default")

	require.NotNil(t, result)
	require.Len(t, result.Relinked, 1)
	got := result.Relinked[0]
	assert.Equal(t, "gonecat", got.SourceID)
	assert.Equal(t, "old", got.FromModID)
	assert.Equal(t, "new", got.ToModID)
	assert.Equal(t, "Mega Points moved to a new ID in Gone Catalog; re-linked (old -> new)", got.Message)
	assert.Equal(t, []string{"new"}, installedIDs(t, svc, game))

	// The check after it finds nothing gone.
	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	assert.Empty(t, report.CatalogMissing)
}

// A source with stable IDs only ever gets the suggestion.
func TestAutoRelink_StableSourceIsNeverReLinked(t *testing.T) {
	svc, game := newUnstableService(t, false, catalogMod("new", "Mega Points", "Donovan", "1.0"))

	result := autoRelink(t, svc, game, "default")

	assert.Nil(t, result)
	assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))
}

func TestAutoRelink_SeveralCandidatesAreLeftToTheUser(t *testing.T) {
	svc, game := newUnstableService(t, true,
		catalogMod("new-a", "Mega Points", "Donovan", "1.0"),
		catalogMod("new-b", "Mega Points", "Donovan", "1.0"),
	)

	assert.Nil(t, autoRelink(t, svc, game, "default"))
	assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))
}

func TestAutoRelink_NoCandidateChangesNothing(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("other", "Mega Points", "Someone Else", "1.0"))

	assert.Nil(t, autoRelink(t, svc, game, "default"))
	assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))
}

// A lock is the user saying "leave this mod alone": the suggestion stays,
// and nothing moves.
func TestAutoRelink_LockedModIsNeverReLinked(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", "1.0"))
	_, err := svc.SetModLock(context.Background(), "gonecat", "old", game.ID, "default", "1.0")
	require.NoError(t, err)

	assert.Nil(t, autoRelink(t, svc, game, "default"))
	assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))
}

// A successor at a newer version is re-linked at the version installed:
// those are the files in the cache, so they carry across and nothing reads
// MISSING; the normal update check then offers the newer version.
func TestAutoRelink_NewerSuccessorKeepsTheInstalledVersionAndItsFiles(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", "2.0"))
	ctx := context.Background()

	result := autoRelink(t, svc, game, "default")
	require.NotNil(t, result)
	require.Len(t, result.Relinked, 1)

	installed := installedFor(t, svc, game)
	require.Len(t, installed, 1)
	assert.Equal(t, "new", installed[0].ID)
	assert.Equal(t, "1.0", installed[0].Version, "the recorded version is the files' version")
	assert.True(t, svc.GetGameCache(game).Exists(game.ID, "gonecat", "new", "1.0"), "the cache entry carried across")

	verify, err := svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)
	for _, f := range verify.Findings {
		assert.NotEqual(t, "missing", f.Status, "nothing is missing after an automatic re-link: %+v", f)
		assert.NotEqual(t, "missing_cache", f.Status, "nothing is missing after an automatic re-link: %+v", f)
	}

	updates, err := svc.CheckGameUpdates(ctx, game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, updates, 1, "the newer version is the update check's to offer")
	assert.Equal(t, "new", updates[0].InstalledMod.ID)
}

// An inactive profile's mods are not the ones in the game directory, and
// an automatic pass never touches them.
func TestAutoRelink_NeverOnAnInactiveProfile(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", "1.0"))
	ctx := context.Background()
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "old", SourceID: "gonecat", Name: "Mega Points", Author: "Donovan", Version: "1.0", GameID: game.ID},
		ProfileName: "other", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"f1"},
	}))
	seedProfileWithMod(t, svc, game.ID, "other", "gonecat", "old", "1.0")
	require.ErrorIs(t, svc.CheckDeployTarget(ctx, game.ID, "other", core.VerbUpdateMany), core.ErrProfileNotActive,
		"the premise: other is not the deployed profile")

	assert.Nil(t, autoRelink(t, svc, game, "other"))
	others, err := svc.GetInstalledMods(ctx, game.ID, "other")
	require.NoError(t, err)
	require.Len(t, others, 1)
	assert.Equal(t, "old", others[0].ID)
}

// The pass never queues: while another mutation holds the slot it steps
// aside at once, having changed nothing, and the next check tries again.
func TestBeginAutoRelink_StepsAsideWhileAMutationRuns(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", "1.0"))
	ctx := context.Background()
	installed := installedFor(t, svc, game)
	report, err := svc.CheckGameUpdateReport(ctx, game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)

	plan, err := svc.PlanAutoRelink(ctx, game, "default", installed, report.CatalogMissing)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, []string{"gonecat:old"}, plan.Mods)

	first, err := svc.BeginAutoRelink(ctx, game, plan)
	require.NoError(t, err)
	_, err = svc.BeginAutoRelink(ctx, game, plan)
	require.ErrorIs(t, err, core.ErrOperationInProgress)
	result, err := svc.AutoRelink(ctx, game, "default", installed, report.CatalogMissing, nil)
	require.NoError(t, err, "the convenience form steps aside too, as no result")
	assert.Nil(t, result)

	first.Abandon()
	first.Abandon() // idempotent
	assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))
}

// A check older than the last change is refused as stale.
func TestBeginAutoRelink_ACheckOlderThanTheLastChangeStepsAside(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", "1.0"))
	ctx := context.Background()
	installed := installedFor(t, svc, game)
	report, err := svc.CheckGameUpdateReport(ctx, game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	plan, err := svc.PlanAutoRelink(ctx, game, "default", installed, report.CatalogMissing)
	require.NoError(t, err)

	_, err = svc.DisableMod(ctx, game, "default", "gonecat", "old")
	require.NoError(t, err)

	_, err = svc.BeginAutoRelink(ctx, game, plan)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// Two gone mods that both match the one successor are ambiguous: which of
// them moved is not known, so neither is re-linked.
func TestAutoRelink_TwoGoneModsClaimingOneSuccessorAreLeftToTheUser(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", "1.0"))
	ctx := context.Background()
	src, err := svc.GetSource("gonecat")
	require.NoError(t, err)
	src.(unstableCatalogSource).gone["old2"] = true
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "old2", SourceID: "gonecat", Name: "Mega Points", Author: "Donovan", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"f1"},
	}))
	seedProfileWithMod(t, svc, game.ID, "default", "gonecat", "old2", "1.0")

	assert.Nil(t, autoRelink(t, svc, game, "default"))
	assert.ElementsMatch(t, []string{"old", "old2"}, installedIDs(t, svc, game))
}

// "Exactly one" counts every same-name, same-author candidate before any
// version filtering: a current copy beside an older one is two candidates,
// so nothing is re-linked, and both are offered.
func TestAutoRelink_ACurrentAndAnOlderCandidateAreLeftToTheUser(t *testing.T) {
	svc, game := newUnstableService(t, true,
		catalogMod("older", "Mega Points", "Donovan", "0.9"),
		catalogMod("current", "Mega Points", "Donovan", "1.0"),
	)

	assert.Nil(t, autoRelink(t, svc, game, "default"))
	assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))

	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, report.CatalogMissing, 1)
	ids := []string{}
	for _, s := range report.CatalogMissing[0].Successors {
		ids = append(ids, s.ModID)
	}
	assert.Equal(t, []string{"current", "older"}, ids, "both suggested, the current one first")
}

// A version that is missing, or not a version at all, cannot say the
// candidate is current: it is suggested, never acted on.
func TestAutoRelink_AnIncomparableVersionIsOnlySuggested(t *testing.T) {
	for _, version := range []string{"", "beta"} {
		t.Run("version="+version, func(t *testing.T) {
			svc, game := newUnstableService(t, true, catalogMod("new", "Mega Points", "Donovan", version))

			assert.Nil(t, autoRelink(t, svc, game, "default"))
			assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))
			report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
			require.NoError(t, err)
			require.Len(t, report.CatalogMissing, 1)
			require.Len(t, report.CatalogMissing[0].Successors, 1, "still suggested")
		})
	}
}

// The automatic path compares names and authors strictly - case and
// runs of spacing aside, punctuation and digits as they are - so "Mega
// Points+" is not "Mega Points" and "MegaPoints" is not "Mega Points".
// The loose comparison still suggests them.
func TestAutoRelink_ALooseNameOrAuthorMatchIsOnlySuggested(t *testing.T) {
	for _, tc := range []struct{ name, author string }{
		{"Mega Points+", "Donovan"},
		{"MegaPoints", "Donovan"},
		{"Mega Points", "Don-ovan"},
		{"Mega Points", "Don ovan"},
	} {
		t.Run(tc.name+"/"+tc.author, func(t *testing.T) {
			svc, game := newUnstableService(t, true, catalogMod("new", tc.name, tc.author, "1.0"))

			assert.Nil(t, autoRelink(t, svc, game, "default"))
			assert.Equal(t, []string{"old"}, installedIDs(t, svc, game))
		})
	}
}

// Case and runs of whitespace are not what makes a mod a different one, on
// the strict comparison either.
func TestAutoRelink_CaseAndSpacingStillMatchStrictly(t *testing.T) {
	svc, game := newUnstableService(t, true, catalogMod("new", "  mega   POINTS ", "DONOVAN", "1.0"))

	result := autoRelink(t, svc, game, "default")
	require.NotNil(t, result)
	require.Len(t, result.Relinked, 1)
	assert.Equal(t, []string{"new"}, installedIDs(t, svc, game))
}
