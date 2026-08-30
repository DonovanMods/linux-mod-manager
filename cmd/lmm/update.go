package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	updateSource  string
	updateProfile string
	updateAll     bool
	updateDryRun  bool
	updateForce   bool
)

// bulkCheckReport assembles the one document a bulk `lmm update --json`
// emits, from the three things the check produced: the updates found
// (nil when there are none - emitJSON encodes that as []), the skip counts
// derived from what was installed, and the check's own failure, if any.
func bulkCheckReport(gameID, profileName string, updates []domain.Update, installed []domain.InstalledMod, checkErr error) *core.UpdateCheckReport {
	report := &core.UpdateCheckReport{
		GameID:  gameID,
		Profile: profileName,
		Updates: updates,
		Skipped: core.CountUpdateSkips(installed),
	}
	if checkErr != nil {
		report.ErrorMessage = checkErr.Error()
	}
	return report
}

// planUpdateResult builds the one-document result `lmm update <mod-id>`
// emits for an outcome core never applied - pinned, up to date, locked, or a
// --dry-run preview. Mod is the profile ref as it stands RIGHT NOW, since
// nothing was written: its Version is the installed version, or the lock's
// target when the ref is locked, and Locked says which. toVersion carries
// what the run would have moved to. The applied branches emit core's own
// UpdateApplyResult instead, whose Mod is the ref actually written.
func planUpdateResult(plan *core.UpdatePlan, toVersion string, status core.UpdateStatus, reason string) *core.UpdateApplyResult {
	ref := domain.ModReference{SourceID: plan.Mod.SourceID, ModID: plan.Mod.ID, Version: plan.Mod.Version, Locked: plan.Locked}
	if plan.Locked {
		ref.Version = plan.LockedVersion
	}
	return &core.UpdateApplyResult{
		Mod:         ref,
		Name:        plan.Mod.Name,
		FromVersion: plan.Mod.Version,
		ToVersion:   toVersion,
		Status:      status,
		Reason:      reason,
	}
}

var updateCmd = &cobra.Command{
	Use:   "update [mod-id]",
	Short: "Check for or apply mod updates",
	Long: `Check for available updates or update specific mods.

Without arguments, checks all installed mods for updates and prints a
table (auto-update-policy mods are then applied automatically; pass --all
to apply every available update). With a mod ID, checks and updates just
that mod. If the same mod ID is installed from more than one source in
the profile, use -s/--source to disambiguate (the error names the sources
in conflict). -s/--source is resolved once up front either way, so on a
game with more than one configured source it also avoids an interactive
source prompt for the bulk check.

If the update check itself fails partway through (e.g. a source outage),
whatever was learned before the failure is still printed and the command
exits non-zero rather than silently claiming success.

For a compile-deploy game (e.g. Icarus), a compiled mod whose game base pak
has changed since it was last compiled ("recompile needed") is checked and
applied through this same command: no new version, just a same-version
recompile against the current base pak.

--json prints exactly one JSON document to stdout, in one of two shapes:
  - Bulk check (no mod ID): {game_id, profile, updates: [...], skipped:
    {pinned, local}, error?}. error is present when the check itself
    failed partway through; updates/skipped still reflect whatever was
    learned first. Each updates[] entry carries the whole installed mod
    under "installed_mod" plus "new_version"; "locked": true means the
    update is reported but will not be applied until the lock moves or
    clears, and "recompile_needed": true with a "recompile_reason" marks
    a base-pak staleness row (new_version equals the installed version -
    the mod itself hasn't changed).
  - Single mod (a mod ID given): {mod, name, from_version, to_version,
    changelog, status, reason, warnings, notes}, where "mod" is the
    profile reference {source_id, mod_id, version, locked}. status is one
    of "updated", "up_to_date", "skipped", "available" (--dry-run),
    "recompiled", or "recompile_available" (--dry-run, same-version
    base-pak recompile); reason is set only when status is "skipped"
    ("pinned", "local", or "locked").
  - 'update rollback' emits the rollback document: {mod, mod_name,
    from_version, to_version, status, reason, warnings, notes}, with
    status "rolled_back" or "skipped".

Examples:
  lmm update --game skyrim-se                    # Check all mods for updates
  lmm update 12345 --game skyrim-se              # Update specific mod
  lmm update 12345 --game skyrim-se --source nexusmods  # Disambiguate by source
  lmm update --game skyrim-se --all              # Apply all available updates
  lmm update --game skyrim-se --dry-run          # Show what would update`,
	Args: cobra.MaximumNArgs(1),
	RunE: runUpdate,
}

var updateRollbackCmd = &cobra.Command{
	Use:   "rollback <mod-id>",
	Short: "Rollback a mod to its previous version",
	Long: `Rollback a mod to the version before the last update.

The previous version must still be available in the cache. If the same
mod ID is installed from more than one source in the profile, use
-s/--source to disambiguate.

--json prints the rollback document (see 'lmm update --help') with status
"rolled_back", or status "skipped" with reason "locked" when the mod is
locked (unlock or move the lock to roll back).

Examples:
  lmm update rollback 12345 --game skyrim-se
  lmm update rollback 12345 --game skyrim-se --source nexusmods`,
	Args: cobra.ExactArgs(1),
	RunE: runUpdateRollback,
}

func init() {
	updateCmd.Flags().StringVarP(&updateSource, "source", "s", "", "mod source (default: the sole configured source; prompts when several are configured)")
	updateCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile to check (default: active profile)")
	updateCmd.Flags().BoolVar(&updateAll, "all", false, "apply all available updates")
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "show what would update without applying")
	updateCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "continue even if hooks fail")

	updateRollbackCmd.Flags().StringVarP(&updateSource, "source", "s", "", "mod source (default: the sole configured source; prompts when several are configured)")
	updateRollbackCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile (default: active profile)")
	updateRollbackCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "continue even if hooks fail")

	updateCmd.AddCommand(updateRollbackCmd)
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doUpdate(ctx, service, game, args)
	})
}

func doUpdate(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	// Resolve source: use flag if set, otherwise first configured source
	var err error
	updateSource, err = resolveSource(service, game, updateSource, false)
	if err != nil {
		return err
	}

	// Determine profile
	profileName, err := resolveProfile(service, game.ID, updateProfile)
	if err != nil {
		return err
	}

	// Get installed mods
	installed, err := service.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("failed to get installed mods: %w", err)
	}

	if len(installed) == 0 {
		switch {
		case jsonOutput && len(args) > 0:
			// Fall through to the single-mod lookup below: with nothing
			// installed, candidates stays empty and it reports the same
			// "not found in profile" error as any other absent mod.
		case jsonOutput:
			// Nothing installed: no updates, nothing skipped. emitJSON
			// encodes the nil Updates slice as [].
			return emitJSON(&core.UpdateCheckReport{GameID: game.ID, Profile: profileName})
		default:
			fmt.Println("No mods installed.")
			return nil
		}
	}

	// If specific mod ID provided, update just that mod
	if len(args) > 0 {
		modID := args[0]
		var targetMod *domain.InstalledMod
		// Mod IDs are only unique within a source, so the same ID can appear
		// more than once in a profile. Collect every candidate rather than
		// keeping one: naming an arbitrary source would be wrong roughly half
		// the time. Matching on source as well as ID is also what used to make
		// a local mod look absent from its own profile.
		var candidates []*domain.InstalledMod
		for i := range installed {
			if installed[i].ID != modID {
				continue
			}
			if installed[i].SourceID == updateSource {
				targetMod = &installed[i]
				break
			}
			candidates = append(candidates, &installed[i])
		}
		if targetMod == nil {
			switch len(candidates) {
			case 0:
				return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
			case 1:
				if candidates[0].SourceID == domain.SourceLocal {
					// Present, just not checkable — informational, not an error.
					if jsonOutput {
						// The one branch with no plan behind it: a local mod
						// is never planned, so the ref is built from the
						// installed row directly.
						local := candidates[0]
						return emitJSON(&core.UpdateApplyResult{
							Mod:         domain.ModReference{SourceID: local.SourceID, ModID: local.ID, Version: local.Version},
							Name:        local.Name,
							FromVersion: local.Version,
							Status:      core.UpdateSkipped,
							Reason:      "local",
						})
					}
					fmt.Printf("%s is a local mod — no remote source to check for updates.\n", candidates[0].Name)
					return nil
				}
				return fmt.Errorf("mod %s in profile %s belongs to source %q, not %q; retry with --source %s",
					modID, profileName, candidates[0].SourceID, updateSource, candidates[0].SourceID)
			default:
				sources := make([]string, 0, len(candidates))
				hasLocal := false
				for _, c := range candidates {
					sources = append(sources, c.SourceID)
					if c.SourceID == domain.SourceLocal {
						hasLocal = true
					}
				}
				sort.Strings(sources) // deterministic regardless of install order
				// The local caveat only earns its place when one of the
				// candidates actually is local; on a purely remote ambiguity
				// (nexusmods vs curseforge) it is noise.
				caveat := ""
				if hasLocal {
					caveat = " (local mods cannot be update-checked)"
				}
				return fmt.Errorf("mod %s is in profile %s under multiple sources (%s); retry with --source to choose%s",
					modID, profileName, strings.Join(sources, ", "), caveat)
			}
		}

		return applySingleUpdate(ctx, service, game, targetMod, profileName)
	}

	var sink core.EventSink
	if verbose && !jsonOutput {
		fmt.Printf("Checking %d mod(s) for updates in %s (profile: %s)...\n", len(installed), game.Name, profileName)
		sink = func(e core.Event) {
			if uc, ok := e.(core.UpdateCheckEvent); ok {
				fmt.Fprintf(os.Stderr, "  %d/%d: %s\n", uc.Index, uc.Total, truncate(uc.ModName, 60))
			}
		}
	}

	// Check for updates (partial results returned even when some mods fail to
	// fetch) plus, for DeployCompile games, merged-pak staleness (#196/#197) -
	// CheckGameUpdates is the single seam the CLI checks through.
	updates, checkErr := service.CheckGameUpdates(ctx, game, profileName, installed, sink)
	if checkErr != nil {
		if errors.Is(checkErr, domain.ErrAuthRequired) {
			return authPromptError(updateSource)
		}
		// Surface warning but continue to show partial updates - under
		// --json the same message already reaches the document via
		// bulkCheckReport's ErrorMessage field, so printing it here too
		// would both leak onto stderr and duplicate it (Ruling 15).
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", checkErr)
		}
	}

	// finish is returned at every exit below rather than bailing out early: a
	// partial check still has results worth printing and auto-updates worth
	// applying, so the non-zero exit has to come after that work, not instead
	// of it. ErrReported because the failure is already on stderr (or in the
	// JSON document) and Execute must not print it twice.
	finish := func() error {
		if checkErr != nil {
			return fmt.Errorf("update check incomplete: %w", ErrReported)
		}
		return nil
	}

	if len(updates) == 0 {
		if jsonOutput {
			if err := emitJSON(bulkCheckReport(game.ID, profileName, nil, installed, checkErr)); err != nil {
				return err
			}
			return finish()
		}
		// "All mods are up to date" would be false if the only reason there is
		// nothing to report is that every mod was skipped. An empty profile
		// returned earlier, so len(installed) is non-zero here.
		skips := core.CountUpdateSkips(installed)
		if skips.Total() == len(installed) {
			printSkipped(skips)
			return finish()
		}
		// A failed check produces no updates too. Claiming currency here would
		// repeat the defect this whole command's reporting was fixed for: the
		// warning goes to stderr, so a caller reading stdout would see only a
		// false success.
		if checkErr != nil {
			fmt.Println("Update check did not complete — see the warning above. No results to report.")
		} else {
			fmt.Println("All mods are up to date.")
		}
		if skips.Total() > 0 {
			fmt.Println()
			printSkipped(skips)
		}
		return finish()
	}

	if jsonOutput {
		if err := emitJSON(bulkCheckReport(game.ID, profileName, updates, installed, checkErr)); err != nil {
			return err
		}
		return finish()
	}

	// Display available updates with policy
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(w, "MOD\tCURRENT\tAVAILABLE\tPOLICY\n"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintf(w, "---\t-------\t---------\t------\n"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	var autoUpdates []domain.Update
	// lockedAuto/lockedNames accumulate every locked mod skipped from
	// application below - auto-policy rows here, plus (further down) any
	// notify-policy rows --all would otherwise have applied. Reported once,
	// after both sections, via a single combined line.
	var lockedAuto int
	var lockedNames []string
	for _, update := range updates {
		policyStr := policyToString(update.InstalledMod.UpdatePolicy)
		isLocked := update.Locked
		if isLocked {
			policyStr += " [locked@" + update.LockedVersion + "]"
		}
		if update.RecompileNeeded {
			// #196: a base-pak staleness row - Available above equals
			// Current, so this marker is the only thing telling it apart
			// from a genuine no-op row in the table.
			policyStr += " [recompile]"
		}
		if update.InstalledMod.UpdatePolicy == domain.UpdateAuto {
			if isLocked {
				lockedAuto++
				lockedNames = append(lockedNames, update.InstalledMod.Name)
			} else {
				policyStr += " ✓"
				autoUpdates = append(autoUpdates, update)
			}
		}
		// Safe to color inline here specifically because POLICY is the
		// LAST column - text/tabwriter never pads after the final cell, so
		// this cell's inflated byte length can't misalign any column after
		// it (see printTable's doc comment; do not do this for an interior
		// column).
		switch {
		case isLocked:
			policyStr = colorYellow(policyStr)
		case strings.HasSuffix(policyStr, " ✓"):
			policyStr = colorGreen(policyStr)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			truncate(update.InstalledMod.Name, 40),
			update.InstalledMod.Version,
			update.NewVersion,
			policyStr,
		); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}
	if err := printTable(&buf, 2, nil); err != nil {
		return fmt.Errorf("writing table: %w", err)
	}

	fmt.Printf("\n%s\n", colorYellow(fmt.Sprintf("%d update(s) available.", len(updates))))
	if skips := core.CountUpdateSkips(installed); skips.Total() > 0 {
		fmt.Println()
		printSkipped(skips)
	}

	// Show changelogs where available
	var withChangelog []domain.Update
	for _, u := range updates {
		if u.Changelog != "" {
			withChangelog = append(withChangelog, u)
		}
	}
	if len(withChangelog) > 0 {
		fmt.Println("\nChangelogs:")
		for _, u := range withChangelog {
			cl := core.CleanChangelog(u.Changelog)
			const maxChangelog = 800
			if len(cl) > maxChangelog {
				cl = cl[:maxChangelog] + "\n..."
			}
			fmt.Printf("\n  %s (%s → %s):\n", u.InstalledMod.Name, u.InstalledMod.Version, u.NewVersion)
			for _, line := range strings.Split(strings.TrimSpace(cl), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}

	// Dry run mode - just show what would happen
	if updateDryRun {
		if len(autoUpdates) > 0 {
			fmt.Printf("\nWould auto-update %d mod(s):\n", len(autoUpdates))
			for _, u := range autoUpdates {
				fmt.Printf("  - %s %s → %s\n", u.InstalledMod.Name, u.InstalledMod.Version, u.NewVersion)
			}
		}
		fmt.Println("\nUse without --dry-run to apply updates.")
		return finish()
	}

	// Apply auto-updates
	if len(autoUpdates) > 0 {
		fmt.Printf("\nApplying %d auto-update(s)...\n", len(autoUpdates))
		for _, update := range autoUpdates {
			if err := applyBulkUpdate(ctx, service, game, update, profileName); err != nil {
				fmt.Printf("  %s %s: %v\n", colorRed("✗"), update.InstalledMod.Name, err)
			} else {
				fmt.Printf("  %s %s %s → %s\n", colorGreen("✓"), update.InstalledMod.Name, update.InstalledMod.Version, update.NewVersion)
			}
		}
	}

	// If --all flag, apply all remaining updates
	if updateAll {
		var notifyUpdates []domain.Update
		for _, update := range updates {
			if update.InstalledMod.UpdatePolicy == domain.UpdateAuto {
				continue // already handled above (applied, or reported as a locked skip)
			}
			if update.Locked {
				lockedAuto++
				lockedNames = append(lockedNames, update.InstalledMod.Name)
				continue
			}
			notifyUpdates = append(notifyUpdates, update)
		}

		if len(notifyUpdates) > 0 {
			fmt.Printf("\nApplying %d remaining update(s)...\n", len(notifyUpdates))
			for _, update := range notifyUpdates {
				if err := applyBulkUpdate(ctx, service, game, update, profileName); err != nil {
					fmt.Printf("  %s %s: %v\n", colorRed("✗"), update.InstalledMod.Name, err)
				} else {
					fmt.Printf("  %s %s %s → %s\n", colorGreen("✓"), update.InstalledMod.Name, update.InstalledMod.Version, update.NewVersion)
				}
			}
		}
	}

	// #97: one combined report for every locked mod that would otherwise
	// have been applied above - auto-policy rows always land here; --all's
	// notify-policy rows only do when --all was actually passed. Placed
	// after both application sections (so it "covers" whichever ran), but
	// fires on its own whenever lockedAuto > 0 even if neither section had
	// anything else to apply.
	if lockedAuto > 0 {
		fmt.Printf("\n%d locked mod(s) not applied: %s — move the lock or unlock to update.\n", lockedAuto, strings.Join(lockedNames, ", "))
	}

	return finish()
}

// applySingleUpdate renders `lmm update <mod-id>`'s outcome for mod: plans
// via Service.PlanUpdate, then switches on the plan exactly the way the
// pre-extraction CLI computed locked/pinned/recompile state inline (see the
// task report for the branch-by-branch mapping). All locked/pinned/
// recompile/changelog facts come from the plan; this function decides
// nothing about them itself - it only renders and, for the version-bump and
// recompile branches, applies.
func applySingleUpdate(ctx context.Context, service *core.Service, game *domain.Game, mod *domain.InstalledMod, profileName string) error {
	plan, err := service.PlanUpdate(ctx, game, profileName, mod.SourceID, mod.ID)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(updateSource)
		}
		return err
	}

	switch {
	case plan.Update == nil && plan.Pinned:
		// A pinned mod is filtered out before the source is queried, so no
		// version comparison ever happened — reporting it as up to date would
		// claim currency that was never checked.
		if jsonOutput {
			return emitJSON(planUpdateResult(plan, "", core.UpdateSkipped, "pinned"))
		}
		lockedSuffix := ""
		if plan.Locked {
			lockedSuffix = " (also locked)"
		}
		fmt.Printf("%s is pinned at v%s and was not checked%s.\n", plan.Mod.Name, plan.Mod.Version, lockedSuffix)
		// #142 round 5: -s/-p, same reasoning as the locked-refusal
		// remedies below - set-update is profile-scoped (SetModUpdatePolicy
		// takes profileName) and the mod ID may exist under more than one
		// configured source.
		fmt.Printf("Unpin with: lmm mod set-update -s %s -p %s %s --notify\n", plan.Mod.SourceID, profileName, plan.Mod.ID)
		return nil

	case plan.Update == nil:
		if jsonOutput {
			return emitJSON(planUpdateResult(plan, "", core.UpdateUpToDate, ""))
		}
		fmt.Printf("%s is already up to date (v%s).\n", plan.Mod.Name, plan.Mod.Version)
		return nil

	case plan.RecompileNeeded:
		// #196: a base-pak staleness row carries no real version change
		// (NewVersion == mod.Version) - branched off before any of the
		// version-bump wording/JSON below, which would otherwise print a
		// misleading "Updating vX → vX...".
		if plan.Locked {
			if jsonOutput {
				return emitJSON(planUpdateResult(plan, plan.Mod.Version, core.UpdateSkipped, "locked"))
			}
			// #294 (Ruling 5): the context line says what is available,
			// then UpdatePlan.Refusal - core.LockedRefRefusalError's
			// canonical text, which already names both -s/-p remedies
			// inline - says why nothing happened. Replaces the hand-worded
			// refusal and its own "Move the lock:" duplicate.
			fmt.Printf("Recompile needed for %s (base pak updated).\n", plan.Mod.Name)
			fmt.Println(plan.Refusal)
			return nil
		}

		if !jsonOutput {
			fmt.Printf("Recompiling %s (base pak updated)...\n", plan.Mod.Name)
		}

		if updateDryRun {
			if jsonOutput {
				return emitJSON(planUpdateResult(plan, plan.Mod.Version, core.UpdateRecompileAvailable, ""))
			}
			fmt.Println("(dry-run: no changes applied)")
			return nil
		}

		regen, err := applyRecompile(ctx, service, game, profileName)
		if err != nil {
			return err
		}

		if jsonOutput {
			// ApplyMergedPakRegen has no mod of its own to report (it
			// regenerates the profile's merged artifact), so its result
			// carries the run's diagnostics and this stamps the identity of
			// the mod the user named onto it.
			result := planUpdateResult(plan, plan.Mod.Version, core.UpdateRecompiled, "")
			result.Warnings, result.Notes = regen.Warnings, regen.Notes
			return emitJSON(result)
		}
		fmt.Printf("\n%s Recompiled: %s (base pak updated)\n", colorGreen("✓"), plan.Mod.Name)
		return nil

	default:
		oldVersion := plan.Mod.Version
		newVersion := plan.Update.NewVersion

		// #97: refuse up front - the core gate (Service.ApplyUpdate)
		// backstops this regardless, but checking here avoids ever printing
		// the "Updating..." header/changelog for a call that will never
		// actually apply, and gives an actionable message naming both
		// remedy commands instead of surfacing the core gate's raw error.
		// #294 (Ruling 5): that message is now UpdatePlan.Refusal -
		// core.LockedRefRefusalError's canonical text, one wording for
		// every lock refusal - printed after a context line stating what
		// is available. The refusal already names both remedies with -s/-p
		// (#142 round 5: update honors -p, and a mod ID may exist under
		// more than one configured source, so a bare copy-paste could
		// otherwise resolve against the wrong profile/an ambiguous source),
		// which is why the hand-worded "Move the lock:" line is gone.
		if plan.Locked {
			if jsonOutput {
				return emitJSON(planUpdateResult(plan, newVersion, core.UpdateSkipped, "locked"))
			}
			fmt.Printf("Update available: %s → %s\n", oldVersion, newVersion)
			fmt.Println(plan.Refusal)
			return nil
		}

		if !jsonOutput {
			fmt.Printf("Updating %s %s → %s...\n", plan.Mod.Name, oldVersion, newVersion)
			if plan.Update.Changelog != "" {
				cl := plan.Changelog
				const maxChangelog = 500
				if len(cl) > maxChangelog {
					cl = cl[:maxChangelog] + "..."
				}
				fmt.Println("Changelog:")
				for _, line := range strings.Split(strings.TrimSpace(cl), "\n") {
					fmt.Printf("  %s\n", line)
				}
				fmt.Println()
			}
		}

		if updateDryRun {
			if jsonOutput {
				result := planUpdateResult(plan, newVersion, core.UpdateAvailable, "")
				// plan.Changelog is core's cleaned changelog (upstream ones
				// are HTML), untruncated - the 500-char cap above is a
				// display concern.
				result.Changelog = plan.Changelog
				return emitJSON(result)
			}
			fmt.Println("(dry-run: no changes applied)")
			return nil
		}

		result, err := applyUpdate(ctx, service, game, plan)
		if err != nil {
			return err
		}

		if jsonOutput {
			// core's own result document, with one substitution: it records
			// the RAW upstream changelog, while --json has always carried
			// core's cleaned one (plan.Changelog), untruncated.
			result.Changelog = plan.Changelog
			return emitJSON(result)
		}

		fmt.Printf("\n%s Updated: %s %s → %s\n", colorGreen("✓"), plan.Mod.Name, oldVersion, newVersion)
		fmt.Println("  Previous version preserved for rollback")
		return nil
	}
}

// applyBulkUpdate re-plans and applies a single row from doUpdate's bulk
// check (its auto-policy and --all loops both call this): ApplyUpdate now
// takes a *core.UpdatePlan, and a plan computed once at the top of doUpdate
// would go stale after the FIRST mod in the loop applies (Ruling 5 - see
// PlanUpdate's doc comment), so each mod is re-planned immediately before
// its own apply. It builds that plan via Service.PlanUpdateFrom, reusing
// update - the exact domain.Update the bulk check already found and the
// loop's own line already printed - rather than PlanUpdate, which would
// re-run the whole CheckGameUpdates (including a live source query) a
// second time per applied mod (#289 review, Important 1); PlanUpdateFrom
// still re-reads the installed-mod row and the profile's lock state locally,
// so a lock taken or a version changed between the listing and this apply is
// still caught. The bulk loop's own printed line still uses update's fields
// (the bulk check's own values), not the freshly re-planned ones - the two
// are the same mod moments apart, but printing from the original avoids
// depending on a race-free re-check.
func applyBulkUpdate(ctx context.Context, service *core.Service, game *domain.Game, update domain.Update, profileName string) error {
	plan, err := service.PlanUpdateFrom(ctx, game, profileName, update)
	if err != nil {
		return err
	}
	// The bulk loop renders its own per-mod line from the check's data and
	// emits no document, so the apply result is not needed here.
	_, err = applyUpdate(ctx, service, game, plan)
	return err
}

// applyUpdate applies plan: resolve -> call Service.ApplyUpdate -> print
// from its progress events. progress prints every diagnostic at its exact
// point of occurrence, driven entirely by core.ApplyUpdate's progress
// events - reproducing the pre-extraction CLI's exact console positioning
// (download progress, forced-hook warnings, after_each hook warnings, and
// the --verbose-gated link-method note).
//
// #196/#197: a RecompileNeeded plan carries no real version change
// (NewVersion == InstalledMod.Version) - it is routed to
// Service.ApplyMergedPakRegen instead, which has no hooks to run and no
// version/FileIDs to record, only the merge-and-redeploy step itself.
func applyUpdate(ctx context.Context, service *core.Service, game *domain.Game, plan *core.UpdatePlan) (*core.UpdateApplyResult, error) {
	if plan.RecompileNeeded {
		return applyRecompile(ctx, service, game, plan.Mod.ProfileName)
	}

	opts := core.UpdateOptions{
		Force:     updateForce,
		SkipHooks: noHooks,
	}

	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.UpdateDownloading:
			if verbose && !jsonOutput {
				fmt.Printf("\r  Downloading: %.1f%%", p.Percent)
			}
		case core.UpdateDownloadDone:
			if verbose && !jsonOutput {
				fmt.Println()
			}
		case core.UpdateBeforeEachForced, core.UpdateWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.UpdateNote:
			if verbose && !jsonOutput {
				fmt.Printf("  %s\n", p.Detail)
			}
		}
	}

	return service.ApplyUpdate(ctx, game, plan, opts, quietSink(progress))
}

// applyRecompile applies a #197 merged-pak staleness row via
// Service.ApplyMergedPakRegen, printing from its progress events the same
// way applyUpdate does for its own (UpdateWarning/UpdateNote are the only
// phases ApplyMergedPakRegen emits - it runs no hooks and downloads
// nothing worth a progress bar).
func applyRecompile(ctx context.Context, service *core.Service, game *domain.Game, profileName string) (*core.UpdateApplyResult, error) {
	result, err := service.ApplyMergedPakRegen(ctx, game, profileName, nil)
	// #197 M4 fix: ApplyMergedPakRegen never emits UpdateWarning/UpdateNote
	// progress events (only UpdateDownloadDone) - its merge warnings (e.g.
	// an asset-path collision, "a loud warning" per the CHANGELOG) travel
	// through result.Warnings instead. A progress callback watching for
	// those phases would silently never fire; print result.Warnings
	// directly so `lmm update`'s apply path surfaces them the same way
	// DeployProfile already does.
	if result == nil {
		// ApplyMergedPakRegen always returns one, but a caller stamping
		// identity onto it must never have to nil-check.
		result = &core.UpdateApplyResult{}
	}
	// Under --json, printing here would both leak onto stderr and duplicate
	// the warnings the caller re-attaches to the document below (Ruling 15).
	if !jsonOutput {
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}
	return result, err
}

func runUpdateRollback(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doUpdateRollback(ctx, service, game, args[0])
	})
}

// doUpdateRollback resolves the target mod via Service.PlanRollback, which
// computes the pre-extraction CLI's four pre-checks (installed, previous
// version, lock state, cache existence) in one call instead of doUpdateRollback
// hand-rolling them (#289) -> prints its own "Rolling back %s %s → %s..."
// header from the plan's own fields (the guard errors "mod not found: %s"
// and "no previous version available for rollback" are PlanRollback errors,
// so the header never prints when either fails, matching the pre-extraction
// ordering exactly; a cache-missing or locked mod is likewise refused/
// skipped from plan data before the header, #143) -> calls
// Service.ApplyRollback, printing from its progress events exactly like
// applyUpdate does for ApplyUpdate (forced-hook warnings, after_each hook
// warnings, and the --verbose-gated link-method note all reuse the SAME
// UpdateBeforeEachForced/UpdateWarning/UpdateNote phases) -> prints the
// final "✓ Rolled back: ..." footer from the result's own fields.
func doUpdateRollback(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	// Resolve source: use flag if set, otherwise first configured source
	var err error
	updateSource, err = resolveSource(service, game, updateSource, false)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, updateProfile)
	if err != nil {
		return err
	}

	plan, err := service.PlanRollback(ctx, game, profileName, updateSource, modID)
	if err != nil {
		if errors.Is(err, domain.ErrModNotFound) {
			return fmt.Errorf("mod not found: %s", modID)
		}
		return err
	}

	if plan.CacheMissing {
		return fmt.Errorf("previous version %s not found in cache", plan.ToVersion)
	}

	// #143: refuse a locked mod up front, mirroring applySingleUpdate's
	// locked pre-check - the core gate (Service.ApplyRollback) backstops
	// this regardless, but checking here avoids ever printing the "Rolling
	// back..." header for a call that will never apply, treats the refusal
	// as a skip (nil error / "skipped"+"locked" document) like the update
	// path does, and names both remedy commands instead of surfacing the
	// core gate's raw error.
	if plan.Locked {
		if jsonOutput {
			// Nothing was written, so nothing changed - but Mod.Version is
			// still the version rolled back TO, per RollbackResult.Mod's own
			// doc comment, on every branch including this refusal one (final
			// review, Important #4 / #302): here that's the same value as
			// ToVersion below, since core never applies past this refusal.
			return emitJSON(&core.RollbackResult{
				Mod:         domain.ModReference{SourceID: plan.Mod.SourceID, ModID: plan.Mod.ID, Version: plan.ToVersion, Locked: true},
				ModName:     plan.Mod.Name,
				FromVersion: plan.FromVersion,
				ToVersion:   plan.ToVersion,
				Status:      core.UpdateSkipped,
				Reason:      "locked",
			})
		}
		// #294 (Ruling 5): RollbackPlan.Refusal, the same canonical text
		// applySingleUpdate's locked branch prints - it carries -s/-p on
		// both remedies for the same reason (#142 round 5: a bare
		// copy-paste could resolve against the wrong profile or an
		// ambiguous source).
		fmt.Printf("Rollback available: %s → %s\n", plan.FromVersion, plan.ToVersion)
		fmt.Println(plan.Refusal)
		return nil
	}

	if !jsonOutput {
		fmt.Printf("Rolling back %s %s → %s...\n", plan.Mod.Name, plan.FromVersion, plan.ToVersion)
	}

	opts := core.RollbackOptions{
		Force:     updateForce,
		SkipHooks: noHooks,
	}

	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.UpdateBeforeEachForced, core.UpdateWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.UpdateNote:
			if verbose && !jsonOutput {
				fmt.Printf("  %s\n", p.Detail)
			}
		}
	}

	result, err := service.ApplyRollback(ctx, game, plan, opts, quietSink(progress))
	if err != nil {
		return err
	}

	if jsonOutput {
		return emitJSON(result)
	}

	fmt.Printf("\n%s Rolled back: %s %s → %s\n", colorGreen("✓"), result.ModName, result.FromVersion, result.ToVersion)
	return nil
}

// printSkipped notes the mods CheckUpdates filtered out, if any. No-op at zero
// so the common case stays quiet.
//
// Pinned and local get separate lines because the remedies differ: a pin is a
// reversible choice, a local mod has no remote and never will.
//
// Emits no leading blank line: when every mod is skipped this is the whole
// output, and a leading newline would render as a stray blank first line.
// Callers with preceding output add their own separator.
func printSkipped(skips core.UpdateSkips) {
	if skips.Pinned > 0 {
		fmt.Printf("%d pinned mod%s skipped — see `lmm list -v`.\n", skips.Pinned, plural(skips.Pinned))
	}
	if skips.Local > 0 {
		fmt.Printf("%d local mod%s skipped (no remote source to check).\n", skips.Local, plural(skips.Local))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func policyToString(policy domain.UpdatePolicy) string {
	return policy.String()
}
