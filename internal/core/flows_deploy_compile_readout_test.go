package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupCompileReadoutGame builds a DeployCompile game (ConvertPaks on, base
// pak present, "default" profile created) for #255's deploy-readout tests,
// mirroring service_icarus_compile_test.go's harness. The caller registers
// its own merge-compiler source under "fake-compiler" first.
func setupCompileReadoutGame(t *testing.T, svc *core.Service) *domain.Game {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy, ConvertPaks: true,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))
	return game
}

// seedExmodzMod installs an enabled native merge-source (exmodz) mod: a
// retained source only, zero deployment members of its own (#197).
func seedExmodzMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, fileID string) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, "1.0", cache.RetainedSourceName(fileID), []byte(name+"-bytes")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: "1.0", FileIDs: []string{fileID}}))
}

// seedLooseMod installs an enabled ordinary loose-file mod (no retained
// merge source): it deploys its own file individually even on a compile game.
func seedLooseMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, fileName string) {
	t.Helper()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "fake-compiler", modID, "1.0", fileName, []byte("loose-data")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: "1.0"}))
}

// deployRecordingProgress runs DeployProfile with a recorder and returns the
// result, every DeployDeployed event keyed by mod name (with its position in
// the event stream), and any DeployMergeSynced events (with positions).
func deployRecordingProgress(t *testing.T, svc *core.Service, game *domain.Game) (*core.DeployResult, map[string]core.DeployProgress, map[string]int, []core.DeployProgress, []int) {
	t.Helper()
	deployed := map[string]core.DeployProgress{}
	deployedAt := map[string]int{}
	var mergeEvents []core.DeployProgress
	var mergeAt []int
	seq := 0
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		seq++
		switch p.Phase {
		case core.DeployDeployed:
			deployed[p.ModName] = p
			deployedAt[p.ModName] = seq
		case core.DeployMergeSynced:
			mergeEvents = append(mergeEvents, p)
			mergeAt = append(mergeAt, seq)
		}
	})
	require.NoError(t, err)
	return result, deployed, deployedAt, mergeEvents, mergeAt
}

// TestDeployProfile_Compile_ClassifiesModsAndEmitsMergeSynced covers #255's
// three-way classification on a DeployCompile game: a native (exmodz) merge
// participant, a ConvertPaks-opted-out pak deploying raw, and an ordinary
// loose-file mod - plus the post-sync DeployMergeSynced event naming the
// merged artifact (via the source's MergedArtifactName, never a core
// literal) and carrying the participant count, and the same readout on
// DeployResult for progress-less callers.
func TestDeployProfile_Compile_ClassifiesModsAndEmitsMergeSynced(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&fakeCompilerSource{})
	game := setupCompileReadoutGame(t, svc)

	seedExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	seedEnabledPakMod(t, svc, game, "fake-compiler", "raw-pak", "1.0", "raw.pak", []byte("raw-pak-bytes"))
	require.NoError(t, svc.SetModConvertPaks(context.Background(), "fake-compiler", "raw-pak", game.ID, "default", false))
	seedLooseMod(t, svc, game, "loose", "Loose Mod", "loose.esp")

	result, deployed, deployedAt, mergeEvents, mergeAt := deployRecordingProgress(t, svc, game)

	require.Equal(t, 3, result.Deployed)

	// Per-mod classification, set on each DeployDeployed event (#255).
	require.Contains(t, deployed, "Bear Mount")
	assert.Equal(t, core.DeployModMerged, deployed["Bear Mount"].ModClass, "a native merge source is carried by the merged artifact")
	require.Contains(t, deployed, "raw-pak")
	assert.Equal(t, core.DeployModRaw, deployed["raw-pak"].ModClass, "an opted-out pak deploys raw, individually")
	require.Contains(t, deployed, "Loose Mod")
	assert.Equal(t, core.DeployModIndividual, deployed["Loose Mod"].ModClass, "a loose-file mod is an ordinary individual deployment")

	// The post-sync merge event: exactly one, after every per-mod event,
	// naming the artifact and counting merge participants.
	require.Len(t, mergeEvents, 1, "exactly one DeployMergeSynced event must fire")
	evt := mergeEvents[0]
	assert.Equal(t, 1, evt.Total, "one mod's content is carried by the merged artifact")
	assert.Equal(t, "zzz_LMM_Merged_P.pak", evt.Detail, "Detail must name the merged artifact (MergedArtifactName)")
	assert.Equal(t, 0, evt.RawFallbacks)
	for name, at := range deployedAt {
		assert.Greater(t, mergeAt[0], at, "DeployMergeSynced must fire after %s's DeployDeployed", name)
	}

	// The same readout lands on DeployResult for callers with no progress
	// stream (a caller may pass nil).
	assert.Equal(t, "zzz_LMM_Merged_P.pak", result.MergedArtifact)
	assert.Equal(t, 1, result.MergedMods)
	assert.Equal(t, 0, result.RawFallbacks)
}

// TestDeployProfile_Compile_ConversionFailure_ReportsRawFallback covers the
// one optimistic case #255 accepts: a convert-opted-in pak is labeled a
// merge participant at DeployDeployed time (the merge hasn't run yet), then
// fails conversion during syncMergedPak - the existing "pak conversion
// failed ... deploying raw" warning carries the correction, and the
// DeployMergeSynced footer reports the raw fallback count accurately.
func TestDeployProfile_Compile_ConversionFailure_ReportsRawFallback(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&pakConversionOutcomeSource{
		fakeCompilerSource: &fakeCompilerSource{},
		failRefs:           map[string]string{"fake-compiler:flaky-pak": "irreconcilable"},
	})
	game := setupCompileReadoutGame(t, svc)

	seedExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	seedEnabledPakMod(t, svc, game, "fake-compiler", "flaky-pak", "1.0", "flaky.pak", []byte("flaky-pak-bytes"))

	var warnings []string
	deployed := map[string]core.DeployProgress{}
	var mergeEvents []core.DeployProgress
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		switch p.Phase {
		case core.DeployDeployed:
			deployed[p.ModName] = p
		case core.DeployWarning:
			warnings = append(warnings, p.Detail)
		case core.DeployMergeSynced:
			mergeEvents = append(mergeEvents, p)
		}
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Deployed)

	// Optimistic inline label: at DeployDeployed time the pak is a merge
	// participant - the correction comes from the warning + footer below.
	require.Contains(t, deployed, "flaky-pak")
	assert.Equal(t, core.DeployModMerged, deployed["flaky-pak"].ModClass)

	// The existing conversion-failure warning still fires (the correction).
	foundConversionWarning := false
	for _, w := range warnings {
		if strings.Contains(w, "pak conversion failed") {
			foundConversionWarning = true
		}
	}
	assert.True(t, foundConversionWarning, "the pak-conversion-failure warning must still fire; got %v", warnings)

	require.Len(t, mergeEvents, 1)
	assert.Equal(t, 1, mergeEvents[0].Total, "only the exmodz mod's content made the merged artifact")
	assert.Equal(t, "zzz_LMM_Merged_P.pak", mergeEvents[0].Detail)
	assert.Equal(t, 1, mergeEvents[0].RawFallbacks, "the failed pak fell back to a raw individual deploy")

	assert.Equal(t, 1, result.MergedMods)
	assert.Equal(t, 1, result.RawFallbacks)
	assert.Equal(t, "zzz_LMM_Merged_P.pak", result.MergedArtifact)
}

// TestDeployProfile_NonCompile_NoMergeReadout guards the gate: a non-compile
// game's deploy must carry no classification and no merge event - its output
// contract is byte-identical to before #255.
func TestDeployProfile_NonCompile_NoMergeReadout(t *testing.T) {
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "plain", Name: "Plain", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)

	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "a", "1.0", "a.esp", []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "a", SourceID: "src", Name: "Mod A", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0"}))

	var mergeEvents []core.DeployProgress
	deployed := map[string]core.DeployProgress{}
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(p core.DeployProgress) {
		switch p.Phase {
		case core.DeployDeployed:
			deployed[p.ModName] = p
		case core.DeployMergeSynced:
			mergeEvents = append(mergeEvents, p)
		}
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Deployed)

	assert.Empty(t, mergeEvents, "a non-compile deploy must emit no DeployMergeSynced event")
	assert.Equal(t, core.DeployModIndividual, deployed["Mod A"].ModClass)
	assert.Zero(t, result.MergedArtifact)
	assert.Zero(t, result.MergedMods)
	assert.Zero(t, result.RawFallbacks)
}
