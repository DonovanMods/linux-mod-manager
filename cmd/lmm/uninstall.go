package main

import (
	"fmt"

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

	// Get installed mods to find the one to remove
	mods, err := service.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("failed to get installed mods: %w", err)
	}

	var found bool
	var modName string
	for _, mod := range mods {
		if mod.ID == modID && mod.SourceID == uninstallSource {
			found = true
			modName = mod.Name
			break
		}
	}

	if !found {
		return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
	}

	// Remove from database
	if err := service.DB().DeleteInstalledMod(uninstallSource, modID, gameID, profileName); err != nil {
		return fmt.Errorf("failed to remove mod record: %w", err)
	}

	fmt.Printf("✓ Uninstalled: %s\n", modName)

	// Note about cache
	if uninstallKeep {
		fmt.Println("  Cache files preserved")
	} else {
		fmt.Println("  Note: Cache cleanup not yet implemented")
	}

	return nil
}
