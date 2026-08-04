package core_test

// Tests for Service.ValidateInstallFileSelection (#211 Task 2): a
// merge-compile source's exmodz variant and its pak sibling are alternate
// forms of the SAME mod (Task 1 - icarus.GetModFiles marks exmodz
// IsPrimary when both exist) - installing both double-applies the mod's
// table edits, since the pak deploys standalone while the exmodz joins the
// merged pak. This file covers the rule itself (table test) plus the two
// core seams that must reject a mixed selection before any download/side
// effect: explicit --file pins through the STRICT path's up-front
// resolution, and a caller-supplied mixed plan.Files (the CLI's
// interactive-override shape) that never touches TargetFileIDs at all.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/require"
)

// variantExclusivitySource is a minimal ModSource + source.MergeCompiler
// fake (mirrors fakeCompilerSource in service_icarus_compile_test.go and
// importFlowCompilerSource in merged_pak_import_flow_test.go) that also
// serves a caller-supplied per-mod file list, the way perModMultiFileSource
// (flows_install_test.go) does for the #140 interplay suite - needed here
// because a single mod must offer BOTH a pak and an exmodz file so
// PlanInstall/ApplyInstall can drive a real mixed selection end to end.
type variantExclusivitySource struct {
	*mockSourceWithDownloads
	files map[string][]domain.DownloadableFile // mod.ID -> served files, verbatim
}

func newVariantExclusivitySource(id string) *variantExclusivitySource {
	return &variantExclusivitySource{
		mockSourceWithDownloads: newMockSourceWithDownloads(id),
		files:                   make(map[string][]domain.DownloadableFile),
	}
}

func (s *variantExclusivitySource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.files[mod.ID], nil
}

// ValidateSource implements source.MergeCompiler - never exercised by these
// tests since a rejected selection must fail before ingest ever calls it.
func (s *variantExclusivitySource) ValidateSource(sourceFilePath string) error { return nil }

// MergeCompile implements source.MergeCompiler - never exercised by these
// tests for the same reason.
func (s *variantExclusivitySource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, error) {
	return nil, nil
}

var (
	_ source.ModSource     = (*variantExclusivitySource)(nil)
	_ source.MergeCompiler = (*variantExclusivitySource)(nil)
)

// TestValidateInstallFileSelection is the unit table test: the rule itself,
// independent of any install flow.
func TestValidateInstallFileSelection(t *testing.T) {
	svc := newFlowsTestService(t)

	mc := newVariantExclusivitySource("mc")
	svc.RegisterSource(mc)
	t.Cleanup(mc.Close)

	plain := newMockSource("plain")
	svc.RegisterSource(plain)

	pakFile := domain.DownloadableFile{ID: "pak", FileName: "Mod_P.pak"}
	exmodzFile := domain.DownloadableFile{ID: "exmodz", FileName: "Mod.exmodz"}

	cases := []struct {
		name     string
		sourceID string
		files    []domain.DownloadableFile
		wantErr  bool
	}{
		{"mixed on merge-compiler source rejected", "mc", []domain.DownloadableFile{pakFile, exmodzFile}, true},
		{"exmodz alone allowed", "mc", []domain.DownloadableFile{exmodzFile}, false},
		{"pak alone allowed (escape hatch)", "mc", []domain.DownloadableFile{pakFile}, false},
		{"two non-exmodz files allowed", "mc", []domain.DownloadableFile{pakFile, {ID: "extra", FileName: "readme.pak"}}, false},
		{"mixed on plain source allowed", "plain", []domain.DownloadableFile{pakFile, exmodzFile}, false},
		{"unknown source is not this check's problem", "ghost", []domain.DownloadableFile{pakFile, exmodzFile}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.ValidateInstallFileSelection(tc.sourceID, tc.files)
			if tc.wantErr {
				require.ErrorContains(t, err, "alternate forms of the same mod")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// setupVariantExclusivityService registers a MergeCompiler source ("mc")
// whose single mod ("mod1") offers exactly two files: a primary pak and an
// exmodz variant - the exact shape Task 1 produces for icarus when both
// exist. Neither file is ever actually downloadable (no AddDownload call);
// a seam test proving rejection happens BEFORE any download must never
// need one to succeed.
func setupVariantExclusivityService(t *testing.T) (*core.Service, *domain.Game, *variantExclusivitySource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mc := newVariantExclusivitySource("mc")
	t.Cleanup(mc.Close)
	svc.RegisterSource(mc)

	mc.files["mod1"] = []domain.DownloadableFile{
		{ID: "pak", Name: "Main", FileName: "Mod_P.pak", IsPrimary: true, Category: "MAIN"},
		{ID: "exmodz", Name: "Exmodz Source", FileName: "Mod.exmodz", Category: "MAIN"},
	}
	mod := &domain.Mod{ID: "mod1", SourceID: "mc", Name: "Mod One", Version: "1.0", GameID: "g1"}
	mc.AddMod(mod.GameID, mod)

	return svc, game, mc
}

// TestApplyInstall_StrictPath_TargetFileIDs_MixedVariants_Rejected is the
// explicit --file pins seam: opts.TargetFileIDs names both variants.
// ApplyInstall must fail with the rejection message before any download
// side effect.
func TestApplyInstall_StrictPath_TargetFileIDs_MixedVariants_Rejected(t *testing.T) {
	svc, game, mc := setupVariantExclusivityService(t)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "mc", "mod1", false)
	require.NoError(t, err)
	require.Empty(t, plan.Dependencies, "must take the STRICT path")
	require.Len(t, plan.Files, 1, "PlanInstall's auto-pick is single-file")

	opts := core.InstallOptions{TargetFileIDs: []string{"pak", "exmodz"}}
	_, err = svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.ErrorContains(t, err, "pak and exmodz are alternate forms of the same mod - select one")

	require.Equal(t, 0, mc.DownloadCount(), "no download may happen for a rejected mixed selection")
	gameCache := svc.GetGameCache(game)
	require.False(t, gameCache.Exists(game.ID, "mc", "mod1", "1.0"), "a rejected selection must leave no cache entry")
}

// TestApplyInstall_StrictPath_CallerSuppliedMixedVariantFiles_Rejected is
// the caller-supplied plan.Files seam (the CLI's interactive-override
// shape): no TargetFileIDs at all, so resolveStrictInstallFiles' up-front
// pin fold is a no-op (opts carries no pins) - the mixed selection reaches
// ApplyInstall entirely through plan.Files itself, exactly as an
// interactive --file prompt or multi-select would leave it.
func TestApplyInstall_StrictPath_CallerSuppliedMixedVariantFiles_Rejected(t *testing.T) {
	svc, game, mc := setupVariantExclusivityService(t)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "mc", "mod1", false)
	require.NoError(t, err)
	require.Empty(t, plan.Dependencies, "must take the STRICT path")

	// Simulate the CLI's interactive override: the caller directly replaces
	// plan.Files with a mixed selection, never touching opts.TargetFileIDs.
	plan.Files = mc.files["mod1"]

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.ErrorContains(t, err, "pak and exmodz are alternate forms of the same mod - select one")

	require.Equal(t, 0, mc.DownloadCount(), "no download may happen for a rejected mixed selection")
	gameCache := svc.GetGameCache(game)
	require.False(t, gameCache.Exists(game.ID, "mc", "mod1", "1.0"), "a rejected selection must leave no cache entry")
}

// TestApplyInstall_BatchPath_TargetFileIDs_MixedVariants_Rejected is the
// BATCH-path seam: when plan.Dependencies is non-empty, the primary's
// TargetFileIDs pins are resolved and validated up-front (flows.go, inside
// ApplyInstall's BATCH branch) before any mod - dependency or primary - is
// touched. This mirrors TestApplyInstall_StrictPath_TargetFileIDs_
// MixedVariants_Rejected above but drives the one path neither existing seam
// test reaches, since both require an empty plan.Dependencies to stay on the
// STRICT path.
//
// mockSource.GetDependencies (inherited unchanged by
// variantExclusivitySource, since it only overrides GetModFiles) returns
// mod.Dependencies verbatim - setting mod1.Dependencies directly is the
// entire mechanism needed to make PlanInstall take the BATCH path; no new
// fixture type or plumbing required.
func TestApplyInstall_BatchPath_TargetFileIDs_MixedVariants_Rejected(t *testing.T) {
	svc, game, mc := setupVariantExclusivityService(t)

	depMod := &domain.Mod{ID: "dep-mod", SourceID: "mc", Name: "Dependency", Version: "1.0", GameID: "g1"}
	mc.AddMod(depMod.GameID, depMod)

	mod1, err := mc.GetMod(context.Background(), game.ID, "mod1")
	require.NoError(t, err)
	mod1.Dependencies = []domain.ModReference{{SourceID: "mc", ModID: "dep-mod"}}

	plan, err := svc.PlanInstall(context.Background(), game, "default", "mc", "mod1", false)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Dependencies, "must take the BATCH path")

	opts := core.InstallOptions{TargetFileIDs: []string{"pak", "exmodz"}}
	_, err = svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.ErrorContains(t, err, "pak and exmodz are alternate forms of the same mod - select one")

	require.Equal(t, 0, mc.DownloadCount(), "no download may happen for a rejected mixed selection")
	gameCache := svc.GetGameCache(game)
	require.False(t, gameCache.Exists(game.ID, "mc", "mod1", "1.0"), "a rejected selection must leave no cache entry")
}

// TestPlanInstall_BothVariants_DefaultsToExmodz is #211's parity acceptance:
// PlanInstall's non-interactive default (what the TUI, --yes, and every
// batch path install) must pick the exmodz variant when both exist. The TUI
// has no file chooser and installs plan.Files exactly as planned
// (internal/tui/service_core.go), so this single core assertion covers both
// interfaces.
func TestPlanInstall_BothVariants_DefaultsToExmodz(t *testing.T) {
	svc, game, mc := setupVariantExclusivityService(t)

	// Mirror Task 1's real icarus behavior: exmodz is the primary file, not pak.
	// For this test, override the fixture's files to mark exmodz primary.
	mc.files["mod1"] = []domain.DownloadableFile{
		{ID: "pak", Name: "Main", FileName: "Mod_P.pak", Category: "MAIN"},
		{ID: "exmodz", Name: "Exmodz Source", FileName: "Mod.exmodz", IsPrimary: true, Category: "MAIN"},
	}

	plan, err := svc.PlanInstall(context.Background(), game, "default", "mc", "mod1", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1, "PlanInstall must select exactly one file when both variants exist")
	require.Equal(t, "exmodz", plan.Files[0].ID, "PlanInstall must default to the exmodz variant")
}
