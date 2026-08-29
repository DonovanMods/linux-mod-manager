package core_test

// Tests for Service.ResolveReorder (v2 Phase 2 Unit... Task 13): the
// identifier-resolution policy lifted out of cmd/lmm's doProfileReorder
// (profile.go:770-870), which cmd/lmm/profile_reorder_test.go characterized
// byte-for-byte before this lift. doProfileReorder now calls ResolveReorder
// for the mutation path and passes its result straight to
// Service.ReorderProfileMods; the "no args" listing path still reads the
// profile directly via Service.NewProfileManager().Get.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupResolveReorderTest returns a *core.Service and game with an empty
// "default" profile ready for addResolveReorderMod to populate.
func setupResolveReorderTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	_, err := svc.NewProfileManager().Create(game.ID, "default")
	require.NoError(t, err)
	return svc, game
}

// addResolveReorderMod appends sourceID:modID (version "1.0", no FileIDs) to
// the game's "default" profile load order.
func addResolveReorderMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID string) {
	t.Helper()
	require.NoError(t, svc.NewProfileManager().AddMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: "1.0"}))
}

func TestService_ResolveReorder_ExplicitSourceModIDKeys(t *testing.T) {
	svc, game := setupResolveReorderTest(t)
	addResolveReorderMod(t, svc, game, "src1", "alpha")
	addResolveReorderMod(t, svc, game, "src1", "beta")
	addResolveReorderMod(t, svc, game, "src1", "gamma")

	got, err := svc.ResolveReorder(context.Background(), game, "default", []string{"src1:gamma", "src1:alpha"})
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, got, "mentioned mods lead in the given order; the unmentioned mod is appended in its original relative position")
}

func TestService_ResolveReorder_BareIDs(t *testing.T) {
	svc, game := setupResolveReorderTest(t)
	addResolveReorderMod(t, svc, game, "src1", "alpha")
	addResolveReorderMod(t, svc, game, "src1", "beta")
	addResolveReorderMod(t, svc, game, "src1", "gamma")

	got, err := svc.ResolveReorder(context.Background(), game, "default", []string{"gamma", "alpha"})
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, got, "an unambiguous bare mod ID resolves the same as its source:modid form")
}

func TestService_ResolveReorder_PartialReorder_AppendsUnmentionedInOriginalOrder(t *testing.T) {
	svc, game := setupResolveReorderTest(t)
	addResolveReorderMod(t, svc, game, "src1", "alpha")
	addResolveReorderMod(t, svc, game, "src1", "beta")
	addResolveReorderMod(t, svc, game, "src1", "gamma")

	got, err := svc.ResolveReorder(context.Background(), game, "default", []string{"gamma"})
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, got, "alpha and beta, both unmentioned, keep their original relative order after the mentioned mod")
}

func TestService_ResolveReorder_DuplicateArgs_Deduped(t *testing.T) {
	svc, game := setupResolveReorderTest(t)
	addResolveReorderMod(t, svc, game, "src1", "alpha")
	addResolveReorderMod(t, svc, game, "src1", "beta")
	addResolveReorderMod(t, svc, game, "src1", "gamma")

	got, err := svc.ResolveReorder(context.Background(), game, "default", []string{"gamma", "gamma", "alpha"})
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src1", ModID: "gamma", Version: "1.0"},
		{SourceID: "src1", ModID: "alpha", Version: "1.0"},
		{SourceID: "src1", ModID: "beta", Version: "1.0"},
	}, got, "a repeated arg contributes only its first occurrence's position")
}

// TestService_ResolveReorder_UnknownBareID_ErrorsIsErrModNotInProfile pins
// the frozen "mod %s not in profile" text (cmd/lmm/profile.go:842 pre-lift)
// for the zero-match bare-ID case.
func TestService_ResolveReorder_UnknownBareID_ErrorsIsErrModNotInProfile(t *testing.T) {
	svc, game := setupResolveReorderTest(t)
	addResolveReorderMod(t, svc, game, "src1", "alpha")

	got, err := svc.ResolveReorder(context.Background(), game, "default", []string{"nope"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModNotInProfile)
	assert.Equal(t, "mod nope not in profile", err.Error())
	assert.Nil(t, got)
}

// TestService_ResolveReorder_ExplicitKeyNotInProfile_SameErrorText proves the
// "mod %s not in profile" text is shared by both the explicit source:modid
// miss and the bare-ID zero-match miss - not two separately worded errors
// (cmd/lmm/profile_reorder_test.go's
// TestDoProfileReorder_ExplicitKeyNotInProfile_SameErrorText, pre-lift).
func TestService_ResolveReorder_ExplicitKeyNotInProfile_SameErrorText(t *testing.T) {
	svc, game := setupResolveReorderTest(t)
	addResolveReorderMod(t, svc, game, "src1", "alpha")

	got, err := svc.ResolveReorder(context.Background(), game, "default", []string{"src2:nope"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModNotInProfile)
	assert.Equal(t, "mod src2:nope not in profile", err.Error())
	assert.Nil(t, got)
}

// TestService_ResolveReorder_AmbiguousBareID_ErrorsIsErrAmbiguousModID pins
// the frozen "ambiguous mod id %s (use source:modid): %s" text
// (cmd/lmm/profile.go:848 pre-lift). Ruling 4 (#298): the candidates are
// sorted by source ID, so with three sources sharing "shared" the reported
// order is fixed (src1, src2, src3) - not merely "some permutation", which
// is what the pre-Ruling-4 map-order build produced.
func TestService_ResolveReorder_AmbiguousBareID_ErrorsIsErrAmbiguousModID(t *testing.T) {
	svc, game := setupResolveReorderTest(t)
	addResolveReorderMod(t, svc, game, "src3", "shared")
	addResolveReorderMod(t, svc, game, "src1", "shared")
	addResolveReorderMod(t, svc, game, "src2", "shared")

	got, err := svc.ResolveReorder(context.Background(), game, "default", []string{"shared"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrAmbiguousModID)
	assert.EqualError(t, err, "ambiguous mod id shared (use source:modid): src1:shared, src2:shared, src3:shared")
	assert.Nil(t, got)
}

// TestService_ResolveReorder_ProfileLoadError_Wrapped covers the profile
// itself missing (e.g. resolveProfile's ErrProfileNotFound fallback to
// "default" for a brand-new game with zero profiles) - pre-lift
// doProfileReorder wrapped this as "loading profile: %w" regardless of
// whether args were given; ResolveReorder must do the same for the
// with-args path.
func TestService_ResolveReorder_ProfileLoadError_Wrapped(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	got, err := svc.ResolveReorder(context.Background(), game, "missing", []string{"alpha"})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
	assert.Equal(t, "loading profile: profile not found", err.Error())
	assert.Nil(t, got)
}
