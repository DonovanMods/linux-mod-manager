package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	purgeProfile   string
	purgeUninstall bool
	purgeYes       bool
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
	defer service.Close()

	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	profileName := purgeProfile
	if profileName == "" {
		profileName = "default"
	}

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

		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	ctx := context.Background()
	linker := service.GetLinker(game.LinkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), linker)

	var succeeded, failed int

	fmt.Printf("\nPurging mods from %s...\n\n", game.Name)

	for _, mod := range mods {
		// Undeploy files from game directory
		if err := installer.Uninstall(ctx, game, &mod.Mod); err != nil {
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
			// Mark mod as disabled since it's no longer deployed
			if err := service.DB().SetModEnabled(mod.SourceID, mod.ID, gameID, profileName, false); err != nil {
				if verbose {
					fmt.Printf("  ⚠ %s - failed to disable: %v\n", mod.Name, err)
				}
			}
		}

		fmt.Printf("  ✓ %s\n", mod.Name)
		succeeded++
	}

	// Final cleanup: remove any empty directories left in mod path
	cleanupEmptyModDirs(game.ModPath)

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

// cleanupEmptyModDirs removes all empty directories under the mod path.
// This catches any leftover directories from previous mod versions or failed installs.
func cleanupEmptyModDirs(modPath string) {
	// Walk the directory tree and collect empty directories
	var emptyDirs []string

	filepath.Walk(modPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if !info.IsDir() {
			return nil // Skip files
		}
		if path == modPath {
			return nil // Don't remove the mod path itself
		}

		// Check if directory is empty
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		if len(entries) == 0 {
			emptyDirs = append(emptyDirs, path)
		}
		return nil
	})

	// Remove empty directories (deepest first by removing in reverse order after sorting by length)
	// Sort by path length descending so deeper paths come first
	for i := len(emptyDirs) - 1; i >= 0; i-- {
		os.Remove(emptyDirs[i])
	}

	// Do a second pass in case removing a directory made its parent empty
	for {
		found := false
		filepath.Walk(modPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() || path == modPath {
				return nil
			}
			entries, err := os.ReadDir(path)
			if err == nil && len(entries) == 0 {
				os.Remove(path)
				found = true
			}
			return nil
		})
		if !found {
			break
		}
	}
}

// purgeDeployedMods undeploys all mods for a game/profile and marks them as disabled.
// This is used by the deploy --purge flag to ensure a clean slate before deploying.
func purgeDeployedMods(ctx context.Context, service *core.Service, game *domain.Game, profileName string) error {
	mods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	if len(mods) == 0 {
		return nil
	}

	linker := service.GetLinker(game.LinkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), linker)

	fmt.Printf("Purging %d mod(s) before deploy...\n", len(mods))

	for _, mod := range mods {
		if err := installer.Uninstall(ctx, game, &mod.Mod); err != nil {
			if verbose {
				fmt.Printf("  ⚠ %s - %v\n", mod.Name, err)
			}
			// Continue anyway - files may have been manually removed
		}

		// Mark mod as disabled since it's no longer deployed
		if err := service.DB().SetModEnabled(mod.SourceID, mod.ID, game.ID, profileName, false); err != nil {
			if verbose {
				fmt.Printf("  ⚠ %s - failed to disable: %v\n", mod.Name, err)
			}
		}
	}

	// Clean up any leftover empty directories
	cleanupEmptyModDirs(game.ModPath)

	fmt.Println()
	return nil
}
