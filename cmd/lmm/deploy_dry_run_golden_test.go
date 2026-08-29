package main

import (
	"bytes"
	"context"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- v2 Phase 2 Unit M Task 24: `lmm deploy --dry-run` ---
//
// --dry-run is NEW output behind a NEW flag, so these goldens are recorded
// in the task that introduces it (unlike the pre-lift characterization
// goldens elsewhere in this package, which are recorded on the PRE-lift
// tree). Once recorded they are frozen like every other golden here: a diff
// is a defect, never a re-record.
//
// Each golden has four sections: stdout, stderr, the returned error, and the
// game directory's contents afterwards. That last section is the point of a
// dry run - it must be byte-identical before and after, which is why every
// scenario records it rather than asserting it ad hoc.
//
// Format (all lines below are dry-run-only):
//
//	Deploy plan for profile "<name>" (dry run)
//	<blank>
//	[Would purge N file(s) before deploy...        -- only with --purge
//	[    - <path>                                  -- only under --verbose
//	<blank>]
//	Deploying N mod(s) using <method>...           -- the live header verbatim
//	<blank>                                        -- ("— compile mode..." on a compile game)
//	  ✓ <mod>[ (merged)|( raw)]                    -- the live per-mod lines,
//	  ✗ <mod> - <reason>                              rendered through doDeploy's own
//	[    + <linked path>                              progress closure
//	     - <removed path>]                         -- only under --verbose
//	[<blank>
//	Merged N mod(s) → <artifact>[ (M deployed raw)]]
//	<blank, unless the merge footer already printed one>
//	Would deploy: N[, Skipped: M]
//	[<blank>
//	Hooks that would run: <a, b, c>]
//
// An empty selection prints the live path's own "No mods to deploy." /
// "No enabled mods to deploy. Use --all to deploy disabled mods." line
// instead of everything from the deploy header down.

var updateDeployDryRunGoldens = flag.Bool("update-deploy-dry-run", false, "rewrite `lmm deploy --dry-run` goldens from current output")

// deployDryRunFixture is one scenario: an already-seeded service/game plus
// the positional args doDeploy is called with. The deploy flag globals are
// set by the fixture itself (setupDoDeployTest resets them all).
type deployDryRunFixture struct {
	svc  *core.Service
	game *domain.Game
	args []string
}

// dumpGameTree renders the game directory's contents - every regular file
// and symlink, relative and sorted - so a dry run's "changed nothing" claim
// is recorded, not assumed.
func dumpGameTree(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, rel)
		return nil
	}))
	sort.Strings(paths)
	if len(paths) == 0 {
		return "<empty>\n"
	}
	return strings.Join(paths, "\n") + "\n"
}

// runDeployDryRunGolden drives doDeploy with --dry-run for one scenario and
// compares (or, with -update-deploy-dry-run, records) the transcript.
func runDeployDryRunGolden(t *testing.T, name string, setup func(t *testing.T) deployDryRunFixture) {
	t.Helper()
	fx := setup(t)
	deployDryRun = true
	t.Cleanup(func() { deployDryRun = false })

	before := dumpGameTree(t, fx.game.ModPath)

	stdout, stderr, runErr := captureStdoutStderrErr(t, func() error {
		return doDeploy(context.Background(), fx.svc, fx.game, fx.args)
	})

	errText := "<nil>"
	if runErr != nil {
		errText = runErr.Error()
	}
	after := dumpGameTree(t, fx.game.ModPath)
	assert.Equal(t, before, after, "a dry run must not touch the game directory")

	var buf bytes.Buffer
	buf.WriteString("## stdout\n")
	buf.WriteString(stdout)
	buf.WriteString("## stderr\n")
	buf.WriteString(stderr)
	buf.WriteString("## error\n")
	buf.WriteString(errText)
	buf.WriteString("\n## tree\n")
	buf.WriteString(after)

	path := filepath.Join("testdata", "deploy_dry_run_golden", name+".golden")
	if *updateDeployDryRunGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing - record it with -update-deploy-dry-run")
	require.Equal(t, string(want), buf.String(), "`lmm deploy --dry-run` output drifted from the recorded golden")
}

func TestDeployDryRunGoldens(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) deployDryRunFixture
	}{
		{"two_mods", twoModsDryRunFixture},
		{"two_mods_verbose", func(t *testing.T) deployDryRunFixture {
			fx := twoModsDryRunFixture(t)
			setVerboseForTest(t, true)
			return fx
		}},
		{"method_override", func(t *testing.T) deployDryRunFixture {
			fx := twoModsDryRunFixture(t)
			deployMethod = "copy"
			return fx
		}},
		{"purge_verbose", func(t *testing.T) deployDryRunFixture {
			fx := twoModsDryRunFixture(t)
			deployPurge = true
			setVerboseForTest(t, true)
			return fx
		}},
		{"hooks", hooksDryRunFixture},
		{"stale_removal_verbose", func(t *testing.T) deployDryRunFixture {
			fx := staleRemovalDryRunFixture(t)
			setVerboseForTest(t, true)
			return fx
		}},
		{"disabled_mod", disabledModDryRunFixture},
		{"unknown_mod", unknownModDryRunFixture},
		{"no_mods", noModsDryRunFixture},
		{"no_mods_all", func(t *testing.T) deployDryRunFixture {
			fx := noModsDryRunFixture(t)
			deployAll = true
			return fx
		}},
		{"compile", compileDryRunFixture},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDeployDryRunGolden(t, tt.name, tt.setup)
		})
	}
}

// setVerboseForTest flips the package-level verbose flag for one test.
func setVerboseForTest(t *testing.T, v bool) {
	t.Helper()
	old := verbose
	verbose = v
	t.Cleanup(func() { verbose = old })
}

func twoModsDryRunFixture(t *testing.T) deployDryRunFixture {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")
	return deployDryRunFixture{svc: svc, game: game}
}

// hooksDryRunFixture configures two install hooks so the plan's hook readout
// has something to list.
func hooksDryRunFixture(t *testing.T) deployDryRunFixture {
	t.Helper()
	fx := twoModsDryRunFixture(t)
	script := filepath.Join(t.TempDir(), "hook.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o755))
	fx.game.Hooks = domain.GameHooks{
		Install: domain.HookConfig{BeforeAll: script, AfterEach: script},
	}
	require.NoError(t, fx.svc.SaveGame(context.Background(), fx.game))
	return fx
}

// staleRemovalDryRunFixture seeds #210's self-heal shape - a cache entry
// whose recorded manifests claim nothing while a stale file sits on disk,
// already linked into the game dir - so the plan has a Remove path to list.
func staleRemovalDryRunFixture(t *testing.T) deployDryRunFixture {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	seedNarrowedStaleCLIMod(t, svc, game, "stale", "Stale Mod", "Stale_P.pak")
	return deployDryRunFixture{svc: svc, game: game}
}

// seedDisabledMod is seedDeployableMod's disabled twin: cached and in the
// profile, but Enabled=false, so `lmm deploy <id>` refuses it without --all.
func seedDisabledMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, fileName string) {
	t.Helper()
	seedDeployableMod(t, svc, game, modID, name, fileName)
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      false,
	}))
}

// seedNarrowedStaleCLIMod mirrors internal/core's seedNarrowedStaleMod for
// this package: a cache entry holding a stale unclaimed pak plus a recorded
// zero-member marker AND a retained source, already linked into the game dir
// - the one shape whose plan lists a Remove path.
func seedNarrowedStaleCLIMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, stalePath string) {
	t.Helper()
	seedDeployableMod(t, svc, game, modID, name, stalePath)
	versionDir := svc.GetGameCache(game).ModPath(game.ID, "src", modID, "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, "exmodz", nil))
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, cache.RetainedSourceName("exmodz")), []byte("zip"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(versionDir, stalePath), filepath.Join(game.ModPath, stalePath)))
}

func disabledModDryRunFixture(t *testing.T) deployDryRunFixture {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	game.SourceIDs = map[string]string{"src": "g1"}
	seedDisabledMod(t, svc, game, "off", "Disabled Mod", "off.esp")
	return deployDryRunFixture{svc: svc, game: game, args: []string{"off"}}
}

func unknownModDryRunFixture(t *testing.T) deployDryRunFixture {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	game.SourceIDs = map[string]string{"src": "g1"}
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	return deployDryRunFixture{svc: svc, game: game, args: []string{"nope"}}
}

func noModsDryRunFixture(t *testing.T) deployDryRunFixture {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	return deployDryRunFixture{svc: svc, game: game}
}

func compileDryRunFixture(t *testing.T) deployDryRunFixture {
	t.Helper()
	svc, game, _ := setupDoDeployCompileTest(t)
	seedCompileExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	seedCompilePakMod(t, svc, game, "raw-pak", "Raw Pak Mod", "raw.pak")
	require.NoError(t, svc.SetModConvertPaks(context.Background(), "fake-compiler", "raw-pak", game.ID, "default", false))
	seedDeployableMod(t, svc, game, "loose", "Loose Mod", "loose.esp")
	return deployDryRunFixture{svc: svc, game: game}
}
