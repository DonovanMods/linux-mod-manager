package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- v2 Phase 2 Unit M Task 25: `lmm uninstall --dry-run` / `lmm purge --dry-run` ---
//
// Both flags are NEW output behind NEW flags, so these goldens are recorded
// in the task that introduces them (unlike the pre-lift characterization
// tests elsewhere in this package, which pin the PRE-lift tree). Once
// recorded they are frozen exactly as every other golden here: a diff is a
// defect, never a re-record.
//
// Each golden has four sections - stdout, stderr, the returned error, and
// the game directory afterwards - and every scenario additionally asserts
// the full-service no-mutation contract through snapshotDryRunState
// (game tree, cache tree, profile YAML, installed-mod DB rows), shared with
// deploy_dry_run_golden_test.go.
//
// `lmm uninstall --dry-run` format (all lines dry-run-only):
//
//	Uninstall plan for profile "<name>" (dry run)
//	<blank>
//	Would uninstall: <Name> (<source>:<id>)      -- the source answers "which
//	  Would remove from profile: <name>             copy did the bare-ID rule pick?"
//	  Would remove N file(s) from the game directory
//	[    - <path>                                -- only under --verbose]
//	  Cache entry would be deleted               -- or "Cache files preserved"
//	[<blank>                                        with --keep-cache
//	Hooks that would run: <a, b, c>]
//
// `lmm purge --dry-run` format:
//
//	Purge plan for profile "<name>" (dry run)
//	<blank>
//	Would undeploy N mod(s) from <Game> (profile: <name>)
//	Mod records will be preserved. Use 'lmm deploy' to restore.
//	<blank>                                      -- the prompt's own second line
//	Purging mods from <Game>...                  -- the live header verbatim,
//	<blank>                                         through doPurge's progress
//	  ✓ <mod>                                       closure
//	<blank>
//	Would purge: N mod(s)
//	[<blank>
//	Hooks that would run: <a, b, c>]
//
// An empty profile prints the live path's own "No mods installed for <Game>
// (profile: <name>)" line and nothing else. A dry run never prompts, which
// "purge_no_prompt" pins with --yes off.

var updateUninstallPurgeDryRunGoldens = flag.Bool("update-uninstall-purge-dry-run",
	false, "rewrite `lmm uninstall --dry-run` / `lmm purge --dry-run` goldens from current output")

// dryRunGoldenFixture is one scenario: a seeded service/game plus the call
// that drives the command under test. The flag globals are set by the
// fixture itself (setupDoUninstallTest/setupDoPurgeTest reset them all).
type dryRunGoldenFixture struct {
	svc  *core.Service
	game *domain.Game
	run  func(ctx context.Context, svc *core.Service, game *domain.Game) error
}

// runDryRunGolden drives one scenario and compares (or, with the update
// flag, records) the transcript, asserting no-mutation on both sides of it.
func runDryRunGolden(t *testing.T, dir, name string, setup func(t *testing.T) dryRunGoldenFixture) {
	t.Helper()
	fx := setup(t)

	before := snapshotDryRunState(t, fx.svc, fx.game, "default")

	stdout, stderr, runErr := captureStdoutStderrErr(t, func() error {
		return fx.run(context.Background(), fx.svc, fx.game)
	})

	errText := "<nil>"
	if runErr != nil {
		errText = runErr.Error()
	}
	after := snapshotDryRunState(t, fx.svc, fx.game, "default")
	assert.Equal(t, before.gameTree, after.gameTree, "a dry run must not touch the game directory")
	assert.Equal(t, before.cacheTree, after.cacheTree, "a dry run must not touch the cache directory")
	assert.Equal(t, before.profileYAML, after.profileYAML, "a dry run must not touch the profile file")
	assert.Equal(t, before.mods, after.mods, "a dry run must not touch the installed-mods DB rows")

	var buf bytes.Buffer
	buf.WriteString("## stdout\n")
	buf.WriteString(stdout)
	buf.WriteString("## stderr\n")
	buf.WriteString(stderr)
	buf.WriteString("## error\n")
	buf.WriteString(errText)
	buf.WriteString("\n## tree\n")
	buf.WriteString(after.gameTree)

	path := filepath.Join("testdata", dir, name+".golden")
	if *updateUninstallPurgeDryRunGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing - record it with -update-uninstall-purge-dry-run")
	require.Equal(t, string(want), buf.String(), "--dry-run output drifted from the recorded golden")
}

// --- uninstall ---

// setupUninstallDryRun builds the shared uninstall fixture: two deployed
// mods, --dry-run on, and doUninstall bound to modID.
func setupUninstallDryRun(t *testing.T, modID string) dryRunGoldenFixture {
	t.Helper()
	svc, game := setupDoPurgeTest(t) // the same seeding shape, without the undeploy obstruction
	oldSource, oldProfile, oldKeep, oldForce := uninstallSource, uninstallProfile, uninstallKeep, uninstallForce
	uninstallSource, uninstallProfile, uninstallKeep, uninstallForce = "", "", false, false
	t.Cleanup(func() {
		uninstallSource, uninstallProfile, uninstallKeep, uninstallForce = oldSource, oldProfile, oldKeep, oldForce
	})
	uninstallDryRun = true
	t.Cleanup(func() { uninstallDryRun = false })
	seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")
	return dryRunGoldenFixture{svc: svc, game: game, run: func(ctx context.Context, s *core.Service, g *domain.Game) error {
		return doUninstall(ctx, s, g, modID)
	}}
}

// seedSecondSourceMod seeds a same-ID copy of a mod from a different source,
// so a bare-ID uninstall has something to disambiguate.
func seedSecondSourceMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, name, fileName string) {
	t.Helper()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, sourceID, modID, "1.0", fileName, []byte("data")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(game.ID, "default",
		domain.ModReference{SourceID: sourceID, ModID: modID, Version: "1.0"}))
}

func TestUninstallDryRunGoldens(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) dryRunGoldenFixture
	}{
		{"deployed_mod", func(t *testing.T) dryRunGoldenFixture {
			return setupUninstallDryRun(t, "a")
		}},
		{"deployed_mod_verbose", func(t *testing.T) dryRunGoldenFixture {
			fx := setupUninstallDryRun(t, "a")
			setVerboseForTest(t, true)
			return fx
		}},
		{"keep_cache", func(t *testing.T) dryRunGoldenFixture {
			fx := setupUninstallDryRun(t, "a")
			uninstallKeep = true
			return fx
		}},
		{"not_deployed", func(t *testing.T) dryRunGoldenFixture {
			fx := setupUninstallDryRun(t, "b")
			seedDeployableMod(t, fx.svc, fx.game, "b", "Mod B", "b.esp")
			return fx
		}},
		{"bare_id_first_match_verbose", func(t *testing.T) dryRunGoldenFixture {
			fx := setupUninstallDryRun(t, "a")
			seedSecondSourceMod(t, fx.svc, fx.game, "other", "a", "Other A", "other-a.esp")
			setVerboseForTest(t, true)
			return fx
		}},
		{"explicit_source", func(t *testing.T) dryRunGoldenFixture {
			fx := setupUninstallDryRun(t, "a")
			seedSecondSourceMod(t, fx.svc, fx.game, "other", "a", "Other A", "other-a.esp")
			fx.game.SourceIDs = map[string]string{"src": "g1", "other": "g1"}
			uninstallSource = "other"
			return fx
		}},
		{"hooks", func(t *testing.T) dryRunGoldenFixture {
			fx := setupUninstallDryRun(t, "a")
			script := filepath.Join(t.TempDir(), "hook.sh")
			require.NoError(t, os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o755))
			fx.game.Hooks = domain.GameHooks{
				Uninstall: domain.HookConfig{BeforeAll: script, AfterEach: script},
			}
			require.NoError(t, fx.svc.SaveGame(context.Background(), fx.game))
			return fx
		}},
		{"unknown_mod", func(t *testing.T) dryRunGoldenFixture {
			return setupUninstallDryRun(t, "nope")
		}},
		{
			// DeployCompile with nothing merged yet: the post-uninstall
			// sync would leave the game directory exactly as it is, so
			// Ruling 8's merged-artifact line is absent entirely.
			"compile", func(t *testing.T) dryRunGoldenFixture {
				return compileDryRunUninstallFixture(t, "bear-mount", nil)
			},
		},
		{
			// DeployCompile, and the target is the LAST merge source: the
			// sync's uninstall-to-zero branch takes the artifact out.
			"compile_remove", func(t *testing.T) dryRunGoldenFixture {
				return compileDryRunUninstallFixture(t, "bear-mount", func(t *testing.T, svc *core.Service, game *domain.Game) {
					syncCompileMergedArtifact(t, svc, game)
				})
			},
		},
		{
			// DeployCompile with another merge source left behind: the
			// artifact is rebuilt rather than removed.
			"compile_resync", func(t *testing.T) dryRunGoldenFixture {
				return compileDryRunUninstallFixture(t, "bear-mount", func(t *testing.T, svc *core.Service, game *domain.Game) {
					seedCompileExmodzMod(t, svc, game, "wolf-mount", "Wolf Mount", "exmodz-file")
					syncCompileMergedArtifact(t, svc, game)
				})
			},
		},
		{
			// DeployCompile, but the uninstall target contributes nothing
			// to the merge: the artifact is untouched, so no line - the
			// case the pre-Ruling-8 unconditional line got wrong.
			"compile_untouched", func(t *testing.T) dryRunGoldenFixture {
				return compileDryRunUninstallFixture(t, "plain", func(t *testing.T, svc *core.Service, game *domain.Game) {
					seedDeployableMod(t, svc, game, "plain", "Plain Mod", "plain.esp")
					syncCompileMergedArtifact(t, svc, game)
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDryRunGolden(t, "uninstall_dry_run_golden", tt.name, tt.setup)
		})
	}
}

// --- purge ---

// setupPurgeDryRun builds the shared purge fixture: two deployed mods and
// --dry-run on.
func setupPurgeDryRun(t *testing.T) dryRunGoldenFixture {
	t.Helper()
	svc, game := setupDoPurgeTest(t)
	purgeDryRun = true
	seedPurgeableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedPurgeableMod(t, svc, game, "b", "Mod B", "b.esp")
	return dryRunGoldenFixture{svc: svc, game: game, run: doPurge}
}

func TestPurgeDryRunGoldens(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) dryRunGoldenFixture
	}{
		{"two_mods", setupPurgeDryRun},
		{"two_mods_verbose", func(t *testing.T) dryRunGoldenFixture {
			fx := setupPurgeDryRun(t)
			setVerboseForTest(t, true)
			return fx
		}},
		{"uninstall_records", func(t *testing.T) dryRunGoldenFixture {
			fx := setupPurgeDryRun(t)
			purgeUninstall = true
			return fx
		}},
		{
			// --yes off: a dry run must render without ever reaching the
			// confirmation prompt (this test provides no stdin to read).
			"no_prompt", func(t *testing.T) dryRunGoldenFixture {
				fx := setupPurgeDryRun(t)
				purgeYes = false
				return fx
			},
		},
		{"hooks", func(t *testing.T) dryRunGoldenFixture {
			fx := setupPurgeDryRun(t)
			script := filepath.Join(t.TempDir(), "hook.sh")
			require.NoError(t, os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o755))
			fx.game.Hooks = domain.GameHooks{
				Uninstall: domain.HookConfig{BeforeEach: script, AfterAll: script},
			}
			require.NoError(t, fx.svc.SaveGame(context.Background(), fx.game))
			return fx
		}},
		{"no_mods", func(t *testing.T) dryRunGoldenFixture {
			svc, game := setupDoPurgeTest(t)
			purgeDryRun = true
			_, err := svc.NewProfileManager().Create(game.ID, "default")
			require.NoError(t, err)
			return dryRunGoldenFixture{svc: svc, game: game, run: doPurge}
		}},
		{
			// DeployCompile with nothing merged yet: there is no artifact
			// to remove, so Ruling 8's line is absent entirely - the case
			// the pre-Ruling-8 unconditional line got wrong.
			"compile", func(t *testing.T) dryRunGoldenFixture {
				return compileDryRunPurgeFixture(t, nil)
			},
		},
		{
			// DeployCompile with the artifact deployed: a purge also
			// removes it, which PurgePlan's mod list cannot say.
			"compile_deployed", func(t *testing.T) dryRunGoldenFixture {
				return compileDryRunPurgeFixture(t, func(t *testing.T, svc *core.Service, game *domain.Game) {
					syncCompileMergedArtifact(t, svc, game)
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDryRunGolden(t, "purge_dry_run_golden", tt.name, tt.setup)
		})
	}
}

// --- DeployCompile fixtures ---
//
// setupDoDeployCompileTest resets the DEPLOY flag globals (it builds on
// setupDoDeployTest), so these two reset the uninstall/purge ones themselves
// rather than routing through setupDoUninstallTest/setupDoPurgeTest, which
// would rebuild the service.

// compileMergedArtifactName is the fake compile source's artifact filename.
const compileMergedArtifactName = "zzz_LMM_Merged_P.pak"

// syncCompileMergedArtifact generates and deploys the profile's merged
// artifact, so a fixture can distinguish "a compile game" from "a compile
// game with something merged" - the two halves of Ruling 8.
func syncCompileMergedArtifact(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(game.ModPath, compileMergedArtifactName))
	require.NoError(t, err, "fixture: the merged artifact must be deployed")
}

// compileDryRunUninstallFixture builds the compile uninstall fixture: one
// seeded exmodz mod plus whatever extra does (a second mod, a merged-artifact
// sync), then a --dry-run uninstall of target.
func compileDryRunUninstallFixture(t *testing.T, target string, extra func(t *testing.T, svc *core.Service, game *domain.Game)) dryRunGoldenFixture {
	t.Helper()
	svc, game, _ := setupDoDeployCompileTest(t)
	seedCompileExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	if extra != nil {
		extra(t, svc, game)
	}

	oldSource, oldProfile, oldKeep, oldForce, oldDryRun, oldNoColor :=
		uninstallSource, uninstallProfile, uninstallKeep, uninstallForce, uninstallDryRun, noColor
	uninstallSource, uninstallProfile, uninstallKeep, uninstallForce = "", "", false, false
	uninstallDryRun = true
	noColor = true
	t.Cleanup(func() {
		uninstallSource, uninstallProfile, uninstallKeep, uninstallForce, uninstallDryRun, noColor =
			oldSource, oldProfile, oldKeep, oldForce, oldDryRun, oldNoColor
	})

	return dryRunGoldenFixture{svc: svc, game: game, run: func(ctx context.Context, s *core.Service, g *domain.Game) error {
		return doUninstall(ctx, s, g, target)
	}}
}

func compileDryRunPurgeFixture(t *testing.T, extra func(t *testing.T, svc *core.Service, game *domain.Game)) dryRunGoldenFixture {
	t.Helper()
	svc, game, _ := setupDoDeployCompileTest(t)
	seedCompileExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	if extra != nil {
		extra(t, svc, game)
	}

	oldProfile, oldUninstall, oldYes, oldForce, oldDryRun, oldNoColor :=
		purgeProfile, purgeUninstall, purgeYes, purgeForce, purgeDryRun, noColor
	purgeProfile, purgeUninstall, purgeForce = "", false, false
	purgeYes = true
	purgeDryRun = true
	noColor = true
	t.Cleanup(func() {
		purgeProfile, purgeUninstall, purgeYes, purgeForce, purgeDryRun, noColor =
			oldProfile, oldUninstall, oldYes, oldForce, oldDryRun, oldNoColor
	})

	return dryRunGoldenFixture{svc: svc, game: game, run: doPurge}
}

// --- dry run predicts the live run (Task 24 review, Important #2) ---

// linesWithPrefix returns out's lines carrying prefix, prefix stripped.
func linesWithPrefix(out, prefix string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			got = append(got, strings.TrimPrefix(l, prefix))
		}
	}
	return got
}

// TestUninstallDryRun_FileListMatchesWhatTheLiveUninstallRemoves is the
// uninstall half of the fidelity contract at the CLI seam: the paths a
// verbose dry run lists are exactly the ones the live uninstall then takes
// out of the game directory - proven by diffing the tree across the real
// run, not by recomputing the plan.
func TestUninstallDryRun_FileListMatchesWhatTheLiveUninstallRemoves(t *testing.T) {
	fx := setupUninstallDryRun(t, "a")
	setVerboseForTest(t, true)

	out := captureStdout(t, func() error {
		return fx.run(context.Background(), fx.svc, fx.game)
	})
	predicted := linesWithPrefix(out, "    - ")
	require.NotEmpty(t, predicted, "fixture: the mod must be deployed, so the dry run has files to list")

	before := dumpTree(t, fx.game.ModPath)
	uninstallDryRun = false
	_, err := captureStdoutErr(t, func() error {
		return fx.run(context.Background(), fx.svc, fx.game)
	})
	require.NoError(t, err)

	beforeSet := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSpace(before), "\n") {
		beforeSet[p] = true
	}
	for _, p := range strings.Split(strings.TrimSpace(dumpTree(t, fx.game.ModPath)), "\n") {
		delete(beforeSet, p)
	}
	removed := make([]string, 0, len(beforeSet))
	for p := range beforeSet {
		removed = append(removed, p)
	}
	sort.Strings(removed)
	sort.Strings(predicted)
	assert.Equal(t, predicted, removed, "the dry run's file list must be what the live uninstall removed")
}

// TestPurgeDryRun_ModLinesMatchTheLiveRun is the purge half: the "✓ <mod>"
// lines a dry run prints are exactly the ones the live purge then prints,
// in the same order - the guarantee that PurgePlan.Mods is one object, not
// two reads that can disagree.
func TestPurgeDryRun_ModLinesMatchTheLiveRun(t *testing.T) {
	fx := setupPurgeDryRun(t)

	dryOut := captureStdout(t, func() error {
		return fx.run(context.Background(), fx.svc, fx.game)
	})
	predicted := linesWithPrefix(dryOut, "  ✓ ")
	require.Len(t, predicted, 2, "fixture: two mods are deployed")

	purgeDryRun = false
	liveOut := captureStdout(t, func() error {
		return fx.run(context.Background(), fx.svc, fx.game)
	})
	assert.Equal(t, predicted, linesWithPrefix(liveOut, "  ✓ "),
		"the dry run's per-mod lines must be what the live purge printed")
	assert.Equal(t, "<empty>\n", dumpTree(t, fx.game.ModPath), "the live purge undeployed every planned mod")
}
