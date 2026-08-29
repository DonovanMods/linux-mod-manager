package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	deploySource  string
	deployProfile string
	deployMethod  string
	deployPurge   bool
	deployAll     bool
	deployForce   bool
	deployDryRun  bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy [mod-id]",
	Short: "Deploy mods to game directory",
	Long: `Deploy mod files from cache to game directory.

Use this when changing deployment methods (symlink, hardlink, copy)
or if mod files need to be refreshed.

Without a mod ID, deploys all enabled mods in the current profile.
With a mod ID, deploys only that specific mod; -s/--source then identifies
which source that mod ID belongs to - it resolves automatically when the
game has exactly one configured source, or prompts interactively when it
has several. -s is ignored when deploying the whole profile.

Use --purge to remove all deployed mods before deploying. This ensures
a clean slate, useful when mods have gotten out of sync.

Use --all to deploy all mods including disabled ones (e.g., after a purge).

Use --dry-run to print what the deploy would do - which mods, which files,
what a --purge pass would remove first - without changing anything.

Examples:
  lmm deploy --game skyrim-se
  lmm deploy --game skyrim-se --all
  lmm deploy --game skyrim-se --method hardlink
  lmm deploy --game skyrim-se --purge
  lmm deploy --game skyrim-se --dry-run
  lmm deploy 12345 --game skyrim-se
  lmm deploy 12345 --game skyrim-se --source curseforge
  lmm deploy 12345 --game skyrim-se --method copy`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDeploy,
}

func init() {
	deployCmd.Flags().StringVarP(&deploySource, "source", "s", "", "mod source for deploying a single mod ID (default: auto-detect when the game has one configured source, prompt when it has several)")
	deployCmd.Flags().StringVarP(&deployProfile, "profile", "p", "", "profile (default: active profile)")
	deployCmd.Flags().StringVarP(&deployMethod, "method", "m", "", "link method: symlink, hardlink, or copy (default: game's configured method)")
	deployCmd.Flags().BoolVar(&deployPurge, "purge", false, "purge all deployed mods before deploying")
	deployCmd.Flags().BoolVarP(&deployAll, "all", "a", false, "deploy all mods including disabled ones")
	deployCmd.Flags().BoolVarP(&deployForce, "force", "f", false, "continue even if hooks fail")
	deployCmd.Flags().BoolVar(&deployDryRun, "dry-run", false, "print what the deploy would do without changing anything")

	rootCmd.AddCommand(deployCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doDeploy(ctx, service, game, args)
	})
}

func doDeploy(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	profileName, err := resolveProfile(service, game.ID, deployProfile)
	if err != nil {
		return err
	}

	var linkMethodOverride *domain.LinkMethod
	if deployMethod != "" {
		var m domain.LinkMethod
		switch deployMethod {
		case "symlink":
			m = domain.LinkSymlink
		case "hardlink":
			m = domain.LinkHardlink
		case "copy":
			m = domain.LinkCopy
		default:
			return fmt.Errorf("invalid link method: %s (use: symlink, hardlink, or copy)", deployMethod)
		}
		linkMethodOverride = &m
	}
	var methodName string
	if linkMethodOverride != nil {
		methodName = linkMethodOverride.String()
	} else {
		method, err := service.GetEffectiveLinkMethod(ctx, game, profileName)
		if err != nil {
			return err
		}
		methodName = method.String()
	}

	opts := core.DeployOptions{
		Purge:      deployPurge,
		LinkMethod: linkMethodOverride,
		All:        deployAll,
		Force:      deployForce,
		SkipHooks:  noHooks,
	}
	if len(args) > 0 {
		resolvedSource, err := resolveSource(service, game, deploySource, false)
		if err != nil {
			return err
		}
		deploySource = resolvedSource
		opts.ModID = args[0]
		opts.SourceID = deploySource
	}

	// deployHeaderPrinted tracks whether we ever reached the deploy loop (as
	// opposed to only a --purge pass, or neither): DeployProfile fires no
	// per-mod progress events at all when there is nothing to deploy, which
	// is exactly the "No mods to deploy" case the pre-extraction CLI checked
	// via len(modsToDeploy) before it had been folded into the flow.
	//
	// On a DeployCompile game the header drops "using <method>" (#255): the
	// listed mods are mostly carried by one merged artifact deployed after
	// the loop, so claiming a per-mod link method up front asserted
	// something untrue about most of the lines below it.
	compileMode := game.DeployMode == domain.DeployCompile
	deployHeaderPrinted := false
	printDeployHeaderOnce := func(total int) {
		if deployHeaderPrinted {
			return
		}
		deployHeaderPrinted = true
		if compileMode {
			fmt.Printf("Deploying %d mod(s) — compile mode...\n\n", total)
		} else {
			fmt.Printf("Deploying %d mod(s) using %s...\n\n", total, methodName)
		}
	}

	// mergeFooterPrinted: a DeployMergeSynced footer was printed (#255), so
	// the summary below skips its own leading blank line - the footer
	// already separates the per-mod block and "Deployed: N" reads as part
	// of the same closing readout.
	mergeFooterPrinted := false

	// progress prints every diagnostic and per-mod status line at its exact
	// point of occurrence, driven entirely by core.DeployProfile's progress
	// events - including diagnostics that also land in result.Warnings/
	// .Notes (see core.DeployResult's doc comment). Those slices are never
	// separately batch-printed below: every entry has a corresponding event
	// here, so doing so would double-print. Phases that occur before the
	// deploy loop starts (a forced before_all warning, anything from the
	// --purge pass) return early, without calling printDeployHeaderOnce -
	// they must print before "Deploying N mod(s)..." even exists.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.DeployBeforeAllForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
			return
		case core.DeployPurging:
			fmt.Printf("Purging %d mod(s) before deploy...\n", p.Total)
			return
		case core.PurgeWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
			return
		case core.PurgeNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
			return
		case core.PurgeComplete:
			fmt.Println()
			return
		case core.PurgeModSkipped, core.PurgeModPurged:
			// PurgeProfile-only phases that never fire during a deploy
			// --purge pass; handled so a future change can't accidentally
			// route them into printDeployHeaderOnce below.
			return
		case core.DeployMergeSynced:
			// #255: the post-sync footer naming the merged artifact. Fires
			// only after the deploy loop (some per-mod event has already
			// printed the header), and its Total counts the mods the merged
			// artifact actually carries (raw fallbacks excluded - they ride
			// RawFallbacks), not the deploy total - so it must not fall
			// through to printDeployHeaderOnce below.
			fmt.Printf("\nMerged %d mod(s) → %s", p.Total, p.Detail)
			if p.RawFallbacks > 0 {
				fmt.Printf(" (%d deployed raw)", p.RawFallbacks)
			}
			fmt.Println()
			mergeFooterPrinted = true
			return
		}

		printDeployHeaderOnce(p.Total)
		switch p.Phase {
		case core.DeployBeforeEachSkipped:
			fmt.Printf("  Skipped: %s\n", p.Detail)
		case core.DeployRedownloading:
			fmt.Printf("  %s %s - cache missing, re-downloading...\n", colorYellow("⚠"), p.ModName)
		case core.DeployDownloading:
			fmt.Printf("\r  ⬇ %s: %.1f%%", p.ModName, p.Percent)
		case core.DeployDownloadDone:
			fmt.Println()
		case core.DeployDownloadFailed:
			fmt.Println()
			fmt.Printf("  %s %s - %s\n", colorRed("✗"), p.ModName, p.Detail)
			fmt.Println()
		case core.DeploySkipped:
			fmt.Printf("  %s %s - %s\n", colorRed("✗"), p.ModName, p.Detail)
		case core.DeployDeployed:
			// #255: on a compile game, label how the mod's content actually
			// reaches the game dir - "(merged)" rides the merged artifact
			// (optimistic for a pak whose conversion then fails; the
			// conversion warning + footer carry the correction), "(raw)" is
			// a ConvertPaks-opted-out pak deploying itself.
			switch p.ModClass {
			case core.DeployModMerged:
				fmt.Printf("  %s %s (merged)\n", colorGreen("✓"), p.ModName)
			case core.DeployModRaw:
				fmt.Printf("  %s %s (raw)\n", colorGreen("✓"), p.ModName)
			default:
				fmt.Printf("  %s %s\n", colorGreen("✓"), p.ModName)
			}
		case core.DeployNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.DeployWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	plan, err := service.PlanDeploy(ctx, game, profileName, opts)
	if err != nil {
		return err
	}

	if deployDryRun {
		renderDeployPlan(plan, progress, printDeployHeaderOnce, &mergeFooterPrinted)
		return nil
	}

	result, err := service.ApplyDeploy(ctx, game, plan, opts, progress)
	if err != nil {
		// Diagnostics accumulated before a fatal error (ApplyDeploy's
		// error-path convention returns them alongside it) were already
		// printed above, live, via progress - nothing left to print here.
		return err
	}

	if !deployHeaderPrinted {
		if deployAll {
			fmt.Println("No mods to deploy.")
		} else {
			fmt.Println("No enabled mods to deploy. Use --all to deploy disabled mods.")
		}
		return nil
	}

	if mergeFooterPrinted {
		fmt.Printf("Deployed: %d", result.Deployed)
	} else {
		fmt.Printf("\nDeployed: %d", result.Deployed)
	}
	if failed := len(result.Skipped); failed > 0 {
		fmt.Printf(", Failed: %d", failed)
	}
	fmt.Println()

	if deployMethod != "" {
		fmt.Printf("\nNote: Used %s method for this deployment.\n", methodName)
		fmt.Printf("To make this permanent, update your games.yaml config.\n")
	}

	return nil
}

// renderDeployPlan prints a core.DeployPlan under a "(dry run)" header, in
// the same vocabulary a real deploy would use: the per-mod and merge lines
// are synthesized as core flow events and pushed through doDeploy's OWN
// progress closure, so a dry run's "✓ Mod (merged)" / "✗ Mod - reason" /
// "⚠ Mod - cache missing, re-downloading..." / "Merged N mod(s) → artifact"
// lines cannot drift from the live ones. A DeployPlanMod.Redownload entry
// renders as the latter followed by its own "✓ Mod" line and counts as
// deployable, never as a red "✗" (Task 24 review, Important #2) - a
// cache-missing mod is exactly what the live deploy heals by re-downloading
// and then deploying it, not a mod that fails. The lines the plan alone owns
// - the header, the purge readout, the per-file detail under --verbose, and
// the "Would deploy:" summary - are printed here; all of them are new output
// behind the new --dry-run flag, so no existing invocation is affected.
//
// The summary deliberately says "Would deploy", not "Deployed", and the
// --method reminder the live path prints ("Note: Used X method for this
// deployment.") is omitted: nothing was deployed, and the header above
// already names the method that would be used.
//
// Exit code: like every other flag, --dry-run's own errors (a bad --method,
// a failing PlanDeploy read) fail normally. But a plan whose OWN data
// records a selection that is certain to fail once applied (an unknown mod
// ID, a disabled single-mod selection - DeployPlanMod.Skipped) still renders
// and returns nil: a plan is data, and doDeploy's real error path is not
// reached because ApplyDeploy is never called under --dry-run. Reported as
// Task 24 review Minor #4; scripted pre-flight checks that need a nonzero
// exit for a doomed deploy cannot rely on --dry-run's exit code alone and
// should inspect a JSON-rendered plan's Mods[].Skipped instead once such a
// frontend exists.
func renderDeployPlan(plan *core.DeployPlan, progress func(core.Event), printHeaderOnce func(int), mergeFooterPrinted *bool) {
	fmt.Printf("Deploy plan for profile %q (dry run)\n\n", plan.Profile)

	if deployPurge {
		fmt.Printf("Would purge %d file(s) before deploy...\n", len(plan.Purge))
		if verbose {
			for _, f := range plan.Purge {
				fmt.Printf("    - %s\n", f)
			}
		}
		fmt.Println()
	}

	if len(plan.Mods) == 0 {
		if deployAll {
			fmt.Println("No mods to deploy.")
		} else {
			fmt.Println("No enabled mods to deploy. Use --all to deploy disabled mods.")
		}
		return
	}

	printHeaderOnce(len(plan.Mods))

	deployable := 0
	for i, m := range plan.Mods {
		scope := core.Scope{
			Op: core.OpDeploy, Index: i + 1, Total: len(plan.Mods),
			ModName: m.Name, Mod: &domain.ModReference{SourceID: m.Ref.SourceID, ModID: m.Ref.ModID},
		}
		if m.Skipped != "" {
			progress(core.ModEvent{Scope: scope, Phase: core.DeploySkipped, Detail: m.Skipped})
			continue
		}
		if m.Redownload {
			// This mod's cache entry is missing, but the live deploy heals
			// that by re-downloading from source and then deploying it -
			// render both live-vocabulary lines (not a red ✗) and count it
			// as deployable (Task 24 review, Important #2).
			progress(core.StepEvent{Scope: scope, Phase: core.DeployRedownloading})
			deployable++
			progress(core.ModEvent{Scope: scope, Phase: core.DeployDeployed, Class: m.Class})
			continue
		}
		deployable++
		progress(core.ModEvent{Scope: scope, Phase: core.DeployDeployed, Class: m.Class})
		if verbose {
			for _, f := range m.Link {
				fmt.Printf("    + %s\n", f)
			}
			for _, f := range m.Remove {
				fmt.Printf("    - %s\n", f)
			}
		}
	}

	if plan.Merged != nil {
		progress(core.MergeEvent{
			Scope:        core.Scope{Op: core.OpDeploy},
			Phase:        core.DeployMergeSynced,
			MergedMods:   len(plan.Merged.Sources),
			Artifact:     plan.Merged.Artifact,
			RawFallbacks: len(plan.Merged.RawFallbacks),
		})
	}

	if *mergeFooterPrinted {
		fmt.Printf("Would deploy: %d", deployable)
	} else {
		fmt.Printf("\nWould deploy: %d", deployable)
	}
	if skipped := len(plan.Mods) - deployable; skipped > 0 {
		fmt.Printf(", Skipped: %d", skipped)
	}
	fmt.Println()

	if len(plan.Hooks) > 0 {
		fmt.Printf("\nHooks that would run: %s\n", strings.Join(plan.Hooks, ", "))
	}
}
