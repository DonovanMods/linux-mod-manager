package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"

	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage mod profiles",
	Long: `Manage mod profiles for organizing different mod configurations.

Profiles allow you to maintain different sets of mods for the same game.
For example, you might have a "vanilla plus" profile and a "total conversion" profile.`,
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all profiles",
	Long: `List all profiles for the specified game.

Examples:
  lmm profile list --game skyrim-se`,
	RunE: runProfileList,
}

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new profile",
	Long: `Create a new empty profile for the specified game.

Examples:
  lmm profile create survival --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileCreate,
}

var profileDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a profile",
	Long: `Delete a profile and its configuration.

Note: This does not remove the installed mods, only the profile configuration.

Examples:
  lmm profile delete old-profile --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileDelete,
}

var profileSwitchCmd = &cobra.Command{
	Use:   "switch <name>",
	Short: "Switch to a different profile",
	Long: `Switch to a different profile, deploying its mods to the game directory.

This will undeploy mods from the current profile and deploy mods from the new profile.

Examples:
  lmm profile switch survival --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileSwitch,
}

var profileExportCmd = &cobra.Command{
	Use:   "export <name>",
	Short: "Export a profile",
	Long: `Export a profile to a portable YAML file.

The exported file can be shared with others or used as a backup.

Examples:
  lmm profile export survival --game skyrim-se > survival.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileExport,
}

var profileImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import a profile",
	Long: `Import a profile from a YAML file.

Examples:
  lmm profile import survival.yaml --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileImport,
}

var profileSyncCmd = &cobra.Command{
	Use:   "sync [name]",
	Short: "Sync profile to match installed mods",
	Long: `Update the profile YAML to match currently installed/enabled mods in the database.

Use this if the profile got out of sync, or to migrate from pre-profile installs.
If no name is given, uses the current/default profile.

Examples:
  lmm profile sync --game skyrim-se
  lmm profile sync survival --game skyrim-se`,
	Args: cobra.MaximumNArgs(1),
	RunE: runProfileSync,
}

var profileApplyCmd = &cobra.Command{
	Use:   "apply [name]",
	Short: "Apply profile to system",
	Long: `Make the system match the profile by installing/enabling/disabling mods.

Use this after manually editing a profile YAML to apply those changes.
If no name is given, uses the current/default profile.

Examples:
  lmm profile apply --game skyrim-se
  lmm profile apply survival --game skyrim-se`,
	Args: cobra.MaximumNArgs(1),
	RunE: runProfileApply,
}

var (
	profileImportForce     bool
	profileImportNoInstall bool
	profileApplyYes        bool
)

func init() {
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileCreateCmd)
	profileCmd.AddCommand(profileDeleteCmd)
	profileCmd.AddCommand(profileSwitchCmd)
	profileCmd.AddCommand(profileExportCmd)
	profileCmd.AddCommand(profileImportCmd)
	profileCmd.AddCommand(profileSyncCmd)
	profileCmd.AddCommand(profileApplyCmd)

	// Import flags
	profileImportCmd.Flags().BoolVar(&profileImportForce, "force", false, "overwrite existing profile")
	profileImportCmd.Flags().BoolVar(&profileImportNoInstall, "no-install", false, "skip installing missing mods")

	// Apply flags
	profileApplyCmd.Flags().BoolVarP(&profileApplyYes, "yes", "y", false, "auto-confirm changes")

	rootCmd.AddCommand(profileCmd)
}

func getProfileManager(service *core.Service) *core.ProfileManager {
	lnk := linker.New(service.GetDefaultLinkMethod())
	return core.NewProfileManager(service.ConfigDir(), service.DB(), service.Cache(), lnk)
}

func runProfileList(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	profiles, err := pm.List(gameID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tMODS\tDEFAULT")
	fmt.Fprintln(w, "----\t----\t-------")

	for _, p := range profiles {
		defaultMark := ""
		if p.IsDefault {
			defaultMark = "*"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", p.Name, len(p.Mods), defaultMark)
	}
	w.Flush()

	return nil
}

func runProfileCreate(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	name := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	profile, err := pm.Create(gameID, name)
	if err != nil {
		return fmt.Errorf("creating profile: %w", err)
	}

	fmt.Printf("✓ Created profile: %s\n", profile.Name)
	return nil
}

func runProfileDelete(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	name := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	if err := pm.Delete(gameID, name); err != nil {
		return fmt.Errorf("deleting profile: %w", err)
	}

	fmt.Printf("✓ Deleted profile: %s\n", name)
	return nil
}

func runProfileSwitch(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	targetName := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	pm := getProfileManager(service)

	// Get target profile
	targetProfile, err := pm.Get(gameID, targetName)
	if err != nil {
		return fmt.Errorf("profile not found: %s", targetName)
	}

	// Get current profile
	currentProfile, err := pm.GetDefault(gameID)
	var currentName string
	if err != nil {
		currentName = "default"
	} else {
		currentName = currentProfile.Name
	}

	if currentName == targetName {
		fmt.Printf("Already on profile: %s\n", targetName)
		return nil
	}

	// Get installed mods for current profile
	currentMods, _ := service.GetInstalledMods(gameID, currentName)

	// Build lookup of current enabled mods
	currentEnabled := make(map[string]*domain.InstalledMod)
	for i := range currentMods {
		if currentMods[i].Enabled {
			key := currentMods[i].SourceID + ":" + currentMods[i].ID
			currentEnabled[key] = &currentMods[i]
		}
	}

	// Build set of target profile mod keys
	targetKeys := make(map[string]domain.ModReference)
	for _, mr := range targetProfile.Mods {
		key := mr.SourceID + ":" + mr.ModID
		targetKeys[key] = mr
	}

	// Get all installed mods (any profile) to check what's available
	allInstalled := make(map[string]*domain.InstalledMod)
	allMods, _ := service.GetInstalledMods(gameID, targetName)
	for i := range allMods {
		key := allMods[i].SourceID + ":" + allMods[i].ID
		allInstalled[key] = &allMods[i]
	}
	// Also add from current profile
	for i := range currentMods {
		key := currentMods[i].SourceID + ":" + currentMods[i].ID
		allInstalled[key] = &currentMods[i]
	}

	// Calculate differences
	var toDisable []*domain.InstalledMod
	var toEnable []*domain.InstalledMod
	var toInstall []domain.ModReference
	needsRedownloadSet := make(map[string]bool) // Track which mods are re-downloads

	// Mods enabled in current profile but not in target - disable
	for key, im := range currentEnabled {
		if _, inTarget := targetKeys[key]; !inTarget {
			toDisable = append(toDisable, im)
		}
	}

	// Mods in target profile
	for key, ref := range targetKeys {
		if im, installed := allInstalled[key]; installed {
			// Already installed - check if cache exists
			if !service.GetGameCache(game).Exists(game.ID, im.SourceID, im.ID, im.Version) {
				// Cache missing - need to re-download, preserve FileIDs from installed mod
				refWithFileIDs := ref
				refWithFileIDs.FileIDs = im.FileIDs
				toInstall = append(toInstall, refWithFileIDs)
				needsRedownloadSet[key] = true
			} else if !im.Enabled {
				toEnable = append(toEnable, im)
			} else if _, wasCurrent := currentEnabled[key]; !wasCurrent {
				// Was installed but not in current profile's enabled set
				toEnable = append(toEnable, im)
			}
		} else {
			// Not installed at all
			toInstall = append(toInstall, ref)
		}
	}

	// Show changes
	fmt.Printf("Switching to profile: %s\n\n", targetName)

	if len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0 {
		// No mod changes, just switch the default
		if err := pm.SetDefault(gameID, targetName); err != nil {
			return fmt.Errorf("setting default profile: %w", err)
		}
		fmt.Printf("✓ Switched to profile: %s\n", targetName)
		return nil
	}

	if len(toDisable) > 0 {
		fmt.Printf("Will disable %d mod(s):\n", len(toDisable))
		for _, im := range toDisable {
			fmt.Printf("  - %s (%s)\n", im.Name, im.ID)
		}
	}

	if len(toEnable) > 0 {
		fmt.Printf("Will enable %d mod(s):\n", len(toEnable))
		for _, im := range toEnable {
			fmt.Printf("  + %s (%s)\n", im.Name, im.ID)
		}
	}

	if len(toInstall) > 0 {
		fmt.Printf("Will install %d mod(s):\n", len(toInstall))
		for _, ref := range toInstall {
			fmt.Printf("  ↓ %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
		}
	}

	// Confirm
	fmt.Print("\nProceed? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if input != "" && input != "y" && input != "yes" {
		fmt.Println("Cancelled.")
		return nil
	}

	ctx := context.Background()
	lnk := service.GetLinker(game.LinkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), lnk)

	// Disable mods
	for _, im := range toDisable {
		if err := installer.Uninstall(ctx, game, &im.Mod); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to undeploy %s: %v\n", im.Name, err)
			}
		}
		if err := service.DB().SetModEnabled(im.SourceID, im.ID, gameID, currentName, false); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to update %s: %v\n", im.Name, err)
			}
		}
		fmt.Printf("  ✓ Disabled: %s\n", im.Name)
	}

	// Enable mods
	for _, im := range toEnable {
		if err := installer.Install(ctx, game, &im.Mod, targetName); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to deploy %s: %v\n", im.Name, err)
			}
			continue
		}
		if err := service.DB().SetModEnabled(im.SourceID, im.ID, gameID, targetName, true); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to update %s: %v\n", im.Name, err)
			}
		}
		fmt.Printf("  ✓ Enabled: %s\n", im.Name)
	}

	// Install missing mods
	if len(toInstall) > 0 {
		fmt.Println("\nInstalling missing mods...")
		for _, ref := range toInstall {
			fmt.Printf("  Installing %s:%s...\n", ref.SourceID, ref.ModID)

			// Fetch mod details
			mod, err := service.GetMod(ctx, ref.SourceID, gameID, ref.ModID)
			if err != nil {
				fmt.Printf("    Error: failed to fetch mod: %v\n", err)
				continue
			}

			// Get files
			files, err := service.GetModFiles(ctx, ref.SourceID, mod)
			if err != nil {
				fmt.Printf("    Error: failed to get files: %v\n", err)
				continue
			}

			if len(files) == 0 {
				fmt.Printf("    Error: no downloadable files\n")
				continue
			}

			// Select files to download - use FileIDs from ref (populated for re-downloads from installed mod, or from profile for new installs)
			filesToDownload, usedFallback := selectFilesToDownload(files, ref.FileIDs)
			if usedFallback && len(ref.FileIDs) > 0 {
				fmt.Printf("    Warning: stored file IDs not found, using primary\n")
			}

			// Download each file
			progressFn := func(p core.DownloadProgress) {
				if p.TotalBytes > 0 {
					fmt.Printf("\r    Downloading: %.1f%%", p.Percentage)
				}
			}

			var downloadedFileIDs []string
			downloadFailed := false
			for _, selectedFile := range filesToDownload {
				_, err = service.DownloadMod(ctx, ref.SourceID, game, mod, selectedFile, progressFn)
				if err != nil {
					fmt.Println()
					fmt.Printf("    Error: download failed: %v\n", err)
					downloadFailed = true
					break
				}
				downloadedFileIDs = append(downloadedFileIDs, selectedFile.ID)
			}
			fmt.Println()

			if downloadFailed {
				continue
			}

			// Deploy
			if err := installer.Install(ctx, game, mod, targetName); err != nil {
				fmt.Printf("    Error: deploy failed: %v\n", err)
				continue
			}

			// Save to DB
			installedMod := &domain.InstalledMod{
				Mod:          *mod,
				ProfileName:  targetName,
				UpdatePolicy: domain.UpdateNotify,
				Enabled:      true,
				FileIDs:      downloadedFileIDs,
			}
			if err := service.DB().SaveInstalledMod(installedMod); err != nil {
				fmt.Printf("    Error: save failed: %v\n", err)
				continue
			}

			// Update profile with actual downloaded FileIDs
			modRef := domain.ModReference{
				SourceID: mod.SourceID,
				ModID:    mod.ID,
				Version:  mod.Version,
				FileIDs:  downloadedFileIDs,
			}
			if err := pm.UpsertMod(gameID, targetName, modRef); err != nil {
				if verbose {
					fmt.Printf("    Warning: could not update profile: %v\n", err)
				}
			}

			fmt.Printf("    ✓ Installed: %s\n", mod.Name)
		}
	}

	// Set new profile as default
	if err := pm.SetDefault(gameID, targetName); err != nil {
		return fmt.Errorf("setting default profile: %w", err)
	}

	fmt.Printf("\n✓ Switched to profile: %s\n", targetName)
	return nil
}

func runProfileExport(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	name := args[0]

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	data, err := pm.Export(gameID, name)
	if err != nil {
		return fmt.Errorf("exporting profile: %w", err)
	}

	fmt.Print(string(data))
	return nil
}

func runProfileImport(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	filePath := args[0]

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
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

	pm := getProfileManager(service)

	// Parse profile first to preview
	profile, err := pm.ParseProfile(data)
	if err != nil {
		return fmt.Errorf("parsing profile: %w", err)
	}

	// Check what mods are already installed (track version and FileIDs for cache check)
	type installedInfo struct {
		Version string
		FileIDs []string
	}
	installedMods, _ := service.GetInstalledMods(gameID, profile.Name)
	installedData := make(map[string]installedInfo) // key -> version and file IDs
	for _, im := range installedMods {
		key := im.SourceID + ":" + im.ID
		installedData[key] = installedInfo{Version: im.Version, FileIDs: im.FileIDs}
	}

	// Also check mods from any profile (might be installed under different profile)
	allProfiles, _ := pm.List(gameID)
	for _, p := range allProfiles {
		mods, _ := service.GetInstalledMods(gameID, p.Name)
		for _, im := range mods {
			key := im.SourceID + ":" + im.ID
			if _, exists := installedData[key]; !exists {
				installedData[key] = installedInfo{Version: im.Version, FileIDs: im.FileIDs}
			}
		}
	}

	// Categorize mods - check both DB and cache
	var installed []domain.ModReference
	var needsRedownload []domain.ModReference
	var missing []domain.ModReference
	needsRedownloadSet := make(map[string]bool) // Track which mods need re-download

	for _, ref := range profile.Mods {
		key := ref.SourceID + ":" + ref.ModID
		if info, inDB := installedData[key]; inDB {
			// In DB - but does cache exist?
			if service.GetGameCache(game).Exists(game.ID, ref.SourceID, ref.ModID, info.Version) {
				installed = append(installed, ref)
			} else {
				needsRedownload = append(needsRedownload, ref)
				needsRedownloadSet[key] = true
			}
		} else {
			missing = append(missing, ref)
		}
	}

	// Show summary
	fmt.Printf("Importing profile: %s\n\n", profile.Name)
	fmt.Printf("Found %d mod(s) in profile.\n", len(profile.Mods))
	if len(installed) > 0 {
		fmt.Printf("  ✓ %d already installed\n", len(installed))
	}
	if len(needsRedownload) > 0 {
		fmt.Printf("  ⚠ %d cache missing, need re-download:\n", len(needsRedownload))
		for _, ref := range needsRedownload {
			fmt.Printf("    - %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
		}
	}
	if len(missing) > 0 {
		fmt.Printf("  ↓ %d need to be downloaded:\n", len(missing))
		for _, ref := range missing {
			fmt.Printf("    - %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
		}
	}

	// Save the profile
	profile, err = pm.ImportWithOptions(data, profileImportForce)
	if err != nil {
		return fmt.Errorf("importing profile: %w", err)
	}

	fmt.Printf("\n✓ Imported profile: %s\n", profile.Name)

	// Combine for download loop
	toDownload := append(needsRedownload, missing...)

	// If nothing to download or --no-install, we're done
	if len(toDownload) == 0 || profileImportNoInstall {
		if len(toDownload) > 0 {
			fmt.Printf("\nSkipped installing %d mod(s). Use 'lmm profile apply %s' to install them later.\n", len(toDownload), profile.Name)
		}
		return nil
	}

	// Ask to install missing mods
	fmt.Print("\nDownload and install mods? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if input != "" && input != "y" && input != "yes" {
		fmt.Printf("Skipped. Use 'lmm profile apply %s' to install them later.\n", profile.Name)
		return nil
	}

	// Download and install mods
	ctx := context.Background()
	lnk := service.GetLinker(game.LinkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), lnk)

	fmt.Println("\nDownloading and installing mods...")
	var installedCount, failedCount int

	for _, ref := range toDownload {
		fmt.Printf("  Installing %s:%s...\n", ref.SourceID, ref.ModID)

		// Fetch mod details
		mod, err := service.GetMod(ctx, ref.SourceID, gameID, ref.ModID)
		if err != nil {
			fmt.Printf("    Error: failed to fetch mod: %v\n", err)
			failedCount++
			continue
		}

		// Get files
		files, err := service.GetModFiles(ctx, ref.SourceID, mod)
		if err != nil {
			fmt.Printf("    Error: failed to get files: %v\n", err)
			failedCount++
			continue
		}

		if len(files) == 0 {
			fmt.Printf("    Error: no downloadable files\n")
			failedCount++
			continue
		}

		// Select files to download - use stored FileIDs for re-downloads, or profile FileIDs for new installs
		key := ref.SourceID + ":" + ref.ModID
		var fileIDsToUse []string
		if needsRedownloadSet[key] {
			// Re-download: use DB-stored FileIDs
			if info, ok := installedData[key]; ok {
				fileIDsToUse = info.FileIDs
			}
		} else if len(ref.FileIDs) > 0 {
			// New install: use FileIDs from imported profile
			fileIDsToUse = ref.FileIDs
		}
		filesToDownload, usedFallback := selectFilesToDownload(files, fileIDsToUse)
		if usedFallback && len(fileIDsToUse) > 0 {
			fmt.Printf("    Warning: stored file IDs not found, using primary\n")
		}

		// Download each file
		progressFn := func(p core.DownloadProgress) {
			if p.TotalBytes > 0 {
				fmt.Printf("\r    Downloading: %.1f%%", p.Percentage)
			}
		}

		var downloadedFileIDs []string
		downloadFailed := false
		for _, selectedFile := range filesToDownload {
			_, err = service.DownloadMod(ctx, ref.SourceID, game, mod, selectedFile, progressFn)
			if err != nil {
				fmt.Println()
				fmt.Printf("    Error: download failed: %v\n", err)
				downloadFailed = true
				break
			}
			downloadedFileIDs = append(downloadedFileIDs, selectedFile.ID)
		}
		fmt.Println()

		if downloadFailed {
			failedCount++
			continue
		}

		// Deploy
		if err := installer.Install(ctx, game, mod, profile.Name); err != nil {
			fmt.Printf("    Error: deploy failed: %v\n", err)
			failedCount++
			continue
		}

		// Save to DB
		installedMod := &domain.InstalledMod{
			Mod:          *mod,
			ProfileName:  profile.Name,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      downloadedFileIDs,
		}
		if err := service.DB().SaveInstalledMod(installedMod); err != nil {
			fmt.Printf("    Error: save failed: %v\n", err)
			failedCount++
			continue
		}

		// Update profile with actual downloaded FileIDs
		modRef := domain.ModReference{
			SourceID: mod.SourceID,
			ModID:    mod.ID,
			Version:  mod.Version,
			FileIDs:  downloadedFileIDs,
		}
		if err := pm.UpsertMod(gameID, profile.Name, modRef); err != nil {
			if verbose {
				fmt.Printf("    Warning: could not update profile: %v\n", err)
			}
		}

		fmt.Printf("    ✓ Installed: %s\n", mod.Name)
		installedCount++
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Installed: %d\n", installedCount)
	if failedCount > 0 {
		fmt.Printf("Failed: %d\n", failedCount)
	}

	return nil
}

func runProfileSync(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	pm := getProfileManager(service)

	// Determine profile name
	var profileName string
	if len(args) > 0 {
		profileName = args[0]
	} else {
		defaultProfile, err := pm.GetDefault(gameID)
		if err != nil {
			profileName = "default"
		} else {
			profileName = defaultProfile.Name
		}
	}

	// Get current profile
	profile, err := pm.Get(gameID, profileName)
	if err != nil {
		if err == domain.ErrProfileNotFound {
			// Create profile if it doesn't exist
			profile, err = pm.Create(gameID, profileName)
			if err != nil {
				return fmt.Errorf("creating profile: %w", err)
			}
		} else {
			return fmt.Errorf("loading profile: %w", err)
		}
	}

	// Get installed mods from database
	installedMods, err := service.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// Build set of installed mod references
	installedRefs := make(map[string]domain.ModReference)
	for _, im := range installedMods {
		if im.Enabled {
			key := im.SourceID + ":" + im.ID
			installedRefs[key] = domain.ModReference{
				SourceID: im.SourceID,
				ModID:    im.ID,
				Version:  im.Version,
				FileIDs:  im.FileIDs,
			}
		}
	}

	// Build set of profile mod references
	profileRefs := make(map[string]domain.ModReference)
	for _, mr := range profile.Mods {
		key := mr.SourceID + ":" + mr.ModID
		profileRefs[key] = mr
	}

	// Calculate differences
	var toAdd []domain.ModReference
	var toRemove []domain.ModReference
	var toUpdate []domain.ModReference // Mods that need FileIDs updated

	// Mods in DB but not in profile
	for key, ref := range installedRefs {
		if profileRef, exists := profileRefs[key]; !exists {
			toAdd = append(toAdd, ref)
		} else if len(ref.FileIDs) > 0 && len(profileRef.FileIDs) == 0 {
			// Mod exists in both but profile is missing FileIDs
			toUpdate = append(toUpdate, ref)
		}
	}

	// Mods in profile but not in DB (or disabled)
	for key, ref := range profileRefs {
		if _, exists := installedRefs[key]; !exists {
			toRemove = append(toRemove, ref)
		}
	}

	// Show changes
	if len(toAdd) == 0 && len(toRemove) == 0 && len(toUpdate) == 0 {
		fmt.Printf("Profile %s is already in sync.\n", profileName)
		return nil
	}

	fmt.Printf("Syncing profile: %s\n\n", profileName)

	if len(toAdd) > 0 {
		fmt.Println("Will add to profile:")
		for _, ref := range toAdd {
			// Try to get mod name from DB
			mod, _ := service.GetInstalledMod(ref.SourceID, ref.ModID, gameID, profileName)
			if mod != nil {
				fmt.Printf("  + %s (%s:%s)\n", mod.Name, ref.SourceID, ref.ModID)
			} else {
				fmt.Printf("  + %s:%s\n", ref.SourceID, ref.ModID)
			}
		}
	}

	if len(toRemove) > 0 {
		fmt.Println("Will remove from profile:")
		for _, ref := range toRemove {
			fmt.Printf("  - %s:%s\n", ref.SourceID, ref.ModID)
		}
	}

	if len(toUpdate) > 0 {
		fmt.Println("Will update FileIDs for:")
		for _, ref := range toUpdate {
			mod, _ := service.GetInstalledMod(ref.SourceID, ref.ModID, gameID, profileName)
			if mod != nil {
				fmt.Printf("  ~ %s (%s:%s)\n", mod.Name, ref.SourceID, ref.ModID)
			} else {
				fmt.Printf("  ~ %s:%s\n", ref.SourceID, ref.ModID)
			}
		}
	}

	// Confirm
	fmt.Print("\nProceed? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	if input != "" && input != "y" && input != "yes" {
		fmt.Println("Cancelled.")
		return nil
	}

	// Apply changes
	for _, ref := range toAdd {
		if err := pm.AddMod(gameID, profileName, ref); err != nil {
			if verbose {
				fmt.Printf("  Warning: %v\n", err)
			}
		}
	}

	for _, ref := range toRemove {
		if err := pm.RemoveMod(gameID, profileName, ref.SourceID, ref.ModID); err != nil {
			if verbose {
				fmt.Printf("  Warning: %v\n", err)
			}
		}
	}

	// Update mods with FileIDs
	for _, ref := range toUpdate {
		if err := pm.UpsertMod(gameID, profileName, ref); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not update %s:%s: %v\n", ref.SourceID, ref.ModID, err)
			}
		}
	}

	fmt.Printf("✓ Synced profile: %s\n", profileName)
	return nil
}

func runProfileApply(cmd *cobra.Command, args []string) error {
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

	pm := getProfileManager(service)

	// Determine profile name
	var profileName string
	if len(args) > 0 {
		profileName = args[0]
	} else {
		defaultProfile, err := pm.GetDefault(gameID)
		if err != nil {
			profileName = "default"
		} else {
			profileName = defaultProfile.Name
		}
	}

	// Get the profile
	profile, err := pm.Get(gameID, profileName)
	if err != nil {
		return fmt.Errorf("profile not found: %s", profileName)
	}

	// Get installed mods from database
	installedMods, err := service.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// Build lookup of installed mods
	installedByKey := make(map[string]*domain.InstalledMod)
	for i := range installedMods {
		key := installedMods[i].SourceID + ":" + installedMods[i].ID
		installedByKey[key] = &installedMods[i]
	}

	// Build set of profile mod keys
	profileKeys := make(map[string]domain.ModReference)
	for _, mr := range profile.Mods {
		key := mr.SourceID + ":" + mr.ModID
		profileKeys[key] = mr
	}

	// Calculate differences
	var toDisable []*domain.InstalledMod
	var toEnable []*domain.InstalledMod
	var toInstall []domain.ModReference
	needsRedownloadSet := make(map[string]bool) // Track which mods are re-downloads

	// Check installed mods against profile
	for key, im := range installedByKey {
		if _, inProfile := profileKeys[key]; !inProfile {
			// Installed but not in profile - disable it
			if im.Enabled {
				toDisable = append(toDisable, im)
			}
		} else {
			// In profile - make sure it's enabled
			if !im.Enabled {
				// Check if cache exists before adding to toEnable
				if service.GetGameCache(game).Exists(game.ID, im.SourceID, im.ID, im.Version) {
					toEnable = append(toEnable, im)
				} else {
					// Cache missing - need to re-download
					toInstall = append(toInstall, domain.ModReference{
						SourceID: im.SourceID,
						ModID:    im.ID,
						Version:  im.Version,
						FileIDs:  im.FileIDs,
					})
					needsRedownloadSet[key] = true
				}
			}
		}
	}

	// Check profile mods against installed
	for key, ref := range profileKeys {
		if _, installed := installedByKey[key]; !installed {
			// In profile but not installed
			toInstall = append(toInstall, ref)
		}
	}

	// Show changes
	if len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0 {
		fmt.Printf("System already matches profile %s.\n", profileName)
		return nil
	}

	fmt.Printf("Applying profile: %s\n\n", profileName)

	if len(toDisable) > 0 {
		fmt.Printf("Will disable %d mod(s):\n", len(toDisable))
		for _, im := range toDisable {
			fmt.Printf("  - %s (%s)\n", im.Name, im.ID)
		}
	}

	if len(toEnable) > 0 {
		fmt.Printf("Will enable %d mod(s):\n", len(toEnable))
		for _, im := range toEnable {
			fmt.Printf("  + %s (%s)\n", im.Name, im.ID)
		}
	}

	if len(toInstall) > 0 {
		fmt.Printf("Will install %d mod(s):\n", len(toInstall))
		for _, ref := range toInstall {
			fmt.Printf("  ↓ %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
		}
	}

	// Confirm unless --yes
	if !profileApplyYes {
		fmt.Print("\nProceed? [Y/n]: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))
		if input != "" && input != "y" && input != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	ctx := context.Background()
	lnk := service.GetLinker(game.LinkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), lnk)

	// Disable mods
	for _, im := range toDisable {
		if err := installer.Uninstall(ctx, game, &im.Mod); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to undeploy %s: %v\n", im.Name, err)
			}
		}
		if err := service.DB().SetModEnabled(im.SourceID, im.ID, gameID, profileName, false); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to update %s: %v\n", im.Name, err)
			}
		}
		fmt.Printf("  ✓ Disabled: %s\n", im.Name)
	}

	// Enable mods
	for _, im := range toEnable {
		if err := installer.Install(ctx, game, &im.Mod, profileName); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to deploy %s: %v\n", im.Name, err)
			}
			continue
		}
		if err := service.DB().SetModEnabled(im.SourceID, im.ID, gameID, profileName, true); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to update %s: %v\n", im.Name, err)
			}
		}
		fmt.Printf("  ✓ Enabled: %s\n", im.Name)
	}

	// Install missing mods
	if len(toInstall) > 0 {
		fmt.Println("\nInstalling missing mods...")
		for _, ref := range toInstall {
			fmt.Printf("  Installing %s:%s...\n", ref.SourceID, ref.ModID)

			// Fetch mod details
			mod, err := service.GetMod(ctx, ref.SourceID, gameID, ref.ModID)
			if err != nil {
				fmt.Printf("    Error: failed to fetch mod: %v\n", err)
				continue
			}

			// Get files
			files, err := service.GetModFiles(ctx, ref.SourceID, mod)
			if err != nil {
				fmt.Printf("    Error: failed to get files: %v\n", err)
				continue
			}

			if len(files) == 0 {
				fmt.Printf("    Error: no downloadable files\n")
				continue
			}

			// Select files to download - use stored FileIDs for re-downloads, or profile FileIDs for new installs
			key := ref.SourceID + ":" + ref.ModID
			var fileIDsToUse []string
			if needsRedownloadSet[key] {
				// Re-download: use DB-stored FileIDs (from ref, which was populated from im.FileIDs)
				fileIDsToUse = ref.FileIDs
			} else if len(ref.FileIDs) > 0 {
				// New install: use FileIDs from profile
				fileIDsToUse = ref.FileIDs
			}
			filesToDownload, usedFallback := selectFilesToDownload(files, fileIDsToUse)
			if usedFallback && len(fileIDsToUse) > 0 {
				fmt.Printf("    Warning: stored file IDs not found, using primary\n")
			}

			// Download each file
			progressFn := func(p core.DownloadProgress) {
				if p.TotalBytes > 0 {
					fmt.Printf("\r    Downloading: %.1f%%", p.Percentage)
				}
			}

			var downloadedFileIDs []string
			downloadFailed := false
			for _, selectedFile := range filesToDownload {
				_, err = service.DownloadMod(ctx, ref.SourceID, game, mod, selectedFile, progressFn)
				if err != nil {
					fmt.Println()
					fmt.Printf("    Error: download failed: %v\n", err)
					downloadFailed = true
					break
				}
				downloadedFileIDs = append(downloadedFileIDs, selectedFile.ID)
			}
			fmt.Println()

			if downloadFailed {
				continue
			}

			// Deploy
			if err := installer.Install(ctx, game, mod, profileName); err != nil {
				fmt.Printf("    Error: deploy failed: %v\n", err)
				continue
			}

			// Save to DB
			installedMod := &domain.InstalledMod{
				Mod:          *mod,
				ProfileName:  profileName,
				UpdatePolicy: domain.UpdateNotify,
				Enabled:      true,
				FileIDs:      downloadedFileIDs,
			}
			if err := service.DB().SaveInstalledMod(installedMod); err != nil {
				fmt.Printf("    Error: save failed: %v\n", err)
				continue
			}

			// Update profile with actual downloaded FileIDs
			modRef := domain.ModReference{
				SourceID: mod.SourceID,
				ModID:    mod.ID,
				Version:  mod.Version,
				FileIDs:  downloadedFileIDs,
			}
			if err := pm.UpsertMod(gameID, profileName, modRef); err != nil {
				if verbose {
					fmt.Printf("    Warning: could not update profile: %v\n", err)
				}
			}

			fmt.Printf("    ✓ Installed: %s\n", mod.Name)
		}
	}

	fmt.Printf("\n✓ Applied profile: %s\n", profileName)
	return nil
}

// selectPrimaryFile returns the primary file from a list of downloadable files,
// or the first file if no primary is marked. Returns nil for empty slice.
func selectPrimaryFile(files []domain.DownloadableFile) *domain.DownloadableFile {
	if len(files) == 0 {
		return nil
	}
	for i := range files {
		if files[i].IsPrimary {
			return &files[i]
		}
	}
	return &files[0]
}

// selectFilesToDownload picks files to download based on stored FileIDs (for re-downloads)
// or primary file (for fresh installs). Returns files to download and whether a fallback was used.
func selectFilesToDownload(files []domain.DownloadableFile, storedFileIDs []string) ([]*domain.DownloadableFile, bool) {
	if len(storedFileIDs) > 0 {
		// Try to use stored file IDs
		found := findFilesByIDs(files, storedFileIDs)
		if len(found) > 0 {
			return found, false
		}
		// Fallback to primary
		return []*domain.DownloadableFile{selectPrimaryFile(files)}, true
	}
	// Fresh install: use primary file
	return []*domain.DownloadableFile{selectPrimaryFile(files)}, false
}
