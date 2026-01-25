package main

import (
	"context"
	"fmt"

	"lmm/internal/core"

	"github.com/spf13/cobra"
)

var (
	uninstallSource  string
	uninstallProfile string
	uninstallKeep    bool
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
	uninstallCmd.Flags().StringVarP(&uninstallSource, "source", "s", "nexusmods", "mod source")
	uninstallCmd.Flags().StringVarP(&uninstallProfile, "profile", "p", "", "profile to uninstall from (default: active profile)")
	uninstallCmd.Flags().BoolVar(&uninstallKeep, "keep-cache", false, "keep cached mod files")

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
	defer service.Close()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// Determine profile
	profileName := uninstallProfile
	if profileName == "" {
		profileName = "default"
	}

	if verbose {
		fmt.Printf("Uninstalling mod %s from %s (profile: %s)...\n", modID, game.Name, profileName)
	}

	// Get the installed mod details
	installedMod, err := service.GetInstalledMod(uninstallSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
	}

	ctx := context.Background()

	// Undeploy files from game directory
	linker := service.GetLinker(game.LinkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), linker)

	if err := installer.Uninstall(ctx, game, &installedMod.Mod); err != nil {
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

	fmt.Printf("✓ Uninstalled: %s\n", installedMod.Name)
	fmt.Printf("  Removed from profile: %s\n", profileName)

	if uninstallKeep {
		fmt.Println("  Cache files preserved")
	}

	return nil
}
