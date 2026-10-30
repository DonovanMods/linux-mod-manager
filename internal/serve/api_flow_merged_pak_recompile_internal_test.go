package serve

// The Updates card's merged-pak recompile row (#589): the row is the
// synthetic "lmm-merged:merged-pak" one core's update check offers once a
// DeployCompile game's merged pak is stale (#196/#197), so applying it
// through the "updates" job must rebuild the pak - from the cache alone,
// with a member its source no longer has - rather than failing the batch
// with a bare "mod not found".

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergingFixtureAdapter is convertFixtureAdapter with a merge that really
// writes its output (the members' bytes, concatenated) and a base pak whose
// fingerprint the test moves, the way a game patch does.
type mergingFixtureAdapter struct {
	convertFixtureAdapter
	base string
}

func (a *mergingFixtureAdapter) FingerprintBase(string) (string, error) { return a.base, nil }
func (a *mergingFixtureAdapter) MergeCompile(_ context.Context, _ string, sources []adapter.MergeSource, outputPath string) ([]string, []adapter.MergeFailure, error) {
	var out []byte
	for _, src := range sources {
		b, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, b...)
	}
	return nil, nil, os.WriteFile(outputPath, out, 0o644)
}

// goneMemberFixtureSource answers 404 for the one member republished
// elsewhere, in its update check and its lookups alike (#539).
type goneMemberFixtureSource struct{ fixtureSource }

const goneMemberID = "gone"

func (*goneMemberFixtureSource) GetMod(_ context.Context, gameID, modID string) (*domain.Mod, error) {
	if modID == goneMemberID {
		return nil, domain.ErrModNotFound
	}
	return &domain.Mod{ID: modID, SourceID: fixtureSourceID, Name: modID, Version: "1.0", GameID: gameID}, nil
}

func (*goneMemberFixtureSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, m := range installed {
		if m.ID == goneMemberID {
			return nil, &source.ModNotFoundError{ModID: m.ID}
		}
	}
	return nil, nil
}

// newStaleMergedPakServer builds the owner's Icarus state: two cached
// members merged into a deployed pak, the base pak patched since (so the
// recompile row is due), and one member gone from its catalog.
func newStaleMergedPakServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	sandboxEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&goneMemberFixtureSource{})
	merger := &mergingFixtureAdapter{base: "base-v1"}
	svc.RegisterAdapter(merger)

	ctx := t.Context()
	game := &domain.Game{
		ID: "g1", Name: "Compile Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkCopy, DeployMode: domain.DeployCompile,
		SourceIDs: map[string]string{fixtureSourceID: ""},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	for _, id := range []string{goneMemberID, "kept"} {
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, fixtureSourceID, id, "1.0",
			cache.RetainedSourceName("file.exmodz"), []byte(id+";")))
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: id, SourceID: fixtureSourceID, Name: id, Version: "1.0", GameID: game.ID},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
			FileIDs: []string{"file.exmodz"},
		}))
		require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
			domain.ModReference{SourceID: fixtureSourceID, ModID: id, Version: "1.0", FileIDs: []string{"file.exmodz"}}))
	}
	_, err = svc.SyncMergedPak(ctx, game, "default")
	require.NoError(t, err)
	merger.base = "base-v2" // the game patched its base pak

	stale, err := svc.CheckMergedPakStaleness(ctx, game, "default")
	require.NoError(t, err)
	require.NotNil(t, stale, "precondition: the recompile row is due")

	return New(ctx, svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), svc, game
}

// TestFlowUpdates_MergedPakRecompileRebuildsFromTheCache drives the Updates
// card's own path - the "updates" plan naming the recompile row, then the
// job - and asserts the end state: the pak rebuilt from both cached members,
// and nothing left stale for the card or Health to offer again.
func TestFlowUpdates_MergedPakRecompileRebuildsFromTheCache(t *testing.T) {
	s, svc, game := newStaleMergedPakServer(t)
	deployed := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	require.NoError(t, os.WriteFile(deployed, []byte("old build"), 0o644))

	j := runFlow(t, s, game, "updates", `{"mods":["`+domain.ModKey(domain.SourceMerged, "merged-pak")+`"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.UpdateBatchResult)
	require.True(t, ok)
	assert.Empty(t, result.Failed, "the recompile must not fail")
	require.Len(t, result.Applied, 1)
	assert.Equal(t, core.UpdateRecompiled, result.Applied[0].Status)
	assert.Equal(t, "Merged Pak", result.Applied[0].Name, "the summary names the row the card offered")

	data, err := os.ReadFile(deployed)
	require.NoError(t, err)
	assert.Equal(t, "gone;kept;", string(data), "both cached members, the gone one included, are rebuilt in")

	stale, err := svc.CheckMergedPakStaleness(t.Context(), game, "default")
	require.NoError(t, err)
	assert.Nil(t, stale)
}
