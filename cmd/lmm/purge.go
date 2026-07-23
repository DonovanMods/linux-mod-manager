package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	purgeProfile   string
	purgeUninstall bool
	purgeYes       bool
	purgeForce     bool
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

Examples:
  lmm purge --game skyrim-se
  lmm purge --game skyrim-se --profile survival
  lmm purge --game skyrim-se --uninstall
  lmm purge --game skyrim-se --yes`,
	RunE: runPurge,
}

func init() {
	purgeCmd.Flags().StringVarP(&purgeProfile, "profile", "p", "", "profile to purge (default: active profile)")
	purgeCmd.Flags().BoolVar(&purgeUninstall, "uninstall", false, "also remove mod records from database (like uninstalling each mod)")
	purgeCmd.Flags().BoolVarP(&purgeYes, "yes", "y", false, "skip confirmation prompt")
	purgeCmd.Flags().BoolVarP(&purgeForce, "force", "f", false, "continue even if hooks fail")

	rootCmd.AddCommand(purgeCmd)
}

func runPurge(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doPurge(ctx, service, game)
	})
}

func doPurge(ctx context.Context, service *core.Service, game *domain.Game) error {
	profileName, err := resolveProfile(service, game.ID, purgeProfile)
	if err != nil {
		return err
	}

	// The mod list is fetched before confirming so the prompt's count is
	// exactly the set core.PurgeProfile will purge.
	mods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	if len(mods) == 0 {
		fmt.Printf("No mods installed for %s (profile: %s)\n", game.Name, profileName)
		return nil
	}

	// Confirmation prompt
	if !purgeYes {
		fmt.Printf("This will undeploy %d mod(s) from %s (profile: %s)\n", len(mods), game.Name, profileName)
		if purgeUninstall {
			fmt.Println("Mod records will also be removed from the database.")
		} else {
			fmt.Println("Mod records will be preserved. Use 'lmm deploy' to restore.")
		}
		fmt.Print("\nContinue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading input: %w", err)
		}
		response := strings.TrimSpace(strings.ToLower(line))
		if response != "y" && response != "yes" {
			return ErrCancelled
		}
	}

	opts := core.PurgeOptions{
		Uninstall:   purgeUninstall,
		Hooks:       getResolvedHooks(service, game, profileName),
		HookRunner:  getHookRunner(service),
		HookContext: makeHookContext(game),
		Force:       purgeForce,
	}

	// progress prints every diagnostic and per-mod line at its exact point
	// of occurrence, driven entirely by core.PurgeProfile's events (the
	// same adapter pattern as doDeploy's). Entries that also land in
	// result.Warnings/.Notes are never separately batch-printed below -
	// every one has a corresponding event here.
	progress := func(p core.DeployProgress) {
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

	result, err := service.PurgeProfile(ctx, game, profileName, mods, opts, progress)
	if err != nil {
		// Diagnostics accumulated before a fatal error were already
		// printed above, live, via progress - nothing left to print here.
		return err
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
