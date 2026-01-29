package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	updateSource  string
	updateProfile string
	updateAll     bool
	updateDryRun  bool
)

var updateCmd = &cobra.Command{
	Use:   "update [mod-id]",
	Short: "Check for or apply mod updates",
	Long: `Check for available updates or update specific mods.

Without arguments, checks all installed mods for updates.
With a mod ID, updates that specific mod.

Examples:
  lmm update --game skyrim-se                    # Check all mods for updates
  lmm update 12345 --game skyrim-se              # Update specific mod
  lmm update --game skyrim-se --all              # Apply all available updates
  lmm update --game skyrim-se --dry-run          # Show what would update`,
	Args: cobra.MaximumNArgs(1),
	RunE: runUpdate,
}

var updateRollbackCmd = &cobra.Command{
	Use:   "rollback <mod-id>",
	Short: "Rollback a mod to its previous version",
	Long: `Rollback a mod to the version before the last update.

The previous version must still be available in the cache.

Examples:
  lmm update rollback 12345 --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runUpdateRollback,
}

func init() {
	updateCmd.Flags().StringVarP(&updateSource, "source", "s", "nexusmods", "mod source")
	updateCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile to check (default: active profile)")
	updateCmd.Flags().BoolVar(&updateAll, "all", false, "apply all available updates")
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "show what would update without applying")

	updateRollbackCmd.Flags().StringVarP(&updateSource, "source", "s", "nexusmods", "mod source")
	updateRollbackCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile (default: active profile)")

	updateCmd.AddCommand(updateRollbackCmd)
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

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
	profileName := profileOrDefault(updateProfile)

	ctx := context.Background()

	// Get installed mods
	installed, err := service.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("failed to get installed mods: %w", err)
	}

	if len(installed) == 0 {
		fmt.Println("No mods installed.")
		return nil
	}

	// If specific mod ID provided, update just that mod
	if len(args) > 0 {
		modID := args[0]
		var targetMod *domain.InstalledMod
		for i := range installed {
			if installed[i].ID == modID && installed[i].SourceID == updateSource {
				targetMod = &installed[i]
				break
			}
		}
		if targetMod == nil {
			return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
		}

		return applySingleUpdate(ctx, service, game, targetMod, profileName)
	}

	if verbose {
		fmt.Printf("Checking %d mod(s) for updates in %s (profile: %s)...\n", len(installed), game.Name, profileName)
	}

	// Check for updates
	updater := core.NewUpdater(service.Registry())
	updates, err := updater.CheckUpdates(ctx, installed)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return fmt.Errorf("NexusMods requires authentication.\nRun 'lmm auth login' to authenticate")
		}
		return fmt.Errorf("failed to check updates: %w", err)
	}

	if len(updates) == 0 {
		fmt.Println("All mods are up to date.")
		return nil
	}

	// Display available updates with policy
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "MOD\tCURRENT\tAVAILABLE\tPOLICY\n")
	fmt.Fprintf(w, "---\t-------\t---------\t------\n")

	var autoUpdates []domain.Update
	for _, update := range updates {
		policyStr := policyToString(update.InstalledMod.UpdatePolicy)
		if update.InstalledMod.UpdatePolicy == domain.UpdateAuto {
			policyStr += " ✓"
			autoUpdates = append(autoUpdates, update)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			truncate(update.InstalledMod.Name, 40),
			update.InstalledMod.Version,
			update.NewVersion,
			policyStr,
		)
	}
	w.Flush()

	fmt.Printf("\n%d update(s) available.\n", len(updates))

	// Dry run mode - just show what would happen
	if updateDryRun {
		if len(autoUpdates) > 0 {
			fmt.Printf("\nWould auto-update %d mod(s):\n", len(autoUpdates))
			for _, u := range autoUpdates {
				fmt.Printf("  - %s %s → %s\n", u.InstalledMod.Name, u.InstalledMod.Version, u.NewVersion)
			}
		}
		fmt.Println("\nUse without --dry-run to apply updates.")
		return nil
	}

	// Apply auto-updates
	if len(autoUpdates) > 0 {
		fmt.Printf("\nApplying %d auto-update(s)...\n", len(autoUpdates))
		for _, update := range autoUpdates {
			if err := applyUpdate(ctx, service, game, &update.InstalledMod, update.NewVersion, profileName); err != nil {
				fmt.Printf("  ✗ %s: %v\n", update.InstalledMod.Name, err)
			} else {
				fmt.Printf("  ✓ %s %s → %s\n", update.InstalledMod.Name, update.InstalledMod.Version, update.NewVersion)
			}
		}
	}

	// If --all flag, apply all remaining updates
	if updateAll {
		var notifyUpdates []domain.Update
		for _, update := range updates {
			if update.InstalledMod.UpdatePolicy != domain.UpdateAuto {
				notifyUpdates = append(notifyUpdates, update)
			}
		}

		if len(notifyUpdates) > 0 {
			fmt.Printf("\nApplying %d remaining update(s)...\n", len(notifyUpdates))
			for _, update := range notifyUpdates {
				if err := applyUpdate(ctx, service, game, &update.InstalledMod, update.NewVersion, profileName); err != nil {
					fmt.Printf("  ✗ %s: %v\n", update.InstalledMod.Name, err)
				} else {
					fmt.Printf("  ✓ %s %s → %s\n", update.InstalledMod.Name, update.InstalledMod.Version, update.NewVersion)
				}
			}
		}
	}

	return nil
}

func applySingleUpdate(ctx context.Context, service *core.Service, game *domain.Game, mod *domain.InstalledMod, profileName string) error {
	// Check for update for this specific mod
	updater := core.NewUpdater(service.Registry())
	updates, err := updater.CheckUpdates(ctx, []domain.InstalledMod{*mod})
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return fmt.Errorf("NexusMods requires authentication.\nRun 'lmm auth login' to authenticate")
		}
		return fmt.Errorf("failed to check update: %w", err)
	}

	if len(updates) == 0 {
		fmt.Printf("%s is already up to date (v%s).\n", mod.Name, mod.Version)
		return nil
	}

	update := updates[0]
	fmt.Printf("Updating %s %s → %s...\n", mod.Name, mod.Version, update.NewVersion)

	if updateDryRun {
		fmt.Println("(dry-run: no changes applied)")
		return nil
	}

	if err := applyUpdate(ctx, service, game, mod, update.NewVersion, profileName); err != nil {
		return err
	}

	fmt.Printf("\n✓ Updated: %s %s → %s\n", mod.Name, mod.Version, update.NewVersion)
	fmt.Println("  Previous version preserved for rollback")
	return nil
}

func applyUpdate(ctx context.Context, service *core.Service, game *domain.Game, mod *domain.InstalledMod, newVersion, profileName string) error {
	// Get the new version's mod info
	newMod, err := service.GetMod(ctx, mod.SourceID, game.ID, mod.ID)
	if err != nil {
		return fmt.Errorf("fetching new version: %w", err)
	}

	// If version doesn't match what we expect, update it
	if newMod.Version != newVersion {
		newMod.Version = newVersion
	}

	// Get available files for the new version
	files, err := service.GetModFiles(ctx, mod.SourceID, newMod)
	if err != nil {
		return fmt.Errorf("getting mod files: %w", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no downloadable files available")
	}

	// Try to use the same file IDs as before, or fall back to primary
	filesToDownload, _ := selectFilesToDownload(files, mod.FileIDs)

	// Download the new version
	progressFn := func(p core.DownloadProgress) {
		if p.TotalBytes > 0 && verbose {
			fmt.Printf("\r  Downloading: %.1f%%", p.Percentage)
		}
	}

	var downloadedFileIDs []string
	for _, selectedFile := range filesToDownload {
		_, err = service.DownloadMod(ctx, mod.SourceID, game, newMod, selectedFile, progressFn)
		if err != nil {
			return fmt.Errorf("downloading update: %w", err)
		}
		downloadedFileIDs = append(downloadedFileIDs, selectedFile.ID)
	}
	if verbose {
		fmt.Println()
	}

	// Undeploy old version
	linkMethod := service.GetGameLinkMethod(game)
	linker := service.GetLinker(linkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), linker, service.DB())

	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		// Log but continue - files may have been manually removed
		if verbose {
			fmt.Printf("  Warning: failed to undeploy old version: %v\n", err)
		}
	}

	// Deploy new version
	if err := installer.Install(ctx, game, newMod, profileName); err != nil {
		return fmt.Errorf("deploying update: %w", err)
	}

	// Update database (preserves previous version for rollback)
	if err := service.UpdateModVersion(mod.SourceID, mod.ID, game.ID, profileName, newVersion); err != nil {
		return fmt.Errorf("updating database: %w", err)
	}

	// Update the link method used for deployment
	if err := service.SetModLinkMethod(mod.SourceID, mod.ID, game.ID, profileName, linkMethod); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update link method: %v\n", err)
		}
	}

	// Update FileIDs in database
	if err := service.SetModFileIDs(mod.SourceID, mod.ID, game.ID, profileName, downloadedFileIDs); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update file IDs: %v\n", err)
		}
	}

	// Update the profile entry with new version and FileIDs
	pm := getProfileManager(service)
	modRef := domain.ModReference{
		SourceID: mod.SourceID,
		ModID:    mod.ID,
		Version:  newVersion,
		FileIDs:  downloadedFileIDs,
	}
	if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update profile: %v\n", err)
		}
	}

	return nil
}

func runUpdateRollback(cmd *cobra.Command, args []string) error {
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

	profileName := profileOrDefault(updateProfile)

	// Get the installed mod
	mod, err := service.GetInstalledMod(updateSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	if mod.PreviousVersion == "" {
		return fmt.Errorf("no previous version available for rollback")
	}

	// Check if previous version exists in cache
	if !service.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, mod.PreviousVersion) {
		return fmt.Errorf("previous version %s not found in cache", mod.PreviousVersion)
	}

	fmt.Printf("Rolling back %s %s → %s...\n", mod.Name, mod.Version, mod.PreviousVersion)

	ctx := context.Background()

	// Undeploy current version
	linkMethod := service.GetGameLinkMethod(game)
	linker := service.GetLinker(linkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), linker, service.DB())

	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		if verbose {
			fmt.Printf("  Warning: failed to undeploy current version: %v\n", err)
		}
	}

	// Deploy previous version
	prevMod := mod.Mod
	prevMod.Version = mod.PreviousVersion
	if err := installer.Install(ctx, game, &prevMod, profileName); err != nil {
		return fmt.Errorf("deploying previous version: %w", err)
	}

	// Swap versions in database
	if err := service.RollbackModVersion(mod.SourceID, mod.ID, game.ID, profileName); err != nil {
		return fmt.Errorf("updating database: %w", err)
	}

	// Update the link method used for deployment
	if err := service.SetModLinkMethod(mod.SourceID, mod.ID, game.ID, profileName, linkMethod); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update link method: %v\n", err)
		}
	}

	fmt.Printf("\n✓ Rolled back: %s %s → %s\n", mod.Name, mod.Version, mod.PreviousVersion)
	return nil
}

func policyToString(policy domain.UpdatePolicy) string {
	switch policy {
	case domain.UpdateAuto:
		return "auto"
	case domain.UpdatePinned:
		return "pinned"
	default:
		return "notify"
	}
}
