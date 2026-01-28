package main

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	purgeProfile     string
	purgeKeepRecords bool
	purgeYes         bool
)

var purgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Remove all deployed mods from game directory",
	Long: `Remove all deployed mod files from the game directory.

This command undeploys all mods, essentially resetting the game directory
back to its pre-modded state. Use this when mods get out of sync or you
want to start fresh.

By default, this also removes mod records from the database. Use --keep-records
to preserve the database entries (mods will show as installed but not deployed).

Examples:
  lmm purge --game skyrim-se
  lmm purge --game skyrim-se --profile survival
  lmm purge --game skyrim-se --keep-records
  lmm purge --game skyrim-se --yes`,
	RunE: runPurge,
}

func init() {
	purgeCmd.Flags().StringVarP(&purgeProfile, "profile", "p", "", "profile to purge (default: default profile)")
	purgeCmd.Flags().BoolVar(&purgeKeepRecords, "keep-records", false, "keep mod records in database (just undeploy files)")
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
		fmt.Printf("This will remove %d mod(s) from %s (profile: %s)\n", len(mods), game.Name, profileName)
		if !purgeKeepRecords {
			fmt.Println("Mod records will also be removed from the database.")
		} else {
			fmt.Println("Mod records will be preserved in the database.")
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

		// Remove from database unless --keep-records
		if !purgeKeepRecords {
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
		}

		fmt.Printf("  ✓ %s\n", mod.Name)
		succeeded++
	}

	fmt.Printf("\nPurged: %d mod(s)", succeeded)
	if failed > 0 {
		fmt.Printf(", Failed: %d", failed)
	}
	fmt.Println()

	if purgeKeepRecords {
		fmt.Println("\nMod records preserved. Use 'lmm redeploy' to restore mods.")
	}

	return nil
}

// purgeDeployedMods undeploys all mods for a game/profile without touching the database.
// This is used by the redeploy --purge flag to ensure a clean slate before redeploying.
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

	fmt.Printf("Purging %d mod(s) before redeploy...\n", len(mods))

	for _, mod := range mods {
		if err := installer.Uninstall(ctx, game, &mod.Mod); err != nil {
			if verbose {
				fmt.Printf("  ⚠ %s - %v\n", mod.Name, err)
			}
			// Continue anyway - files may have been manually removed
		}
	}

	fmt.Println()
	return nil
}
