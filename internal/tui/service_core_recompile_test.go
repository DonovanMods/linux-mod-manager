package tui_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
	"github.com/stretchr/testify/require"
)

// recompileFakeSource is a minimal ModSource + source.MergeCompiler
// standing in for internal/source/icarus.Icarus, mirroring
// internal/core/service_icarus_compile_test.go's fakeCompilerSource at the
// TUI layer.
type recompileFakeSource struct {
	validateCalls int
	compileCalls  int
}

func (s *recompileFakeSource) ID() string      { return "fake-compiler" }
func (s *recompileFakeSource) Name() string    { return "Fake Compiler Source" }
func (s *recompileFakeSource) AuthURL() string { return "" }
func (s *recompileFakeSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (s *recompileFakeSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}
func (s *recompileFakeSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}
func (s *recompileFakeSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}
func (s *recompileFakeSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}
func (s *recompileFakeSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", source.ErrNotSupported
}
func (s *recompileFakeSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// ValidateSource confirms the archive exists.
func (s *recompileFakeSource) ValidateSource(sourceFilePath string) error {
	s.validateCalls++
	_, err := os.Stat(sourceFilePath)
	return err
}

// MergeCompile concatenates every source's bytes - enough to prove a
// merge/regen actually happened and used the retained content.
func (s *recompileFakeSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	s.compileCalls++
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, data...)
	}
	return nil, nil, os.WriteFile(outputPath, out, 0o644)
}

var (
	_ source.ModSource     = (*recompileFakeSource)(nil)
	_ source.MergeCompiler = (*recompileFakeSource)(nil)
)

// newRecompileActionsFixture builds a DeployCompile game with a registered
// merge-compiler-capable source and an ENABLED exmodz mod, deliberately
// leaving the merged pak un-generated so the TUI's update check reports/
// applies a #197 merge-needed row end to end, returning the ActionProvider
// (#196's CLI/TUI parity seam), the fake compiler, and the merged pak's
// game-dir path.
func newRecompileActionsFixture(t *testing.T) (tui.ActionProvider, *recompileFakeSource, string) {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	w, err := unrealpak.Create(basePak)
	require.NoError(t, err)
	require.NoError(t, w.AddFile("Data/D_Fixture.json", []byte(`{"fixture":true}`)))
	require.NoError(t, w.Close())

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	compiler := &recompileFakeSource{}
	svc.RegisterSource(compiler)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	const modID, version, fileID = "bear-mount", "3.3", "exmodz-file-id"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("retained-exmodz-bytes")))

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{fileID},
	}
	require.NoError(t, svc.SaveInstalledMod(im))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	return tui.NewCoreActions(svc, game, "default"), compiler, filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
}

// TestCoreProviderActions_CheckUpdates_ReportsRecompileNeeded proves the TUI
// update check reports a #197 merged-pak staleness row through the same
// CheckGameUpdates seam the CLI uses.
func TestCoreProviderActions_CheckUpdates_ReportsRecompileNeeded(t *testing.T) {
	actions, _, _ := newRecompileActionsFixture(t)

	view, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Updates, 1)
	u := view.Updates[0]
	// #197: a merged-pak staleness row's identity is the SYNTHETIC
	// merged-pak mod, not the contributing "bear-mount" mod -
	// CheckMergedPakStaleness is profile-scoped, not per-mod.
	require.Equal(t, "merged-pak", u.ID)
	require.True(t, u.RecompileNeeded)
	require.Equal(t, u.FromVersion, u.ToVersion, "a staleness row has no real version change")
	require.Equal(t, "base pak updated", u.RecompileReason, "a never-before-synced profile's reason is the fingerprint-mismatch default")
	require.Equal(t, "(base pak updated)", u.VersionLabel())
}

// TestCoreProviderActions_CheckUpdates_ReportsNotDeployedReason guards a
// #203 release-review finding: the TUI used to hardcode "(base pak
// updated)" for every staleness row, even when the real cause (the
// fingerprint still matches; the artifact is just missing from the game
// directory - core.CheckMergedPakStaleness's own "not deployed" case,
// internal/core/merged_pak.go) is different. After a real sync/deploy, the
// merged pak is removed WITHOUT anything else changing - the fingerprint
// still matches, so the distinct "not deployed" reason must surface, not
// the generic mismatch wording `lmm verify` would never use here either.
func TestCoreProviderActions_CheckUpdates_ReportsNotDeployedReason(t *testing.T) {
	actions, _, deployedPath := newRecompileActionsFixture(t)

	view, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Updates, 1)
	_, err = actions.ApplyUpdate(context.Background(), view.Updates[0], nil)
	require.NoError(t, err)
	require.FileExists(t, deployedPath, "precondition: the merged pak must be deployed")

	require.NoError(t, os.Remove(deployedPath))

	view, err = actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Updates, 1)
	u := view.Updates[0]
	require.True(t, u.RecompileNeeded)
	require.Equal(t, "not deployed", u.RecompileReason)
	require.Equal(t, "(not deployed)", u.VersionLabel())
}

// TestUpdateItem_VersionLabel is a pure unit test over VersionLabel's own
// branches, independent of any real staleness fixture: the normal arrow
// case, both real RecompileReason values core.CheckMergedPakStaleness can
// produce, and the defensive empty-reason fallback (core always sets one
// alongside RecompileNeeded, but VersionLabel must not render a bare "()"
// if some future caller ever leaves it unset).
func TestUpdateItem_VersionLabel(t *testing.T) {
	tests := []struct {
		name string
		item tui.UpdateItem
		want string
	}{
		{
			name: "normal update shows the version arrow",
			item: tui.UpdateItem{FromVersion: "1.0", ToVersion: "2.0"},
			want: "1.0 → 2.0",
		},
		{
			name: "recompile needed: base pak updated",
			item: tui.UpdateItem{RecompileNeeded: true, RecompileReason: "base pak updated"},
			want: "(base pak updated)",
		},
		{
			name: "recompile needed: not deployed",
			item: tui.UpdateItem{RecompileNeeded: true, RecompileReason: "not deployed"},
			want: "(not deployed)",
		},
		{
			name: "recompile needed: empty reason falls back to base pak updated",
			item: tui.UpdateItem{RecompileNeeded: true},
			want: "(base pak updated)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.item.VersionLabel())
		})
	}
}

// TestCoreProviderActions_ApplyUpdate_Recompile_AppliesAndRedeploys proves
// ApplyUpdate dispatches a RecompileNeeded row to Service.ApplyMergedPakRegen
// (checking RecompileNeeded BEFORE resolving a real InstalledMod - see
// coreProvider.ApplyUpdate's own doc comment for the defect this guards):
// the retained source is merged and deployed, provable via the on-disk
// (LinkCopy) deployed bytes.
func TestCoreProviderActions_ApplyUpdate_Recompile_AppliesAndRedeploys(t *testing.T) {
	actions, compiler, deployedPath := newRecompileActionsFixture(t)

	view, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Updates, 1)

	outcome, err := actions.ApplyUpdate(context.Background(), view.Updates[0], nil)
	require.NoError(t, err)
	require.Equal(t, 1, compiler.compileCalls)
	require.Contains(t, outcome.Message, "Recompiled")

	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "retained-exmodz-bytes", string(data), "redeploy must reflect the freshly merged bytes")
}
