package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	verifyFix     bool
	verifyProfile string
)

type verifyJSONOutput struct {
	GameID   string           `json:"game_id"`
	Profile  string           `json:"profile"`
	Files    []verifyFileJSON `json:"files"`
	Issues   int              `json:"issues"`
	Warnings int              `json:"warnings"`
}

type verifyFileJSON struct {
	ModID   string `json:"mod_id"`
	ModName string `json:"mod_name"`
	FileID  string `json:"file_id"`
	Status  string `json:"status"` // ok, missing, no_checksum, file_count_mismatch, skipped
}

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
	verifyCmd.Flags().BoolVar(&verifyFix, "fix", false, "Re-download missing files and populate missing checksums by re-downloading")
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
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(verifyJSONOutput{GameID: game.ID, Profile: profile, Files: []verifyFileJSON{}, Issues: 0, Warnings: 0}); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
		}
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
	var jsonFiles []verifyFileJSON

	if !jsonOutput {
		fmt.Println("Verifying cached mods...")
		fmt.Println()
	}

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
				if jsonOutput {
					jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: "", Status: "file_count_mismatch"})
				} else {
					fmt.Printf("! %s - FILE COUNT MISMATCH (expected content from %d download(s), cache has 0 files)\n", mod.Name, expectedCount)
				}
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
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: f.ModID, ModName: "", FileID: f.FileID, Status: "skipped"})
			} else {
				fmt.Printf("? Unknown mod %s - SKIPPED\n", f.ModID)
			}
			warnings++
			continue
		}

		// Check cache existence for this mod version (per file/version)
		cacheExists := gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version)

		if !cacheExists {
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "missing"})
			} else {
				fmt.Printf("%s %s (%s) - MISSING (version %s not in cache)\n", colorRed("X"), mod.Name, f.FileID, mod.Version)
			}
			issues++
			if verifyFix && mod.SourceID != domain.SourceLocal {
				if err := redownloadModFile(cmd, svc, game, profile, mod, f.FileID); err != nil {
					if !jsonOutput {
						fmt.Printf("  Re-download failed: %v\n", err)
					}
				} else {
					if !jsonOutput {
						fmt.Printf("  %s\n", colorGreen("Re-downloaded OK"))
					}
					issues--
				}
			}
			continue
		}

		// Check if checksum stored
		if f.Checksum == "" {
			if verifyFix && mod.SourceID != domain.SourceLocal {
				if err := redownloadModFile(cmd, svc, game, profile, mod, f.FileID); err != nil {
					if jsonOutput {
						jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum"})
					} else {
						fmt.Printf("%s %s (%s) - NO CHECKSUM\n", colorYellow("?"), mod.Name, f.FileID)
						fmt.Printf("  Re-download to populate checksum failed: %v\n", err)
					}
					warnings++
				} else {
					if jsonOutput {
						jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"})
					} else {
						fmt.Printf("%s %s (%s) - %s (checksum populated)\n", colorGreen("+"), mod.Name, f.FileID, colorGreen("OK"))
					}
				}
				continue
			}
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum"})
			} else {
				fmt.Printf("%s %s (%s) - NO CHECKSUM\n", colorYellow("?"), mod.Name, f.FileID)
			}
			warnings++
			continue
		}

		// Cache exists and checksum stored - consider OK
		if jsonOutput {
			jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"})
		} else {
			fmt.Printf("%s %s (%s) - %s\n", colorGreen("+"), mod.Name, f.FileID, colorGreen("OK"))
		}
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(verifyJSONOutput{GameID: game.ID, Profile: profile, Files: jsonFiles, Issues: issues, Warnings: warnings}); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return nil
	}

	fmt.Println()

	if checked == 0 && modFilter != "" {
		fmt.Printf("No files found for mod %s\n", modFilter)
		return nil
	}

	if issues > 0 || warnings > 0 {
		fmt.Printf("%d issue(s), %d warning(s) found.\n", issues, warnings)
		if (issues > 0 || warnings > 0) && !verifyFix {
			fmt.Println("Run with --fix to re-download missing files and populate missing checksums.")
		}
	} else {
		fmt.Println(colorGreen("All files verified OK."))
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
