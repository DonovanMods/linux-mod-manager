package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"

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
	purgeCmd.Flags().StringVarP(&purgeProfile, "profile", "p", "", "profile to purge (default: default profile)")
	purgeCmd.Flags().BoolVar(&purgeUninstall, "uninstall", false, "also remove mod records from database (like uninstalling each mod)")
	purgeCmd.Flags().BoolVarP(&purgeYes, "yes", "y", false, "skip confirmation prompt")
	purgeCmd.Flags().BoolVarP(&purgeForce, "force", "f", false, "continue even if hooks fail")

	rootCmd.AddCommand(purgeCmd)
}

func runPurge(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	profileName := profileOrDefault(purgeProfile)

	// Get all installed mods for this profile
	mods, err := service.GetInstalledMods(gameID, profileName)
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

	ctx := context.Background()
	installer := service.GetInstaller(game)

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

	// Run uninstall.before_all hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeAll != "" {
		hookCtx.HookName = "uninstall.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeAll, hookCtx); err != nil {
			if !purgeForce {
				return fmt.Errorf("uninstall.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: uninstall.before_all hook failed (forced): %v\n", err)
		}
	}

	var succeeded, failed int

	fmt.Printf("\nPurging mods from %s...\n\n", game.Name)

	for _, mod := range mods {
		// Run uninstall.before_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeEach != "" {
			hookCtx.HookName = "uninstall.before_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeEach, hookCtx); err != nil {
				fmt.Printf("  Skipped %s: uninstall.before_each hook failed: %v\n", mod.Name, err)
				failed++
				continue // Skip this mod, continue with others
			}
		}

		// Undeploy files from game directory
		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			if verbose {
				fmt.Printf("  ⚠ %s - %v\n", mod.Name, err)
			}
			// Continue anyway - files may have been manually removed
		}

		// Remove from database only if --uninstall is set
		if purgeUninstall {
			if err := service.DB().DeleteInstalledMod(mod.SourceID, mod.ID, gameID, profileName); err != nil {
				if verbose {
					fmt.Printf("  ⚠ %s - failed to remove record: %v\n", mod.Name, err)
				}
				failed++
				continue
			}

			// Also remove from profile YAML
			pm := getProfileManager(service)
			if err := pm.RemoveMod(gameID, profileName, mod.SourceID, mod.ID); err != nil {
				if verbose {
					fmt.Printf("  Note: %s - %v\n", mod.Name, err)
				}
			}
		} else {
			// Mark mod as not deployed (files removed from game directory)
			if err := service.DB().SetModDeployed(mod.SourceID, mod.ID, gameID, profileName, false); err != nil {
				if verbose {
					fmt.Printf("  ⚠ %s - failed to mark as not deployed: %v\n", mod.Name, err)
				}
			}
		}

		// Run uninstall.after_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.AfterEach != "" {
			hookCtx.HookName = "uninstall.after_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.AfterEach, hookCtx); err != nil {
				hookErrors = append(hookErrors, fmt.Errorf("uninstall.after_each hook failed for %s: %w", mod.Name, err))
			}
		}

		fmt.Printf("  ✓ %s\n", mod.Name)
		succeeded++
	}

	// Run uninstall.after_all hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.AfterAll != "" {
		hookCtx.HookName = "uninstall.after_all"
		hookCtx.ModID = ""
		hookCtx.ModName = ""
		hookCtx.ModVersion = ""
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.AfterAll, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("uninstall.after_all hook failed: %w", err))
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

	// Final cleanup: remove any empty directories left in mod path
	linker.CleanupEmptyDirs(game.ModPath)

	fmt.Printf("\nPurged: %d mod(s)", succeeded)
	if failed > 0 {
		fmt.Printf(", Failed: %d", failed)
	}
	fmt.Println()

	if !purgeUninstall {
		fmt.Println("\nMod records preserved. Use 'lmm deploy' to restore mods.")
	}

	return nil
}

// purgeDeployedMods undeploys all mods for a game/profile and marks them as disabled.
// This is used by the deploy --purge flag to ensure a clean slate before deploying.
// It fires uninstall hooks for the purge phase.
func purgeDeployedMods(ctx context.Context, service *core.Service, game *domain.Game, profileName string, force bool) error {
	mods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	if len(mods) == 0 {
		return nil
	}

	// Set up hooks for purge phase (uses uninstall hooks)
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

	// Run uninstall.before_all hook (for purge phase)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeAll != "" {
		hookCtx.HookName = "uninstall.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeAll, hookCtx); err != nil {
			if !force {
				return fmt.Errorf("uninstall.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: uninstall.before_all hook failed (forced): %v\n", err)
		}
	}

	installer := service.GetInstaller(game)

	fmt.Printf("Purging %d mod(s) before deploy...\n", len(mods))

	for _, mod := range mods {
		// Run uninstall.before_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeEach != "" {
			hookCtx.HookName = "uninstall.before_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeEach, hookCtx); err != nil {
				fmt.Printf("  Skipped: uninstall.before_each hook failed: %v\n", err)
				continue // Skip this mod, continue with others
			}
		}

		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			if verbose {
				fmt.Printf("  ⚠ %s - %v\n", mod.Name, err)
			}
			// Continue anyway - files may have been manually removed
		}

		// Mark mod as not deployed (files removed from game directory)
		if err := service.DB().SetModDeployed(mod.SourceID, mod.ID, game.ID, profileName, false); err != nil {
			if verbose {
				fmt.Printf("  ⚠ %s - failed to mark as not deployed: %v\n", mod.Name, err)
			}
		}

		// Run uninstall.after_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.AfterEach != "" {
			hookCtx.HookName = "uninstall.after_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.AfterEach, hookCtx); err != nil {
				hookErrors = append(hookErrors, fmt.Errorf("uninstall.after_each hook failed for %s: %w", mod.ID, err))
			}
		}
	}

	// Run uninstall.after_all hook (for purge phase)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.AfterAll != "" {
		hookCtx.HookName = "uninstall.after_all"
		hookCtx.ModID = ""
		hookCtx.ModName = ""
		hookCtx.ModVersion = ""
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.AfterAll, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("uninstall.after_all hook failed: %w", err))
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

	// Clean up any leftover empty directories
	linker.CleanupEmptyDirs(game.ModPath)

	fmt.Println()
	return nil
}
