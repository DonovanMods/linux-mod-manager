package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	purgeProfile   string
	purgeUninstall bool
	purgeYes       bool
	purgeForce     bool
	purgeDryRun    bool
)

var purgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Remove all deployed mods from game directory",
	Long: `Remove all deployed mod files from the game directory.

This command undeploys all mods, essentially resetting the game directory
back to its pre-modded state. Use this when mods get out of sync or you
want to start fresh.

Mod records are preserved in the database, so you can deploy them later
with 'lmm deploy'. Use --uninstall to also remove the database records.

Use --dry-run to print what the purge would do - which mods would be
undeployed and what happens to their records - without changing anything
and without prompting.

Examples:
  lmm purge --game skyrim-se
  lmm purge --game skyrim-se --profile survival
  lmm purge --game skyrim-se --uninstall
  lmm purge --game skyrim-se --dry-run
  lmm purge --game skyrim-se --yes`,
	RunE: runPurge,
}

func init() {
	purgeCmd.Flags().StringVarP(&purgeProfile, "profile", "p", "", "profile to purge (default: active profile)")
	purgeCmd.Flags().BoolVar(&purgeUninstall, "uninstall", false, "also remove mod records from database (like uninstalling each mod)")
	purgeCmd.Flags().BoolVarP(&purgeYes, "yes", "y", false, "skip confirmation prompt")
	purgeCmd.Flags().BoolVarP(&purgeForce, "force", "f", false, "continue even if hooks fail")
	purgeCmd.Flags().BoolVar(&purgeDryRun, "dry-run", false, "print what the purge would do without changing anything")

	rootCmd.AddCommand(purgeCmd)
}

func runPurge(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doPurge(ctx, service, game)
	})
}

func doPurge(ctx context.Context, service *core.Service, game *domain.Game) error {
	profileName, err := resolveProfile(ctx, service, game.ID, purgeProfile)
	if err != nil {
		return err
	}

	opts := core.PurgeOptions{
		Uninstall: purgeUninstall,
		Force:     purgeForce,
		SkipHooks: noHooks,
	}

	// PlanPurge does the installed-mods read this command used to do
	// itself, before confirming: plan.Mods is both the count the prompt
	// below states and the set ApplyPurge goes on to purge - one object,
	// so the two can never disagree.
	plan, err := service.PlanPurge(ctx, game, profileName, opts)
	if err != nil {
		return err
	}
	mods := plan.Mods

	// Ruling 15: --dry-run --json is the Plan document, emitted before any
	// other early return - including the empty-profile one below, which an
	// empty PurgePlan already states on its own (zero-length Mods) -
	// matching import/profile switch/apply/sync's identical ordering
	// (Phase 3 close wave, Important 1).
	if purgeDryRun && jsonOutput {
		return emitJSON(plan)
	}

	if len(mods) == 0 {
		// Ruling 15: nothing to purge is not an error, and a --json caller
		// is still owed a document - the Result a purge of nothing
		// produces, rather than the console sentence.
		if jsonOutput {
			return emitJSON(&core.PurgeResult{})
		}
		fmt.Printf("No mods installed for %s (profile: %s)\n", game.Name, profileName)
		return nil
	}

	// progress prints every diagnostic and per-mod line at its exact point
	// of occurrence, driven entirely by core.ApplyPurge's events (the
	// same adapter pattern as doDeploy's). Entries that also land in
	// result.Warnings/.Notes are never separately batch-printed below -
	// every one has a corresponding event here.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.DeployBeforeAllForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.DeployPurging:
			fmt.Printf("\nPurging mods from %s...\n\n", game.Name)
		case core.PurgeModSkipped:
			fmt.Printf("  Skipped %s: %s\n", p.ModName, p.Detail)
		case core.PurgeNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.PurgeModPurged:
			fmt.Printf("  ✓ %s\n", p.ModName)
		case core.PurgeWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	if purgeDryRun {
		// Ruling 15: --dry-run --json already returned above, before the
		// empty-profile check; this is the plain-text rendering only.
		renderPurgePlan(plan, game, progress)
		return nil
	}

	// Confirmation prompt. Under --json the prompt is unanswerable (Ruling
	// 2: readPromptLine refuses to read stdin and returns
	// core.ErrConfirmationRequired), so the preamble is not printed either -
	// it would be console text beside an error envelope.
	if !purgeYes {
		if !jsonOutput {
			fmt.Printf("This will undeploy %d mod(s) from %s (profile: %s)\n", len(mods), game.Name, profileName)
			if purgeUninstall {
				fmt.Println("Mod records will also be removed from the database.")
			} else {
				fmt.Println("Mod records will be preserved. Use 'lmm deploy' to restore.")
			}
			fmt.Print("\nContinue? [y/N] ")
		}
		response, err := readPromptLine()
		if err != nil {
			return err
		}
		if response != "y" && response != "yes" {
			return ErrCancelled
		}
	}

	result, err := service.ApplyPurge(ctx, game, plan, opts, quietSink(progress))
	if err != nil {
		// Diagnostics accumulated before a fatal error were already
		// printed above, live, via progress - nothing left to print here.
		return err
	}

	// Ruling 15: the applying run's document is the Result.
	if jsonOutput {
		return emitJSON(result)
	}

	fmt.Printf("\nPurged: %d mod(s)", result.Purged)
	if failed := len(result.Skipped); failed > 0 {
		fmt.Printf(", Failed: %d", failed)
	}
	fmt.Println()

	if !purgeUninstall {
		fmt.Println("\nMod records preserved. Use 'lmm deploy' to restore mods.")
	}

	return nil
}

// renderPurgePlan prints a core.PurgePlan under a "(dry run)" header, in the
// same vocabulary a real purge would use: the "Purging mods from <Game>..."
// header and the per-mod "✓ <name>" lines are synthesized as core flow
// events and pushed through doPurge's OWN progress closure, so a dry run's
// lines cannot drift from the live ones. The lines the plan alone owns - the
// header, the records statement, the summary and the hook readout - are
// printed here. All of it is new output behind the new --dry-run flag, so no
// existing invocation is affected.
//
// A dry run does not prompt: there is nothing to confirm when nothing will
// change. It states the records consequence in the prompt's own words
// instead, which is the half of that block a dry run still needs to convey.
//
// The summary says "Would purge", not "Purged", and the live trailer
// ("Mod records preserved. Use 'lmm deploy' to restore mods.") is omitted -
// the records line above already covers it, unconditionally, for both
// --uninstall and not.
//
// PurgePlan lists mods, not files. On a DeployCompile game a purge also
// removes the profile's merged artifact (purgeMergedPak) - an effect Mods
// cannot express, so it gets a line of its own rather than letting the mod
// count read as the whole story. That line comes from plan.MergedArtifact
// and is printed only when there is a deployed artifact to remove (Ruling
// 8): a compile game with nothing merged yet gets no line at all.
//
// Exit code: like `deploy --dry-run` and `uninstall --dry-run`, a dry run
// that renders successfully returns nil. A game/profile that cannot be
// resolved at all is still a PlanPurge error and fails normally, exactly as
// the live path does.
func renderPurgePlan(plan *core.PurgePlan, game *domain.Game, progress func(core.Event)) {
	fmt.Printf("Purge plan for profile %q (dry run)\n\n", plan.Profile)

	fmt.Printf("Would undeploy %d mod(s) from %s (profile: %s)\n", len(plan.Mods), game.Name, plan.Profile)
	if plan.Uninstall {
		fmt.Println("Mod records will also be removed from the database.")
	} else {
		fmt.Println("Mod records will be preserved. Use 'lmm deploy' to restore.")
	}

	total := len(plan.Mods)
	progress(core.StepEvent{Scope: core.Scope{Op: core.OpPurge, Total: total}, Phase: core.DeployPurging})
	for i, m := range plan.Mods {
		progress(core.ModEvent{
			Scope: core.Scope{
				Op: core.OpPurge, Index: i + 1, Total: total,
				ModName: m.Name, Mod: &domain.ModReference{SourceID: m.SourceID, ModID: m.ID},
			},
			Phase: core.PurgeModPurged,
		})
	}

	fmt.Printf("\nWould purge: %d mod(s)\n", total)
	if plan.MergedArtifact != nil {
		fmt.Println("The profile's merged artifact would be removed too")
	}

	if len(plan.Hooks) > 0 {
		fmt.Printf("\nHooks that would run: %s\n", strings.Join(plan.Hooks, ", "))
	}
}
