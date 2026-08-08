package tui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/go-unrealpak"
	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
)

// healthE2ESource combines recompileFakeSource's MergeCompiler capability
// (ValidateSource/MergeCompile, service_core_recompile_test.go - same
// package, reused rather than duplicated) with a real GetModFiles/
// GetDownloadURL pair serving pak bytes over an httptest.Server: the #224
// Task 4 needs_reingest --fix repair (verify.go's redownloadModFile, wired
// through here via ActionProvider.RunHealthCheck) actually calls
// GetModFiles/GetDownloadURL/DownloadMod for real, unlike
// recompileFakeSource's own ErrNotSupported stubs (that file's tests never
// exercise a redownload). Mirrors internal/core/verify_test.go's
// reingestFixSource/newByteServer pair, the CLI-side engine test for this
// exact repair.
//
// trap additionally reproduces health_provider_test.go's healthTrapSource
// pattern: armed before DataProvider.Health (Local tier, no network) and
// disarmed before ActionProvider.RunHealthCheck's full+fix run, so the SAME
// source both proves the Local-tier call never reaches the network AND
// backs the real redownload the fix step performs - a single fixture
// covering both contracts instead of two disjoint sources.
type healthE2ESource struct {
	*recompileFakeSource
	t                *testing.T
	fileID, fileName string
	server           *httptest.Server
	content          []byte
	trap             bool
}

func newHealthE2ESource(t *testing.T, fileID, fileName string, content []byte) *healthE2ESource {
	t.Helper()
	s := &healthE2ESource{recompileFakeSource: &recompileFakeSource{}, t: t, fileID: fileID, fileName: fileName, content: content}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(s.content)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *healthE2ESource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	if s.trap {
		s.t.Fatal("GetModFiles must not be called by the Local tier (DataProvider.Health)")
	}
	return []domain.DownloadableFile{{ID: s.fileID, Name: s.fileName, FileName: s.fileName, IsPrimary: true}}, nil
}

func (s *healthE2ESource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	if s.trap {
		s.t.Fatal("GetDownloadURL must not be called by the Local tier (DataProvider.Health)")
	}
	return s.server.URL, nil
}

var (
	_ source.ModSource     = (*healthE2ESource)(nil)
	_ source.MergeCompiler = (*healthE2ESource)(nil)
)

// newHealthE2EFixture builds the #224 success-criterion-3 lifecycle fixture:
// a DeployCompile game (mirroring newRecompileActionsFixture's real base-pak/
// merge-compiler-source setup) carrying BOTH a pre-#221 pak state - a
// deployable cache member with no retained source, the exact shape
// internal/core/verify_test.go's newNeedsReingestFixGame builds, needs_
// reingest's own lazy-migration fixture - AND a dangling game-dir symlink
// into the cache root (verify_test.go's strayDanglingSymlink), the
// convergence pass's stale_deployment candidate. Unlike
// internal/core/pak_convert_e2e_test.go's TestPakConvertEndToEnd (package
// core_test, unreachable here), this stays in package tui_test so it can
// return the real DataProvider/ActionProvider seams the Health screen
// actually uses.
func newHealthE2EFixture(t *testing.T) (tui.DataProvider, tui.ActionProvider, *core.Service, *domain.Game, *healthE2ESource) {
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

	src := newHealthE2ESource(t, "pak", "legacypak.pak", []byte("re-ingested-legacy-bytes"))
	src.trap = true // armed by default; TestHealthE2E_Lifecycle disarms it around the fix step
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy, ConvertPaks: true,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	// Pre-#221 pak state: a deployable cache member ("legacypak.pak") with
	// NO retained source - PakNeedsReingest flags this as needs_reingest.
	// SaveInstalledMod deliberately omits ConvertPaks (the schema default
	// covers it, per mods.go's own SaveInstalledMod doc comment - the same
	// precedent newNeedsReingestFixGame follows).
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", "legacypak", "1.0", "legacypak.pak", []byte("legacy-bytes")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "legacypak", SourceID: "fake-compiler", Name: "Legacy Pak", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"pak"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "legacypak", Version: "1.0", FileIDs: []string{"pak"}}))

	// A dangling game-dir symlink into the cache root, with no deployed_files
	// row at all - the simplest ConvergeDeployedFiles/stale_deployment
	// candidate (verify_test.go's strayDanglingSymlink).
	cacheRoot := svc.GetGameCachePath(game)
	target := filepath.Join(cacheRoot, game.ID, "src-stray", "1.0", "stray.pak")
	require.NoError(t, os.Symlink(target, filepath.Join(game.ModPath, "stray.pak")))

	return tui.NewCoreProvider(svc, game, "default"), tui.NewCoreActions(svc, game, "default"), svc, game, src
}

// findMergedOutcome looks up modID's entry in a MergedPakOutcomes result -
// mirrors internal/core/pak_convert_e2e_test.go's findOutcome (package
// core_test, unreachable here).
func findMergedOutcome(outcomes []core.MergedFingerprintEntry, modID string) (core.MergedFingerprintEntry, bool) {
	for _, o := range outcomes {
		if o.ModID == modID {
			return o, true
		}
	}
	return core.MergedFingerprintEntry{}, false
}

// TestHealthE2E_Lifecycle is the #224 spec's success-criterion-3 proof
// end-to-end through the REAL tui provider seams (no fakes for the service
// layer, only the source itself - the same boundary every other e2e test in
// this repo fakes at): a health-affecting state must be visible AND
// repairable from the TUI, not just the CLI.
//
//  1. DataProvider.Health (Local tier, no network - src.trap armed, mirroring
//     health_provider_test.go's TestHealthProvider_Local_NeverTouchesNetwork)
//     surfaces BOTH the needs_reingest and stale_deployment findings.
//  2. RunHealthCheck(full=true, fix=true) - trap disarmed, since this run
//     legitimately re-ingests over the network - resolves both: the
//     returned view carries fixed_needs_reingest and fixed_stale_deployment
//     rows, the dangling symlink is gone from disk, and the merged pak
//     regenerates with the re-ingested pak now converted.
//  3. A second Health call (trap re-armed) comes back clean: zero findings,
//     0/0 summary counts.
func TestHealthE2E_Lifecycle(t *testing.T) {
	provider, actions, svc, game, src := newHealthE2EFixture(t)
	mergedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	danglingPath := filepath.Join(game.ModPath, "stray.pak")

	// --- Step 1: both findings visible, Local tier, no network ---
	_, statErr := os.Lstat(danglingPath)
	require.NoError(t, statErr, "precondition: the dangling symlink exists before any check runs")
	_, statErr = os.Stat(mergedPath)
	require.True(t, os.IsNotExist(statErr), "precondition: no merged pak has ever been generated")

	before, err := provider.Health(context.Background())
	require.NoError(t, err)

	assert.False(t, before.Full, "Health always runs the Local tier")
	assert.Equal(t, 0, before.Issues, "both findings are warnings, not issues")
	assert.Equal(t, 2, before.Warnings)
	require.Len(t, before.Findings, 2)

	byStatus := make(map[string]tui.HealthFinding, len(before.Findings))
	for _, f := range before.Findings {
		byStatus[f.Status] = f
	}
	reingest, ok := byStatus["needs_reingest"]
	require.True(t, ok, "expected a needs_reingest finding: %+v", before.Findings)
	assert.Equal(t, "legacypak", reingest.ModID)
	stale, ok := byStatus["stale_deployment"]
	require.True(t, ok, "expected a stale_deployment finding: %+v", before.Findings)
	assert.Equal(t, "stray.pak", stale.FileID)

	// --- Step 2: full + fix resolves both (network use is now legitimate) ---
	src.trap = false
	var progress []string
	fixed, err := actions.RunHealthCheck(context.Background(), true, true, func(p tui.ActionProgress) {
		progress = append(progress, p.Line)
	})
	require.NoError(t, err)
	assert.NotEmpty(t, progress, "the fix run must stream progress")

	assert.True(t, fixed.Full)
	assert.Equal(t, 0, fixed.Issues)
	assert.Equal(t, 0, fixed.Warnings, "both repairs resolve - nothing outstanding")
	require.Len(t, fixed.Findings, 2)

	byStatus = make(map[string]tui.HealthFinding, len(fixed.Findings))
	for _, f := range fixed.Findings {
		byStatus[f.Status] = f
	}
	fixedReingest, ok := byStatus["fixed_needs_reingest"]
	require.True(t, ok, "expected a fixed_needs_reingest row: %+v", fixed.Findings)
	assert.Equal(t, "legacypak", fixedReingest.ModID)
	fixedStale, ok := byStatus["fixed_stale_deployment"]
	require.True(t, ok, "expected a fixed_stale_deployment row: %+v", fixed.Findings)
	assert.Equal(t, "stray.pak", fixedStale.FileID)

	_, statErr = os.Lstat(danglingPath)
	require.True(t, os.IsNotExist(statErr), "the fix must remove the dangling symlink from disk")

	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "the merged pak must be regenerated and deployed")

	outcomes, ok := svc.MergedPakOutcomes(game, "default")
	require.True(t, ok, "a merge must have run")
	legacyOutcome, found := findMergedOutcome(outcomes, "legacypak")
	require.True(t, found, "the re-ingested pak must appear in the merge fingerprint")
	assert.True(t, legacyOutcome.Converted, "the re-ingested pak must be reported converted, not raw-fallback")

	// --- Step 3: a second Health call comes back clean (Local tier again) ---
	// 2026-08-07 smoke feedback (#224): "clean" no longer means an empty
	// Findings list - the repaired file's row is now kept as a plain OK
	// entry (CLI parity), so this asserts every remaining row is "fine"
	// (healthStatusClass's own bucket for "ok") rather than that the list is
	// empty.
	src.trap = true
	after, err := provider.Health(context.Background())
	require.NoError(t, err)

	assert.False(t, after.Full)
	assert.Equal(t, 0, after.Issues)
	assert.Equal(t, 0, after.Warnings)
	for _, f := range after.Findings {
		assert.Equal(t, "ok", f.Status, "no outstanding (non-ok) findings remain: %+v", after.Findings)
	}
}
