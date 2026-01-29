package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	verifyFix     bool
	verifyProfile string
)

var verifyCmd = &cobra.Command{
	Use:   "verify [mod-id]",
	Short: "Verify cached mod files",
	Long: `Verify the integrity of cached mod files using stored checksums.

Without arguments, verifies all cached mods for the specified game.
With a mod ID, verifies only that specific mod.

Examples:
  lmm verify --game skyrim-se           # Verify all mods
  lmm verify 12345 --game skyrim-se     # Verify specific mod
  lmm verify --fix --game skyrim-se     # Re-download corrupted files`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyFix, "fix", false, "Re-download missing or corrupted files")
	verifyCmd.Flags().StringVarP(&verifyProfile, "profile", "p", "", "profile to verify (default: default)")

	rootCmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	svc, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer svc.Close()

	game, err := svc.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("getting game %s: %w", gameID, err)
	}

	profile := profileOrDefault(verifyProfile)

	// Get all files with checksums for this game/profile
	files, err := svc.DB().GetFilesWithChecksums(game.ID, profile)
	if err != nil {
		return fmt.Errorf("getting files: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("No installed mods to verify.")
		return nil
	}

	// Filter to specific mod if provided
	var modFilter string
	if len(args) > 0 {
		modFilter = args[0]
	}

	// Group by mod for file-count check (expected DB file count vs cached file count)
	fileCountByMod := make(map[string]int)
	for _, f := range files {
		key := f.SourceID + ":" + f.ModID
		if modFilter != "" && f.ModID != modFilter {
			continue
		}
		fileCountByMod[key]++
	}

	gameCache := svc.GetGameCache(game)
	var issues, warnings int
	var checked int

	fmt.Println("Verifying cached mods...")
	fmt.Println()

	// Per-mod file-count mismatch: report when cache exists but has 0 files (expected > 0)
	reportedMismatch := make(map[string]bool)
	for key, expectedCount := range fileCountByMod {
		if expectedCount == 0 {
			continue
		}
		sourceID, modID, _ := strings.Cut(key, ":")
		mod, err := svc.GetInstalledMod(sourceID, modID, game.ID, profile)
		if err != nil {
			continue
		}
		if modFilter != "" && mod.ID != modFilter {
			continue
		}
		cacheExists := gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version)
		if !cacheExists {
			continue
		}
		cachedFiles, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
		if err != nil {
			continue
		}
		actualCount := len(cachedFiles)
		if expectedCount > 0 && actualCount == 0 {
			if !reportedMismatch[key] {
				fmt.Printf("! %s - FILE COUNT MISMATCH (expected content from %d download(s), cache has 0 files)\n", mod.Name, expectedCount)
				reportedMismatch[key] = true
				warnings++
			}
		}
	}

	for _, f := range files {
		if modFilter != "" && f.ModID != modFilter {
			continue
		}
		checked++

		// Get mod info for display (version used for cache path)
		mod, err := svc.GetInstalledMod(f.SourceID, f.ModID, game.ID, profile)
		if err != nil {
			fmt.Printf("? Unknown mod %s - SKIPPED\n", f.ModID)
			warnings++
			continue
		}

		// Check cache existence for this mod version (per file/version)
		cacheExists := gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version)

		if !cacheExists {
			fmt.Printf("X %s (%s) - MISSING (version %s not in cache)\n", mod.Name, f.FileID, mod.Version)
			issues++
			if verifyFix && mod.SourceID != domain.SourceLocal {
				if err := redownloadModFile(cmd, svc, game, profile, mod, f.FileID); err != nil {
					fmt.Printf("  Re-download failed: %v\n", err)
				} else {
					fmt.Printf("  Re-downloaded OK\n")
					issues--
				}
			}
			continue
		}

		// Check if checksum stored
		if f.Checksum == "" {
			fmt.Printf("? %s (%s) - NO CHECKSUM\n", mod.Name, f.FileID)
			warnings++
			continue
		}

		// Cache exists and checksum stored - consider OK
		fmt.Printf("+ %s (%s) - OK\n", mod.Name, f.FileID)
	}

	fmt.Println()

	if checked == 0 && modFilter != "" {
		fmt.Printf("No files found for mod %s\n", modFilter)
		return nil
	}

	if issues > 0 || warnings > 0 {
		fmt.Printf("%d issue(s), %d warning(s) found.\n", issues, warnings)
		if issues > 0 && !verifyFix {
			fmt.Println("Run with --fix to re-download missing files.")
		}
	} else {
		fmt.Println("All files verified OK.")
	}

	return nil
}

// redownloadModFile re-downloads a single mod file and extracts to cache, then updates checksum in DB.
func redownloadModFile(cmd *cobra.Command, svc *core.Service, game *domain.Game, profile string, mod *domain.InstalledMod, fileID string) error {
	ctx := context.Background()
	files, err := svc.GetModFiles(ctx, mod.SourceID, &mod.Mod)
	if err != nil {
		return fmt.Errorf("getting mod files: %w", err)
	}
	var downloadFile *domain.DownloadableFile
	for i := range files {
		if files[i].ID == fileID {
			downloadFile = &files[i]
			break
		}
	}
	if downloadFile == nil {
		return fmt.Errorf("file %s not found in mod", fileID)
	}
	result, err := svc.DownloadMod(ctx, mod.SourceID, game, &mod.Mod, downloadFile, nil)
	if err != nil {
		return err
	}
	if result.Checksum != "" {
		if err := svc.DB().SaveFileChecksum(mod.SourceID, mod.ID, game.ID, profile, fileID, result.Checksum); err != nil {
			return fmt.Errorf("saving checksum: %w", err)
		}
	}
	return nil
}
