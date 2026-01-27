package main

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"

	"github.com/spf13/cobra"
)

var (
	redeploySource  string
	redeployProfile string
	redeployMethod  string
)

var redeployCmd = &cobra.Command{
	Use:   "redeploy [mod-id]",
	Short: "Re-deploy mods to game directory",
	Long: `Re-deploy mod files from cache to game directory.

Use this when changing deployment methods (symlink, hardlink, copy)
or if mod files need to be refreshed.

Without a mod ID, re-deploys all enabled mods in the current profile.
With a mod ID, re-deploys only that specific mod.

Examples:
  lmm redeploy --game skyrim-se
  lmm redeploy --game skyrim-se --method hardlink
  lmm redeploy 12345 --game skyrim-se
  lmm redeploy 12345 --game skyrim-se --method copy`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRedeploy,
}

func init() {
	redeployCmd.Flags().StringVarP(&redeploySource, "source", "s", "nexusmods", "mod source")
	redeployCmd.Flags().StringVarP(&redeployProfile, "profile", "p", "", "profile (default: active profile)")
	redeployCmd.Flags().StringVarP(&redeployMethod, "method", "m", "", "link method: symlink, hardlink, or copy (default: game's configured method)")

	rootCmd.AddCommand(redeployCmd)
}

func runRedeploy(cmd *cobra.Command, args []string) error {
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

	profileName := redeployProfile
	if profileName == "" {
		profileName = "default"
	}

	// Determine link method
	var linkMethod domain.LinkMethod
	if redeployMethod != "" {
		switch redeployMethod {
		case "symlink":
			linkMethod = domain.LinkSymlink
		case "hardlink":
			linkMethod = domain.LinkHardlink
		case "copy":
			linkMethod = domain.LinkCopy
		default:
			return fmt.Errorf("invalid link method: %s (use: symlink, hardlink, or copy)", redeployMethod)
		}
	} else {
		linkMethod = service.GetGameLinkMethod(game)
	}

	ctx := context.Background()
	lnk := linker.New(linkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), lnk)

	// Get mods to redeploy
	var modsToRedeploy []*domain.InstalledMod

	if len(args) > 0 {
		// Specific mod
		modID := args[0]
		mod, err := service.GetInstalledMod(redeploySource, modID, gameID, profileName)
		if err != nil {
			return fmt.Errorf("mod not found: %s", modID)
		}
		if !mod.Enabled {
			return fmt.Errorf("mod %s is disabled - enable it first with 'lmm mod enable %s'", mod.Name, modID)
		}
		modsToRedeploy = append(modsToRedeploy, mod)
	} else {
		// All enabled mods in profile
		mods, err := service.GetInstalledMods(gameID, profileName)
		if err != nil {
			return fmt.Errorf("getting installed mods: %w", err)
		}

		for i := range mods {
			if mods[i].Enabled {
				modsToRedeploy = append(modsToRedeploy, &mods[i])
			}
		}
	}

	if len(modsToRedeploy) == 0 {
		fmt.Println("No enabled mods to redeploy.")
		return nil
	}

	methodName := linkMethodName(linkMethod)
	fmt.Printf("Re-deploying %d mod(s) using %s...\n\n", len(modsToRedeploy), methodName)

	var succeeded, failed int

	for _, mod := range modsToRedeploy {
		// Check if mod is in cache
		if !service.GetGameCache(game).Exists(gameID, mod.SourceID, mod.ID, mod.Version) {
			fmt.Printf("  ⚠ %s - cache missing, re-downloading...\n", mod.Name)

			// Fetch mod info from source
			fetchedMod, err := service.GetMod(ctx, mod.SourceID, gameID, mod.ID)
			if err != nil {
				fmt.Printf("  ✗ %s - failed to fetch: %v\n", mod.Name, err)
				failed++
				continue
			}

			// Get available files
			files, err := service.GetModFiles(ctx, mod.SourceID, fetchedMod)
			if err != nil || len(files) == 0 {
				fmt.Printf("  ✗ %s - no files available\n", mod.Name)
				failed++
				continue
			}

			// Select primary file
			selectedFile := selectPrimaryFile(files)

			// Download to cache
			progressFn := func(p core.DownloadProgress) {
				if p.TotalBytes > 0 {
					fmt.Printf("\r  ⬇ %s: %.1f%%", mod.Name, p.Percentage)
				}
			}

			_, err = service.DownloadMod(ctx, mod.SourceID, game, fetchedMod, selectedFile, progressFn)
			if err != nil {
				fmt.Println()
				fmt.Printf("  ✗ %s - download failed: %v\n", mod.Name, err)
				failed++
				continue
			}
			fmt.Println() // Clear progress line
		}

		// Undeploy first (remove old links/files)
		if err := installer.Uninstall(ctx, game, &mod.Mod); err != nil {
			if verbose {
				fmt.Printf("  Warning: undeploy %s: %v\n", mod.Name, err)
			}
		}

		// Redeploy with new method
		if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
			fmt.Printf("  ✗ %s - %v\n", mod.Name, err)
			failed++
			continue
		}

		// Update the link method in database
		if err := service.SetModLinkMethod(mod.SourceID, mod.ID, gameID, profileName, linkMethod); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not update link method: %v\n", err)
			}
		}

		fmt.Printf("  ✓ %s\n", mod.Name)
		succeeded++
	}

	fmt.Printf("\nRe-deployed: %d", succeeded)
	if failed > 0 {
		fmt.Printf(", Failed: %d", failed)
	}
	fmt.Println()

	if redeployMethod != "" {
		fmt.Printf("\nNote: Used %s method for this deployment.\n", methodName)
		fmt.Printf("To make this permanent, update your games.yaml config.\n")
	}

	return nil
}

func linkMethodName(method domain.LinkMethod) string {
	switch method {
	case domain.LinkSymlink:
		return "symlink"
	case domain.LinkHardlink:
		return "hardlink"
	case domain.LinkCopy:
		return "copy"
	default:
		return "unknown"
	}
}
