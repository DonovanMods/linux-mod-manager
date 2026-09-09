package main

import (
	"context"
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
