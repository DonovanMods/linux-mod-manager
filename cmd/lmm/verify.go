package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	Status  string `json:"status"`         // ok, missing, no_checksum, file_count_mismatch, skipped, version_mismatch, version_unverifiable
	Note    string `json:"note,omitempty"` // optional detail: a blocked cache rename, sibling-repair results, a --fix repair/redownload failure reason, or a file-count-check lookup failure - omitted when there's nothing extra to add
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
under the effective version, the rename is skipped - the existing
deployment is left pointing at the still-intact recorded-version cache
dir rather than being re-linked into the unverified pre-existing one -
and a note is printed (also included as "note" in --json output), but
the DB and profile are still corrected. Since the mod cache is shared
across profiles, a rename also corrects any OTHER profile whose record
still holds the same stale version AND the same file selection (DB,
profile record, and re-linking a symlink deployment the same way) -
printed as its own "Repaired (profile ...)" line per profile in text
mode, or folded into the "note" field as "also repaired in profile(s):
..." in --json. A same-version sibling recording DIFFERENT files is left
alone instead, with a note naming it and suggesting a manual
"verify --fix -p" run scoped to that profile. VERSION UNVERIFIABLE is
never fixable automatically since there is no matching upstream file to
compare against - reinstall the mod instead.

--json emits {game_id, profile, files: [{mod_id, mod_name, file_id,
status, note}], issues, warnings}; status is one of "ok", "missing",
"no_checksum", "file_count_mismatch", "skipped", "version_mismatch", or
"version_unverifiable"; note adds detail where there's something extra to
say - a blocked cache rename, sibling-repair results, a --fix repair or
redownload failure's reason, or a file-count-check lookup failure - and is
omitted otherwise. issues counts MISSING files and VERSION MISMATCH rows
(a successful --fix repair of either decrements it back out); warnings
counts everything else that isn't OK.

Examples:
  lmm verify --game skyrim-se           # Verify all mods
  lmm verify 12345 --game skyrim-se     # Verify specific mod
  lmm verify --fix --game skyrim-se     # Re-download missing files, populate checksums, repair version records`,
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
	// checked mixes two counters: per-FILE increments from the main loop
	// below and per-MOD skip/ok increments from the version-record pre-pass
	// above it - both feed the same "no files found" zero-check, so neither
	// needed its own variable.
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
			// A not-installed mod (an orphaned checksum row - the main
			// per-file loop below reports this exact case itself, as
			// "Unknown mod ... - SKIPPED") is a normal, silent skip here;
			// reporting it again in this pre-pass would just duplicate
			// that warning. Any OTHER lookup error (a genuine DB failure)
			// is NOT a normal skip and must not be swallowed (epic98 audit
			// Finding 5).
			if !errors.Is(err, domain.ErrModNotFound) {
				if jsonOutput {
					jsonFiles = append(jsonFiles, verifyFileJSON{ModID: modID, ModName: "", FileID: "", Status: "skipped", Note: err.Error()})
				} else {
					fmt.Printf("%s Mod %s - could not check file count: %v\n", colorYellow("?"), modID, err)
				}
				warnings++
			}
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
			// A real filesystem error walking the cache dir (permission
			// denied, I/O error) is not the same as "cache is empty" - the
			// FILE COUNT MISMATCH case just below - and must be surfaced,
			// not silently treated as "nothing to report" (audit Finding 5).
			if !reportedMismatch[key] {
				if jsonOutput {
					jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: "", Status: "skipped", Note: err.Error()})
				} else {
					fmt.Printf("%s %s - could not check cached file count: %v\n", colorYellow("?"), mod.Name, err)
				}
				reportedMismatch[key] = true
				warnings++
			}
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

		sourceFiles, err := svc.GetModFiles(ctx, mod.SourceID, sourceMappedMod(game, &mod.Mod))
		if err != nil {
			if jsonOutput {
				// Copilot round 6 (PR #128): the source-unreachable reason
				// must reach --json too, not just the text-mode line below
				// - status stays "skipped" and warnings is unaffected,
				// only the note is new.
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: "", Status: "skipped", Note: fmt.Sprintf("could not check version: %v", err)})
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
				note, siblingFailures, repairErr := repairModVersion(cmd, svc, game, profile, mod, effective)
				// Sibling failures are warnings regardless of how the
				// PRIMARY row's own repair turned out - a failed sibling
				// repair is real, surfaced work left undone, and the
				// warnings counter (fed into both the --json output and the
				// text summary line below) must reflect it either way, not
				// just when the primary repair also happened to succeed.
				warnings += siblingFailures
				if repairErr != nil {
					if jsonOutput {
						// The PRIMARY row itself wasn't fixed (status/issues
						// stay as already recorded above). The failure
						// reason itself (audit Finding 7) and note can
						// still carry a successful SIBLING repair - see
						// repairModVersion's doc comment - and dropping
						// either here would make them invisible to a
						// --json caller even though they genuinely
						// happened/occurred.
						repairNote := "repair failed: " + repairErr.Error()
						if note != "" {
							repairNote += "; " + note
						}
						jsonFiles[len(jsonFiles)-1].Note = repairNote
					} else {
						fmt.Printf("  Repair failed: %v\n", repairErr)
					}
				} else {
					if jsonOutput {
						jsonFiles[len(jsonFiles)-1].Status = "ok"
						jsonFiles[len(jsonFiles)-1].Note = note
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
					if jsonOutput {
						// audit Finding 7: the row was already appended
						// above (Status "missing") - the failure reason
						// must reach --json too, not just this text line.
						jsonFiles[len(jsonFiles)-1].Note = err.Error()
					} else {
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
						// audit Finding 7: the failure reason must reach
						// --json, not just the text-mode line below.
						jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: err.Error()})
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
			fmt.Println("Run with --fix to re-download missing files, populate missing checksums, and repair version-record mismatches.")
		}
	} else {
		fmt.Println(colorGreen("All files verified OK."))
	}

	return nil
}

// sourceMappedMod returns a copy of mod with GameID translated through the
// game's per-source ID mapping (game.SourceIDs) - the same rule
// Service.GetMod already applies (internal/core/service.go) before calling
// into a source. Installed rows persist the LMM game ID (see
// setupDoVerifyVersionTest and every InstalledMod fixture: GameID:
// game.ID), but Service.GetModFiles does NOT translate it itself - unlike
// GetMod, it forwards straight to the source. Sources like NexusMods
// address games by their own domain (e.g. "skyrimspecialedition"), so any
// direct svc.GetModFiles call driven off an installed row's mod.Mod (as
// opposed to one already sourced from the source itself, e.g. a fresh
// search result) needs this translation first or it silently queries the
// wrong game. An empty mapping value means "this source applies to any
// game" (e.g. directory sources: `donovan-mods: ""`) and must not blank
// out the LMM ID - matches Service.GetMod's `ok && id != ""` guard exactly.
func sourceMappedMod(game *domain.Game, mod *domain.Mod) *domain.Mod {
	mapped := *mod
	if id, ok := game.SourceIDs[mod.SourceID]; ok && id != "" {
		mapped.GameID = id
	}
	return &mapped
}

// cacheDirExists reports whether a cache mod-version directory exists,
// distinguishing "genuinely absent" (fs.ErrNotExist - a normal state: the
// version was cleared, or never cached) from any OTHER stat failure
// (permission denied, I/O error, a corrupted mount, etc.), which must NOT
// be silently treated as "absent" (epic98 audit Finding 4): proceeding to
// a DB write on the strength of a check that itself failed would violate
// the invariant that no DB write happens unless the cache state was
// actually verified first - the same invariant os.Rename's own failure
// path already protects.
func cacheDirExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// repairModVersion repairs a VERSION MISMATCH row for --fix (issue #94):
// the recorded Version doesn't match what the stored file ID(s) actually
// are upstream (effective), so the cache entry, DB row, and profile record
// are all keyed by the wrong version. Fix sequence:
//
//  1. Cache re-key: if the cache dir for the recorded version exists and
//     the one for effective does not, rename it. If the effective-version
//     dir already exists, leave the cache alone and return a note instead
//     of clobbering it via os.Rename - the existing deployment (if any)
//     still points at the intact recorded-version dir, so step 4 below is
//     skipped in this case rather than re-linking into the unvetted
//     pre-existing one. If the rename itself fails, OR either stat check
//     itself fails for a reason other than "doesn't exist" (audit Finding
//     4), return immediately with no DB write - the row stays
//     version_mismatch and the DB/cache never disagree in a new way.
//  2. Profile: upsert the corrected version into the active profile's YAML
//     record. Runs BEFORE step 3's DB write (audit Finding 3, swapped from
//     the original order): the DB row is the signal doVerify's own
//     mismatch detection reads (GetInstalledMods, not the profile YAML),
//     so it must be the LAST of these two writes to succeed - if the
//     upsert fails here, the DB is untouched, so a retry still detects
//     version_mismatch and can converge. The original order (DB first)
//     let a profile-upsert failure leave the DB already at effective, so
//     retries never re-detected the mismatch and the stale YAML ref could
//     never self-heal.
//  3. DB: SetModVersion - a plain version-column update (audit Finding 1),
//     deliberately NOT SaveInstalledMod's full-row upsert, which always
//     re-keys installed_mod_files (DELETE + a checksum-less re-INSERT)
//     even though FileIDs is completely unchanged here, silently wiping
//     every stored checksum for the mod on every repair. Also deliberately
//     not UpdateModVersion, which would shift the wrong value into
//     PreviousVersion and poison rollback - this is a correction, not an
//     update.
//  4. Re-link: skipped when step 1 left the cache alone (note != "" - the
//     current symlinks already point at valid content, so there's nothing
//     to fix). Otherwise, if the mod is Deployed via a symlink, the
//     game-dir symlinks point INTO the cache dir renamed in step 1 and are
//     now dangling - re-run the installer to refresh them. Hardlink/copy
//     deployments are untouched by the cache rename, so they're left
//     alone regardless. On failure, SetModDeployed (not SaveInstalledMod -
//     audit Finding 1 again) clears the flag without touching file IDs.
//  5. Sibling profiles: runs when step 1 renamed the cache THIS run, OR
//     (audit Finding 2) when the cache already lives under the effective
//     version from an EARLIER, partially-failed run - old path absent,
//     new path present, and no blocked-rename note (which would mean the
//     new dir was pre-existing/unvetted content, not this repair's own
//     prior success). Without this, a retry after a run that renamed the
//     cache but then failed before completing would see oldPath already
//     gone, renamed=false, and skip siblings entirely - even though the
//     shared cache dir has, in fact, already moved out from under them.
//     Every OTHER profile's installed_mods row for the same (SourceID,
//     ModID, GameID) whose Version still equals the OLD recorded version
//     is corrected the same way (DB + profile YAML), and re-linked if
//     Deployed via symlink - see repairSiblingProfiles. Runs
//     unconditionally once triggered, even if step 4 above (the PRIMARY
//     row's own re-link) failed - the primary's re-link outcome has no
//     bearing on whether siblings were orphaned by the same rename.
//
// siblingFailures is the count of per-sibling failures/declines from step
// 5 (a pm.List failure counts as one), structurally propagated - not
// string-matched out of note - so the caller can add it straight into its
// own warnings counter. It's independent of err: siblingFailures can be
// non-zero whether or not the PRIMARY row's own repair (this function's
// err) succeeded, since a sibling's fate has no bearing on the primary's.
//
// mod.Version is mutated in place only once step 3's DB write has actually
// succeeded, so a failure partway through never leaves the in-memory row
// claiming a fix that didn't happen. If step 4 itself fails (e.g. a
// read-only game dir), the DB/profile version correction from steps 2-3 is
// NOT rolled back - retrying it would just repeat the same
// rename-already-done state - but Deployed is cleared and saved before
// returning the error: effective == mod.Version by then, so a later
// `verify --fix` no longer sees a version_mismatch to retry, and leaving
// Deployed true would falsely claim a working deployment that isn't
// there. Clearing it at least keeps that one field honest so other
// surfaces (status displays, a future `lmm deploy`, which redeploys every
// enabled mod regardless of its recorded Deployed value) aren't misled.
func repairModVersion(cmd *cobra.Command, svc *core.Service, game *domain.Game, profile string, mod *domain.InstalledMod, effective string) (note string, siblingFailures int, err error) {
	recorded := mod.Version
	gameCache := svc.GetGameCache(game)
	oldPath := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, recorded)
	newPath := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, effective)

	oldExists, statErr := cacheDirExists(oldPath)
	if statErr != nil {
		return "", 0, fmt.Errorf("checking cache entry for %s: %w", recorded, statErr)
	}
	newExists, statErr := cacheDirExists(newPath)
	if statErr != nil {
		return "", 0, fmt.Errorf("checking cache entry for %s: %w", effective, statErr)
	}

	var renamed bool
	if oldExists {
		if newExists {
			note = fmt.Sprintf("cache entry for %s already exists; left %s in place", effective, recorded)
		} else if renameErr := os.Rename(oldPath, newPath); renameErr != nil {
			return "", 0, fmt.Errorf("renaming cache entry: %w", renameErr)
		} else {
			renamed = true
		}
	}

	pm := getProfileManager(svc)
	if err := pm.UpsertMod(game.ID, profile, domain.ModReference{
		SourceID: mod.SourceID,
		ModID:    mod.ID,
		Version:  effective,
		FileIDs:  mod.FileIDs,
	}); err != nil {
		return note, 0, fmt.Errorf("updating profile record: %w", err)
	}

	if err := svc.SetModVersion(mod.SourceID, mod.ID, mod.GameID, profile, effective); err != nil {
		return note, 0, fmt.Errorf("saving installed mod: %w", err)
	}
	mod.Version = effective

	// A blocked rename (note != "") leaves the existing deployment pointed
	// at the still-intact recorded-version cache dir - nothing dangles, so
	// re-linking would only repoint a working deployment into the
	// unvetted pre-existing effective-version dir for no reason.
	var relinkErr error
	if note == "" && mod.Deployed && mod.LinkMethod == domain.LinkSymlink {
		if err := svc.GetInstaller(game).Install(cmd.Context(), game, &mod.Mod, profile); err != nil {
			mod.Deployed = false
			if saveErr := svc.SetModDeployed(mod.SourceID, mod.ID, mod.GameID, profile, false); saveErr != nil {
				relinkErr = fmt.Errorf("relinking deployed files: %w (also failed to clear deployed flag: %v)", err, saveErr)
			} else {
				relinkErr = fmt.Errorf("relinking deployed files: %w", err)
			}
		}
	}

	// Sibling repair runs regardless of the primary re-link's own outcome
	// (above) - a sibling row's orphaning is caused by the cache rename,
	// not by anything that happens to the primary row's deployment.
	if renamed || (!oldExists && newExists && note == "") {
		note, siblingFailures = repairSiblingProfiles(cmd, svc, game, profile, mod, recorded, effective)
	}

	if relinkErr != nil {
		return note, siblingFailures, relinkErr
	}

	return note, siblingFailures, nil
}

// fileIDsEqual reports whether two FileID sets are equal, ignoring order.
// Used by repairSiblingProfiles (epic98 audit Finding 6) to gate
// auto-repair on a sibling actually recording the SAME file selection as
// the primary, not just the same (wrong) version string - two profiles can
// legitimately record the same wrong version for DIFFERENT files (e.g. a
// different optional file chosen at install time), and blindly stamping
// the sibling with the primary's effective version would be wrong for
// that sibling's own files.
func fileIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, id := range a {
		counts[id]++
	}
	for _, id := range b {
		counts[id]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

// repairSiblingProfiles corrects every OTHER profile's installed_mods row
// for the same (SourceID, ModID, GameID) that still records the OLD
// (pre-repair) version, after repairModVersion has renamed the shared
// cache dir out from under them - cache.ModPath has no profile segment
// (internal/storage/cache/cache.go:29), so a rename done for one profile's
// row silently orphans any sibling profile still pointing at the old
// version, until that sibling is itself verified. Only called when the
// cache has actually moved to the effective version (this run's rename,
// or an earlier run's - see repairModVersion's step 5 doc); a blocked or
// never-attempted rename never orphans anything, so siblings are left
// alone in that case.
//
// A profile that never had the mod installed at all (GetInstalledMod
// returning domain.ErrModNotFound) is a normal, silent skip - it was never
// a repair candidate. Any OTHER lookup error (a genuine DB failure) is
// deliberately NOT treated the same way: it means we couldn't even tell
// whether that profile needed repair, which is surfaced as a per-sibling
// failure below rather than silently looking identical to "not a
// candidate".
//
// Sibling rows whose Version differs from recorded are a different state
// entirely (not something this repair caused) and are left untouched. A
// sibling whose Version MATCHES but whose FileIDs do NOT (audit Finding 6,
// via fileIDsEqual) is also left untouched - the recorded version is wrong
// for that sibling too, but auto-stamping it with the PRIMARY's effective
// version would be wrong when the underlying files differ - instead it's
// surfaced as its own warning pointing at the right manual fix
// (`verify --fix -p <profile>`).
//
// Like the primary path (repairModVersion), the profile YAML is upserted
// BEFORE the DB version is set (audit Finding 3): the DB row is what a
// LATER verify run's mismatch detection reads for that sibling too, so if
// the upsert fails, the DB must stay at the old, still-detectable version
// rather than silently converging to "no mismatch, nothing to retry".
// SetModVersion/SetModDeployed (never SaveInstalledMod's full-row upsert,
// which would wipe stored checksums - audit Finding 1) do the writes.
//
// A sibling Deployed via symlink has its links re-created through the
// installer exactly like the primary row's would be; on a per-sibling
// re-link failure, that row's Deployed flag alone is cleared (matching the
// primary row's own re-link-failure handling), but - unlike the DB/profile
// steps, which did succeed - this is reported as a FAILURE, not folded
// into "repaired": the version record is correct, but the deployment
// itself is still broken, so printing the same green "Repaired" line
// would misreport a partial fix as a clean success. Processing continues
// with the remaining siblings rather than aborting the whole pass.
//
// Per DEV.md's "never swallow errors without logging/context": a failure
// to correct one sibling (pm.List itself, or a given sibling's
// UpsertMod/SetModVersion/re-link) is NOT silently skipped - it's printed
// as a warning in text mode and folded into the returned summary, since a
// write or re-link failure here leaves that profile in exactly the
// orphaned/dangling state this whole repair exists to fix,
// indistinguishable from "was never a candidate" if left unreported. The
// loop still continues past a single sibling's failure - best-effort, not
// all-or-nothing.
//
// Returns a human-readable summary of which profiles were repaired,
// failed, and/or declined for differing file selection (empty if none),
// for the caller to fold into repairModVersion's own note - the --json
// contract's vehicle for surfacing this, per the primary row's existing
// "note" field - and, structurally (not by string-matching the note
// text), the combined count of per-sibling failures and declines, so the
// caller can add it straight into its own warnings counter: both are
// real, surfaced work left undone and must count as a warning regardless
// of how the PRIMARY row's own repair turned out. A pm.List failure
// counts as one.
func repairSiblingProfiles(cmd *cobra.Command, svc *core.Service, game *domain.Game, currentProfile string, mod *domain.InstalledMod, recorded, effective string) (note string, failedCount int) {
	pm := getProfileManager(svc)
	profiles, err := pm.List(game.ID)
	if err != nil {
		msg := fmt.Sprintf("could not enumerate profiles to check for shared-cache siblings: %v", err)
		if !jsonOutput {
			fmt.Printf("  Warning: %s\n", msg)
		}
		return "sibling repair check FAILED: " + msg, 1
	}

	var repaired, failed, differs []string
	for _, p := range profiles {
		if p.Name == currentProfile {
			continue
		}

		sibling, err := svc.GetInstalledMod(mod.SourceID, mod.ID, game.ID, p.Name)
		if err != nil {
			if !errors.Is(err, domain.ErrModNotFound) {
				// A genuine lookup failure (DB error, corruption, etc.) is
				// NOT the same as "this profile never had the mod" - the
				// latter is a normal, silent skip; the former means we
				// couldn't even tell whether this profile needed repair,
				// which must be surfaced the same as any other per-sibling
				// failure rather than looking indistinguishable from "not a
				// candidate".
				failed = append(failed, fmt.Sprintf("%s (checking sibling: %v)", p.Name, err))
				if !jsonOutput {
					fmt.Printf("  Warning: could not repair profile %s: %v\n", p.Name, err)
				}
			}
			continue
		}
		if sibling.Version != recorded {
			continue
		}
		if !fileIDsEqual(sibling.FileIDs, mod.FileIDs) {
			differs = append(differs, p.Name)
			if !jsonOutput {
				fmt.Printf("  Warning: profile %s records the same version but differs in file selection; run verify --fix -p %s\n", p.Name, p.Name)
			}
			continue
		}

		if err := pm.UpsertMod(game.ID, p.Name, domain.ModReference{
			SourceID: sibling.SourceID,
			ModID:    sibling.ID,
			Version:  effective,
			FileIDs:  sibling.FileIDs,
		}); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", p.Name, err))
			if !jsonOutput {
				fmt.Printf("  Warning: could not repair profile %s: %v\n", p.Name, err)
			}
			continue
		}

		if err := svc.SetModVersion(sibling.SourceID, sibling.ID, sibling.GameID, p.Name, effective); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", p.Name, err))
			if !jsonOutput {
				fmt.Printf("  Warning: could not repair profile %s: %v\n", p.Name, err)
			}
			continue
		}
		sibling.Version = effective

		if sibling.Deployed && sibling.LinkMethod == domain.LinkSymlink {
			if err := svc.GetInstaller(game).Install(cmd.Context(), game, &sibling.Mod, p.Name); err != nil {
				if saveErr := svc.SetModDeployed(sibling.SourceID, sibling.ID, sibling.GameID, p.Name, false); saveErr != nil {
					failed = append(failed, fmt.Sprintf("%s (relinking deployed files: %v; also failed to clear deployed flag: %v)", p.Name, err, saveErr))
				} else {
					failed = append(failed, fmt.Sprintf("%s (relinking deployed files: %v)", p.Name, err))
				}
				if !jsonOutput {
					fmt.Printf("  Warning: could not repair profile %s: %v\n", p.Name, err)
				}
				// The version correction (DB + profile, above) stands, but
				// the deployment itself is still broken - report this as a
				// failure, not a clean "Repaired" success.
				continue
			}
		}

		repaired = append(repaired, p.Name)
		if !jsonOutput {
			fmt.Printf("  %s\n", colorGreen(fmt.Sprintf("Repaired (profile %s): %s → %s", p.Name, recorded, effective)))
		}
	}

	var parts []string
	if len(repaired) > 0 {
		parts = append(parts, fmt.Sprintf("also repaired in profile(s): %s", strings.Join(repaired, ", ")))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("repair FAILED in profile(s): %s", strings.Join(failed, ", ")))
	}
	if len(differs) > 0 {
		parts = append(parts, fmt.Sprintf("differs in file selection in profile(s): %s (run verify --fix -p <profile>)", strings.Join(differs, ", ")))
	}
	return strings.Join(parts, "; "), len(failed) + len(differs)
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
