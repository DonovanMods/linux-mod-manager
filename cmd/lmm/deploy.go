package main

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

var (
	deploySource  string
	deployProfile string
	deployMethod  string
	deployPurge   bool
	deployAll     bool
	deployForce   bool
)

var deployCmd = &cobra.Command{
	Use:   "deploy [mod-id]",
	Short: "Deploy mods to game directory",
	Long: `Deploy mod files from cache to game directory.

Use this when changing deployment methods (symlink, hardlink, copy)
or if mod files need to be refreshed.

Without a mod ID, deploys all enabled mods in the current profile.
With a mod ID, deploys only that specific mod.

Use --purge to remove all deployed mods before deploying. This ensures
a clean slate, useful when mods have gotten out of sync.

Use --all to deploy all mods including disabled ones (e.g., after a purge).

Examples:
  lmm deploy --game skyrim-se
  lmm deploy --game skyrim-se --all
  lmm deploy --game skyrim-se --method hardlink
  lmm deploy --game skyrim-se --purge
  lmm deploy 12345 --game skyrim-se
  lmm deploy 12345 --game skyrim-se --method copy`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDeploy,
}

func init() {
	deployCmd.Flags().StringVarP(&deploySource, "source", "s", "nexusmods", "mod source")
	deployCmd.Flags().StringVarP(&deployProfile, "profile", "p", "", "profile (default: active profile)")
	deployCmd.Flags().StringVarP(&deployMethod, "method", "m", "", "link method: symlink, hardlink, or copy (default: game's configured method)")
	deployCmd.Flags().BoolVar(&deployPurge, "purge", false, "purge all deployed mods before deploying")
	deployCmd.Flags().BoolVarP(&deployAll, "all", "a", false, "deploy all mods including disabled ones")
	deployCmd.Flags().BoolVarP(&deployForce, "force", "f", false, "continue even if hooks fail")

	rootCmd.AddCommand(deployCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
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

	profileName := profileOrDefault(deployProfile)

	ctx := context.Background()

	// If --purge flag is set, purge all deployed mods first
	// We remember which mods were enabled before purging so we can redeploy them
	var enabledBeforePurge map[string]bool
	if deployPurge {
		// Remember enabled mods before purge
		mods, err := service.GetInstalledMods(gameID, profileName)
		if err != nil {
			return fmt.Errorf("getting installed mods: %w", err)
		}
		enabledBeforePurge = make(map[string]bool)
		for _, mod := range mods {
			if mod.Enabled {
				enabledBeforePurge[mod.SourceID+":"+mod.ID] = true
			}
		}

		if err := purgeDeployedMods(ctx, service, game, profileName, deployForce); err != nil {
			return fmt.Errorf("purging mods: %w", err)
		}
	}

	// Determine link method
	var linkMethod domain.LinkMethod
	if deployMethod != "" {
		switch deployMethod {
		case "symlink":
			linkMethod = domain.LinkSymlink
		case "hardlink":
			linkMethod = domain.LinkHardlink
		case "copy":
			linkMethod = domain.LinkCopy
		default:
			return fmt.Errorf("invalid link method: %s (use: symlink, hardlink, or copy)", deployMethod)
		}
	} else {
		linkMethod = service.GetGameLinkMethod(game)
	}

	lnk := linker.New(linkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), lnk, service.DB())

	// Get mods to deploy
	var modsToDeploy []*domain.InstalledMod

	if len(args) > 0 {
		// Specific mod
		modID := args[0]
		mod, err := service.GetInstalledMod(deploySource, modID, gameID, profileName)
		if err != nil {
			return fmt.Errorf("mod not found: %s", modID)
		}
		if !mod.Enabled && !deployAll {
			return fmt.Errorf("mod %s is disabled - use --all to deploy disabled mods, or enable it with 'lmm mod enable %s'", mod.Name, modID)
		}
		modsToDeploy = append(modsToDeploy, mod)
	} else {
		// Get mods in profile load order (first = lowest priority)
		mods, err := service.GetInstalledModsInProfileOrder(gameID, profileName)
		if err != nil {
			return fmt.Errorf("getting installed mods: %w", err)
		}

		for i := range mods {
			shouldDeploy := false
			if deployAll {
				shouldDeploy = true
			} else if enabledBeforePurge != nil {
				shouldDeploy = enabledBeforePurge[mods[i].SourceID+":"+mods[i].ID]
			} else {
				shouldDeploy = mods[i].Enabled
			}

			if shouldDeploy {
				modsToDeploy = append(modsToDeploy, &mods[i])
			}
		}
	}

	if len(modsToDeploy) == 0 {
		if deployAll {
			fmt.Println("No mods to deploy.")
		} else {
			fmt.Println("No enabled mods to deploy. Use --all to deploy disabled mods.")
		}
		return nil
	}

	// Set up hooks for deploy phase (uses install hooks)
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

	// Run install.before_all hook (for deploy phase)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeAll != "" {
		hookCtx.HookName = "install.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeAll, hookCtx); err != nil {
			if !deployForce {
				return fmt.Errorf("install.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_all hook failed (forced): %v\n", err)
		}
	}

	methodName := linkMethod.String()
	fmt.Printf("Deploying %d mod(s) using %s...\n\n", len(modsToDeploy), methodName)

	var succeeded, failed int

	for _, mod := range modsToDeploy {
		// Run install.before_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeEach != "" {
			hookCtx.HookName = "install.before_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeEach, hookCtx); err != nil {
				fmt.Printf("  Skipped: install.before_each hook failed: %v\n", err)
				failed++
				continue // Skip this mod, continue with others
			}
		}
		// Check if mod is in cache
		if !service.GetGameCache(game).Exists(gameID, mod.SourceID, mod.ID, mod.Version) {
			fmt.Printf("  %s %s - cache missing, re-downloading...\n", colorYellow("⚠"), mod.Name)

			// Fetch mod info from source
			fetchedMod, err := service.GetMod(ctx, mod.SourceID, gameID, mod.ID)
			if err != nil {
				fmt.Printf("  %s %s - failed to fetch: %v\n", colorRed("✗"), mod.Name, err)
				failed++
				continue
			}

			// Get available files
			files, err := service.GetModFiles(ctx, mod.SourceID, fetchedMod)
			if err != nil || len(files) == 0 {
				fmt.Printf("  %s %s - no files available\n", colorRed("✗"), mod.Name)
				failed++
				continue
			}

			// Find files to download - use stored FileIDs or fall back to primary
			filesToDownload, usedFallback, err := selectFilesToDownload(files, mod.FileIDs)
			if err != nil {
				fmt.Printf("  %s %s - %v\n", colorRed("✗"), mod.Name, err)
				failed++
				continue
			}
			if usedFallback {
				fmt.Printf("  %s %s - stored file IDs not found, using primary\n", colorYellow("⚠"), mod.Name)
			}

			// Download each file
			downloadFailed := false
			for _, selectedFile := range filesToDownload {
				progressFn := func(p core.DownloadProgress) {
					if p.TotalBytes > 0 {
						fmt.Printf("\r  ⬇ %s: %.1f%%", mod.Name, p.Percentage)
					}
				}

				_, err = service.DownloadMod(ctx, mod.SourceID, game, fetchedMod, selectedFile, progressFn)
				if err != nil {
					fmt.Println()
					fmt.Printf("  %s %s - download failed: %v\n", colorRed("✗"), mod.Name, err)
					downloadFailed = true
					break
				}
			}
			fmt.Println() // Clear progress line

			if downloadFailed {
				failed++
				continue
			}
		}

		// Undeploy first (remove old links/files)
		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			if verbose {
				fmt.Printf("  Warning: undeploy %s: %v\n", mod.Name, err)
			}
		}

		// Redeploy with new method
		if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
			fmt.Printf("  %s %s - %v\n", colorRed("✗"), mod.Name, err)
			failed++
			continue
		}

		// Update the link method in database
		if err := service.SetModLinkMethod(mod.SourceID, mod.ID, gameID, profileName, linkMethod); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not update link method: %v\n", err)
			}
		}

		// Mark mod as deployed (files are now in game directory)
		if err := service.DB().SetModDeployed(mod.SourceID, mod.ID, gameID, profileName, true); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not mark as deployed: %v\n", err)
			}
		}

		fmt.Printf("  %s %s\n", colorGreen("✓"), mod.Name)
		succeeded++

		// Run install.after_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterEach != "" {
			hookCtx.HookName = "install.after_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterEach, hookCtx); err != nil {
				hookErrors = append(hookErrors, fmt.Errorf("install.after_each hook failed for %s: %w", mod.ID, err))
			}
		}
	}

	// Run install.after_all hook (for deploy phase)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterAll != "" {
		hookCtx.HookName = "install.after_all"
		hookCtx.ModID = ""
		hookCtx.ModName = ""
		hookCtx.ModVersion = ""
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterAll, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_all hook failed: %w", err))
		}
	}

	// Apply profile overrides (INI tweaks, etc.)
	if profile, err := config.LoadProfile(service.ConfigDir(), gameID, profileName); err == nil && len(profile.Overrides) > 0 {
		if err := core.ApplyProfileOverrides(game, profile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: applying profile overrides: %v\n", err)
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

	fmt.Printf("\nDeployed: %d", succeeded)
	if failed > 0 {
		fmt.Printf(", Failed: %d", failed)
	}
	fmt.Println()

	if deployMethod != "" {
		fmt.Printf("\nNote: Used %s method for this deployment.\n", methodName)
		fmt.Printf("To make this permanent, update your games.yaml config.\n")
	}

	return nil
}

// findFilesByIDs finds downloadable files matching the given IDs
func findFilesByIDs(files []domain.DownloadableFile, fileIDs []string) []*domain.DownloadableFile {
	idSet := make(map[string]bool)
	for _, id := range fileIDs {
		idSet[id] = true
	}

	var result []*domain.DownloadableFile
	for i := range files {
		if idSet[files[i].ID] {
			result = append(result, &files[i])
		}
	}
	return result
}
