package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"lmm/internal/core"
	"lmm/internal/domain"

	"github.com/spf13/cobra"
)

var (
	installSource       string
	installProfile      string
	installVersion      string
	installModID        string
	installFileID       string
	installYes          bool
	installShowArchived bool
)

var installCmd = &cobra.Command{
	Use:   "install <query>",
	Short: "Install a mod",
	Long: `Install a mod from the configured source.

The mod will be searched for by name and added to the specified profile
(or default profile if not specified).

Examples:
  lmm install "ore stack" --game starrupture
  lmm install "skyui" --game skyrim-se --profile survival
  lmm install --id 12345 --game skyrim-se
  lmm install "mod name" -g skyrim-se -y  # Auto-select first match`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringVarP(&installSource, "source", "s", "nexusmods", "mod source")
	installCmd.Flags().StringVarP(&installProfile, "profile", "p", "", "profile to install to (default: active profile)")
	installCmd.Flags().StringVar(&installVersion, "version", "", "specific version to install (default: latest)")
	installCmd.Flags().StringVar(&installModID, "id", "", "mod ID (skips search)")
	installCmd.Flags().StringVar(&installFileID, "file", "", "file ID (skips file selection)")
	installCmd.Flags().BoolVarP(&installYes, "yes", "y", false, "auto-select first/primary option (no prompts)")
	installCmd.Flags().BoolVar(&installShowArchived, "show-archived", false, "show archived/old files")

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
	defer service.Close()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
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
				return fmt.Errorf("NexusMods requires authentication.\nRun 'lmm auth login' to authenticate")
			}
			return fmt.Errorf("failed to fetch mod: %w", err)
		}
	} else {
		// Search for mod
		query := args[0]
		fmt.Printf("Searching for \"%s\"...\n\n", query)

		mods, err := service.SearchMods(ctx, installSource, gameID, query)
		if err != nil {
			if errors.Is(err, domain.ErrAuthRequired) {
				return fmt.Errorf("NexusMods requires authentication.\nRun 'lmm auth login' to authenticate")
			}
			return fmt.Errorf("search failed: %w", err)
		}

		if len(mods) == 0 {
			return fmt.Errorf("no mods found matching \"%s\"", query)
		}

		// Get installed mods to mark already-installed ones
		profileName := installProfile
		if profileName == "" {
			profileName = "default"
		}
		installedMods, _ := service.GetInstalledMods(gameID, profileName)
		installedIDs := make(map[string]bool)
		for _, im := range installedMods {
			if im.SourceID == installSource {
				installedIDs[im.ID] = true
			}
		}

		// Select the mod
		if len(mods) == 1 || installYes {
			mod = &mods[0]
		} else {
			// Show selection
			for i, m := range mods {
				if i >= 10 {
					fmt.Printf("  ... and %d more\n", len(mods)-10)
					break
				}
				installedMark := ""
				if installedIDs[m.ID] {
					installedMark = " [installed]"
				}
				fmt.Printf("  [%d] %s v%s by %s (ID: %s)%s\n", i+1, m.Name, m.Version, m.Author, m.ID, installedMark)
			}

			selection, err := promptSelection("Select mod", 1, len(mods))
			if err != nil {
				return err
			}
			mod = &mods[selection-1]
		}
	}

	fmt.Printf("\nSelected: %s v%s by %s\n", mod.Name, mod.Version, mod.Author)

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

	// Select file
	var selectedFile *domain.DownloadableFile
	if installFileID != "" {
		// Direct file ID
		for i := range files {
			if files[i].ID == installFileID {
				selectedFile = &files[i]
				break
			}
		}
		if selectedFile == nil {
			return fmt.Errorf("file ID %s not found", installFileID)
		}
	} else if len(files) == 1 {
		selectedFile = &files[0]
	} else {
		// Find primary file
		var primaryFile *domain.DownloadableFile
		for i := range files {
			if files[i].IsPrimary {
				primaryFile = &files[i]
				break
			}
		}

		if installYes && primaryFile != nil {
			selectedFile = primaryFile
		} else if installYes {
			selectedFile = &files[0]
		} else {
			// Show file selection
			fmt.Println("\nAvailable files:")
			for i, f := range files {
				sizeStr := formatSize(f.Size)
				defaultMark := ""
				if f.IsPrimary {
					defaultMark = " <- default"
				}
				fmt.Printf("  [%d] %s (%s, %s)%s\n", i+1, f.FileName, f.Category, sizeStr, defaultMark)
			}

			defaultChoice := 1
			if primaryFile != nil {
				for i, f := range files {
					if f.ID == primaryFile.ID {
						defaultChoice = i + 1
						break
					}
				}
			}

			selection, err := promptSelectionWithDefault("Select file", defaultChoice, len(files))
			if err != nil {
				return err
			}
			selectedFile = &files[selection-1]
		}
	}

	fmt.Printf("\nFile: %s\n", selectedFile.FileName)

	// Download the mod
	fmt.Printf("\nDownloading %s...\n", selectedFile.FileName)

	progressFn := func(p core.DownloadProgress) {
		if p.TotalBytes > 0 {
			bar := progressBar(p.Percentage, 30)
			fmt.Printf("\r  [%s] %.1f%% (%s / %s)", bar, p.Percentage,
				formatSize(p.Downloaded), formatSize(p.TotalBytes))
		} else {
			fmt.Printf("\r  Downloaded %s", formatSize(p.Downloaded))
		}
	}

	fileCount, err := service.DownloadMod(ctx, installSource, game, mod, selectedFile, progressFn)
	if err != nil {
		fmt.Println() // newline after progress
		return fmt.Errorf("download failed: %w", err)
	}
	fmt.Println() // newline after progress

	fmt.Println("\nExtracting to cache...")

	// Deploy to game directory
	fmt.Println("Deploying to game directory...")

	linker := service.GetLinker(service.GetDefaultLinkMethod())
	installer := core.NewInstaller(service.Cache(), linker)

	profileName := installProfile
	if profileName == "" {
		profileName = "default"
	}

	if err := installer.Install(ctx, game, mod, profileName); err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	// Save to database
	installedMod := &domain.InstalledMod{
		Mod:          *mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}

	if err := service.DB().SaveInstalledMod(installedMod); err != nil {
		return fmt.Errorf("failed to save mod: %w", err)
	}

	fmt.Printf("\n✓ Installed: %s v%s\n", mod.Name, mod.Version)
	fmt.Printf("  Files deployed: %d\n", fileCount)

	return nil
}

// promptSelection prompts the user to select a number in range [1, max]
func promptSelection(prompt string, defaultChoice, max int) (int, error) {
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Printf("\n%s [%d]: ", prompt, defaultChoice)
		input, err := reader.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("reading input: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			return defaultChoice, nil
		}

		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > max {
			fmt.Printf("Please enter a number between 1 and %d\n", max)
			continue
		}

		return n, nil
	}
}

// promptSelectionWithDefault is like promptSelection but shows the default
func promptSelectionWithDefault(prompt string, defaultChoice, max int) (int, error) {
	return promptSelection(prompt, defaultChoice, max)
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
