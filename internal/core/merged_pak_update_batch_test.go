package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/require"
)

// goneMemberSource is a catalog whose update check knows every member mod
// but one: gone answers 404 everywhere, the way a mod republished under a
// new ID does (#539) - the shape #589 hit on a real Icarus install.
type goneMemberSource struct {
	fakeCompilerSource
	gone string
}

func (s *goneMemberSource) ID() string   { return "catalog" }
func (s *goneMemberSource) Name() string { return "Catalog" }
func (s *goneMemberSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if modID == s.gone {
		return nil, domain.ErrModNotFound
	}
	return &domain.Mod{ID: modID, SourceID: s.ID(), Name: modID, Version: "1.0", GameID: gameID}, nil
}
func (s *goneMemberSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID == s.gone {
		return nil, domain.ErrModNotFound
	}
	return nil, nil
}
func (s *goneMemberSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, m := range installed {
		if m.ID == s.gone {
			return nil, &source.ModNotFoundError{ModID: m.ID}
		}
	}
	return nil, nil
}

// newStaleMergedPakWithGoneMember builds #589's world: a merged pak whose
// inputs moved since it was built (a recompile is due), over two cached
// members, one of which its source's catalog no longer has.
func newStaleMergedPakWithGoneMember(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc, game, _ := newMergedPakTestGame(t)
	svc.RegisterSource(&goneMemberSource{gone: "gone-mod"})
	game.SourceIDs["catalog"] = "external-icarus-id"
	require.NoError(t, svc.SaveGame(ctx, game))

	seedEnabledExmodzMod(t, svc, game, "catalog", "gone-mod", "1.0", "exmodz-file", []byte("gone-bytes"))
	_, err := svc.SyncMergedPak(ctx, game, "default")
	require.NoError(t, err)
	seedEnabledExmodzMod(t, svc, game, "catalog", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))
	return svc, game
}

// TestApplyUpdateBatch_MergedPakRecompile is the #589 regression: the
// merged-pak recompile row CheckGameUpdates offers is synthetic - no
// installed row stands behind it - so the batch must route it to the regen
// without looking it up as an installed mod (it used to fail as a bare
// "mod not found", the web UI Updates card's whole path), and the regen
// builds from the cache alone, so a member gone from its catalog does not
// stop it.
func TestApplyUpdateBatch_MergedPakRecompile(t *testing.T) {
	ctx := context.Background()
	svc, game := newStaleMergedPakWithGoneMember(t)

	plan, err := svc.PlanUpdateBatch(ctx, game, "default", []string{domain.ModKey(domain.SourceMerged, "merged-pak")})
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)
	require.True(t, plan.Updates[0].RecompileNeeded)

	result, err := svc.ApplyUpdateBatch(ctx, game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Len(t, result.Applied, 1)
	require.Equal(t, core.UpdateRecompiled, result.Applied[0].Status)

	data, err := os.ReadFile(filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak"))
	require.NoError(t, err)
	require.Equal(t, "gone-byteswolf-bytes", string(data))

	stale, err := svc.CheckMergedPakStaleness(ctx, game, "default")
	require.NoError(t, err)
	require.Nil(t, stale, "the recompile must leave nothing for Health or the Updates card to offer")
}

// TestApplyUpdateBatch_MergedPakRecompile_ReportsTheMergedRow pins the
// applied row's identity: the synthetic merged-pak row, named as the plan
// named it, so a frontend's summary reads the same thing it offered.
func TestApplyUpdateBatch_MergedPakRecompile_ReportsTheMergedRow(t *testing.T) {
	ctx := context.Background()
	svc, game := newStaleMergedPakWithGoneMember(t)

	plan, err := svc.PlanUpdateBatch(ctx, game, "default", nil)
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)
	offered := plan.Updates[0]

	result, err := svc.ApplyUpdateBatch(ctx, game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Len(t, result.Applied, 1)
	applied := result.Applied[0]
	require.Equal(t, domain.SourceMerged, applied.Mod.SourceID)
	require.Equal(t, offered.InstalledMod.ID, applied.Mod.ModID)
	require.Equal(t, offered.InstalledMod.Name, applied.Name)
	require.Equal(t, offered.InstalledMod.Version, applied.FromVersion)
	require.Equal(t, offered.NewVersion, applied.ToVersion)
}

// TestApplyUpdateBatch_MergedPakRecompile_MissingMemberCacheNamesIt: the
// rebuild reads members from the cache alone, so a member whose cache entry
// is gone cannot be rebuilt from - and silently leaving it out of the merged
// pak would drop the mod from the game. The failure names the member and
// the way to restore it (#589).
func TestApplyUpdateBatch_MergedPakRecompile_MissingMemberCacheNamesIt(t *testing.T) {
	ctx := context.Background()
	svc, game := newStaleMergedPakWithGoneMember(t)
	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, "catalog", "gone-mod", "1.0"))

	plan, err := svc.PlanUpdateBatch(ctx, game, "default", nil)
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)

	result, err := svc.ApplyUpdateBatch(ctx, game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Applied)
	require.Len(t, result.Failed, 1)
	msg := result.Failed[0].Error
	require.Contains(t, msg, "gone-mod")
	require.Contains(t, msg, "lmm verify --fix")
	require.NotEqual(t, "mod not found", msg)

	var missing *core.MergeMemberCacheMissingError
	require.ErrorAs(t, result.Failed[0].Cause(), &missing)
}

// TestApplyMergedPakRegen_MissingMemberCacheNamesIt is the same refusal on
// the direct regen, which the CLI's single-mod recompile drives.
func TestApplyMergedPakRegen_MissingMemberCacheNamesIt(t *testing.T) {
	ctx := context.Background()
	svc, game := newStaleMergedPakWithGoneMember(t)
	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, "catalog", "gone-mod", "1.0"))

	_, err := svc.ApplyMergedPakRegen(ctx, game, "default", nil)
	var missing *core.MergeMemberCacheMissingError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "gone-mod", missing.ModID)
	require.Equal(t, "catalog", missing.SourceID)
}

// TestVerify_Fix_MergedPakMissingMemberCache_RefusesTheResync is the Health
// card's Repair twin of the refusal above (#589): verify --fix's resync is
// the same rebuild, so it refuses the same way - a sync warning naming the
// member - instead of rebuilding without it.
func TestVerify_Fix_MergedPakMissingMemberCache_RefusesTheResync(t *testing.T) {
	ctx := context.Background()
	svc, game := newStaleMergedPakWithGoneMember(t)
	require.NoError(t, svc.GetGameCache(game).Delete(game.ID, "catalog", "gone-mod", "1.0"))
	before, err := os.ReadFile(filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak"))
	require.NoError(t, err)

	sink, rec := core.RecordEvents()
	_, err = svc.VerifyForTest(ctx, game, "default", core.VerifyOptions{Fix: true}, sink)
	require.NoError(t, err)

	var warnings []string
	for _, e := range verifyEvents(*rec) {
		if e.Kind == core.VerifyEvSyncWarning {
			warnings = append(warnings, e.Detail)
		}
	}
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "gone-mod")
	require.Contains(t, warnings[0], "lmm verify --fix")

	after, err := os.ReadFile(filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak"))
	require.NoError(t, err)
	require.Equal(t, before, after, "the merged pak must not be rebuilt without the member")
}
