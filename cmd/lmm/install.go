package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

// installPlan contains the ordered list of mods to install
type installPlan struct {
	mods          []*domain.Mod // In install order (dependencies first, target last)
	missing       []string      // Dependencies that couldn't be fetched (warning only)
	cycleDetected bool          // True if a circular dependency was detected
}

// depFetcher is the interface needed for dependency resolution
type depFetcher interface {
	GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error)
	GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error)
}

// serviceDepFetcher wraps core.Service to implement depFetcher
type serviceDepFetcher struct {
	svc      *core.Service
	sourceID string
}

func (s *serviceDepFetcher) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return s.svc.GetMod(ctx, s.sourceID, gameID, modID)
}

func (s *serviceDepFetcher) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return s.svc.GetDependencies(ctx, s.sourceID, mod)
}

var (
	installSource       string
	installProfile      string
	installVersion      string
	installModID        string
	installFileID       string
	installYes          bool
	installShowArchived bool
	skipVerify          bool
	installForce        bool
	installNoDeps       bool
)

var installCmd = &cobra.Command{
	Use:   "install <query>",
	Short: "Install a mod",
	Long: `Install a mod from the configured source.

The mod will be searched for by name and added to the specified profile
(or default profile if not specified).

Dependencies are automatically resolved and installed. Use --no-deps to skip.

When selecting files, you can choose multiple files (e.g., main + optional patches)
using comma-separated values or ranges: 1,3,5 or 1-3 or 1,3-5

Examples:
  lmm install "ore stack" --game starrupture
  lmm install "skyui" --game skyrim-se --profile survival
  lmm install --id 12345 --game skyrim-se
  lmm install "mod name" -g skyrim-se -y       # Auto-select and auto-confirm
  lmm install "mod name" -g skyrim-se --no-deps  # Skip dependencies`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringVarP(&installSource, "source", "s", "", "mod source (default: first configured source alphabetically)")
	installCmd.Flags().StringVarP(&installProfile, "profile", "p", "", "profile to install to (default: active profile)")
	installCmd.Flags().StringVar(&installVersion, "version", "", "specific version to install (default: latest)")
	installCmd.Flags().StringVar(&installModID, "id", "", "mod ID (skips search)")
	installCmd.Flags().StringVar(&installFileID, "file", "", "file ID(s), comma-separated (skips file selection)")
	installCmd.Flags().BoolVarP(&installYes, "yes", "y", false, "auto-select first/primary option (no prompts)")
	installCmd.Flags().BoolVar(&installShowArchived, "show-archived", false, "show archived/old files")
	installCmd.Flags().BoolVar(&skipVerify, "skip-verify", false, "skip checksum storage and display")
	installCmd.Flags().BoolVarP(&installForce, "force", "f", false, "install without conflict prompts")
	installCmd.Flags().BoolVar(&installNoDeps, "no-deps", false, "skip automatic dependency installation")

	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	// Either query or --id is required
	if len(args) == 0 && installModID == "" {
		return fmt.Errorf("either a search query or --id is required")
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

	// Resolve source: use flag if set, otherwise first configured source
	installSource, err = resolveSource(game, installSource, installYes)
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Get the mod to install
	var mod *domain.Mod
	if installModID != "" {
		// Direct ID - fetch mod directly
		if verbose {
			fmt.Printf("Fetching mod %s from %s...\n", installModID, installSource)
		}
		mod, err = service.GetMod(ctx, installSource, gameID, installModID)
		if err != nil {
			if errors.Is(err, domain.ErrAuthRequired) {
				return fmt.Errorf("authentication required; run 'lmm auth login %s' to authenticate", installSource)
			}
			return fmt.Errorf("failed to fetch mod: %w", err)
		}
	} else {
		// Search for mod
		query := args[0]
		fmt.Printf("Searching for \"%s\"...\n\n", query)

		const displayPageSize = 10

		searchResult, err := service.SearchMods(ctx, installSource, gameID, query, "", nil, 0, displayPageSize)
		if err != nil {
			if errors.Is(err, domain.ErrAuthRequired) {
				return fmt.Errorf("authentication required; run 'lmm auth login %s' to authenticate", installSource)
			}
			return fmt.Errorf("search failed: %w", err)
		}

		if len(searchResult.Mods) == 0 {
			return fmt.Errorf("no mods found matching \"%s\"", query)
		}

		// Get installed mods to mark already-installed ones
		profileName := profileOrDefault(installProfile)
		installedMods, _ := service.GetInstalledMods(gameID, profileName)
		installedIDs := make(map[string]bool)
		for _, im := range installedMods {
			if im.SourceID == installSource {
				installedIDs[im.ID] = true
			}
		}

		// Select the mod(s) with pagination
		var selectedMods []*domain.Mod
		if len(searchResult.Mods) == 1 || installYes {
			selectedMods = []*domain.Mod{&searchResult.Mods[0]}
		} else {
			currentPage := 0
			currentResult := searchResult
			reader := bufio.NewReader(os.Stdin)

			for {
				mods := currentResult.Mods
				for i, m := range mods {
					installedMark := ""
					if installedIDs[m.ID] {
						installedMark = " [installed]"
					}
					fmt.Printf("  [%d] %s v%s by %s (ID: %s)%s\n", i+1, m.Name, m.Version, m.Author, m.ID, installedMark)
				}

				// Pagination options
				hasMore := false
				if currentResult.TotalCount > 0 {
					remaining := currentResult.TotalCount - (currentPage+1)*displayPageSize
					if remaining > 0 {
						hasMore = true
						fmt.Printf("  [n] Next page (%d more)\n", remaining)
					}
				} else if len(mods) == displayPageSize {
					hasMore = true
					fmt.Printf("  [n] Next page\n")
				}
				if currentPage > 0 {
					fmt.Printf("  [p] Previous page\n")
				}
				fmt.Printf("  [q] Cancel\n")

				fmt.Printf("\nSelect mod(s) (e.g., 1 or 1,3,5 or 1-3) [1]: ")
				input, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("reading input: %w", err)
				}
				input = strings.TrimSpace(input)

				if input == "q" || input == "Q" {
					fmt.Println("Search cancelled.")
					return nil
				}
				if (input == "n" || input == "N") && hasMore {
					currentPage++
					currentResult, err = service.SearchMods(ctx, installSource, gameID, query, "", nil, currentPage, displayPageSize)
					if err != nil {
						return fmt.Errorf("search failed: %w", err)
					}
					if len(currentResult.Mods) == 0 {
						fmt.Println("No more results.")
						currentPage--
						currentResult, _ = service.SearchMods(ctx, installSource, gameID, query, "", nil, currentPage, displayPageSize)
					}
					fmt.Println()
					continue
				}
				if (input == "p" || input == "P") && currentPage > 0 {
					currentPage--
					currentResult, err = service.SearchMods(ctx, installSource, gameID, query, "", nil, currentPage, displayPageSize)
					if err != nil {
						return fmt.Errorf("search failed: %w", err)
					}
					fmt.Println()
					continue
				}

				if input == "" {
					input = "1"
				}
				selections, err := parseRangeSelection(input, len(mods))
				if err != nil {
					fmt.Printf("Invalid selection: %v\n", err)
					continue
				}
				for _, sel := range selections {
					selectedMods = append(selectedMods, &currentResult.Mods[sel-1])
				}
				break
			}
		}

		// If multiple mods selected, install each one
		if len(selectedMods) > 1 {
			return installMultipleMods(ctx, service, game, selectedMods, profileName)
		}

		mod = selectedMods[0]
	}

	fmt.Printf("\nSelected: %s v%s by %s\n", mod.Name, mod.Version, mod.Author)

	// Determine profile name early
	profileName := profileOrDefault(installProfile)

	// Resolve dependencies (unless --no-deps or local mod)
	var modsToInstall []*domain.Mod
	if !installNoDeps && mod.SourceID != domain.SourceLocal {
		fmt.Println("\nResolving dependencies...")

		// Get already-installed mods
		installedMods, _ := service.GetInstalledMods(gameID, profileName)
		installedIDs := make(map[string]bool)
		for _, im := range installedMods {
			installedIDs[im.SourceID+":"+im.ID] = true
		}

		// Resolve dependencies
		fetcher := &serviceDepFetcher{svc: service, sourceID: installSource}
		plan, err := resolveDependencies(ctx, fetcher, mod, installedIDs)
		if err != nil {
			return fmt.Errorf("resolving dependencies: %w", err)
		}

		// If there are dependencies to install, show plan and confirm
		if len(plan.mods) > 1 || len(plan.missing) > 0 {
			showInstallPlan(plan, mod.ID)

			if !installYes {
				fmt.Printf("\nInstall %d mod(s)? [Y/n]: ", len(plan.mods))
				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				input = strings.TrimSpace(strings.ToLower(input))
				if input == "n" || input == "no" {
					return fmt.Errorf("installation cancelled")
				}
			}
		}

		modsToInstall = plan.mods
	} else {
		modsToInstall = []*domain.Mod{mod}
	}

	// If multiple mods to install (target + deps), use batch install
	if len(modsToInstall) > 1 {
		return installModsWithDeps(ctx, service, game, modsToInstall, profileName)
	}

	// Single mod install - continue with existing flow

	// Get available files
	files, err := service.GetModFiles(ctx, installSource, mod)
	if err != nil {
		return fmt.Errorf("failed to get mod files: %w", err)
	}

	// Filter and sort files
	files = filterAndSortFiles(files, installShowArchived)

	if len(files) == 0 {
		return fmt.Errorf("no downloadable files available for this mod")
	}

	// Select file(s)
	var selectedFiles []*domain.DownloadableFile
	if installFileID != "" {
		// Direct file ID (can be comma-separated)
		fileIDs := strings.Split(installFileID, ",")
		for _, fid := range fileIDs {
			fid = strings.TrimSpace(fid)
			found := false
			for i := range files {
				if files[i].ID == fid {
					selectedFiles = append(selectedFiles, &files[i])
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("file ID %s not found", fid)
			}
		}
	} else if len(files) == 1 {
		selectedFiles = []*domain.DownloadableFile{&files[0]}
	} else {
		// Find primary file index for default
		defaultChoice := 1
		for i := range files {
			if files[i].IsPrimary {
				defaultChoice = i + 1
				break
			}
		}

		if installYes {
			// Auto-select primary or first file
			selectedFiles = []*domain.DownloadableFile{&files[defaultChoice-1]}
		} else {
			// Show file selection with multi-select support
			fmt.Println("\nAvailable files:")
			for i, f := range files {
				sizeStr := formatSize(f.Size)
				defaultMark := ""
				if f.IsPrimary {
					defaultMark = " <- default"
				}
				fmt.Printf("  [%d] %s (%s, %s)%s\n", i+1, f.FileName, f.Category, sizeStr, defaultMark)
			}

			selections, err := promptMultiSelection("Select file(s) (e.g., 1 or 1,3 or 1-3)", defaultChoice, len(files))
			if err != nil {
				return err
			}
			if selections == nil {
				return nil
			}
			for _, sel := range selections {
				selectedFiles = append(selectedFiles, &files[sel-1])
			}
		}
	}

	// Show selected files
	if len(selectedFiles) == 1 {
		fmt.Printf("\nFile: %s\n", selectedFiles[0].FileName)
	} else {
		fmt.Printf("\nFiles (%d):\n", len(selectedFiles))
		for _, f := range selectedFiles {
			fmt.Printf("  - %s\n", f.FileName)
		}
	}

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

	// Run install.before_all hook (for single mod, this also serves as before_each)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeAll != "" {
		hookCtx.HookName = "install.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeAll, hookCtx); err != nil {
			if !installForce {
				return fmt.Errorf("install.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_all hook failed (forced): %v\n", err)
		}
	}

	// Run install.before_each hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeEach != "" {
		hookCtx.HookName = "install.before_each"
		hookCtx.ModID = mod.ID
		hookCtx.ModName = mod.Name
		hookCtx.ModVersion = mod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeEach, hookCtx); err != nil {
			if !installForce {
				return fmt.Errorf("install.before_each hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_each hook failed (forced): %v\n", err)
		}
	}

	// Set up installer
	linkMethod := service.GetGameLinkMethod(game)
	linker := service.GetLinker(linkMethod)
	installer := core.NewInstaller(service.GetGameCache(game), linker, service.DB())

	// Check if mod is already installed - if so, uninstall old files first
	existingMod, err := service.GetInstalledMod(installSource, mod.ID, gameID, profileName)
	if err == nil && existingMod != nil {
		fmt.Println("\nRemoving previous installation...")
		// Uninstall using the OLD version info to remove correct files
		if err := installer.Uninstall(ctx, game, &existingMod.Mod, profileName); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not remove old files: %v\n", err)
			}
		}
		// Delete old cache for this mod/version to ensure clean slate
		if err := service.GetGameCache(game).Delete(gameID, existingMod.SourceID, existingMod.ID, existingMod.Version); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not clear old cache: %v\n", err)
			}
		}
	}

	// Download each selected file
	var totalFileCount int
	var downloadedFileIDs []string
	fileChecksums := make(map[string]string) // fileID -> checksum

	for i, selectedFile := range selectedFiles {
		if len(selectedFiles) > 1 {
			fmt.Printf("\n[%d/%d] Downloading %s...\n", i+1, len(selectedFiles), selectedFile.FileName)
		} else {
			fmt.Printf("\nDownloading %s...\n", selectedFile.FileName)
		}

		progressFn := func(p core.DownloadProgress) {
			if p.TotalBytes > 0 {
				bar := progressBar(p.Percentage, 30)
				fmt.Printf("\r  [%s] %.1f%% (%s / %s)", bar, p.Percentage,
					formatSize(p.Downloaded), formatSize(p.TotalBytes))
			} else {
				fmt.Printf("\r  Downloaded %s", formatSize(p.Downloaded))
			}
		}

		downloadResult, err := service.DownloadMod(ctx, installSource, game, mod, selectedFile, progressFn)
		if err != nil {
			fmt.Println() // newline after progress
			if strings.Contains(err.Error(), "third-party downloads") && mod.SourceURL != "" {
				fmt.Println()
				fmt.Println("  â  This mod author has disabled API downloads.")
				fmt.Println("  To install manually:")
				fmt.Println()
				fmt.Printf("    1. Download from: %s\n", mod.SourceURL)
				fmt.Printf("    2. Import:        lmm import <downloaded-file> --id %s\n", mod.ID)
				fmt.Println()
				cmd.SilenceUsage = true
				return fmt.Errorf("download unavailable via API")
			}
			return fmt.Errorf("download failed: %w", err)
		}
		fmt.Println() // newline after progress

		// Display checksum (truncated for readability) unless --skip-verify
		if !skipVerify && downloadResult.Checksum != "" {
			displayChecksum := downloadResult.Checksum
			if len(displayChecksum) > 12 {
				displayChecksum = displayChecksum[:12] + "..."
			}
			fmt.Printf("  Checksum: %s\n", displayChecksum)
			fileChecksums[selectedFile.ID] = downloadResult.Checksum
		}

		totalFileCount += downloadResult.FilesExtracted
		downloadedFileIDs = append(downloadedFileIDs, selectedFile.ID)
	}

	fmt.Println("\nExtracting to cache...")

	// Check for conflicts (unless --force)
	if !installForce {
		conflicts, err := installer.GetConflicts(ctx, game, mod, profileName)
		if err != nil {
			if verbose {
				fmt.Printf("Warning: could not check conflicts: %v\n", err)
			}
		} else if len(conflicts) > 0 {
			fmt.Printf("\n⚠ File conflicts detected:\n")

			// Group conflicts by mod
			modConflicts := make(map[string][]string) // "sourceID:modID" -> []paths
			for _, c := range conflicts {
				key := c.CurrentSourceID + ":" + c.CurrentModID
				modConflicts[key] = append(modConflicts[key], c.RelativePath)
			}

			for key, paths := range modConflicts {
				parts := strings.SplitN(key, ":", 2)
				sourceID, modID := parts[0], parts[1]

				// Try to get mod name
				conflictMod, _ := service.GetInstalledMod(sourceID, modID, gameID, profileName)
				modName := modID
				if conflictMod != nil {
					modName = conflictMod.Name
				}

				fmt.Printf("  From %s (%s):\n", modName, modID)
				maxShow := 5
				for i, p := range paths {
					if i >= maxShow {
						fmt.Printf("    ... and %d more\n", len(paths)-maxShow)
						break
					}
					fmt.Printf("    - %s\n", p)
				}
			}

			fmt.Printf("\n%d file(s) will be overwritten. Continue? [y/N]: ", len(conflicts))
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))
			if input != "y" && input != "yes" {
				return fmt.Errorf("installation cancelled")
			}
		}
	}

	// Deploy to game directory
	fmt.Println("Deploying to game directory...")

	if err := installer.Install(ctx, game, mod, profileName); err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	// Save to database
	installedMod := &domain.InstalledMod{
		Mod:          *mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
		FileIDs:      downloadedFileIDs,
	}

	if err := service.DB().SaveInstalledMod(installedMod); err != nil {
		return fmt.Errorf("failed to save mod: %w", err)
	}

	// Store checksums in database
	for fileID, checksum := range fileChecksums {
		if err := service.DB().SaveFileChecksum(
			installSource, mod.ID, game.ID, profileName, fileID, checksum,
		); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save checksum for file %s: %v\n", fileID, err)
		}
	}

	// Add mod to current profile (with FileIDs)
	pm := getProfileManager(service)
	modRef := domain.ModReference{
		SourceID: mod.SourceID,
		ModID:    mod.ID,
		Version:  mod.Version,
		FileIDs:  downloadedFileIDs,
	}

	// Ensure profile exists, create if needed
	if _, err := pm.Get(gameID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(gameID, profileName); err != nil {
				// Log but don't fail - mod is installed
				if verbose {
					fmt.Printf("  Warning: could not create profile: %v\n", err)
				}
			}
		}
	}

	// Add or update mod in profile (handles both new installs and re-installs)
	if err := pm.UpsertMod(gameID, profileName, modRef); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update profile: %v\n", err)
		}
	}

	// Run install.after_each hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterEach != "" {
		hookCtx.HookName = "install.after_each"
		hookCtx.ModID = mod.ID
		hookCtx.ModName = mod.Name
		hookCtx.ModVersion = mod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterEach, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_each hook failed: %w", err))
		}
	}

	// Run install.after_all hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterAll != "" {
		hookCtx.HookName = "install.after_all"
		hookCtx.ModID = ""
		hookCtx.ModName = ""
		hookCtx.ModVersion = ""
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterAll, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_all hook failed: %w", err))
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

	fmt.Printf("\n✓ Installed: %s v%s\n", mod.Name, mod.Version)
	fmt.Printf("  Files deployed: %d\n", totalFileCount)
	fmt.Printf("  Added to profile: %s\n", profileName)

	return nil
}

// promptMultiSelection prompts the user to select one or more numbers
// Accepts formats like: "1", "1,3,5", "1-3", "1..3", "1,3-5"
func promptMultiSelection(prompt string, defaultChoice, max int) ([]int, error) {
	return promptMultiSelectionFrom(os.Stdin, prompt, defaultChoice, max)
}

// promptMultiSelectionFrom is the testable core of promptMultiSelection
func promptMultiSelectionFrom(r io.Reader, prompt string, defaultChoice, max int) ([]int, error) {
	reader := bufio.NewReader(r)

	for {
		fmt.Printf("\n%s (q to cancel) [%d]: ", prompt, defaultChoice)
		input, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading input: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			return []int{defaultChoice}, nil
		}
		if input == "q" || input == "Q" {
			fmt.Println("Selection cancelled.")
			return nil, nil
		}

		selections, err := parseRangeSelection(input, max)
		if err != nil {
			fmt.Printf("Invalid selection: %v\n", err)
			continue
		}

		return selections, nil
	}
}

// formatSize formats bytes to human-readable string
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// progressBar creates a progress bar string
func progressBar(percentage float64, width int) string {
	filled := int(percentage / 100 * float64(width))
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return bar
}

// filterAndSortFiles filters out archived files (unless showArchived is true)
// and sorts files by category: MAIN first, OPTIONAL second, others after
func filterAndSortFiles(files []domain.DownloadableFile, showArchived bool) []domain.DownloadableFile {
	// Filter out archived/old files unless requested
	var filtered []domain.DownloadableFile
	for _, f := range files {
		category := strings.ToUpper(f.Category)
		if !showArchived && (category == "ARCHIVED" || category == "OLD_VERSION" || category == "DELETED") {
			continue
		}
		filtered = append(filtered, f)
	}

	// Sort by category priority: MAIN > OPTIONAL > UPDATE > MISCELLANEOUS > others
	sort.SliceStable(filtered, func(i, j int) bool {
		return fileCategoryPriority(filtered[i].Category) < fileCategoryPriority(filtered[j].Category)
	})

	return filtered
}

// installMultipleMods handles installing multiple mods sequentially
func installMultipleMods(ctx context.Context, service *core.Service, game *domain.Game, mods []*domain.Mod, profileName string) error {
	fmt.Printf("\nInstalling %d mod(s)...\n", len(mods))

	// Get profile manager and ensure profile exists
	pm := getProfileManager(service)
	if _, err := pm.Get(game.ID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(game.ID, profileName); err != nil {
				return fmt.Errorf("could not create profile: %w", err)
			}
		}
	}

	// Get link method for this game
	linkMethod := service.GetGameLinkMethod(game)

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)

	// Run install.before_all hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeAll != "" {
		hookCtx.HookName = "install.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeAll, hookCtx); err != nil {
			if !installForce {
				return fmt.Errorf("install.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_all hook failed (forced): %v\n", err)
		}
	}

	var installed []string
	var failed []string
	var hookErrors []error

	for i, mod := range mods {
		fmt.Printf("\n[%d/%d] Installing: %s v%s\n", i+1, len(mods), mod.Name, mod.Version)

		// Run install.before_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeEach != "" {
			hookCtx.HookName = "install.before_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeEach, hookCtx); err != nil {
				fmt.Printf("  Skipped: install.before_each hook failed: %v\n", err)
				failed = append(failed, mod.Name)
				continue
			}
		}

		// Set up installer
		linker := service.GetLinker(linkMethod)
		installer := core.NewInstaller(service.GetGameCache(game), linker, service.DB())

		// Check if mod is already installed - if so, uninstall old files first
		existingMod, err := service.GetInstalledMod(installSource, mod.ID, game.ID, profileName)
		if err == nil && existingMod != nil {
			fmt.Printf("  Removing previous installation...\n")
			if err := installer.Uninstall(ctx, game, &existingMod.Mod, profileName); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not remove old files: %v\n", err)
				}
			}
			if err := service.GetGameCache(game).Delete(game.ID, existingMod.SourceID, existingMod.ID, existingMod.Version); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not clear old cache: %v\n", err)
				}
			}
		}

		// Get available files
		files, err := service.GetModFiles(ctx, installSource, mod)
		if err != nil {
			fmt.Printf("  Error: failed to get mod files: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Filter and sort files
		files = filterAndSortFiles(files, installShowArchived)

		if len(files) == 0 {
			fmt.Printf("  Error: no downloadable files available\n")
			failed = append(failed, mod.Name)
			continue
		}

		// Auto-select primary or first file for batch install
		var selectedFile *domain.DownloadableFile
		for i := range files {
			if files[i].IsPrimary {
				selectedFile = &files[i]
				break
			}
		}
		if selectedFile == nil {
			selectedFile = &files[0]
		}

		fmt.Printf("  File: %s\n", selectedFile.FileName)

		// Download the mod
		progressFn := func(p core.DownloadProgress) {
			if p.TotalBytes > 0 {
				bar := progressBar(p.Percentage, 20)
				fmt.Printf("\r  [%s] %.1f%%", bar, p.Percentage)
			}
		}

		downloadResult, err := service.DownloadMod(ctx, installSource, game, mod, selectedFile, progressFn)
		if err != nil {
			fmt.Println()
			fmt.Printf("  Error: download failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}
		fmt.Println()

		// Display checksum (truncated for readability) unless --skip-verify
		if !skipVerify && downloadResult.Checksum != "" {
			displayChecksum := downloadResult.Checksum
			if len(displayChecksum) > 12 {
				displayChecksum = displayChecksum[:12] + "..."
			}
			fmt.Printf("  Checksum: %s\n", displayChecksum)
		}

		if err := installer.Install(ctx, game, mod, profileName); err != nil {
			fmt.Printf("  Error: deployment failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Save to database
		installedMod := &domain.InstalledMod{
			Mod:          *mod,
			ProfileName:  profileName,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			Deployed:     true,
			LinkMethod:   linkMethod,
			FileIDs:      []string{selectedFile.ID},
		}

		if err := service.DB().SaveInstalledMod(installedMod); err != nil {
			fmt.Printf("  Error: failed to save mod: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Store checksum in database (unless --skip-verify)
		if !skipVerify && downloadResult.Checksum != "" {
			if err := service.DB().SaveFileChecksum(
				installSource, mod.ID, game.ID, profileName, selectedFile.ID, downloadResult.Checksum,
			); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to save checksum: %v\n", err)
			}
		}

		// Add or update mod in profile (with FileIDs)
		modRef := domain.ModReference{
			SourceID: mod.SourceID,
			ModID:    mod.ID,
			Version:  mod.Version,
			FileIDs:  []string{selectedFile.ID},
		}
		if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not update profile: %v\n", err)
			}
		}

		fmt.Printf("  ✓ Installed (%d files)\n", downloadResult.FilesExtracted)
		installed = append(installed, mod.Name)

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

	// Run install.after_all hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterAll != "" {
		hookCtx.HookName = "install.after_all"
		hookCtx.ModID = ""
		hookCtx.ModName = ""
		hookCtx.ModVersion = ""
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterAll, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_all hook failed: %w", err))
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

	// Summary
	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Installed: %d\n", len(installed))
	if len(failed) > 0 {
		fmt.Printf("Failed: %d (%s)\n", len(failed), strings.Join(failed, ", "))
	}

	return nil
}

// fileCategoryPriority returns sort priority for file categories (lower = first)
func fileCategoryPriority(category string) int {
	switch strings.ToUpper(category) {
	case "MAIN":
		return 0
	case "OPTIONAL":
		return 1
	case "UPDATE":
		return 2
	case "MISCELLANEOUS":
		return 3
	case "ARCHIVED", "OLD_VERSION", "DELETED":
		return 99
	default:
		return 50
	}
}

// parseRangeSelection parses a selection string like "1,3-5,8" or "1..3"
// Returns sorted, unique slice of integers in range [1, max]
func parseRangeSelection(input string, max int) ([]int, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty selection")
	}

	seen := make(map[int]bool)
	var result []int

	// Split by comma
	parts := strings.Split(input, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check for range (either "-" or "..")
		var rangeStart, rangeEnd int
		var err error

		if strings.Contains(part, "..") {
			// Handle ".." range
			rangeParts := strings.Split(part, "..")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			rangeStart, err = strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[0])
			}
			rangeEnd, err = strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[1])
			}
		} else if strings.Contains(part, "-") && !strings.HasPrefix(part, "-") {
			// Handle "-" range (but not negative numbers)
			rangeParts := strings.SplitN(part, "-", 2)
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			rangeStart, err = strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[0])
			}
			rangeEnd, err = strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[1])
			}
		} else {
			// Single number
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", part)
			}
			rangeStart = n
			rangeEnd = n
		}

		// Validate range
		if rangeStart > rangeEnd {
			return nil, fmt.Errorf("invalid range: start %d > end %d", rangeStart, rangeEnd)
		}
		if rangeStart < 1 || rangeEnd > max {
			return nil, fmt.Errorf("selection out of range (1-%d): %s", max, part)
		}

		// Add to result
		for i := rangeStart; i <= rangeEnd; i++ {
			if !seen[i] {
				seen[i] = true
				result = append(result, i)
			}
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid selections")
	}

	// Sort for consistent output
	sort.Ints(result)
	return result, nil
}

// resolveDependencies fetches all dependencies for a mod and returns them in install order.
// Dependencies are fetched recursively. Already-installed mods are skipped.
// Missing dependencies (not found on source) are recorded but don't cause failure.
// Circular dependencies are detected and recorded for warning.
func resolveDependencies(ctx context.Context, fetcher depFetcher, target *domain.Mod, installedIDs map[string]bool) (*installPlan, error) {
	plan := &installPlan{}
	visited := make(map[string]bool)
	stack := make(map[string]bool) // Keys currently being visited (for cycle detection)

	var collect func(mod *domain.Mod) error
	collect = func(mod *domain.Mod) error {
		key := mod.SourceID + ":" + mod.ID
		if visited[key] {
			return nil
		}
		visited[key] = true
		stack[key] = true
		defer func() { delete(stack, key) }()

		// Fetch dependencies for this mod
		deps, err := fetcher.GetDependencies(ctx, mod)
		if err != nil {
			return nil
		}

		for _, ref := range deps {
			depKey := ref.SourceID + ":" + ref.ModID

			if installedIDs[depKey] {
				continue
			}

			if stack[depKey] {
				plan.cycleDetected = true
				continue
			}
			if visited[depKey] {
				continue
			}

			// Fetch the dependency mod (use target game domain so fetch is correct)
			gameIDForFetch := target.GameID
			if gameIDForFetch == "" {
				gameIDForFetch = mod.GameID
			}
			depMod, err := fetcher.GetMod(ctx, gameIDForFetch, ref.ModID)
			if err != nil {
				// Dependency not available (external like SKSE)
				plan.missing = append(plan.missing, depKey)
				continue
			}
			// Keep actual source from fetch; validate against ref
			if depMod.SourceID != "" && depMod.SourceID != ref.SourceID {
				// Mismatch: dependency listed for different source
				plan.missing = append(plan.missing, depKey)
				continue
			}
			if depMod.SourceID == "" {
				depMod.SourceID = ref.SourceID
			}

			// Recursively collect transitive dependencies
			if err := collect(depMod); err != nil {
				return err
			}

			// Add dependency after its dependencies (topological order)
			plan.mods = append(plan.mods, depMod)
		}

		return nil
	}

	// Collect all dependencies
	if err := collect(target); err != nil {
		return nil, err
	}

	// Add target mod last
	plan.mods = append(plan.mods, target)

	return plan, nil
}

// showInstallPlan displays the install plan (dependency tree order) to the user
func showInstallPlan(plan *installPlan, targetModID string) {
	fmt.Printf("\nDependency tree (install order):\n")
	for i, mod := range plan.mods {
		label := "[dependency]"
		if mod.ID == targetModID {
			label = "[target]"
		}
		fmt.Printf("  %d. %s v%s (ID: %s) %s\n", i+1, mod.Name, mod.Version, mod.ID, label)
	}

	if plan.cycleDetected {
		fmt.Fprintf(os.Stderr, "\n⚠ Warning: Circular dependency detected among dependencies; install order is best-effort.\n")
	}

	if len(plan.missing) > 0 {
		fmt.Printf("\n⚠ Warning: %d dependency(ies) not available on source:\n", len(plan.missing))
		for _, m := range plan.missing {
			fmt.Printf("  - %s (may require manual install)\n", m)
		}
	}
}

// installModsWithDeps installs multiple mods in order (dependencies first)
func installModsWithDeps(ctx context.Context, service *core.Service, game *domain.Game, mods []*domain.Mod, profileName string) error {
	fmt.Printf("\nInstalling %d mod(s)...\n", len(mods))

	// Get profile manager and ensure profile exists
	pm := getProfileManager(service)
	if _, err := pm.Get(game.ID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(game.ID, profileName); err != nil {
				return fmt.Errorf("could not create profile: %w", err)
			}
		}
	}

	// Get link method for this game
	linkMethod := service.GetGameLinkMethod(game)

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)

	// Run install.before_all hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeAll != "" {
		hookCtx.HookName = "install.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeAll, hookCtx); err != nil {
			if !installForce {
				return fmt.Errorf("install.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_all hook failed (forced): %v\n", err)
		}
	}

	var installed []string
	var failed []string
	var hookErrors []error

	for i, mod := range mods {
		fmt.Printf("\n[%d/%d] Installing: %s v%s\n", i+1, len(mods), mod.Name, mod.Version)

		// Run install.before_each hook
		if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeEach != "" {
			hookCtx.HookName = "install.before_each"
			hookCtx.ModID = mod.ID
			hookCtx.ModName = mod.Name
			hookCtx.ModVersion = mod.Version
			if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeEach, hookCtx); err != nil {
				fmt.Printf("  Skipped: install.before_each hook failed: %v\n", err)
				failed = append(failed, mod.Name)
				continue
			}
		}

		// Set up installer
		linker := service.GetLinker(linkMethod)
		installer := core.NewInstaller(service.GetGameCache(game), linker, service.DB())

		// Check if mod is already installed - if so, uninstall old files first
		existingMod, err := service.GetInstalledMod(mod.SourceID, mod.ID, game.ID, profileName)
		if err == nil && existingMod != nil {
			fmt.Printf("  Removing previous installation...\n")
			if err := installer.Uninstall(ctx, game, &existingMod.Mod, profileName); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not remove old files: %v\n", err)
				}
			}
			if err := service.GetGameCache(game).Delete(game.ID, existingMod.SourceID, existingMod.ID, existingMod.Version); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not clear old cache: %v\n", err)
				}
			}
		}

		// Get available files
		files, err := service.GetModFiles(ctx, mod.SourceID, mod)
		if err != nil {
			fmt.Printf("  Error: failed to get mod files: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Filter and sort files
		files = filterAndSortFiles(files, installShowArchived)

		if len(files) == 0 {
			fmt.Printf("  Error: no downloadable files available\n")
			failed = append(failed, mod.Name)
			continue
		}

		// Auto-select primary or first file
		var selectedFile *domain.DownloadableFile
		for j := range files {
			if files[j].IsPrimary {
				selectedFile = &files[j]
				break
			}
		}
		if selectedFile == nil {
			selectedFile = &files[0]
		}

		fmt.Printf("  File: %s\n", selectedFile.FileName)

		// Download the mod
		progressFn := func(p core.DownloadProgress) {
			if p.TotalBytes > 0 {
				bar := progressBar(p.Percentage, 20)
				fmt.Printf("\r  [%s] %.1f%%", bar, p.Percentage)
			}
		}

		downloadResult, err := service.DownloadMod(ctx, mod.SourceID, game, mod, selectedFile, progressFn)
		if err != nil {
			fmt.Println()
			fmt.Printf("  Error: download failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}
		fmt.Println()

		// Display checksum unless --skip-verify
		if !skipVerify && downloadResult.Checksum != "" {
			displayChecksum := downloadResult.Checksum
			if len(displayChecksum) > 12 {
				displayChecksum = displayChecksum[:12] + "..."
			}
			fmt.Printf("  Checksum: %s\n", displayChecksum)
		}

		// Check for conflicts (unless --force)
		if !installForce {
			conflicts, err := installer.GetConflicts(ctx, game, mod, profileName)
			if err == nil && len(conflicts) > 0 {
				fmt.Printf("  ⚠ %d file conflict(s) - will overwrite\n", len(conflicts))
			}
		}

		if err := installer.Install(ctx, game, mod, profileName); err != nil {
			fmt.Printf("  Error: deployment failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Save to database
		installedMod := &domain.InstalledMod{
			Mod:          *mod,
			ProfileName:  profileName,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			Deployed:     true,
			LinkMethod:   linkMethod,
			FileIDs:      []string{selectedFile.ID},
		}

		if err := service.DB().SaveInstalledMod(installedMod); err != nil {
			fmt.Printf("  Error: failed to save mod: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Store checksum in database
		if !skipVerify && downloadResult.Checksum != "" {
			if err := service.DB().SaveFileChecksum(
				mod.SourceID, mod.ID, game.ID, profileName, selectedFile.ID, downloadResult.Checksum,
			); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to save checksum: %v\n", err)
			}
		}

		// Add to profile
		modRef := domain.ModReference{
			SourceID: mod.SourceID,
			ModID:    mod.ID,
			Version:  mod.Version,
			FileIDs:  []string{selectedFile.ID},
		}
		if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not update profile: %v\n", err)
			}
		}

		fmt.Printf("  ✓ Installed (%d files)\n", downloadResult.FilesExtracted)
		installed = append(installed, mod.Name)

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

	// Run install.after_all hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterAll != "" {
		hookCtx.HookName = "install.after_all"
		hookCtx.ModID = ""
		hookCtx.ModName = ""
		hookCtx.ModVersion = ""
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.AfterAll, hookCtx); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_all hook failed: %w", err))
		}
	}

	// Print hook warnings
	printHookWarnings(hookErrors)

	// Summary
	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Installed: %d\n", len(installed))
	if len(failed) > 0 {
		fmt.Printf("Failed: %d (%s)\n", len(failed), strings.Join(failed, ", "))
	}

	return nil
}
