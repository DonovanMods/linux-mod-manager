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
	Status  string `json:"status"` // ok, missing, no_checksum, file_count_mismatch, skipped, version_mismatch, version_unverifiable
}

var verifyCmd = &cobra.Command{
	Use:   "verify [mod-id]",
	Short: "Verify cached mod files",
	Long: `Verify the integrity of cached mod files using stored checksums.

Without arguments, verifies all cached mods for the specified game.
With a mod ID, verifies only that specific mod.

Each file is reported with one of:

    + NAME (FILE) - OK                  cache exists and a checksum is stored
    X NAME (FILE) - MISSING             version not present in cache
    ? NAME (FILE) - NO CHECKSUM         cached, but no checksum was ever stored
    ! NAME - FILE COUNT MISMATCH        cache exists but is empty (per-mod, not per-file)
    ? Unknown mod ID - SKIPPED          checksum row references a mod no longer installed

Use --fix to re-download files that are MISSING or have NO CHECKSUM,
storing a fresh checksum afterwards. FILE COUNT MISMATCH and SKIPPED
are not repaired by --fix.

verify also contacts each installed mod's source to check its recorded
version against what the stored file ID(s) actually are upstream (issue
#94: older installs could record the mod's "latest" version instead of
the version of the file that was actually downloaded and deployed):

    X NAME - VERSION MISMATCH (recorded X, source reports Y)
                                         recorded version doesn't match
                                         what the installed file ID(s)
                                         report upstream
    ? NAME - VERSION UNVERIFIABLE       none of the recorded file ID(s)
                                         are listed by the source anymore
                                         (reinstall to refresh)

Mods installed from a local source, mods requiring manual download, and
mods with no recorded file IDs are skipped silently - there is nothing
to check against. If the source can't be reached, the mod is reported
as skipped with a warning instead of failing the whole run.

Use --fix to repair VERSION MISMATCH too: the cache entry is re-keyed
from the recorded version to the effective (source-reported) one, the DB
row and the active profile's record are corrected to match, and any
symlink deployment is re-linked (its target lived inside the just-renamed
cache dir and would otherwise dangle). If a cache entry already exists
under the effective version, the rename is skipped and a note is printed,
but the DB and profile are still corrected. VERSION UNVERIFIABLE is never
fixable automatically since there is no matching upstream file to compare
against - reinstall the mod instead.

--json emits {game_id, profile, files: [{mod_id, mod_name, file_id,
status}], issues, warnings}; status is one of "ok", "missing",
"no_checksum", "file_count_mismatch", "skipped", "version_mismatch", or
"version_unverifiable". issues counts MISSING files and VERSION MISMATCH
rows (a successful --fix repair of either decrements it back out);
warnings counts everything else that isn't OK.

Examples:
  lmm verify --game skyrim-se           # Verify all mods
  lmm verify 12345 --game skyrim-se     # Verify specific mod
  lmm verify --fix --game skyrim-se     # Re-download missing files, populate missing checksums`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyFix, "fix", false, "re-download files that are missing or lack a stored checksum")
	verifyCmd.Flags().StringVarP(&verifyProfile, "profile", "p", "", "profile to verify (default: active profile)")

	rootCmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doVerify(cmd, svc, game, args)
	})
}

func doVerify(cmd *cobra.Command, svc *core.Service, game *domain.Game, args []string) error {
	profile, err := resolveProfile(svc, game.ID, verifyProfile)
	if err != nil {
		return err
	}

	// Get all files with checksums for this game/profile
	files, err := svc.GetFilesWithChecksums(game.ID, profile)
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

	// Per-mod version-record check: the recorded Version vs what the stored
	// file ID(s) actually report upstream (issue #94's detection half -
	// installs before this fix could stamp the mod's "latest" version
	// instead of the version of the file that was really downloaded and
	// deployed; Task A7 adds the --fix repair for VERSION MISMATCH rows).
	// One entry per installed mod, FileID left empty (there's no single
	// file at fault - the row's version metadata as a whole is what's
	// wrong).
	ctx := cmd.Context()
	installedMods, err := svc.GetInstalledMods(game.ID, profile)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}
	for i := range installedMods {
		mod := &installedMods[i]
		if modFilter != "" && mod.ID != modFilter {
			continue
		}
		// Nothing to check against: local imports and manual downloads have
		// no source to query, and a mod with no recorded file IDs predates
		// even the buggy stamping this check exists to catch.
		if mod.SourceID == domain.SourceLocal || mod.ManualDownload || len(mod.FileIDs) == 0 {
			continue
		}

		sourceFiles, err := svc.GetModFiles(ctx, mod.SourceID, &mod.Mod)
		if err != nil {
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: "", Status: "skipped"})
			} else {
				fmt.Printf("%s %s - could not check version (source unreachable)\n", colorYellow("?"), mod.Name)
			}
			warnings++
			continue
		}

		var matched []*domain.DownloadableFile
		for _, id := range mod.FileIDs {
			for j := range sourceFiles {
				if sourceFiles[j].ID == id {
					matched = append(matched, &sourceFiles[j])
					break
				}
			}
		}

		if len(matched) == 0 {
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: "", Status: "version_unverifiable"})
			} else {
				fmt.Printf("%s %s - VERSION UNVERIFIABLE (recorded file ID(s) no longer found upstream; reinstall to refresh the recorded version)\n", colorYellow("?"), mod.Name)
			}
			warnings++
			continue
		}

		effective := domain.EffectiveInstalledVersion(mod.Version, matched)
		if effective != mod.Version {
			recorded := mod.Version
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: "", Status: "version_mismatch"})
			} else {
				fmt.Printf("%s %s - VERSION MISMATCH (recorded %s, source reports %s)\n", colorRed("X"), mod.Name, recorded, effective)
			}
			issues++
			if verifyFix && mod.SourceID != domain.SourceLocal {
				note, repairErr := repairModVersion(cmd, svc, game, profile, mod, effective)
				if repairErr != nil {
					if !jsonOutput {
						fmt.Printf("  Repair failed: %v\n", repairErr)
					}
				} else {
					if jsonOutput {
						jsonFiles[len(jsonFiles)-1].Status = "ok"
					} else {
						fmt.Printf("  %s\n", colorGreen(fmt.Sprintf("Repaired: %s → %s", recorded, effective)))
						if note != "" {
							fmt.Printf("  Note: %s\n", note)
						}
					}
					issues--
				}
			}
			continue
		}

		// Recorded version matches what the source reports - OK, but not
		// printed per-line (same quiet-ok convention as the file loop below).
		checked++
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
					} else {
						jsonFiles[len(jsonFiles)-1].Status = "ok"
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

// repairModVersion repairs a VERSION MISMATCH row for --fix (issue #94):
// the recorded Version doesn't match what the stored file ID(s) actually
// are upstream (effective), so the cache entry, DB row, and profile record
// are all keyed by the wrong version. Fix sequence:
//
//  1. Cache re-key: if the cache dir for the recorded version exists and
//     the one for effective does not, rename it. If the effective-version
//     dir already exists, leave the cache alone and return a note instead
//     of clobbering it via os.Rename. If the rename itself fails, return
//     immediately with no DB write - the row stays version_mismatch and
//     the DB/cache never disagree in a new way.
//  2. DB: mutate mod.Version and save the full row (deliberately not
//     UpdateModVersion, which would shift the wrong value into
//     PreviousVersion and poison rollback).
//  3. Profile: upsert the corrected version into the active profile's YAML
//     record.
//  4. Re-link: if the mod is Deployed via a symlink, the game-dir symlinks
//     point INTO the cache dir renamed in step 1 and are now dangling -
//     re-run the installer to refresh them. Hardlink/copy deployments are
//     untouched by the cache rename, so they're left alone.
//
// mod is mutated in place (Version set to effective) only once the DB save
// that carries it has actually succeeded, so a failure partway through
// never leaves the in-memory row claiming a fix that didn't happen.
func repairModVersion(cmd *cobra.Command, svc *core.Service, game *domain.Game, profile string, mod *domain.InstalledMod, effective string) (note string, err error) {
	gameCache := svc.GetGameCache(game)
	oldPath := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	newPath := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, effective)

	if _, statErr := os.Stat(oldPath); statErr == nil {
		if _, statErr := os.Stat(newPath); statErr == nil {
			note = fmt.Sprintf("cache entry for %s already exists; left %s in place", effective, mod.Version)
		} else if renameErr := os.Rename(oldPath, newPath); renameErr != nil {
			return "", fmt.Errorf("renaming cache entry: %w", renameErr)
		}
	}

	updated := *mod
	updated.Version = effective
	if err := svc.SaveInstalledMod(&updated); err != nil {
		return note, fmt.Errorf("saving installed mod: %w", err)
	}
	*mod = updated

	pm := getProfileManager(svc)
	if err := pm.UpsertMod(game.ID, profile, domain.ModReference{
		SourceID: mod.SourceID,
		ModID:    mod.ID,
		Version:  effective,
		FileIDs:  mod.FileIDs,
	}); err != nil {
		return note, fmt.Errorf("updating profile record: %w", err)
	}

	if mod.Deployed && mod.LinkMethod == domain.LinkSymlink {
		if err := svc.GetInstaller(game).Install(cmd.Context(), game, &mod.Mod, profile); err != nil {
			return note, fmt.Errorf("relinking deployed files: %w", err)
		}
	}

	return note, nil
}

// redownloadModFile re-downloads a single mod file and extracts to cache, then updates checksum in DB.
func redownloadModFile(cmd *cobra.Command, svc *core.Service, game *domain.Game, profile string, mod *domain.InstalledMod, fileID string) error {
	ctx := cmd.Context()
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
		if err := svc.SaveFileChecksum(mod.SourceID, mod.ID, game.ID, profile, fileID, result.Checksum); err != nil {
			return fmt.Errorf("saving checksum: %w", err)
		}
	}
	return nil
}
