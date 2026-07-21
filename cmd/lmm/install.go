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

	"github.com/spf13/cobra"
)

// confirmInstallConflicts prompts the user to confirm overwriting files that
// installing the plan's mod would conflict with. conflicts is sourced from
// plan.Conflicts (computed by PlanInstall) rather than a fresh GetConflicts
// call - see InstallPlan.Conflicts' doc comment for why a fresh call isn't
// possible without downloading first (PlanInstall never downloads). Prints
// them and prompts for confirmation, returning an error to abort the install
// when the user declines.
func confirmInstallConflicts(service *core.Service, game *domain.Game, profileName string, conflicts []core.Conflict) error {
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

	// Get the mod to install (by --id or interactive search) - unchanged,
	// CLI-side.
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

	if !installNoDeps && mod.SourceID != domain.SourceLocal {
		fmt.Println("\nResolving dependencies...")
	}

	// PlanInstall resolves dependencies, files (its own non-interactive
	// default), conflicts (against whatever is already cached), and any
	// existing installed row - all read-only. --no-deps/local-mod dep
	// skipping and interactive/--file file selection are deliberately NOT
	// part of PlanInstall (see its doc comment); both are applied to the
	// plan below, CLI-side, before ApplyInstall ever runs.
	plan, err := service.PlanInstall(ctx, game, profileName, installSource, mod.ID, installShowArchived)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(installSource)
		}
		return err
	}

	if installNoDeps || mod.SourceID == domain.SourceLocal {
		plan.Dependencies = nil
		plan.MissingDependencies = nil
		plan.CycleDetected = false
	}

	// If there are dependencies to install (or unresolvable ones to warn
	// about), show the plan and confirm.
	if len(plan.Dependencies) > 0 || len(plan.MissingDependencies) > 0 {
		showInstallPlan(plan)

		if !installYes {
			fmt.Printf("\nInstall %d mod(s)? [Y/n]: ", len(plan.Dependencies)+1)
			input, err := readPromptLine()
			if err != nil {
				return err
			}
			if input == "n" || input == "no" {
				return fmt.Errorf("installation cancelled")
			}
		}
	}

	// Get available files for the PRIMARY mod - unchanged, CLI-side:
	// PlanInstall's own Files already picked its non-interactive default;
	// interactive/--file selection below overrides plan.Files with exactly
	// what the user chose (see InstallPlan.Files' doc comment - ApplyInstall
	// installs exactly plan.Files, no re-selection).
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
	plan.Files = make([]domain.DownloadableFile, len(selectedFiles))
	for i, f := range selectedFiles {
		plan.Files[i] = *f
	}

	// Show selected files - unchanged, CLI-side.
	if len(selectedFiles) == 1 {
		fmt.Printf("\nFile: %s\n", displayFileLabel(*selectedFiles[0]))
	} else {
		fmt.Printf("\nFiles (%d):\n", len(selectedFiles))
		for _, f := range selectedFiles {
			fmt.Printf("  - %s\n", displayFileLabel(*f))
		}
	}

	// Conflict prompt - sourced from plan.Conflicts (computed by
	// PlanInstall against whatever is already cached), not a fresh
	// GetConflicts call. --force skips it, unchanged.
	if !installForce {
		if err := confirmInstallConflicts(service, game, profileName, plan.Conflicts); err != nil {
			return err
		}
	}

	opts := core.InstallOptions{
		SkipVerify:  skipVerify,
		Hooks:       getResolvedHooks(service, game, profileName),
		HookRunner:  getHookRunner(service),
		HookContext: makeHookContext(game),
		Force:       installForce,
	}

	// progress prints every diagnostic and status line at its exact point
	// of occurrence, driven entirely by core.ApplyInstall's progress events
	// - including diagnostics that also land in result.Warnings/.Notes (see
	// core.InstallResult's doc comment). Those slices are never separately
	// batch-printed below: every entry has a corresponding event here, so
	// doing so would double-print.
	progress := func(p core.DeployProgress) {
		switch p.Phase {
		case core.InstallBeforeAllForced, core.InstallBeforeEachForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.InstallDepInstalling:
			fmt.Printf("\n[%d/%d] Installing dependency: %s\n", p.Index, p.Total, p.ModName)
		case core.InstallDepSkipped:
			fmt.Printf("  Skipped: %s: %s\n", p.ModName, p.Detail)
		case core.InstallDepConflictWarning:
			fmt.Printf("  ⚠ %s\n", p.Detail)
		case core.InstallDepDownloading:
			bar := progressBar(p.Percent, 20)
			fmt.Printf("\r  [%s] %.1f%%", bar, p.Percent)
		case core.InstallDepInstalled:
			fmt.Printf("\n  ✓ Installed: %s\n", p.ModName)
		case core.InstallDownloadStarted:
			if p.Total > 1 {
				fmt.Printf("\n[%d/%d] Downloading %s...\n", p.Index, p.Total, displayFileLabel(*p.File))
			} else {
				fmt.Printf("\nDownloading %s...\n", displayFileLabel(*p.File))
			}
		case core.InstallDownloading:
			if p.TotalBytes > 0 {
				bar := progressBar(p.Percent, 30)
				fmt.Printf("\r  [%s] %.1f%% (%s / %s)", bar, p.Percent, formatSize(p.Downloaded), formatSize(p.TotalBytes))
			} else {
				fmt.Printf("\r  Downloaded %s", formatSize(p.Downloaded))
			}
		case core.InstallDownloadDone:
			fmt.Println()
		case core.InstallDownloadFailed:
			if strings.Contains(p.Detail, "third-party downloads") && mod.SourceURL != "" {
				fmt.Println()
				fmt.Println("  ⚠  This mod author has disabled API downloads.")
				fmt.Println("  To install manually:")
				fmt.Println()
				fmt.Printf("    1. Download from: %s\n", mod.SourceURL)
				fmt.Printf("    2. Import:        lmm import <downloaded-file> --id %s\n", mod.ID)
				fmt.Println()
			}
		case core.InstallChecksumComputed:
			fmt.Printf("  Checksum: %s\n", truncateChecksum(p.Detail))
		case core.InstallExtracting:
			fmt.Println("\nExtracting to cache...")
		case core.InstallDeploying:
			fmt.Println("Deploying to game directory...")
		case core.InstallNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.InstallWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.ApplyInstall(ctx, game, plan, opts, progress)
	if err != nil {
		// Diagnostics accumulated before a fatal error (ApplyInstall's
		// error-path convention returns them alongside it) were already
		// printed above, live, via progress - nothing left to print here.
		return err
	}

	fmt.Printf("\n✓ Installed: %s v%s\n", mod.Name, mod.Version)
	fmt.Printf("  Files deployed: %d\n", result.FilesDeployed)
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

// showInstallPlan displays PlanInstall's resolved dependency tree (install
// order: dependencies first, target last) and any missing/cyclic-dependency
// warnings - byte-identical to the pre-refit CLI's own showInstallPlan
// (which took a locally-resolved dependency list), now sourced from
// *core.InstallPlan since dependency resolution itself moved into
// Service.PlanInstall - see the task report.
func showInstallPlan(plan *core.InstallPlan) {
	fmt.Printf("\nDependency tree (install order):\n")
	i := 1
	for _, dep := range plan.Dependencies {
		fmt.Printf("  %d. %s v%s (ID: %s) [dependency]\n", i, dep.Name, dep.Version, dep.ID)
		i++
	}
	fmt.Printf("  %d. %s v%s (ID: %s) [target]\n", i, plan.Mod.Name, plan.Mod.Version, plan.Mod.ID)

	if plan.CycleDetected {
		fmt.Fprintf(os.Stderr, "\n⚠ Warning: Circular dependency detected among dependencies; install order is best-effort.\n")
	}

	if len(plan.MissingDependencies) > 0 {
		fmt.Printf("\n⚠ Warning: %d dependency(ies) not available on source:\n", len(plan.MissingDependencies))
		for _, ref := range plan.MissingDependencies {
			fmt.Printf("  - %s (may require manual install)\n", domain.ModKey(ref.SourceID, ref.ModID))
		}
	}
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

// batchInstallMods is the shared implementation for installing multiple mods
// sequentially. Used by installMultipleMods (multi-select from search) -
// dependency-resolved installs go through PlanInstall/ApplyInstall instead
// as of Phase 5b Task 2 (see doInstall and ApplyInstall's doc comments for
// why the two paths were unified there rather than here). Each mod's
// SourceID is used for API calls (set during search/dep resolution).
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

		// Save to database. Normalize GameID to the lmm game (see comment on
		// the single-mod save site above for why).
		installedMod := &domain.InstalledMod{
			Mod:          *mod,
			ProfileName:  profileName,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			Deployed:     true,
			LinkMethod:   linkMethod,
			FileIDs:      []string{selectedFile.ID},
		}
		installedMod.Mod.GameID = game.ID
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
