package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
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
	updateForce   bool
)

type updateJSONOutput struct {
	GameID  string          `json:"game_id"`
	Profile string          `json:"profile"`
	Updates []updateModJSON `json:"updates"`
}

type updateModJSON struct {
	ModID        string `json:"mod_id"`
	Name         string `json:"name"`
	Current      string `json:"current_version"`
	Available    string `json:"available_version"`
	UpdatePolicy string `json:"update_policy"`
}

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
	updateCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "continue even if hooks fail")

	updateRollbackCmd.Flags().StringVarP(&updateSource, "source", "s", "nexusmods", "mod source")
	updateRollbackCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile (default: active profile)")
	updateRollbackCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "continue even if hooks fail")

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
		ctx = context.WithValue(ctx, domain.UpdateProgressContextKey, domain.UpdateProgressFunc(func(n, total int, name string) {
			fmt.Fprintf(os.Stderr, "  %d/%d: %s\n", n, total, truncate(name, 60))
		}))
	}

	// Check for updates (partial results returned even when some mods fail to fetch)
	updater := core.NewUpdater(service.Registry())
	updates, err := updater.CheckUpdates(ctx, installed)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return fmt.Errorf("authentication required; run 'lmm auth login <source>' to authenticate")
		}
		// Surface warning but continue to show partial updates
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	if len(updates) == 0 {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(updateJSONOutput{GameID: gameID, Profile: profileName, Updates: []updateModJSON{}}); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
		}
		fmt.Println("All mods are up to date.")
		return nil
	}

	if jsonOutput {
		out := updateJSONOutput{GameID: gameID, Profile: profileName, Updates: make([]updateModJSON, len(updates))}
		for i, u := range updates {
			out.Updates[i] = updateModJSON{
				ModID:        u.InstalledMod.ID,
				Name:         u.InstalledMod.Name,
				Current:      u.InstalledMod.Version,
				Available:    u.NewVersion,
				UpdatePolicy: policyToString(u.InstalledMod.UpdatePolicy),
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return nil
	}

	// Display available updates with policy
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(w, "MOD\tCURRENT\tAVAILABLE\tPOLICY\n"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintf(w, "---\t-------\t---------\t------\n"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	var autoUpdates []domain.Update
	for _, update := range updates {
		policyStr := policyToString(update.InstalledMod.UpdatePolicy)
		if update.InstalledMod.UpdatePolicy == domain.UpdateAuto {
			policyStr += " ✓"
			autoUpdates = append(autoUpdates, update)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			truncate(update.InstalledMod.Name, 40),
			update.InstalledMod.Version,
			update.NewVersion,
			policyStr,
		); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	fmt.Printf("\n%d update(s) available.\n", len(updates))

	// Show changelogs where available
	var withChangelog []domain.Update
	for _, u := range updates {
		if u.Changelog != "" {
			withChangelog = append(withChangelog, u)
		}
	}
	if len(withChangelog) > 0 {
		fmt.Println("\nChangelogs:")
		for _, u := range withChangelog {
			cl := stripHTMLForTerminal(u.Changelog)
			const maxChangelog = 800
			if len(cl) > maxChangelog {
				cl = cl[:maxChangelog] + "\n..."
			}
			fmt.Printf("\n  %s (%s → %s):\n", u.InstalledMod.Name, u.InstalledMod.Version, u.NewVersion)
			for _, line := range strings.Split(strings.TrimSpace(cl), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}

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
			if err := applyUpdate(ctx, service, game, &update.InstalledMod, update.NewVersion, profileName, update.FileIDReplacements); err != nil {
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
				if err := applyUpdate(ctx, service, game, &update.InstalledMod, update.NewVersion, profileName, update.FileIDReplacements); err != nil {
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
			return fmt.Errorf("authentication required; run 'lmm auth login <source>' to authenticate")
		}
		return fmt.Errorf("failed to check update: %w", err)
	}

	if len(updates) == 0 {
		fmt.Printf("%s is already up to date (v%s).\n", mod.Name, mod.Version)
		return nil
	}

	update := updates[0]
	oldVersion := mod.Version
	newVersion := update.NewVersion
	fmt.Printf("Updating %s %s → %s...\n", mod.Name, oldVersion, newVersion)
	if update.Changelog != "" {
		cl := stripHTMLForTerminal(update.Changelog)
		const maxChangelog = 500
		if len(cl) > maxChangelog {
			cl = cl[:maxChangelog] + "..."
		}
		fmt.Println("Changelog:")
		for _, line := range strings.Split(strings.TrimSpace(cl), "\n") {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	}

	if updateDryRun {
		fmt.Println("(dry-run: no changes applied)")
		return nil
	}

	if err := applyUpdate(ctx, service, game, mod, newVersion, profileName, updates[0].FileIDReplacements); err != nil {
		return err
	}

	fmt.Printf("\n✓ Updated: %s %s → %s\n", mod.Name, oldVersion, newVersion)
	fmt.Println("  Previous version preserved for rollback")
	return nil
}

func applyUpdate(ctx context.Context, service *core.Service, game *domain.Game, mod *domain.InstalledMod, newVersion, profileName string, fileIDReplacements map[string]string) error {
	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

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

	// Resolve file IDs: use replacements when a file was superseded (e.g. NexusMods FileUpdates)
	effectiveFileIDs := mod.FileIDs
	if len(fileIDReplacements) > 0 {
		effectiveFileIDs = make([]string, len(mod.FileIDs))
		for i, fid := range mod.FileIDs {
			if newID, ok := fileIDReplacements[fid]; ok {
				effectiveFileIDs[i] = newID
			} else {
				effectiveFileIDs[i] = fid
			}
		}
	}
	// Try to use the same file IDs as before (or superseding IDs), or fall back to primary
	filesToDownload, _, err := selectFilesToDownload(files, effectiveFileIDs)
	if err != nil {
		return fmt.Errorf("selecting files to download: %w", err)
	}

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

	// Run uninstall.before_each hook (before uninstalling old version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeEach != "" {
		hookCtx.HookName = "uninstall.before_each"
		hookCtx.ModID = mod.ID
		hookCtx.ModName = mod.Name
		hookCtx.ModVersion = mod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeEach, hookCtx); err != nil {
			if !updateForce {
				return fmt.Errorf("uninstall.before_each hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: uninstall.before_each hook failed (forced): %v\n", err)
		}
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

	// Run uninstall.after_each hook (after uninstalling old version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.AfterEach != "" {
		hookCtx.HookName = "uninstall.after_each"
		hookCtx.ModID = mod.ID
		hookCtx.ModName = mod.Name
		hookCtx.ModVersion = mod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.AfterEach, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("uninstall.after_each hook failed: %w", err))
		}
	}

	// Run install.before_each hook (before installing new version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeEach != "" {
		hookCtx.HookName = "install.before_each"
		hookCtx.ModID = newMod.ID
		hookCtx.ModName = newMod.Name
		hookCtx.ModVersion = newMod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeEach, hookCtx); err != nil {
			if !updateForce {
				return fmt.Errorf("install.before_each hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_each hook failed (forced): %v\n", err)
		}
	}

	// Deploy new version
	if err := installer.Install(ctx, game, newMod, profileName); err != nil {
		return fmt.Errorf("deploying update: %w", err)
	}

	// Run install.after_each hook (after installing new version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterEach != "" {
		hookCtx.HookName = "install.after_each"
		hookCtx.ModID = newMod.ID
		hookCtx.ModName = newMod.Name
		hookCtx.ModVersion = newMod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterEach, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_each hook failed: %w", err))
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

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

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

	// Run uninstall.before_each hook (before uninstalling current version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.BeforeEach != "" {
		hookCtx.HookName = "uninstall.before_each"
		hookCtx.ModID = mod.ID
		hookCtx.ModName = mod.Name
		hookCtx.ModVersion = mod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.BeforeEach, hookCtx); err != nil {
			if !updateForce {
				return fmt.Errorf("uninstall.before_each hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: uninstall.before_each hook failed (forced): %v\n", err)
		}
	}

	// Undeploy current version
	linkMethod := service.GetGameLinkMethod(game)
	linker := service.GetLinker(linkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), linker, service.DB())

	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		if verbose {
			fmt.Printf("  Warning: failed to undeploy current version: %v\n", err)
		}
	}

	// Run uninstall.after_each hook (after uninstalling current version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Uninstall.AfterEach != "" {
		hookCtx.HookName = "uninstall.after_each"
		hookCtx.ModID = mod.ID
		hookCtx.ModName = mod.Name
		hookCtx.ModVersion = mod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Uninstall.AfterEach, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("uninstall.after_each hook failed: %w", err))
		}
	}

	// Deploy previous version
	prevMod := mod.Mod
	prevMod.Version = mod.PreviousVersion

	// Run install.before_each hook (before installing previous version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeEach != "" {
		hookCtx.HookName = "install.before_each"
		hookCtx.ModID = prevMod.ID
		hookCtx.ModName = prevMod.Name
		hookCtx.ModVersion = prevMod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeEach, hookCtx); err != nil {
			if !updateForce {
				return fmt.Errorf("install.before_each hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_each hook failed (forced): %v\n", err)
		}
	}

	if err := installer.Install(ctx, game, &prevMod, profileName); err != nil {
		return fmt.Errorf("deploying previous version: %w", err)
	}

	// Run install.after_each hook (after installing previous version)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterEach != "" {
		hookCtx.HookName = "install.after_each"
		hookCtx.ModID = prevMod.ID
		hookCtx.ModName = prevMod.Name
		hookCtx.ModVersion = prevMod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterEach, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_each hook failed: %w", err))
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

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

// stripHTMLForTerminal removes HTML tags for readable terminal output.
func stripHTMLForTerminal(html string) string {
	// Replace block/line breaks with newlines
	html = regexp.MustCompile(`(?i)<br\s*/?>|</p>|<p[^>]*>`).ReplaceAllString(html, "\n")
	// Remove remaining tags
	html = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(html, "")
	// Decode common entities
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	return strings.TrimSpace(html)
}
