package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// --- #224 Task 1: pre-refactor golden capture ---
//
// These golden transcripts freeze doVerify's CURRENT byte-for-byte stdout
// output across the scenarios that matter for the verify renderer work:
// they are the safety net the later renderer-refactor tasks diff against, so
// they must never be regenerated once recorded on this pre-refactor tree.
//
// One exception, taken once: the *_json goldens were re-recorded in v2
// Phase 3 Task 6 (#302), when --json switched from the cmd-local
// verifyJSONOutput view struct to core.VerifyReport - the single deliberate
// JSON shape change Ruling 3 reserves for the v2.0.0 window. The *_plain and
// *_fix (text) goldens were NOT touched by that re-record and are still the
// pre-refactor bytes.

var updateGolden = flag.Bool("update", false, "rewrite verify golden files from current output")

// runVerifyGolden runs doVerify against a fresh fixture with the given
// output-mode globals and compares (or, with -update, records) the exact
// stdout transcript. Colors are already suppressed in tests (no TTY), so
// the transcript is stable. Each invocation builds its OWN fixture: fix
// mode mutates state, so scenarios can never share one.
func runVerifyGolden(t *testing.T, name string, fixture func(*testing.T) (*cobra.Command, *core.Service, *domain.Game), fix, json bool) {
	t.Helper()
	cmd, svc, game := fixture(t)
	oldFix, oldJSON := verifyFix, jsonOutput
	verifyFix, jsonOutput = fix, json
	t.Cleanup(func() { verifyFix, jsonOutput = oldFix, oldJSON })

	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	path := filepath.Join("testdata", "verify_golden", name+".golden")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing - run with -update on the PRE-refactor tree only")
	require.Equal(t, string(want), out, "verify output must be byte-identical to the pre-refactor golden")
}

// versionMismatchGoldenFixture adapts setupDoVerifyFixTest (verify_test.go)
// to the golden harness signature: an installed row recorded at "1.5" whose
// stored file is really "1.0" per the source, unlocked, discarding the
// fixture's own verifyFix=true side effect (runVerifyGolden sets the mode
// globals itself, after the fixture returns).
func versionMismatchGoldenFixture(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()
	return setupDoVerifyFixTest(t, false)
}

// versionMismatchLockedGoldenFixture layers a mod lock onto the same
// version-mismatch shape, reproducing the fixture TestDoVerify_Fix_VersionMismatchLocked_RefusesRepair
// (verify_test.go) uses to guard the "--fix skipped: ... is locked" refusal
// text and its "locked" JSON note.
func versionMismatchLockedGoldenFixture(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()
	cmd, svc, game := setupDoVerifyFixTest(t, false)
	pm := getProfileManager(svc)
	require.NoError(t, pm.SetModLock(game.ID, "default", "test-src", "mod1", "1.5"))
	return cmd, svc, game
}

// needsReingestGoldenFixture adapts TestVerifyNeedsReingest_ReportsThenFixes's
// fixture-building half (verify_convert_test.go): a convert-eligible pak
// whose cache entry predates pak retention (deployable pak present, no
// retained source), the #221 "needs_reingest" / `verify --fix` re-ingest
// shape.
func needsReingestGoldenFixture(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "legacy-pak-mod", "1.0", "LegacyMod.pak"
	seedLegacyPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("legacy-pak-bytes"))

	compiler.AddMod(&domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Legacy Pak Mod", Version: version, GameID: game.ID},
		[]domain.DownloadableFile{{ID: fileID, FileName: "LegacyMod.pak", IsPrimary: true}})
	compiler.AddDownload(fileID, []byte("fresh-pak-bytes"))

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd, svc, game
}

// conversionFailedGoldenFixture adapts TestVerifyReportsConversionFailed's
// fixture-building half (verify_convert_test.go): a #221 pak-conversion
// failure recorded on the merged pak's own stored fingerprint, the
// "conversion_failed" / "deploying raw" shape.
func conversionFailedGoldenFixture(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
	t.Helper()
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "raw-pak-mod", "1.0", "modfile.pak"
	seedEnabledPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("pak-bytes"))

	outcome := &pakOutcomeCompilerSource{
		compilerInstallSource: compiler,
		failRefs:              map[string]string{"fake-compiler:" + modID: "table X not present in current base"},
	}
	svc.RegisterSource(outcome)

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	return cmd, svc, game
}

func TestVerifyGolden(t *testing.T) {
	scenarios := []struct {
		name    string
		fixture func(*testing.T) (*cobra.Command, *core.Service, *domain.Game)
	}{
		{"empty_profile", func(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
			cmd, svc, game, _ := setupDoVerifyEmptyProfileConvergeTest(t)
			return cmd, svc, game
		}},
		{"converge", setupDoVerifyConvergeTest},
		{"version_mismatch", versionMismatchGoldenFixture},
		{"version_mismatch_locked", versionMismatchLockedGoldenFixture},
		{"needs_reingest", needsReingestGoldenFixture},
		{"conversion_failed", conversionFailedGoldenFixture},
	}
	for _, sc := range scenarios {
		for _, mode := range []struct {
			suffix    string
			fix, json bool
		}{{"plain", false, false}, {"fix", true, false}, {"json", false, true}, {"fix_json", true, true}} {
			t.Run(sc.name+"_"+mode.suffix, func(t *testing.T) {
				runVerifyGolden(t, sc.name+"_"+mode.suffix, sc.fixture, mode.fix, mode.json)
			})
		}
	}
}
