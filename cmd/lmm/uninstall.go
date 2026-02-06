package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	uninstallSource  string
	uninstallProfile string
	uninstallKeep    bool
	uninstallForce   bool
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall <mod-id>",
	Short: "Uninstall a mod",
	Long: `Uninstall a mod from the specified profile.

By default, the mod files are removed from the game directory and the cache.
Use --keep-cache to preserve the cached files for potential reinstallation.

Examples:
  lmm uninstall 12345 --game skyrim-se
  lmm uninstall 12345 --game skyrim-se --profile survival
  lmm uninstall 12345 --game skyrim-se --keep-cache`,
	Args: cobra.ExactArgs(1),
	RunE: runUninstall,
}

func init() {
	uninstallCmd.Flags().StringVarP(&uninstallSource, "source", "s", "", "mod source (default: first configured source for game)")
	uninstallCmd.Flags().StringVarP(&uninstallProfile, "profile", "p", "", "profile to uninstall from (default: active profile)")
	uninstallCmd.Flags().BoolVar(&uninstallKeep, "keep-cache", false, "keep cached mod files")
	uninstallCmd.Flags().BoolVarP(&uninstallForce, "force", "f", false, "continue even if hooks fail")

	rootCmd.AddCommand(uninstallCmd)
}

func runUninstall(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// Resolve source: use flag if set, otherwise first configured source
	uninstallSource, err = resolveSource(game, uninstallSource, false)
	if err != nil {
		return err
	}

	// Determine profile
	profileName := profileOrDefault(uninstallProfile)

	if verbose {
		fmt.Printf("Uninstalling mod %s from %s (profile: %s)...\n", modID, game.Name, profileName)
	}

	// Get the installed mod details
	installedMod, err := service.GetInstalledMod(uninstallSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
	}

	ctx := context.Background()

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

	// Run uninstall.before_all hook (for single mod, this also serves as before_each)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeAll != "" {
		hookCtx.HookName = "uninstall.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeAll, hookCtx); err != nil {
			if !uninstallForce {
				return fmt.Errorf("uninstall.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: uninstall.before_all hook failed (forced): %v\n", err)
		}
	}

	// Run uninstall.before_each hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeEach != "" {
		hookCtx.HookName = "uninstall.before_each"
		hookCtx.ModID = installedMod.ID
		hookCtx.ModName = installedMod.Name
		hookCtx.ModVersion = installedMod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeEach, hookCtx); err != nil {
			if !uninstallForce {
				return fmt.Errorf("uninstall.before_each hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: uninstall.before_each hook failed (forced): %v\n", err)
		}
	}

	// Undeploy files from game directory
	installer := service.GetInstaller(game)

	if err := installer.Uninstall(ctx, game, &installedMod.Mod, profileName); err != nil {
		// Warn but continue - files may have been manually removed
		if verbose {
			fmt.Printf("  Warning: failed to undeploy some files: %v\n", err)
		}
	}

	// Clean up cache unless --keep-cache is set
	if !uninstallKeep {
		if err := service.GetGameCache(game).Delete(gameID, uninstallSource, modID, installedMod.Version); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to clean cache: %v\n", err)
			}
		}
	}

	// Remove from database
	if err := service.DB().DeleteInstalledMod(uninstallSource, modID, gameID, profileName); err != nil {
		return fmt.Errorf("failed to remove mod record: %w", err)
	}

	// Remove from profile
	pm := getProfileManager(service)
	if err := pm.RemoveMod(gameID, profileName, uninstallSource, modID); err != nil {
		// Don't fail if not in profile
		if verbose {
			fmt.Printf("  Note: %v\n", err)
		}
	}

	// Run uninstall.after_each hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.AfterEach != "" {
		hookCtx.HookName = "uninstall.after_each"
		hookCtx.ModID = installedMod.ID
		hookCtx.ModName = installedMod.Name
		hookCtx.ModVersion = installedMod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.AfterEach, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("uninstall.after_each hook failed: %w", err))
		}
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

	fmt.Printf("✓ Uninstalled: %s\n", installedMod.Name)
	fmt.Printf("  Removed from profile: %s\n", profileName)

	if uninstallKeep {
		fmt.Println("  Cache files preserved")
	}

	return nil
}
