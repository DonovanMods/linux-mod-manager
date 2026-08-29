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
	uninstallSource  string
	uninstallProfile string
	uninstallKeep    bool
	uninstallForce   bool
	uninstallDryRun  bool
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall <mod-id>",
	Short: "Uninstall a mod",
	Long: `Uninstall a mod from the specified profile.

By default, the mod files are removed from the game directory and the cache.
Use --keep-cache to preserve the cached files for potential reinstallation.

Without -s/--source, every installed mod in the profile is searched for a
matching ID and the first hit is used; if the same ID is installed from
more than one source, pass -s/--source to name the one you mean. Use
--force to continue uninstalling even if an uninstall hook fails.

Use --dry-run to print what the uninstall would do - which mod it resolves
to, which files would leave the game directory, what happens to the cache -
without changing anything.

Examples:
  lmm uninstall 12345 --game skyrim-se
  lmm uninstall 12345 --game skyrim-se --profile survival
  lmm uninstall 12345 --game skyrim-se --keep-cache
  lmm uninstall 12345 --game skyrim-se --dry-run
  lmm uninstall 12345 --game skyrim-se --source curseforge`,
	Args: cobra.ExactArgs(1),
	RunE: runUninstall,
}

func init() {
	uninstallCmd.Flags().StringVarP(&uninstallSource, "source", "s", "", "mod source (if omitted, searches all sources for mod ID)")
	uninstallCmd.Flags().StringVarP(&uninstallProfile, "profile", "p", "", "profile to uninstall from (default: active profile)")
	uninstallCmd.Flags().BoolVar(&uninstallKeep, "keep-cache", false, "keep cached mod files")
	uninstallCmd.Flags().BoolVarP(&uninstallForce, "force", "f", false, "continue even if hooks fail")
	uninstallCmd.Flags().BoolVar(&uninstallDryRun, "dry-run", false, "print what the uninstall would do without changing anything")

	rootCmd.AddCommand(uninstallCmd)
}

func runUninstall(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doUninstall(ctx, service, game, args[0])
	})
}

func doUninstall(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	// Determine profile
	profileName, err := resolveProfile(service, game.ID, uninstallProfile)
	if err != nil {
		return err
	}

	if verbose {
		fmt.Printf("Uninstalling mod %s from %s (profile: %s)...\n", modID, game.Name, profileName)
	}

	// -s names a source the game must actually have configured. This check
	// stays here, ahead of the plan: it validates a CLI flag against the
	// game's config, not the installed set, and its refusal must still be
	// the first thing a bad -s produces.
	if uninstallSource != "" && uninstallSource != domain.SourceLocal {
		if _, ok := game.SourceIDs[uninstallSource]; !ok {
			return fmt.Errorf("source %q is not configured for %s", uninstallSource, game.Name)
		}
	}

	opts := core.UninstallOptions{
		KeepCache: uninstallKeep,
		Force:     uninstallForce,
		SkipHooks: noHooks,
	}

	// PlanUninstall resolves the mod - the named source's copy, or (bare
	// ID) the first installed mod carrying that ID - and carries both
	// not-found wordings this command has always printed.
	plan, err := service.PlanUninstall(ctx, game, profileName, uninstallSource, modID, opts)
	if err != nil {
		return err
	}

	if uninstallDryRun {
		renderUninstallPlan(plan, game, profileName)
		return nil
	}

	result, err := service.ApplyUninstall(ctx, game, plan, opts)
	if err != nil {
		// ApplyUninstall's error-path convention returns any diagnostics
		// accumulated before the fatal error alongside it (see
		// UninstallResult's doc comment); print them now, or they'd
		// otherwise be lost even though they already happened.
		printUninstallDiagnostics(result)
		return err
	}

	printUninstallDiagnostics(result)

	fmt.Printf("✓ Uninstalled: %s\n", plan.Mod.Name)
	fmt.Printf("  Removed from profile: %s\n", profileName)

	if uninstallKeep {
		fmt.Println("  Cache files preserved")
	}

	return nil
}

// renderUninstallPlan prints a core.UninstallPlan under a "(dry run)"
// header. Every line here is new output behind the new --dry-run flag, so no
// existing invocation is affected; the wording deliberately mirrors the live
// path's own ("Cache files preserved" verbatim, "Removed from profile" in
// its would-be form) so a user can map a dry run onto the real thing.
//
// The mod is named with its source (`Mod A (src:a)`) because that is the
// answer a bare-ID uninstall's dry run exists to give: which of several
// same-ID mods the first-match rule picked. The file list is a count always,
// and the paths themselves under --verbose, matching `deploy --dry-run`'s
// treatment of its own per-mod paths.
//
// UninstallPlan.Files is the undeploy step's own removal set. On a
// DeployCompile game the uninstall ALSO resyncs the profile's merged
// artifact afterwards (rebuilding it, or removing it once the last merge
// source is gone) - an effect the plan type does not model, so the render
// says so in a line of its own rather than letting the file count read as
// the whole story.
//
// Exit code: like `deploy --dry-run`, a dry run that renders successfully
// returns nil. A mod that cannot be resolved at all is still a PlanUninstall
// error and fails normally, exactly as the live path does.
func renderUninstallPlan(plan *core.UninstallPlan, game *domain.Game, profileName string) {
	fmt.Printf("Uninstall plan for profile %q (dry run)\n\n", profileName)

	fmt.Printf("Would uninstall: %s (%s)\n", plan.Mod.Name, domain.ModKey(plan.Mod.SourceID, plan.Mod.ID))
	fmt.Printf("  Would remove from profile: %s\n", profileName)
	fmt.Printf("  Would remove %d file(s) from the game directory\n", len(plan.Files))
	if verbose {
		for _, f := range plan.Files {
			fmt.Printf("    - %s\n", f)
		}
	}
	if plan.KeepCache {
		fmt.Println("  Cache files preserved")
	} else {
		fmt.Println("  Cache entry would be deleted")
	}
	if game.DeployMode == domain.DeployCompile {
		fmt.Println("  The profile's merged artifact would be resynced afterwards")
	}

	if len(plan.Hooks) > 0 {
		fmt.Printf("\nHooks that would run: %s\n", strings.Join(plan.Hooks, ", "))
	}
}

// printUninstallDiagnostics prints result's accumulated diagnostics using
// the display contract documented on core.UninstallResult: Notes go to
// stdout, only under --verbose (each entry already carries its historical
// prefix word); Warnings go to stderr, unconditionally. Safe to call with a
// nil result (nothing to print) - result is nil only when ApplyUninstall
// failed before it could allocate the result struct.
func printUninstallDiagnostics(result *core.UninstallResult) {
	if result == nil {
		return
	}

	if verbose {
		for _, n := range result.Notes {
			fmt.Printf("  %s\n", n)
		}
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", w)
	}
}
