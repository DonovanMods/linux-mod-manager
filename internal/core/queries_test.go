package core_test

// Tests for the read-only query types in internal/core/queries.go - the
// documents `lmm list`, `lmm status`, `lmm search`, `lmm game list` and
// `lmm verify` render (v2 Phase 3 Task 3, #301). Each query is asserted on
// its assembled fields here; its wire shape is pinned separately by
// TestJSONGoldens.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ListMods ---

// TestListMods_ProfileOrderAndIdentity covers the base case: the listing
// carries the game/profile it was asked for and its mods come back in
// OrderByProfile order (profile order, absent-from-profile first), not the
// DB's installed_at order.
func TestListMods_ProfileOrderAndIdentity(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "2.0", true, nil)
	// Profile order is the REVERSE of the install order, so a listing that
	// merely echoed the DB order would fail here.
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "2.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, list)

	assert.Equal(t, "g1", list.GameID)
	assert.Equal(t, "default", list.Profile)
	require.Len(t, list.Mods, 2)
	assert.Equal(t, "b", list.Mods[0].ID)
	assert.Equal(t, "a", list.Mods[1].ID)
	assert.Equal(t, "Mod B", list.Mods[0].Name, "the whole InstalledMod is embedded, not a bare reference")
}

// TestListMods_LockStateFromProfile covers the lock join: lock state lives on
// the profile YAML ref, not the DB row ListMods reads its mods from.
func TestListMods_LockStateFromProfile(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "2.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "2.0")
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), "g1", "default", "src", "a", "1.0"))

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 2)

	byID := map[string]core.ModListing{}
	for _, m := range list.Mods {
		byID[m.ID] = m
	}
	assert.True(t, byID["a"].Locked)
	assert.Equal(t, "1.0", byID["a"].LockedVersion, "the lock's own version, from the profile ref")
	assert.False(t, byID["b"].Locked)
	assert.Empty(t, byID["b"].LockedVersion)
}

// TestListMods_ConvertPaksOnlyForCompileGames covers the tri-state
// ConvertPaks pointer: nil means "pak conversion does not apply here at
// all", which is every mod of a non-DeployCompile game.
func TestListMods_ConvertPaksOnlyForCompileGames(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 1)
	assert.Nil(t, list.Mods[0].ConvertPaks)
}

// TestListMods_MissingProfileYAMLIsNotAnError covers the tolerated case: a
// profile with no YAML on disk yet just means nothing is locked and every
// mod sorts as "absent from the load order" - not a failed listing.
func TestListMods_MissingProfileYAMLIsNotAnError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 1)
	assert.False(t, list.Mods[0].Locked)
}

// TestListMods_EmptyProfileHasEmptyMods pins that a profile with nothing
// installed still yields a listing (not a nil one) whose Mods marshals as
// [], the shape the --json contract promises.
func TestListMods_EmptyProfileHasEmptyMods(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	list, err := svc.ListMods(context.Background(), game, "default")
	require.NoError(t, err)
	require.NotNil(t, list)
	assert.Empty(t, list.Mods)
}

// --- ListProfiles ---

// TestListProfiles_NamesModCountsAndDefault covers the document `lmm
// profile list --json` renders: one ProfileSummary row per profile, in
// ProfileManager.List order, each with its own mod count and default
// marker - the same rows GameStatus.Profiles carries, for a query scoped to
// profiles alone.
func TestListProfiles_NamesModCountsAndDefault(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "2.0")
	_, err := svc.NewProfileManager().Create(ctx, "g1", "survival")
	require.NoError(t, err)
	require.NoError(t, svc.NewProfileManager().SetDefault(ctx, "g1", "default"))

	listing, err := svc.ListProfiles(ctx, "g1")
	require.NoError(t, err)
	require.NotNil(t, listing)
	assert.Equal(t, "g1", listing.GameID)
	require.Len(t, listing.Profiles, 2)

	byName := map[string]core.ProfileSummary{}
	for _, p := range listing.Profiles {
		byName[p.Name] = p
	}
	assert.Equal(t, 2, byName["default"].ModCount)
	assert.True(t, byName["default"].IsDefault)
	assert.Equal(t, 0, byName["survival"].ModCount)
	assert.False(t, byName["survival"].IsDefault)
}

// TestListProfiles_NoProfilesIsEmptyNotNil pins the zero-profiles shape: the
// listing itself is never nil, and its Profiles slice marshals as [].
func TestListProfiles_NoProfilesIsEmptyNotNil(t *testing.T) {
	listing, err := newFlowsTestService(t).ListProfiles(context.Background(), "g1")
	require.NoError(t, err)
	require.NotNil(t, listing)
	assert.Equal(t, "g1", listing.GameID)
	assert.Empty(t, listing.Profiles)
}

// --- Status / GameStatus ---

// TestStatus_GamesByIDWithCountsAndDefault covers the summary `lmm status`
// renders: one row per configured game, ordered by ID (#299), each carrying
// its profile names, its active profile's mod count and whether it is the
// default game.
func TestStatus_GamesByIDWithCountsAndDefault(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	// Saved out of ID order, so a report that echoed insertion (or map)
	// order would fail here.
	zeta := &domain.Game{ID: "zeta", Name: "Zeta", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	alpha := &domain.Game{ID: "alpha", Name: "Alpha", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, zeta))
	require.NoError(t, svc.SaveGame(ctx, alpha))
	require.NoError(t, svc.SetDefaultGame(ctx, "alpha"))

	seedNamedInstalledMod(t, svc, alpha, "src", "a", "Mod A", "1.0", true, nil)
	seedProfileWithMod(t, svc, "alpha", "default", "src", "a", "1.0")
	require.NoError(t, svc.NewProfileManager().SetDefault(context.Background(), "alpha", "default"))

	report, statusErr := svc.Status(ctx)
	require.NoError(t, statusErr)
	require.NotNil(t, report)
	require.Len(t, report.Games, 2)

	assert.Equal(t, "alpha", report.Games[0].ID)
	assert.Equal(t, "zeta", report.Games[1].ID)
	assert.True(t, report.Games[0].IsDefault)
	assert.False(t, report.Games[1].IsDefault)
	assert.Equal(t, []string{"default"}, report.Games[0].Profiles)
	assert.Equal(t, 1, report.Games[0].ModCount)
	assert.Equal(t, 0, report.Games[1].ModCount)
	assert.Equal(t, domain.LinkSymlink, report.Games[0].LinkMethod)
}

// TestStatus_NoGamesIsEmptyNotNil pins the zero-games shape: an empty
// report, whose Games marshals as "[]" rather than "null".
func TestStatus_NoGamesIsEmptyNotNil(t *testing.T) {
	report, statusErr := newFlowsTestService(t).Status(context.Background())
	require.NoError(t, statusErr)
	require.NotNil(t, report)
	assert.Empty(t, report.Games)
}

// TestStatus_ConvertPaksOnlyForCompileGames covers the same tri-state
// ConvertPaks pointer TestListMods_ConvertPaksOnlyForCompileGames pins for
// ModListing (final review, Important #5 / #302): nil means "not
// applicable" for a non-DeployCompile game, non-nil (carrying the game's own
// value) for one that is.
func TestStatus_ConvertPaksOnlyForCompileGames(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	extract := &domain.Game{ID: "extract-game", Name: "Extract", ModPath: t.TempDir()}
	compile := &domain.Game{ID: "compile-game", Name: "Compile", ModPath: t.TempDir(), DeployMode: domain.DeployCompile, ConvertPaks: true}
	require.NoError(t, svc.SaveGame(ctx, extract))
	require.NoError(t, svc.SaveGame(ctx, compile))

	report, statusErr := svc.Status(ctx)
	require.NoError(t, statusErr)
	require.Len(t, report.Games, 2)

	byID := map[string]core.GameSummary{}
	for _, g := range report.Games {
		byID[g.ID] = g
	}
	assert.Nil(t, byID["extract-game"].ConvertPaks)
	require.NotNil(t, byID["compile-game"].ConvertPaks)
	assert.True(t, *byID["compile-game"].ConvertPaks)
}

// TestGameStatus_ActiveProfileDetail covers the per-game detail `lmm status
// --game X` renders: the profile list, the active profile's installed/
// enabled counts, and its never-deployed state.
func TestGameStatus_ActiveProfileDetail(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", InstallPath: t.TempDir(), ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(ctx, game))

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, nil)
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "2.0", false, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "2.0")
	require.NoError(t, svc.NewProfileManager().SetDefault(context.Background(), "g1", "default"))

	st, err := svc.GameStatus(ctx, game)
	require.NoError(t, err)
	require.NotNil(t, st)

	assert.Equal(t, "g1", st.ID)
	assert.Equal(t, "default", st.ActiveProfile)
	assert.Equal(t, 2, st.InstalledModCount)
	assert.Equal(t, 1, st.EnabledModCount)
	assert.Nil(t, st.LastDeploy, "a profile that has never been deployed reports no timestamp")
	assert.Equal(t, 0, st.ConversionFailures)
	require.Len(t, st.Profiles, 1)
	assert.Equal(t, "default", st.Profiles[0].Name)
	assert.Equal(t, 2, st.Profiles[0].ModCount)
	assert.True(t, st.Profiles[0].IsDefault)
}

// TestGameStatus_LinkMethodSource covers the three-level link-method
// resolution the detail view reports: global default, per-game override,
// per-profile override (#155/#81). LinkMethod always stays the GAME-level
// answer; only EffectiveLinkMethod follows the profile.
func TestGameStatus_LinkMethodSource(t *testing.T) {
	ctx := context.Background()

	t.Run("global", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
		require.NoError(t, svc.SaveGame(ctx, game))
		st, err := svc.GameStatus(ctx, game)
		require.NoError(t, err)
		assert.Equal(t, "global", st.LinkMethodSource)
		assert.Equal(t, st.LinkMethod, st.EffectiveLinkMethod)
	})

	t.Run("game", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
		require.NoError(t, svc.SaveGame(ctx, game))
		st, err := svc.GameStatus(ctx, game)
		require.NoError(t, err)
		assert.Equal(t, "game", st.LinkMethodSource)
		assert.Equal(t, domain.LinkCopy, st.LinkMethod)
		assert.Equal(t, domain.LinkCopy, st.EffectiveLinkMethod)
	})

	t.Run("profile", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy, LinkMethodExplicit: true}
		require.NoError(t, svc.SaveGame(ctx, game))
		pm := svc.NewProfileManager()
		_, err := pm.Create(context.Background(), "g1", "default")
		require.NoError(t, err)
		setProfileLinkMethod(t, svc, "g1", "default", domain.LinkHardlink)
		require.NoError(t, pm.SetDefault(context.Background(), "g1", "default"))

		st, err := svc.GameStatus(ctx, game)
		require.NoError(t, err)
		assert.Equal(t, "profile", st.LinkMethodSource)
		assert.Equal(t, domain.LinkCopy, st.LinkMethod, "LinkMethod stays the game-level answer (#155)")
		assert.Equal(t, domain.LinkHardlink, st.EffectiveLinkMethod)
	})
}

// TestGameStatus_NoProfilesIsEmptyNotNil pins the zero-profiles shape: a
// game with no profiles yet reports an empty list (marshalling as "[]") and
// no active profile, not an error.
func TestGameStatus_NoProfilesIsEmptyNotNil(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(ctx, game))

	st, err := svc.GameStatus(ctx, game)
	require.NoError(t, err)
	assert.Empty(t, st.Profiles)
	assert.Empty(t, st.ActiveProfile)
	assert.Equal(t, svc.GetGameCachePath(game), st.ResolvedCachePath)
}

// TestGameStatus_ConvertPaksOnlyForCompileGames is GameStatus's counterpart
// to TestStatus_ConvertPaksOnlyForCompileGames (final review, Important #5 /
// #302).
func TestGameStatus_ConvertPaksOnlyForCompileGames(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()

	extract := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(ctx, extract))
	st, err := svc.GameStatus(ctx, extract)
	require.NoError(t, err)
	assert.Nil(t, st.ConvertPaks)

	compile := &domain.Game{ID: "g2", Name: "Compile Game", ModPath: t.TempDir(), DeployMode: domain.DeployCompile, ConvertPaks: true}
	require.NoError(t, svc.SaveGame(ctx, compile))
	st, err = svc.GameStatus(ctx, compile)
	require.NoError(t, err)
	require.NotNil(t, st.ConvertPaks)
	assert.True(t, *st.ConvertPaks)
}

// errCallerIsProfileManagerGetDefault reports whether the caller of the
// current Err() method (one frame further up, i.e. two frames from HERE) is
// ProfileManager.GetDefault's own top-of-method guard - GetDefault's inner
// pm.List(ctx, gameID) call also invokes ctx.Err(), but that Err() call's
// immediate caller is ProfileManager.List, not GetDefault, so this
// distinguishes GameStatus's own pm.GetDefault gate (queries.go:312) from
// the pm.List read just before it, which must be allowed to pass
// uncancelled for the cancellation to land specifically on the site NEW-5
// fixed.
func errCallerIsProfileManagerGetDefault() bool {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return false
	}
	name := runtime.FuncForPC(pc).Name()
	return strings.HasSuffix(name, "core.(*ProfileManager).GetDefault")
}

// cancelAtGetDefaultGuard is live until GameStatus reaches its own
// pm.GetDefault call, then cancels itself - landing the cancellation
// exactly on the tolerant gate NEW-5 fixed, after the earlier pm.List read
// has already succeeded.
type cancelAtGetDefaultGuard struct {
	context.Context
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *cancelAtGetDefaultGuard) Err() error {
	if errCallerIsProfileManagerGetDefault() && c.fired.CompareAndSwap(false, true) {
		c.cancel()
	}
	return c.Context.Err()
}

// TestGameStatus_CancelledDefaultProfileRead_DoesNotDegradeToNoDefault is
// the class-(C) shape test for the task 18 re-review round-2 NEW-5 finding:
// GameStatus's pm.GetDefault gate treated every error - including a
// cancellation - as "no default profile, that's fine", the same tolerant
// answer Status's identical gate gave before Ruling 16 (C) fixed it at
// queries.go:256. A cancellation says nothing about whether a default
// profile exists, so it must not render an empty ActiveProfile as if it
// were a truthful one.
//
// The fixture has a genuinely-set default profile (asserted via the
// uncancelled fixture check), so the test cannot pass vacuously against a
// gate that would have answered "no default" anyway.
func TestGameStatus_CancelledDefaultProfileRead_DoesNotDegradeToNoDefault(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), "g1", "default"))

	fixture, err := svc.GameStatus(context.Background(), game)
	require.NoError(t, err)
	require.Equal(t, "default", fixture.ActiveProfile, "fixture check: an uncancelled read must resolve the default profile")

	inner, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx := &cancelAtGetDefaultGuard{Context: inner, cancel: cancel}

	st, err := svc.GameStatus(ctx, game)
	require.Error(t, err, "a cancelled read must not answer 'no default profile'")
	assert.ErrorIs(t, err, context.Canceled)
	require.True(t, ctx.fired.Load(), "the cancellation must have landed on the pm.GetDefault gate")
	assert.Nil(t, st)
}

// --- Search ---

// TestSearch_MarksInstalledAndCountsResults covers the join `lmm search`
// needs: every hit carries whether it is already installed in the profile
// searched against (source-aware - a mod ID is only unique within its
// source), and the report names the game/query it answers.
func TestSearch_MarksInstalledAndCountsResults(t *testing.T) {
	src := &searchStubSource{id: "alpha", result: source.SearchResult{
		Mods: mods("alpha", "installed-one", "not-installed"), TotalCount: 2,
	}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, src)
	seedNamedInstalledMod(t, svc, game, "alpha", "installed-one", "Installed One", "1.0", true, nil)
	// Same mod ID under a DIFFERENT source must not count as installed.
	seedNamedInstalledMod(t, svc, game, "beta", "not-installed", "Other Source", "1.0", true, nil)

	report, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{})
	require.NoError(t, err)
	require.NotNil(t, report)

	assert.Equal(t, game.ID, report.GameID)
	assert.Equal(t, "query", report.Query)
	assert.Equal(t, 2, report.TotalResults)
	require.Len(t, report.Mods, 2)

	byID := map[string]core.SearchHit{}
	for _, hit := range report.Mods {
		byID[hit.ID] = hit
	}
	assert.True(t, byID["installed-one"].Installed)
	assert.False(t, byID["not-installed"].Installed, "an ID installed under another source is not this hit")
	assert.Equal(t, "installed-one", byID["installed-one"].Name, "the whole domain.Mod is embedded")
}

// TestSearch_AggregateWarningsAndAttemptedCount covers the aggregate path's
// two extra signals: a per-source failure is a warning (not an error), and
// AttemptedCount reports how many sources were actually searched - zero
// meaning none of them can search at all (#58 item 3), which a caller
// cannot otherwise tell from a genuine empty result.
func TestSearch_AggregateWarningsAndAttemptedCount(t *testing.T) {
	ok := &searchStubSource{id: "alpha", result: source.SearchResult{Mods: mods("alpha", "found"), TotalCount: 1}}
	broken := &searchStubSource{id: "beta", err: errors.New("network down")}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": "", "beta": ""}, ok, broken)

	report, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{})
	require.NoError(t, err)
	require.Len(t, report.Mods, 1)
	require.Len(t, report.Warnings, 1)
	assert.Equal(t, "beta", report.Warnings[0].SourceID)
	assert.Error(t, report.Warnings[0].Err, "the structured warning keeps its error, not a pre-formatted line")
	assert.Equal(t, 2, report.AttemptedCount)
}

// TestSearch_SingleSourceHasNoAttemptedCount pins that a named --source
// reports AttemptedCount -1 ("not applicable"): that path either resolves to
// exactly one source or fails outright, with no "zero capable sources" case
// of its own to distinguish.
func TestSearch_SingleSourceHasNoAttemptedCount(t *testing.T) {
	src := &searchStubSource{id: "alpha", result: source.SearchResult{Mods: mods("alpha", "found"), TotalCount: 1}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, src)

	report, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{SourceID: "alpha"})
	require.NoError(t, err)
	require.Len(t, report.Mods, 1)
	assert.Equal(t, -1, report.AttemptedCount)
	assert.Empty(t, report.Warnings)
}

// TestSearch_NoResultsIsEmptyNotNil pins the zero-hit shape: an empty
// report (Mods marshalling as "[]"), not an error and not a nil slice.
func TestSearch_NoResultsIsEmptyNotNil(t *testing.T) {
	src := &searchStubSource{id: "alpha", result: source.SearchResult{}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, src)

	report, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{})
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Empty(t, report.Mods)
	assert.Equal(t, 0, report.TotalResults)
}

// TestSearch_ErrorsPropagateUnwrapped pins that a source failure reaches the
// caller as-is: the CLI matches source.ErrNotSupported/domain.ErrAuthRequired
// on it to choose its own notice, so core must not bury it under a wrapper
// of its own.
func TestSearch_ErrorsPropagateUnwrapped(t *testing.T) {
	src := &searchStubSource{id: "alpha", err: fmt.Errorf("searching: %w", source.ErrNotSupported)}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, src)

	_, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{SourceID: "alpha"})
	require.Error(t, err)
	assert.ErrorIs(t, err, source.ErrNotSupported)
}

// TestSearch_LimitCapsMods pins SearchOptions.Limit (final review, Important
// #3 / #302): the cap applies to the RETURNED Mods, while TotalResults keeps
// the untruncated hit count regardless - the two fields' own documented
// contract, and the reason this cap lives here instead of a caller
// truncating the result after the fact (a caller and `lmm serve` rendering
// the same call must see the identical document).
func TestSearch_LimitCapsMods(t *testing.T) {
	src := &searchStubSource{id: "alpha", result: source.SearchResult{
		Mods: mods("alpha", "a", "b", "c"), TotalCount: 3,
	}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, src)

	report, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, 3, report.TotalResults, "TotalResults stays the untruncated hit count")
	require.Len(t, report.Mods, 2, "Mods is capped to Limit")
	assert.Equal(t, "a", report.Mods[0].ID)
	assert.Equal(t, "b", report.Mods[1].ID)
}

// TestSearch_NonPositiveLimitDoesNotTruncate mirrors the historical
// negative-limit-panic fix (#57, formerly pinned against cmd/lmm's own
// limitResults helper before the cap moved into core): a zero or negative
// Limit must leave Mods untouched, not truncate to nothing or panic on a
// negative slice bound.
func TestSearch_NonPositiveLimitDoesNotTruncate(t *testing.T) {
	for _, limit := range []int{0, -1} {
		src := &searchStubSource{id: "alpha", result: source.SearchResult{
			Mods: mods("alpha", "a", "b", "c"), TotalCount: 3,
		}}
		svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, src)

		report, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{Limit: limit})
		require.NoError(t, err)
		assert.Equal(t, 3, report.TotalResults)
		assert.Len(t, report.Mods, 3, "limit %d must not truncate", limit)
	}
}

// TestSearch_LimitAboveLenIsNoop pins the boundary opposite
// TestSearch_LimitCapsMods: a Limit at or above the hit count changes
// nothing.
func TestSearch_LimitAboveLenIsNoop(t *testing.T) {
	src := &searchStubSource{id: "alpha", result: source.SearchResult{
		Mods: mods("alpha", "a"), TotalCount: 1,
	}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, src)

	report, err := svc.Search(context.Background(), game, "default", "query", core.SearchOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, report.TotalResults)
	require.Len(t, report.Mods, 1)
}

// --- ListGameEntries ---

// TestListGameEntries_ByIDWithDefaultMarked covers `lmm game list`: every
// configured game, ordered by ID, with the default game marked.
func TestListGameEntries_ByIDWithDefaultMarked(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{ID: "zeta", Name: "Zeta", ModPath: t.TempDir()}))
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{ID: "alpha", Name: "Alpha", ModPath: t.TempDir()}))
	require.NoError(t, svc.SetDefaultGame(ctx, "zeta"))

	entries, err := svc.ListGameEntries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "alpha", entries[0].ID)
	assert.Equal(t, "zeta", entries[1].ID)
	assert.False(t, entries[0].Default)
	assert.True(t, entries[1].Default)
	assert.Equal(t, "Zeta", entries[1].Name, "the whole domain.Game is embedded")
}

// TestListGameEntries_NoGamesIsEmptyNotNil pins the zero-games shape.
func TestListGameEntries_NoGamesIsEmptyNotNil(t *testing.T) {
	entries, err := newFlowsTestService(t).ListGameEntries(context.Background())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestListGameEntries_ConvertPaksOnlyForCompileGames is GameListEntry's
// counterpart to TestListMods_ConvertPaksOnlyForCompileGames (final review,
// Important #5 / #302).
func TestListGameEntries_ConvertPaksOnlyForCompileGames(t *testing.T) {
	svc := newFlowsTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{ID: "extract-game", Name: "Extract", ModPath: t.TempDir()}))
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{
		ID: "compile-game", Name: "Compile", ModPath: t.TempDir(), DeployMode: domain.DeployCompile, ConvertPaks: true,
	}))

	entries, err := svc.ListGameEntries(ctx)
	require.NoError(t, err)

	byID := map[string]core.GameListEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	assert.Nil(t, byID["extract-game"].ConvertPaks)
	require.NotNil(t, byID["compile-game"].ConvertPaks)
	assert.True(t, *byID["compile-game"].ConvertPaks)
}

// --- VerifyReport ---

// TestVerifyReport_WrapsResultWithIdentity covers the wrapper: the same
// VerifyResult Verify returns, plus the game/profile it describes, so a
// frontend renders one document instead of re-stamping the identity itself.
func TestVerifyReport_WrapsResultWithIdentity(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, "g1", report.GameID)
	assert.Equal(t, "default", report.Profile)
	require.NotNil(t, report.Result)
	assert.False(t, report.Result.HasFiles, "an empty profile ran the #217 no-files path")
}
