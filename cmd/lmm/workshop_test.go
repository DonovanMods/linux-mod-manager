package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #269's CLI surfaces: `lmm import --workshop`, the EXTERNAL marker in
// `lmm list`, the external count in `lmm status`, `lmm mod show`'s
// "Managed by: Steam Workshop" block, and the update summary line. Every
// test drives a fake workshop source - nothing here reads a real Steam
// library or reaches Valve.

// cliWorkshopSource is the minimum ModSource that satisfies the two
// optional capabilities core's workshop adopt uses.
type cliWorkshopSource struct {
	scan     source.WorkshopScan
	describe []source.ModDescription
}

func (s *cliWorkshopSource) ID() string   { return "steamworkshop" }
func (s *cliWorkshopSource) Name() string { return "Steam Workshop" }
func (s *cliWorkshopSource) AuthURL() string {
	return ""
}

func (s *cliWorkshopSource) TypeLabel() string { return "built-in" }
func (s *cliWorkshopSource) Capabilities() source.Capabilities {
	return source.Capabilities{Updates: true}
}

func (s *cliWorkshopSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (s *cliWorkshopSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}

func (s *cliWorkshopSource) GetMod(_ context.Context, gameID, modID string) (*domain.Mod, error) {
	for _, d := range s.describe {
		if d.ModID == modID && !d.Unavailable {
			mod := d.Mod
			return &mod, nil
		}
	}
	return nil, domain.ErrModNotFound
}

func (s *cliWorkshopSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}

func (s *cliWorkshopSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}

func (s *cliWorkshopSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}

func (s *cliWorkshopSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

func (s *cliWorkshopSource) ScanWorkshopItems(context.Context, string) (source.WorkshopScan, error) {
	return s.scan, nil
}

func (s *cliWorkshopSource) DescribeMods(_ context.Context, _ string, ids []string, _ bool) ([]source.ModDescription, error) {
	out := make([]source.ModDescription, 0, len(ids))
	for _, id := range ids {
		found := false
		for _, d := range s.describe {
			if d.ModID == id {
				out = append(out, d)
				found = true
				break
			}
		}
		if !found {
			out = append(out, source.ModDescription{ModID: id, Unavailable: true, Note: "not described"})
		}
	}
	return out, nil
}

// setupWorkshopCLI wires a Service whose game maps the fake workshop source
// and has one subscribed item sitting on a temp "Steam" tree.
func setupWorkshopCLI(t *testing.T) (*core.Service, *domain.Game, *cliWorkshopSource, string) {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	game.SourceIDs = map[string]string{"steamworkshop": "1133870"}

	steamDir := filepath.Join(t.TempDir(), "steamapps", "workshop", "content", "1133870", "3617086610")
	require.NoError(t, os.MkdirAll(steamDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(steamDir, "mod.pak"), []byte("steam"), 0o644))

	src := &cliWorkshopSource{
		scan: source.WorkshopScan{
			Roots: []string{"/GOLDEN/steam"},
			Items: []domain.WorkshopItem{{
				FileID: "3617086610", Path: steamDir, SizeOnDisk: 572330,
				Manifest: "7987119735124793734", TimeUpdated: 1764767935,
			}},
		},
		describe: []source.ModDescription{{
			ModID: "3617086610",
			Mod: domain.Mod{
				ID: "3617086610", SourceID: "steamworkshop", Name: "Sample Workshop Item",
				Author: "76561198000000000", Category: "Blueprint",
				// The live source stamps the item's 19-digit content id here
				// (steamworkshop/client.go#modFromDetails), so the fixture must
				// too: a catalog document with an EMPTY Version is a state
				// production never produces, and a golden recorded against one
				// cannot see a surface printing the manifest as a version.
				Version:   "7987119735124793734",
				Summary:   "An item subscribed in the Steam client.",
				SourceURL: "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610",
				UpdatedAt: time.Unix(1764767935, 0).UTC(),
			},
		}},
	}
	svc.RegisterSource(src)
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return svc, game, src, steamDir
}

// withWorkshopImportFlags sets the import command's package-level flags for
// one test and restores them after.
func withWorkshopImportFlags(t *testing.T, dryRun, force bool) {
	t.Helper()
	oldWorkshop, oldDry, oldForce, oldSkip := importWorkshop, importDryRun, importForce, importSkipMatch
	importWorkshop, importDryRun, importForce, importSkipMatch = true, dryRun, force, false
	t.Cleanup(func() {
		importWorkshop, importDryRun, importForce, importSkipMatch = oldWorkshop, oldDry, oldForce, oldSkip
	})
}

func TestImportWorkshop_DryRunJSONEmitsThePlan(t *testing.T) {
	svc, game, _, steamDir := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, true, false)
	withJSONOutput(t)

	out := captureStdout(t, func() error {
		return runImportWorkshop(context.Background(), svc, game, "default")
	})
	assertJSONCLIGolden(t, "import_workshop_dry_run", out, steamDir, "/GOLDEN/workshop/3617086610")
}

func TestImportWorkshop_JSONEmitsTheResult(t *testing.T) {
	svc, game, _, _ := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	withJSONOutput(t)

	out := captureStdout(t, func() error {
		return runImportWorkshop(context.Background(), svc, game, "default")
	})
	assertJSONCLIGolden(t, "import_workshop_result", out)

	mod, err := svc.GetInstalledMod(context.Background(), "steamworkshop", "3617086610", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.External)
}

func TestImportWorkshop_RefusesAnArchiveArgumentAndSkipMatch(t *testing.T) {
	svc, game, _, _ := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)

	err := doImportWorkshopGate(svc, game, []string{"/tmp/mod.zip"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot take an archive path")

	importSkipMatch = true
	err = doImportWorkshopGate(svc, game, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--skip-match does not apply")
}

func TestImportWorkshop_JSONWithoutForceRefusesRatherThanPrompting(t *testing.T) {
	svc, game, _, _ := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, false)
	withJSONOutput(t)

	_, err := captureStdoutErr(t, func() error {
		return runImportWorkshop(context.Background(), svc, game, "default")
	})
	require.ErrorIs(t, err, core.ErrConfirmationRequired)
}

func TestList_ShowsTheExternalMarkerAndADateNotAContentID(t *testing.T) {
	svc, game, _, _ := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))

	out := listVerbose(t, svc, game, false)
	assert.Contains(t, out, "EXTERNAL")
	assert.Contains(t, out, "1 tracked from Steam")
	assert.Contains(t, out, "2025-12-03", "the revision date, not the 19-digit content id")
	assert.NotContains(t, out, "7987119735124793734")
}

func TestList_ExternalJSONGolden(t *testing.T) {
	svc, game, _, steamDir := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))

	assertJSONCLIGolden(t, "list_external", listVerbose(t, svc, game, true),
		steamDir, "/GOLDEN/workshop/3617086610")
}

func TestModShow_RendersTheManagedBySteamBlock(t *testing.T) {
	svc, game, _, steamDir := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))

	old := modProfile
	modProfile = "default"
	t.Cleanup(func() { modProfile = old })

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "3617086610")
	})
	assert.Contains(t, out, "Managed by: Steam Workshop")
	assert.Contains(t, out, steamDir)
	assert.Contains(t, out, "Steam content id: 7987119735124793734",
		"the content id is shown, labelled as itself, never as a version")
	assert.Contains(t, out, "rollback are not available")

	// The whole block, frozen: the scrub keeps the machine-specific Steam
	// path out of the golden.
	assertModShowGolden(t, "external", strings.ReplaceAll(out, steamDir, "/GOLDEN/workshop/3617086610"))
}

func TestPrintExternalUpdateSummary_SaysSteamAppliesThem(t *testing.T) {
	updates := []domain.Update{
		{InstalledMod: domain.InstalledMod{
			Mod:      domain.Mod{ID: "3617086610", SourceID: "steamworkshop", Name: "Sample Workshop Item"},
			External: true,
		}, NewVersion: "8100000000000000001"},
		{InstalledMod: domain.InstalledMod{
			Mod: domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Ordinary Mod", Version: "1.0"},
		}, NewVersion: "1.1"},
	}
	out := captureStdout(t, func() error { printExternalUpdateSummary(updates); return nil })
	assert.Contains(t, out, "1 Steam Workshop item(s) have updates")
	assert.Contains(t, out, "Steam applies these itself")
}

func TestPrintExternalUpdateSummary_SilentWithNoExternalUpdates(t *testing.T) {
	updates := []domain.Update{{
		InstalledMod: domain.InstalledMod{Mod: domain.Mod{ID: "42", SourceID: "nexusmods"}},
		NewVersion:   "1.1",
	}}
	out := captureStdout(t, func() error { printExternalUpdateSummary(updates); return nil })
	assert.Empty(t, strings.TrimSpace(out))
}

// The dry run and the live run must say the SAME thing about the same
// profile. Before the fix renderDeployPlan walked past DeployModExternal,
// counted it as deployable and printed a green "âœ“ <name>" - promising a
// deployment lmm never makes - while the live path emitted
// DeployExternalSkipped, which no case in the CLI's switch handled, so it
// printed nothing at all.
func TestDeploy_ExternalModReadsTheSameDryRunAndLive(t *testing.T) {
	svc, game, _, steamDir := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))

	oldProfile, oldDry := deployProfile, deployDryRun
	deployProfile = "default"
	t.Cleanup(func() { deployProfile, deployDryRun = oldProfile, oldDry })

	deployDryRun = true
	dry := captureStdout(t, func() error { return doDeploy(context.Background(), svc, game, nil) })
	deployDryRun = false
	live := captureStdout(t, func() error { return doDeploy(context.Background(), svc, game, nil) })

	for name, out := range map[string]string{"dry run": dry, "live": live} {
		assert.Contains(t, out, "Sample Workshop Item", "%s names the item", name)
		assert.Contains(t, out, "tracked from Steam, not deployed", "%s says what lmm will not do", name)
		assert.NotContains(t, out, "âœ“ Sample Workshop Item",
			"%s must not promise a deployment lmm never makes", name)
	}
	assert.Contains(t, dry, "Would deploy: 0", "an external mod is not deployable")
	assert.Contains(t, dry, "Skipped: 1")
	assert.DirExists(t, steamDir, "Steam still owns its files")
}

// The uninstall confirmation must not state falsehoods about a mod lmm does
// not own. Before the fix the dry run printed "Would remove 0 file(s) from
// the game directory" and "Cache entry would be deleted" - there is no cache
// entry, and --keep-cache is a documented no-op for this row.
func TestUninstall_DryRunSaysItRemovesTrackingOnly(t *testing.T) {
	svc, game, _, steamDir := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))

	plan, err := svc.PlanUninstall(context.Background(), game, "default",
		"steamworkshop", "3617086610", core.UninstallOptions{})
	require.NoError(t, err)
	require.True(t, plan.External, "precondition: core classes the row external")

	out := captureStdout(t, func() error { renderUninstallPlan(plan, "default"); return nil })
	assert.Contains(t, out, core.UninstallExternalNote)
	assert.Contains(t, out, steamDir, "the confirmation names where the item really lives")
	assert.NotContains(t, out, "Cache entry would be deleted", "there is no cache entry")
	assert.NotContains(t, out, "file(s) from the game directory", "lmm deploys none of its files")
}

// The LIVE readout too: "Uninstalled" claims more than happened, and the
// note reached stdout only under --verbose (via result.Notes) before.
func TestUninstall_LiveReadoutSaysItStoppedTracking(t *testing.T) {
	svc, game, _, _ := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))

	oldProfile := uninstallProfile
	uninstallProfile = "default"
	t.Cleanup(func() { uninstallProfile = oldProfile })

	out := captureStdout(t, func() error {
		return doUninstall(context.Background(), svc, game, "3617086610")
	})
	assert.Contains(t, out, "Stopped tracking: Sample Workshop Item")
	assert.Contains(t, out, core.UninstallExternalNote)
	assert.NotContains(t, out, "Uninstalled:", "lmm removed its tracking, not the mod")
}

// PurgePlan.External exists "so the preview says what it will not touch"
// (design §2); before the fix core populated it and neither preview printed
// it, so the user saw a shorter mod count with no explanation.
func TestPurge_DryRunNamesWhatItLeavesAlone(t *testing.T) {
	svc, game, _, _ := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))

	plan, err := svc.PlanPurge(context.Background(), game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"Sample Workshop Item"}, plan.External,
		"precondition: core excludes it from the purge set and names it")

	out := captureStdout(t, func() error {
		renderPurgePlan(plan, game, func(core.Event) {})
		return nil
	})
	assert.Contains(t, out, "Left alone (tracked from Steam): Sample Workshop Item")
}

// The version DISPLAY rule is one date per instant, on every surface. `lmm
// mod show` renders a .UTC() time and the SPA renders toISOString, so this
// formatter must be UTC too or the same item reads as two different dates
// for a user far enough east - a bug no test in the reviewer's timezone
// could see.
func TestWorkshopRevisionDate_IsUTCWhateverTheMachinesZone(t *testing.T) {
	// time.Local is process-global, so it is moved and put back around the
	// two calls rather than in a Cleanup - nothing else in this package runs
	// in parallel, and the window is one function call wide.
	saved := time.Local
	time.Local = time.FixedZone("LINT", 14*60*60) // Kiritimati, UTC+14
	// 1764767935 = 2025-12-03T12:38:55Z, which is 2025-12-04 locally there.
	got, empty := workshopRevisionDate(1764767935), workshopRevisionDate(0)
	time.Local = saved

	assert.Equal(t, "2025-12-03", got)
	assert.Empty(t, empty)
}

func TestPrintBatchSkips_SplitsLockedFromSteamWorkshop(t *testing.T) {
	out := captureStdout(t, func() error {
		printBatchSkips([]core.UpdateApplyResult{
			{
				Mod:    domain.ModReference{SourceID: "nexusmods", ModID: "42", Locked: true},
				Name:   "Locked Mod",
				Status: core.UpdateSkipped,
				Reason: "locked",
			},
			{
				Mod:    domain.ModReference{SourceID: "steamworkshop", ModID: "3617086610"},
				Name:   "Sample Workshop Item",
				Status: core.UpdateSkipped,
				Reason: core.ReasonExternalNoUpdate,
			},
		})
		return nil
	})
	assert.Contains(t, out, "1 locked mod(s) not applied: Locked Mod")
	assert.Contains(t, out, "1 Steam Workshop item(s) not applied: Sample Workshop Item")
	assert.Contains(t, out, "Steam applies these itself")
	assert.NotContains(t, out, "Sample Workshop Item — unlock to update",
		"unlocking is not the remedy for an item Steam owns")
}

func TestPrintBatchSkips_SilentWithNothingSkipped(t *testing.T) {
	out := captureStdout(t, func() error { printBatchSkips(nil); return nil })
	assert.Empty(t, strings.TrimSpace(out))
}

// doImportWorkshopGate exercises doImport's --workshop exclusivity gate
// without going near the archive or scan modes it guards.
func doImportWorkshopGate(svc *core.Service, game *domain.Game, args []string) error {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return doImport(context.Background(), cmd, svc, game, args)
}

// runImportWorkshopQuiet performs the adopt and discards its output, for a
// test whose subject is what happens AFTER the items are tracked.
func runImportWorkshopQuiet(t *testing.T, svc *core.Service, game *domain.Game) error {
	t.Helper()
	var err error
	captureStdout(t, func() error {
		err = runImportWorkshop(context.Background(), svc, game, "default")
		return nil
	})
	return err
}

// TestProfileSwitch_NamesTheWorkshopItemsItLeavesActive is design §2's
// `profile switch / apply / sync` row: "one advisory note: 'N Steam Workshop
// items stay active regardless of profile - manage subscriptions in the Steam
// client.'" Core did its half all along - both flows append
// core.NoteExternalProfileScope to result.Notes AND emit it as a
// DeployExternalSkipped step event - but neither CLI renderer had a case for
// that phase, and both deliberately skip batch-printing result.Notes, so
// `lmm profile switch` said nothing at all about what it left alone.
//
// The no-work path is covered too: a profile holding only Workshop items has
// nothing to disable, enable or install, so plan.NoChanges is true - which is
// exactly the run most likely to leave a user wondering.
func TestProfileSwitch_NamesTheWorkshopItemsItLeavesActive(t *testing.T) {
	const advisory = "1 Steam Workshop item(s) stay active regardless of profile"

	t.Run("with other work to do", func(t *testing.T) {
		svc, game, _, _ := setupWorkshopCLI(t)
		withWorkshopImportFlags(t, false, true)
		require.NoError(t, runImportWorkshopQuiet(t, svc, game))
		seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
		_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "other")
		require.NoError(t, err)
		withProfileSwitchYes(t)

		out := captureStdout(t, func() error {
			return doProfileSwitch(context.Background(), svc, game, "other")
		})
		assert.Contains(t, out, advisory)
		assert.Contains(t, out, "manage subscriptions in the Steam client")
	})

	t.Run("with nothing else to do", func(t *testing.T) {
		svc, game, _, _ := setupWorkshopCLI(t)
		withWorkshopImportFlags(t, false, true)
		require.NoError(t, runImportWorkshopQuiet(t, svc, game))
		_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "other")
		require.NoError(t, err)
		withProfileSwitchYes(t)

		out := captureStdout(t, func() error {
			return doProfileSwitch(context.Background(), svc, game, "other")
		})
		assert.Contains(t, out, advisory,
			"the plan.NoChanges path passed core a nil sink, so the one note it does emit was thrown away")
	})
}

// TestProfileApply_NamesTheWorkshopItemsItLeavesActive is the same §2 row for
// the sibling flow, and it has TWO paths, not one.
//
// With work to do, core's DeployExternalSkipped event carries the advisory and
// the renderer prints it. With nothing else to do there is no event at all:
// the CLI returns "System already matches profile %s." before calling Apply,
// and core's own `if plan.NoChanges { return }` sits ABOVE the emit, so
// neither half can produce it. That early return is a deliberate contract - a
// frontend calling Apply unconditionally must not get a sync the CLI never
// performed - so the honest fix is in the renderer, exactly where
// doProfileSwitch's sink hoist put its own.
//
// A profile holding only Workshop items IS plan.NoChanges, and it is the run
// most likely to leave a user wondering what happened. README.md:1596 asserts
// the delivered behaviour for `apply` as well as `switch`.
func TestProfileApply_NamesTheWorkshopItemsItLeavesActive(t *testing.T) {
	const advisory = "1 Steam Workshop item(s) stay active regardless of profile"

	setup := func(t *testing.T) (*core.Service, *domain.Game) {
		t.Helper()
		svc, game, _, _ := setupWorkshopCLI(t)
		withWorkshopImportFlags(t, false, true)
		require.NoError(t, runImportWorkshopQuiet(t, svc, game))
		origYes := profileApplyYes
		profileApplyYes = true
		t.Cleanup(func() { profileApplyYes = origYes })
		return svc, game
	}

	t.Run("with other work to do", func(t *testing.T) {
		svc, game := setup(t)
		// Installed + enabled but absent from profile.Mods -> the disable
		// bucket, so the apply has real work and core gets past its own
		// plan.NoChanges guard.
		seedApplyCandidateMod(t, svc, game, "src", "dis1", "Dis One", "1.0", true,
			map[string][]byte{"dis1.esp": []byte("dis")})

		out := captureStdout(t, func() error {
			return doProfileApply(context.Background(), svc, game, nil)
		})
		assert.Contains(t, out, advisory)
		assert.Contains(t, out, "manage subscriptions in the Steam client")
	})

	t.Run("with nothing else to do", func(t *testing.T) {
		svc, game := setup(t)

		out := captureStdout(t, func() error {
			return doProfileApply(context.Background(), svc, game, nil)
		})
		assert.Contains(t, out, "System already matches profile")
		assert.Contains(t, out, advisory,
			"core returns before emitting when plan.NoChanges, so the renderer owes the note itself")
		assert.Contains(t, out, "manage subscriptions in the Steam client")
	})

	// --json stays exactly one document (Ruling 15): the advisory is a human
	// line and must not leak onto stdout beside the JSON. What that document
	// does NOT carry on this path is the note itself - the CLI emits a
	// zero-valued ProfileApplyResult, where the with-work path gets
	// Result.Notes from core - which is a core-side gap, recorded for the
	// follow-up issue rather than papered over in the renderer.
	t.Run("json output stays one document", func(t *testing.T) {
		svc, game := setup(t)
		withJSONOutput(t)

		out := captureStdout(t, func() error {
			return doProfileApply(context.Background(), svc, game, nil)
		})
		assert.NotContains(t, out, "stay active regardless of profile")
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &doc))
	})
}

// TestModShow_HeaderShowsTheRevisionDateNotTheContentID: the approval note
// names this surface by hand. The catalog document's Version is Steam's
// 19-digit content id (steamworkshop/client.go#modFromDetails), so printing
// it in the header's "Version:" slot is the forbidden shape - and it appeared
// there alongside the correctly labelled "Steam content id" line below, the
// same twice-on-screen defect the mod panel and the mod page had.
func TestModShow_HeaderShowsTheRevisionDateNotTheContentID(t *testing.T) {
	showWorkshopItem := func(t *testing.T, svc *core.Service, game *domain.Game) string {
		t.Helper()
		old := modProfile
		modProfile = "default"
		t.Cleanup(func() { modProfile = old })
		return captureStdout(t, func() error {
			return doModShow(context.Background(), svc, game, "3617086610")
		})
	}

	t.Run("adopted", func(t *testing.T) {
		svc, game, _, _ := setupWorkshopCLI(t)
		withWorkshopImportFlags(t, false, true)
		require.NoError(t, runImportWorkshopQuiet(t, svc, game))

		out := showWorkshopItem(t, svc, game)
		assert.NotContains(t, out, "Version: 7987119735124793734",
			"the header must not print the content id in the slot a version goes")
		assert.Contains(t, out, "2025-12-03", "the header shows the revision date instead")
		assert.Contains(t, out, "Steam content id: 7987119735124793734",
			"the manifest still appears once, labelled, where the design put it")
	})

	// The item the user has NOT adopted has no installed row to carry
	// External and prints no "Installed:" line, so the header is the only
	// version text on screen - and the catalog document's Version is the
	// content id. Without the source-capability branch this case shows the
	// forbidden shape with nothing else to correct it.
	t.Run("not adopted", func(t *testing.T) {
		svc, game, _, _ := setupWorkshopCLI(t)

		out := showWorkshopItem(t, svc, game)
		assert.NotContains(t, out, "7987119735124793734",
			"an un-adopted Workshop item names the content id nowhere at all")
		assert.Contains(t, out, "revision of 2025-12-03")
		assert.NotContains(t, out, "Installed:")
	})
}

// TestModShowAndList_ALockedExternalModNeverNamesTheContentID is the CLI half
// of the SPA modrows.js#lockedNote rule. Nothing refuses `lmm mod lock` for a
// Workshop item, so its lock TARGET is the content id, and "locked at
// v7987119735124793734" is the forbidden shape with a "v" in front of it.
// `list -v` had already fixed its VERSION column and left LOCKED raw, so one
// row disagreed with itself.
func TestModShowAndList_ALockedExternalModNeverNamesTheContentID(t *testing.T) {
	svc, game, _, _ := setupWorkshopCLI(t)
	withWorkshopImportFlags(t, false, true)
	require.NoError(t, runImportWorkshopQuiet(t, svc, game))
	_, err := svc.SetModLock(context.Background(), "steamworkshop", "3617086610",
		game.ID, "default", "7987119735124793734")
	require.NoError(t, err)

	old := modProfile
	modProfile = "default"
	t.Cleanup(func() { modProfile = old })

	show := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "3617086610")
	})
	assert.NotContains(t, show, "locked at v7987119735124793734", "mod show's Lock: line")
	assert.Contains(t, show, "Lock: locked")
	assert.NotContains(t, show, "run 'lmm profile apply' to converge",
		"the converge hint compares two content ids, and `profile apply` deliberately skips this mod")

	list := listVerbose(t, svc, game, false)
	assert.NotContains(t, list, "7987119735124793734", "list -v's LOCKED column")
	assert.Contains(t, list, "EXTERNAL")
}

// TestModLockAndPin_NeverEchoTheContentIDBack is the other half of the same
// rule: the two commands that PUT a Workshop item into that state print their
// own confirmation lines, and both wrapped the target in "v%s".
func TestModLockAndPin_NeverEchoTheContentIDBack(t *testing.T) {
	const contentID = "7987119735124793734"

	setup := func(t *testing.T) (*core.Service, *domain.Game) {
		t.Helper()
		svc, game, _, _ := setupWorkshopCLI(t)
		withWorkshopImportFlags(t, false, true)
		require.NoError(t, runImportWorkshopQuiet(t, svc, game))
		old := modProfile
		modProfile = "default"
		t.Cleanup(func() { modProfile = old })
		return svc, game
	}

	// `lmm mod lock` never reaches its own wording for a Workshop item: the
	// source reports Versions:false, and doModLock's static capability gate
	// refuses on that BEFORE any lock is written. Pinned here so the gate
	// cannot be relaxed without someone re-reading doModLock's wording, which
	// goes through displayLockTarget for exactly that day (the reachable way
	// into a locked external row is core.SetModLock direct - what
	// TestModShowAndList_... above and `lmm serve`'s lock route both do).
	t.Run("mod lock is refused before it can word anything", func(t *testing.T) {
		svc, game := setup(t)
		out := captureStdout(t, func() error {
			err := doModLock(context.Background(), svc, game, "3617086610", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot resolve versions")
			assert.NotContains(t, err.Error(), contentID)
			return nil
		})
		assert.NotContains(t, out, contentID)
	})

	t.Run("mod set-update --pin", func(t *testing.T) {
		svc, game := setup(t)
		oldPin := modSetPin
		modSetPin = true
		t.Cleanup(func() { modSetPin = oldPin })

		out := captureStdout(t, func() error {
			return doModSetUpdate(context.Background(), svc, game, "3617086610")
		})
		assert.NotContains(t, out, contentID)
		assert.Contains(t, out, "pinned")
	})
}
