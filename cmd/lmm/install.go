package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

	"github.com/spf13/cobra"
)

// downloadSelectedFiles fetches each selected mod file into downloadCache,
// printing progress and returning per-file metadata for the caller to record.
// sourceID is passed in (rather than read from the installSource global) so
// the helper is reusable and testable in isolation.
func downloadSelectedFiles(ctx context.Context, service *core.Service, downloadCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, selectedFiles []*domain.DownloadableFile) (totalFileCount int, downloadedFileIDs []string, fileChecksums map[string]string, err error) {
	fileChecksums = make(map[string]string)

	for i, selectedFile := range selectedFiles {
		if len(selectedFiles) > 1 {
			fmt.Printf("\n[%d/%d] Downloading %s...\n", i+1, len(selectedFiles), displayFileLabel(*selectedFile))
		} else {
			fmt.Printf("\nDownloading %s...\n", displayFileLabel(*selectedFile))
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

		downloadResult, dlErr := service.DownloadModToCache(ctx, downloadCache, sourceID, game, mod, selectedFile, progressFn)
		if dlErr != nil {
			fmt.Println()
			if strings.Contains(dlErr.Error(), "third-party downloads") && mod.SourceURL != "" {
				fmt.Println()
				fmt.Println("  ⚠  This mod author has disabled API downloads.")
				fmt.Println("  To install manually:")
				fmt.Println()
				fmt.Printf("    1. Download from: %s\n", mod.SourceURL)
				fmt.Printf("    2. Import:        lmm import <downloaded-file> --id %s\n", mod.ID)
				fmt.Println()
				return 0, nil, nil, fmt.Errorf("download unavailable via API")
			}
			return 0, nil, nil, fmt.Errorf("download failed: %w", dlErr)
		}
		fmt.Println()

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
	return totalFileCount, downloadedFileIDs, fileChecksums, nil
}

// confirmInstallConflicts inspects what files the about-to-install mod would
// overwrite. If conflicts exist it prints them and prompts for confirmation,
// returning an error to abort the install when the user declines.
func confirmInstallConflicts(ctx context.Context, service *core.Service, installer *core.Installer, game *domain.Game, mod *domain.Mod, profileName string) error {
	conflicts, err := installer.GetConflicts(ctx, game, mod, profileName)
	if err != nil {
		if verbose {
			fmt.Printf("Warning: could not check conflicts: %v\n", err)
		}
		return nil
	}
	if len(conflicts) == 0 {
		return nil
	}

	fmt.Printf("\n⚠ File conflicts detected:\n")

	modConflicts := make(map[string][]string)
	for _, c := range conflicts {
		key := domain.ModKey(c.CurrentSourceID, c.CurrentModID)
		modConflicts[key] = append(modConflicts[key], c.RelativePath)
	}

	for key, paths := range modConflicts {
		parts := strings.SplitN(key, ":", 2)
		sourceID, modID := parts[0], parts[1]

		conflictMod, _ := service.GetInstalledMod(sourceID, modID, game.ID, profileName)
		modName := modID
		if conflictMod != nil {
			modName = conflictMod.Name
		}

		fmt.Printf("  From %s (%s):\n", modName, modID)
		const maxShow = 5
		for i, p := range paths {
			if i >= maxShow {
				fmt.Printf("    ... and %d more\n", len(paths)-maxShow)
				break
			}
			fmt.Printf("    - %s\n", p)
		}
	}

	fmt.Printf("\n%d file(s) will be overwritten. Continue? [y/N]: ", len(conflicts))
	input, err := readPromptLine()
	if err != nil {
		return err
	}
	if input != "y" && input != "yes" {
		return fmt.Errorf("installation cancelled")
	}
	return nil
}

// selectInstallFiles applies the --file flag, single-file shortcut, --yes default,
// or interactive prompt to choose which downloadable files to install.
func selectInstallFiles(files []domain.DownloadableFile) ([]*domain.DownloadableFile, error) {
	// Direct file ID(s) via --file flag
	if installFileID != "" {
		var selected []*domain.DownloadableFile
		for _, fid := range strings.Split(installFileID, ",") {
			fid = strings.TrimSpace(fid)
			found := false
			for i := range files {
				if files[i].ID == fid {
					selected = append(selected, &files[i])
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("file ID %s not found", fid)
			}
		}
		return selected, nil
	}

	if len(files) == 1 {
		return []*domain.DownloadableFile{&files[0]}, nil
	}

	// Find primary file index for default
	defaultChoice := 1
	for i := range files {
		if files[i].IsPrimary {
			defaultChoice = i + 1
			break
		}
	}

	if installYes {
		return []*domain.DownloadableFile{&files[defaultChoice-1]}, nil
	}

	fmt.Println("\nAvailable files:")
	for i, f := range files {
		sizeStr := formatSize(f.Size)
		defaultMark := ""
		if f.IsPrimary {
			defaultMark = " <- default"
		}
		fmt.Printf("  [%d] %s (%s, %s)%s\n", i+1, displayFileLabel(f), f.Category, sizeStr, defaultMark)
	}

	selections, err := promptMultiSelection("Select file(s) (e.g., 1 or 1,3 or 1-3)", defaultChoice, len(files))
	if err != nil {
		return nil, err
	}
	selected := make([]*domain.DownloadableFile, 0, len(selections))
	for _, sel := range selections {
		selected = append(selected, &files[sel-1])
	}
	return selected, nil
}

// searchAndSelectMods runs an interactive paginated search for query and returns
// the user's selection. If only one match exists or installYes is set, it auto-
// selects without prompting. Returns ErrCancelled if the user types 'q'.
func searchAndSelectMods(ctx context.Context, service *core.Service, gameID, source, query, profileName string) ([]*domain.Mod, error) {
	const displayPageSize = 10

	fmt.Printf("Searching for \"%s\"...\n\n", query)

	searchResult, err := service.SearchMods(ctx, source, gameID, query, "", nil, 0, displayPageSize)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return nil, authPromptError(source)
		}
		return nil, fmt.Errorf("search failed: %w", err)
	}
	if len(searchResult.Mods) == 0 {
		return nil, fmt.Errorf("no mods found matching \"%s\"", query)
	}

	// Mark already-installed mods in the listing
	installedMods, _ := service.GetInstalledMods(gameID, profileName)
	installedIDs := make(map[string]bool)
	for _, im := range installedMods {
		if im.SourceID == source {
			installedIDs[im.ID] = true
		}
	}

	// Trivial selections
	if len(searchResult.Mods) == 1 || installYes {
		return []*domain.Mod{&searchResult.Mods[0]}, nil
	}

	// Interactive paginated selection
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
			return nil, fmt.Errorf("reading input: %w", err)
		}
		input = strings.TrimSpace(input)

		if input == "q" || input == "Q" {
			return nil, ErrCancelled
		}
		if (input == "n" || input == "N") && hasMore {
			currentPage++
			currentResult, err = service.SearchMods(ctx, source, gameID, query, "", nil, currentPage, displayPageSize)
			if err != nil {
				return nil, fmt.Errorf("search failed: %w", err)
			}
			if len(currentResult.Mods) == 0 {
				fmt.Println("No more results.")
				currentPage--
				currentResult, err = service.SearchMods(ctx, source, gameID, query, "", nil, currentPage, displayPageSize)
				if err != nil {
					return nil, fmt.Errorf("search failed: %w", err)
				}
			}
			fmt.Println()
			continue
		}
		if (input == "p" || input == "P") && currentPage > 0 {
			currentPage--
			currentResult, err = service.SearchMods(ctx, source, gameID, query, "", nil, currentPage, displayPageSize)
			if err != nil {
				return nil, fmt.Errorf("search failed: %w", err)
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
		var selectedMods []*domain.Mod
		for _, sel := range selections {
			selectedMods = append(selectedMods, &currentResult.Mods[sel-1])
		}
		return selectedMods, nil
	}
}

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

func displayFileLabel(file domain.DownloadableFile) string {
	name := strings.TrimSpace(file.Name)
	fileName := strings.TrimSpace(file.FileName)

	if fileName == "" {
		return name
	}
	if name == "" {
		return fileName
	}
	if strings.ContainsAny(fileName, `/\`) {
		return name
	}
	if looksOpaqueFileName(fileName) {
		return name
	}
	return fileName
}

func looksOpaqueFileName(fileName string) bool {
	if filepath.Ext(fileName) != "" {
		return false
	}
	if strings.Count(fileName, "-") < 4 {
		return false
	}

	compact := strings.ReplaceAll(fileName, "-", "")
	if len(compact) < 24 {
		return false
	}

	for _, r := range compact {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}

	return true
}

func runInstall(cmd *cobra.Command, args []string) error {
	// Either query or --id is required
	if len(args) == 0 && installModID == "" {
		return fmt.Errorf("either a search query or --id is required")
	}
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doInstall(ctx, service, game, args)
	})
}

func doInstall(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	// Resolve source: use flag if set, otherwise first configured source
	var err error
	installSource, err = resolveSource(game, installSource, installYes)
	if err != nil {
		return err
	}

	profileName := profileOrDefault(installProfile)

	// Get the mod to install (by --id or interactive search)
	var mod *domain.Mod
	if installModID != "" {
		if verbose {
			fmt.Printf("Fetching mod %s from %s...\n", installModID, installSource)
		}
		mod, err = service.GetMod(ctx, installSource, game.ID, installModID)
		if err != nil {
			if errors.Is(err, domain.ErrAuthRequired) {
				return authPromptError(installSource)
			}
			return fmt.Errorf("failed to fetch mod: %w", err)
		}
	} else {
		selectedMods, err := searchAndSelectMods(ctx, service, game.ID, installSource, args[0], profileName)
		if err != nil {
			return err
		}
		if len(selectedMods) > 1 {
			return installMultipleMods(ctx, service, game, selectedMods, profileName)
		}
		mod = selectedMods[0]
	}

	fmt.Printf("\nSelected: %s v%s by %s\n", mod.Name, mod.Version, mod.Author)

	// Resolve dependencies (unless --no-deps or local mod)
	var modsToInstall []*domain.Mod
	if !installNoDeps && mod.SourceID != domain.SourceLocal {
		fmt.Println("\nResolving dependencies...")

		// Get already-installed mods
		installedMods, _ := service.GetInstalledMods(game.ID, profileName)
		installedIDs := make(map[string]bool)
		for _, im := range installedMods {
			installedIDs[domain.ModKey(im.SourceID, im.ID)] = true
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
				input, err := readPromptLine()
				if err != nil {
					return err
				}
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

	files = filterAndSortFiles(files, installShowArchived)
	if len(files) == 0 {
		return fmt.Errorf("no downloadable files available for this mod")
	}

	selectedFiles, err := selectInstallFiles(files)
	if err != nil {
		return err
	}

	// Show selected files
	if len(selectedFiles) == 1 {
		fmt.Printf("\nFile: %s\n", displayFileLabel(*selectedFiles[0]))
	} else {
		fmt.Printf("\nFiles (%d):\n", len(selectedFiles))
		for _, f := range selectedFiles {
			fmt.Printf("  - %s\n", displayFileLabel(*f))
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
	installer := service.GetInstaller(game)
	var reinstallTxn *reinstallCacheTransaction
	downloadCache := service.GetGameCache(game)

	// Check if mod is already installed so we can replace it atomically later.
	existingMod, err := service.GetInstalledMod(installSource, mod.ID, game.ID, profileName)
	if err != nil {
		if !errors.Is(err, domain.ErrModNotFound) {
			return fmt.Errorf("checking existing installed mod: %w", err)
		}
		existingMod = nil
	} else if existingMod.Version == mod.Version {
		reinstallTxn, err = prepareReinstallCacheTransaction(service.GetGameCache(game), game.ID, existingMod.SourceID, existingMod.ID, existingMod.Version)
		if err != nil {
			return fmt.Errorf("preparing reinstall cache: %w", err)
		}
		downloadCache = reinstallTxn.staged
		defer func() {
			if reinstallTxn != nil {
				_ = reinstallTxn.Rollback()
			}
		}()
	}

	totalFileCount, downloadedFileIDs, fileChecksums, err := downloadSelectedFiles(ctx, service, downloadCache, installSource, game, mod, selectedFiles)
	if err != nil {
		return err
	}

	fmt.Println("\nExtracting to cache...")

	if !installForce {
		if err := confirmInstallConflicts(ctx, service, installer, game, mod, profileName); err != nil {
			return err
		}
	}

	// Deploy to game directory
	fmt.Println("Deploying to game directory...")

	if existingMod != nil {
		if reinstallTxn != nil {
			if err := reinstallTxn.Activate(); err != nil {
				return fmt.Errorf("activating reinstall cache: %w", err)
			}
		}
		var replaceErr error
		if reinstallTxn != nil {
			replaceErr = installer.ReplaceWithOldCache(ctx, game, reinstallTxn.snapshot, &existingMod.Mod, mod, profileName)
		} else {
			replaceErr = installer.Replace(ctx, game, &existingMod.Mod, mod, profileName)
		}
		if replaceErr != nil {
			if reinstallTxn != nil {
				_ = reinstallTxn.RestoreLive()
				_ = installer.ReplaceWithCaches(ctx, game, reinstallTxn.snapshot, service.GetGameCache(game), &existingMod.Mod, &existingMod.Mod, profileName)
			}
			return fmt.Errorf("deployment failed: %w", replaceErr)
		}
	} else if err := installer.Install(ctx, game, mod, profileName); err != nil {
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

	if err := service.SaveInstalledMod(installedMod); err != nil {
		if existingMod != nil {
			if reinstallTxn != nil {
				_ = reinstallTxn.RestoreLive()
				_ = installer.ReplaceWithCaches(ctx, game, reinstallTxn.staged, service.GetGameCache(game), mod, &existingMod.Mod, profileName)
			} else {
				_ = installer.Replace(ctx, game, mod, &existingMod.Mod, profileName)
			}
		} else {
			_ = installer.Uninstall(ctx, game, mod, profileName)
		}
		return fmt.Errorf("failed to save mod: %w", err)
	}
	if reinstallTxn != nil {
		if err := reinstallTxn.Commit(); err != nil && verbose {
			fmt.Printf("  Warning: could not finalize reinstall cache transaction: %v\n", err)
		}
		reinstallTxn = nil
	}

	// Store checksums in database
	for fileID, checksum := range fileChecksums {
		if err := service.SaveFileChecksum(
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
	if _, err := pm.Get(game.ID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(game.ID, profileName); err != nil {
				// Log but don't fail - mod is installed
				if verbose {
					fmt.Printf("  Warning: could not create profile: %v\n", err)
				}
			}
		}
	}

	// Add or update mod in profile (handles both new installs and re-installs)
	if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update profile: %v\n", err)
		}
	}

	if existingMod != nil && existingMod.Version != mod.Version {
		if err := service.GetGameCache(game).Delete(game.ID, existingMod.SourceID, existingMod.ID, existingMod.Version); err != nil && verbose {
			fmt.Printf("  Warning: could not clear old cache: %v\n", err)
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

type reinstallCacheTransaction struct {
	live      *cache.Cache
	snapshot  *cache.Cache
	staged    *cache.Cache
	tempDir   string
	gameID    string
	sourceID  string
	modID     string
	version   string
	activated bool
}

func prepareReinstallCacheTransaction(live *cache.Cache, gameID, sourceID, modID, version string) (*reinstallCacheTransaction, error) {
	tempDir, err := os.MkdirTemp("", "lmm-reinstall-cache-*")
	if err != nil {
		return nil, fmt.Errorf("creating cache snapshot: %w", err)
	}
	snapshot := cache.New(filepath.Join(tempDir, "snapshot"))
	staged := cache.New(filepath.Join(tempDir, "staged"))
	if err := live.CloneMod(snapshot, gameID, sourceID, modID, version); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("snapshotting existing cache: %w", err)
	}
	return &reinstallCacheTransaction{
		live:     live,
		snapshot: snapshot,
		staged:   staged,
		tempDir:  tempDir,
		gameID:   gameID,
		sourceID: sourceID,
		modID:    modID,
		version:  version,
	}, nil
}

func (s *reinstallCacheTransaction) Activate() error {
	if s == nil {
		return nil
	}
	if s.activated {
		return nil
	}
	if err := s.live.Delete(s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	if err := s.staged.CloneMod(s.live, s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	s.activated = true
	return nil
}

func (s *reinstallCacheTransaction) RestoreLive() error {
	if s == nil || !s.activated {
		return nil
	}
	if err := s.live.Delete(s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	if err := s.snapshot.CloneMod(s.live, s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	s.activated = false
	return nil
}

func (s *reinstallCacheTransaction) Rollback() error {
	if s == nil {
		return nil
	}
	if err := s.RestoreLive(); err != nil {
		return err
	}
	err := os.RemoveAll(s.tempDir)
	*s = reinstallCacheTransaction{}
	return err
}

func (s *reinstallCacheTransaction) Commit() error {
	if s == nil {
		return nil
	}
	err := os.RemoveAll(s.tempDir)
	*s = reinstallCacheTransaction{}
	return err
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
			return nil, ErrCancelled
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

// installMultipleMods handles installing multiple mods sequentially.
// Delegates to batchInstallMods.
func installMultipleMods(ctx context.Context, service *core.Service, game *domain.Game, mods []*domain.Mod, profileName string) error {
	return batchInstallMods(ctx, service, game, mods, profileName)
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
		key := domain.ModKey(mod.SourceID, mod.ID)
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
			depKey := domain.ModKey(ref.SourceID, ref.ModID)

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

// installModsWithDeps installs multiple mods in order (dependencies first).
// Delegates to batchInstallMods.
func installModsWithDeps(ctx context.Context, service *core.Service, game *domain.Game, mods []*domain.Mod, profileName string) error {
	return batchInstallMods(ctx, service, game, mods, profileName)
}

// truncateChecksum returns a display-friendly checksum (first 12 chars + "...").
func truncateChecksum(checksum string) string {
	if len(checksum) > 12 {
		return checksum[:12] + "..."
	}
	return checksum
}

// runInstallHook runs a named hook if configured. Returns an error if the hook fails.
func runInstallHook(ctx context.Context, runner *core.HookRunner, hooks *core.ResolvedHooks, hookCtx *core.HookContext, hookName, command string) error {
	if runner == nil || hooks == nil || command == "" {
		return nil
	}
	hookCtx.HookName = hookName
	_, err := runner.Run(ctx, command, *hookCtx)
	return err
}

// batchInstallMods is the shared implementation for installing multiple mods sequentially.
// Used by installMultipleMods (multi-select from search) and installModsWithDeps (dependency-resolved).
// Each mod's SourceID is used for API calls (set during search/dep resolution).
func batchInstallMods(ctx context.Context, service *core.Service, game *domain.Game, mods []*domain.Mod, profileName string) error {
	fmt.Printf("\nInstalling %d mod(s)...\n", len(mods))

	// Ensure profile exists
	pm := getProfileManager(service)
	if _, err := pm.Get(game.ID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(game.ID, profileName); err != nil {
				return fmt.Errorf("could not create profile: %w", err)
			}
		}
	}

	linkMethod := service.GetGameLinkMethod(game)

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)

	// Run install.before_all hook
	if err := runInstallHook(ctx, hookRunner, resolvedHooks, &hookCtx, "install.before_all", resolvedHooks.GetInstallBeforeAll()); err != nil {
		if !installForce {
			return fmt.Errorf("install.before_all hook failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Warning: install.before_all hook failed (forced): %v\n", err)
	}

	var installed, failed []string
	var hookErrors []error

	for i, mod := range mods {
		fmt.Printf("\n[%d/%d] Installing: %s v%s\n", i+1, len(mods), mod.Name, mod.Version)

		sourceID := mod.SourceID

		// Run install.before_each hook
		hookCtx.ModID = mod.ID
		hookCtx.ModName = mod.Name
		hookCtx.ModVersion = mod.Version
		if err := runInstallHook(ctx, hookRunner, resolvedHooks, &hookCtx, "install.before_each", resolvedHooks.GetInstallBeforeEach()); err != nil {
			fmt.Printf("  Skipped: install.before_each hook failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		installer := service.GetInstaller(game)

		// Remove previous installation if re-installing
		if existingMod, err := service.GetInstalledMod(sourceID, mod.ID, game.ID, profileName); err == nil && existingMod != nil {
			fmt.Printf("  Removing previous installation...\n")
			if err := installer.Uninstall(ctx, game, &existingMod.Mod, profileName); err != nil && verbose {
				fmt.Printf("  Warning: could not remove old files: %v\n", err)
			}
			if err := service.GetGameCache(game).Delete(game.ID, existingMod.SourceID, existingMod.ID, existingMod.Version); err != nil && verbose {
				fmt.Printf("  Warning: could not clear old cache: %v\n", err)
			}
		}

		// Get and filter available files
		files, err := service.GetModFiles(ctx, sourceID, mod)
		if err != nil {
			fmt.Printf("  Error: failed to get mod files: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}
		files = filterAndSortFiles(files, installShowArchived)
		if len(files) == 0 {
			fmt.Printf("  Error: no downloadable files available\n")
			failed = append(failed, mod.Name)
			continue
		}

		selectedFile := selectPrimaryFile(files)
		fmt.Printf("  File: %s\n", displayFileLabel(*selectedFile))

		// Download
		progressFn := func(p core.DownloadProgress) {
			if p.TotalBytes > 0 {
				bar := progressBar(p.Percentage, 20)
				fmt.Printf("\r  [%s] %.1f%%", bar, p.Percentage)
			}
		}
		downloadResult, err := service.DownloadMod(ctx, sourceID, game, mod, selectedFile, progressFn)
		if err != nil {
			fmt.Println()
			fmt.Printf("  Error: download failed: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}
		fmt.Println()

		if !skipVerify && downloadResult.Checksum != "" {
			fmt.Printf("  Checksum: %s\n", truncateChecksum(downloadResult.Checksum))
		}

		// Check conflicts in batch mode (warn but proceed)
		if !installForce {
			if conflicts, err := installer.GetConflicts(ctx, game, mod, profileName); err == nil && len(conflicts) > 0 {
				fmt.Printf("  ⚠ %d file conflict(s) - will overwrite\n", len(conflicts))
			}
		}

		// Deploy
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
		if err := service.SaveInstalledMod(installedMod); err != nil {
			fmt.Printf("  Error: failed to save mod: %v\n", err)
			failed = append(failed, mod.Name)
			continue
		}

		// Store checksum
		if !skipVerify && downloadResult.Checksum != "" {
			if err := service.SaveFileChecksum(sourceID, mod.ID, game.ID, profileName, selectedFile.ID, downloadResult.Checksum); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: failed to save checksum: %v\n", err)
			}
		}

		// Update profile
		modRef := domain.ModReference{
			SourceID: mod.SourceID,
			ModID:    mod.ID,
			Version:  mod.Version,
			FileIDs:  []string{selectedFile.ID},
		}
		if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil && verbose {
			fmt.Printf("  Warning: could not update profile: %v\n", err)
		}

		fmt.Printf("  ✓ Installed (%d files)\n", downloadResult.FilesExtracted)
		installed = append(installed, mod.Name)

		// Run install.after_each hook
		if err := runInstallHook(ctx, hookRunner, resolvedHooks, &hookCtx, "install.after_each", resolvedHooks.GetInstallAfterEach()); err != nil {
			hookErrors = append(hookErrors, fmt.Errorf("install.after_each hook failed for %s: %w", mod.ID, err))
		}
	}

	// Run install.after_all hook
	hookCtx.ModID = ""
	hookCtx.ModName = ""
	hookCtx.ModVersion = ""
	if err := runInstallHook(ctx, hookRunner, resolvedHooks, &hookCtx, "install.after_all", resolvedHooks.GetInstallAfterAll()); err != nil {
		hookErrors = append(hookErrors, fmt.Errorf("install.after_all hook failed: %w", err))
	}

	printHookWarnings(hookErrors)

	// Summary
	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Installed: %d\n", len(installed))
	if len(failed) > 0 {
		fmt.Printf("Failed: %d (%s)\n", len(failed), strings.Join(failed, ", "))
	}

	return nil
}
