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
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

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
	Status  string `json:"status"`         // ok, missing, no_checksum, file_count_mismatch, skipped, version_mismatch, version_unverifiable, stale_compile
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
storing a fresh checksum afterwards. "OK (checksum populated)" is only
reported when a checksum was actually written; if the re-download
succeeds but yields no checksum to store (e.g. a directory-source mod
whose directory holds no regular files), the NO CHECKSUM warning remains
with a note explaining why. FILE COUNT MISMATCH and SKIPPED are not
repaired by --fix.

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
                                         (reinstall or 'lmm update')

For a compile-deploy game (e.g. Icarus), verify also compares each
compiled mod's recorded base-pak fingerprint against the game's live base
pak (#196, "the Friday problem" - a weekly base pak refresh silently
reverts a compiled mod's patched tables, with nothing to notice
otherwise):

    ? NAME - RECOMPILE NEEDED           the game's base pak has changed
                                         since this mod was compiled; run
                                         'lmm update' to recompile it

This check is entirely local (no source contacted) and applies to every
compiled mod regardless of source, including local imports. --fix does
not repair it - use 'lmm update' (or 'lmm update --all').

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

A locked mod's VERSION MISMATCH is still reported and counted the same as
any other (verify stays honest about state), but --fix refuses to repair
it - rewriting a locked record would silently move what the lock means
instead of fixing anything. The refusal names the mod and the lock's
target version, and points at the remedy: 'lmm mod lock' to move the
lock, or 'lmm mod unlock' to release it; in --json the row keeps
"version_mismatch" and gains note "locked". Separately, a locked mod whose
recorded version hasn't yet caught up to the lock's target (pending a
"lmm profile apply", not corruption) prints its own "~ NAME - lock pending
convergence" informational line instead of being silently folded into the
OK case - this is never counted in issues or warnings.

--json emits {game_id, profile, files: [{mod_id, mod_name, file_id,
status, note}], issues, warnings}; status is one of "ok", "missing",
"no_checksum", "file_count_mismatch", "skipped", "version_mismatch",
"version_unverifiable", or "stale_compile"; note adds detail where
there's something extra to say - a blocked cache rename, sibling-repair
results, a --fix repair or
redownload failure's reason, why a successful re-download stored no
checksum, a file-count-check lookup failure, a --fix refusal on a locked
record ("locked"), or a locked record's pending convergence detail - and
is omitted otherwise. issues counts MISSING files
and VERSION MISMATCH rows (a successful --fix repair of either decrements
it back out; a locked VERSION MISMATCH stays counted since --fix refuses
it); warnings counts everything else that isn't OK. Lock-pending-
convergence rows are informational only and count toward neither.

Examples:
  lmm verify --game skyrim-se           # Verify all mods
  lmm verify 12345 --game skyrim-se     # Verify specific mod
  lmm verify --fix --game skyrim-se     # Re-download missing files, populate checksums, repair version records`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyFix, "fix", false, "re-download missing files, fill missing checksums, repair version records")
	verifyCmd.Flags().StringVarP(&verifyProfile, "profile", "p", "", "profile to verify (default: active profile)")

	rootCmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doVerify(cmd, svc, game, args)
	})
}

// hasRetainedSource reports whether any of fileIDs has a retained compile
// source (cache.RetainedSourceName) on disk for sourceID/modID/version -
// the signal that a cache entry is a DeployCompile ".exmodz" validate+
// retain-only entry (#197 I4), which deploys zero files of its own by
// design and must not be flagged as a FILE COUNT MISMATCH.
func hasRetainedSource(gameCache *cache.Cache, gameID, sourceID, modID, version string, fileIDs []string) bool {
	for _, fileID := range fileIDs {
		retainedPath := gameCache.GetFilePath(gameID, sourceID, modID, version, cache.RetainedSourceName(fileID))
		if _, err := os.Stat(retainedPath); err == nil {
			return true
		}
	}
	return false
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
	// below and per-MOD increments from the version-record pre-pass - and
	// the pre-pass only counts its quiet-OK path (recorded version matches);
	// its skip/mismatch paths continue without counting. Both feed the same
	// "no files found" zero-check, so neither needed its own variable.
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
		// #197 I4 fix: a DeployCompile game's ".exmodz" mod is ingested as
		// validate+retain ONLY (Task 2/3) - it has zero deployment members
		// of its own by design (the shared merged pak, checked separately
		// above, is what actually deploys), so ListFiles == 0 here is
		// correct, healthy state, not a mismatch. Detected by checking
		// whether any of the mod's own FileIDs has a retained source on
		// disk - that is the one signal that distinguishes "this entry
		// deploys nothing on purpose" from a genuinely corrupted/emptied
		// cache entry (which the check below must still catch for every
		// OTHER deploy mode, and even for a compile-mode game's own plain
		// prebuilt ".pak" mods, which still deploy normally).
		if game.DeployMode == domain.DeployCompile && hasRetainedSource(gameCache, game.ID, mod.SourceID, mod.ID, mod.Version, mod.FileIDs) {
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
	// #97 (Task 8): load the profile once, up front, so the per-mod loop
	// below can look up each ref's lock state (Profile.FindRef) without
	// reloading the profile per mod. A missing/unreadable profile is
	// treated as unlocked - same tolerant precedent as ApplyUpdate's core
	// gate (internal/core/flows.go) and applySingleUpdate (cmd/lmm/update.go):
	// a lock cannot exist in a profile that can't be loaded, so FindRef's
	// nil-receiver-safe behavior on a nil *domain.Profile does the right
	// thing here without an extra guard.
	prof, _ := config.LoadProfile(svc.ConfigDir(), game.ID, profile)

	ctx := cmd.Context()
	installedMods, err := svc.GetInstalledMods(game.ID, profile)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// Merged-pak staleness check (#197, generalizing #196's per-mod
	// version): for a DeployCompile game, compare the profile's merged
	// pak's recorded fingerprint against the game's CURRENT enabled-mod
	// set/order/versions/base pak. Entirely local/offline. modFilter has no
	// effect here - the merged pak is profile-scoped, not per-mod, so
	// `lmm verify <mod-id>` still checks it (a single mod's own version
	// mismatch and the profile's overall merge staleness are independent
	// facts).
	if game.DeployMode == domain.DeployCompile {
		staleUpd, serr := svc.CheckMergedPakStaleness(game, profile)
		if serr != nil {
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{Status: "skipped", Note: fmt.Sprintf("could not check merged pak staleness: %v", serr)})
			} else {
				fmt.Printf("%s could not check merged pak staleness: %v\n", colorYellow("?"), serr)
			}
			warnings++
		}
		checked++
		if staleUpd != nil {
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: staleUpd.InstalledMod.ID, ModName: staleUpd.InstalledMod.Name, Status: "stale_compile"})
			} else {
				fmt.Printf("%s %s - RECOMPILE NEEDED (base pak updated - run 'lmm update' to fix)\n", colorYellow("?"), staleUpd.InstalledMod.Name)
			}
			warnings++
		}
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

		ref := prof.FindRef(mod.SourceID, mod.ID)

		sourceFiles, err := svc.GetModFiles(ctx, mod.SourceID, sourceMappedMod(game, &mod.Mod))
		if err != nil {
			if jsonOutput {
				// Copilot round 6 (PR #128): the source-unreachable reason
				// must reach --json too, not just the text-mode line below.
				// Status stays "skipped", and the note carries the reason;
				// the shared warnings++ below fires for both output modes,
				// exactly as it did before the note existed.
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
				fmt.Printf("%s %s - VERSION UNVERIFIABLE (recorded file ID(s) no longer found upstream; reinstall the mod or run 'lmm update' to adopt the current version)\n", colorYellow("?"), mod.Name)
			}
			warnings++
			continue
		}

		// When every matched file reports an empty Version (custom sources
		// whose mappings carry no per-file versions), the fallback inside
		// EffectiveInstalledVersion returns mod.Version and this comparison
		// passes vacuously. That is a deliberate quiet OK, not a missed
		// VERSION UNVERIFIABLE (PR #128 triage): install-time stamping
		// applies the same fallback, so a per-file version mis-stamp cannot
		// exist for versionless sources - there is nothing to verify, and
		// warning would flag every mod on every verify for such sources.
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
				// #97 (Task 8): a locked ref's Version is the lock's
				// TARGET, not a repairable mistake - rewriting it here
				// would silently move what the lock means instead of
				// fixing anything. The mismatch stays reported/counted
				// above (verify stays honest about state); only the
				// repair itself is refused.
				if ref != nil && ref.Locked {
					// #142 round 5: name the source/profile in both remedies -
					// same "copy-paste acts on the wrong target" fix already
					// applied to the core gates (internal/core/flows.go's
					// LockedRefRefusalError) and the sibling-repair warning
					// below - a bare 'lmm mod lock <id> <version>' would
					// resolve against the active profile/an ambiguous source
					// if this mod's lock lives elsewhere.
					refusal := fmt.Sprintf("--fix skipped: %s is locked at v%s in profile %s — the record is the lock's target; move the lock with 'lmm mod lock -s %s -p %s %s <version>' or unlock with 'lmm mod unlock -s %s -p %s %s' instead of rewriting it.", mod.Name, ref.Version, profile, mod.SourceID, profile, mod.ID, mod.SourceID, profile, mod.ID)
					if jsonOutput {
						// The Note field already exists on this contract
						// (repair-failure/rename-blocked detail) - this is
						// an additive use of it, not a new field. Kept
						// short ("locked") rather than the full refusal
						// sentence since a --json caller mainly needs the
						// machine-checkable reason; the sentence itself is
						// the text-mode surface.
						jsonFiles[len(jsonFiles)-1].Note = "locked"
					} else {
						fmt.Printf("  %s\n", refusal)
					}
					continue
				}
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
						// The PRIMARY row keeps reporting version_mismatch
						// (status/issues stay as already recorded above),
						// but the repair may have PARTIALLY succeeded: a
						// step-4 re-link failure surfaces here after the
						// cache rename and the DB/profile version
						// correction have already landed and are not
						// rolled back (see repairModVersion's doc). The
						// failure reason itself (audit Finding 7) and note
						// can still carry a successful SIBLING repair -
						// see repairModVersion's doc comment - and
						// dropping either here would make them invisible
						// to a --json caller even though they genuinely
						// happened.
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
		// printed per-line (same quiet-ok convention as the file loop below)
		// - UNLESS the mod is locked and the DB version hasn't yet
		// converged to the lock's target (ref.Version): that's expected
		// drift pending a `profile apply`, not corruption (#97 Task 8), so
		// it gets its own informational note instead of pure silence. Never
		// counted in issues (or warnings) - it isn't a problem.
		if ref != nil && ref.Locked && ref.Version != mod.Version {
			convergenceNote := fmt.Sprintf("lock pending convergence (installed v%s, locked v%s)", mod.Version, ref.Version)
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: "", Status: "ok", Note: convergenceNote})
			} else {
				fmt.Printf("~ %s — %s — run 'lmm profile apply'\n", mod.Name, convergenceNote)
			}
		}
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
				persisted, err := redownloadModFile(cmd, svc, game, profile, mod, f.FileID)
				switch {
				case err != nil:
					if jsonOutput {
						// audit Finding 7: the row was already appended
						// above (Status "missing") - the failure reason
						// must reach --json too, not just this text line.
						jsonFiles[len(jsonFiles)-1].Note = err.Error()
					} else {
						fmt.Printf("  Re-download failed: %v\n", err)
					}
				case persisted:
					if !jsonOutput {
						fmt.Printf("  %s\n", colorGreen("Re-downloaded OK"))
					} else {
						jsonFiles[len(jsonFiles)-1].Status = "ok"
					}
					issues--
				default:
					// The re-download restored the cache - the MISSING issue
					// is genuinely repaired - but no checksum was available
					// to store, so the row remains a NO CHECKSUM warning
					// (#164: don't report "ok" for a write that never
					// happened).
					if jsonOutput {
						jsonFiles[len(jsonFiles)-1].Status = "no_checksum"
						jsonFiles[len(jsonFiles)-1].Note = "re-downloaded, but no checksum was available to store"
					} else {
						fmt.Printf("  Re-downloaded, but no checksum was available to store - NO CHECKSUM remains\n")
					}
					issues--
					warnings++
				}
			}
			continue
		}

		// Check if checksum stored
		if f.Checksum == "" {
			if verifyFix && mod.SourceID != domain.SourceLocal {
				persisted, err := redownloadModFile(cmd, svc, game, profile, mod, f.FileID)
				switch {
				case err != nil:
					if jsonOutput {
						// audit Finding 7: the failure reason must reach
						// --json, not just the text-mode line below.
						jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: err.Error()})
					} else {
						fmt.Printf("%s %s (%s) - NO CHECKSUM\n", colorYellow("?"), mod.Name, f.FileID)
						fmt.Printf("  Re-download to populate checksum failed: %v\n", err)
					}
					warnings++
				case persisted:
					if jsonOutput {
						jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"})
					} else {
						fmt.Printf("%s %s (%s) - %s (checksum populated)\n", colorGreen("+"), mod.Name, f.FileID, colorGreen("OK"))
					}
				default:
					// The download succeeded but produced no checksum to
					// store - nothing was written, so the warning stands
					// with an honest reason (#164: "checksum populated" was
					// a lie here, and the summary lied with it).
					if jsonOutput {
						jsonFiles = append(jsonFiles, verifyFileJSON{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: "re-downloaded, but no checksum was available to store"})
					} else {
						fmt.Printf("%s %s (%s) - NO CHECKSUM\n", colorYellow("?"), mod.Name, f.FileID)
						fmt.Printf("  Re-downloaded, but no checksum was available to store\n")
					}
					warnings++
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

	// #197 postsmoke seam-audit fix: --fix can repair a VERSION MISMATCH
	// (repairModVersion moves the cache dir and the recorded version) or
	// redownload a file whose content has since changed upstream
	// (redownloadModFile) - both are merge-fingerprint inputs with no
	// other seam to catch them. No-op when --fix wasn't passed (nothing
	// mutated) or the game isn't DeployCompile (SyncMergedPak's own guard).
	if verifyFix {
		if syncWarnings, syncErr := svc.SyncMergedPak(cmd.Context(), game, profile); syncErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not sync merged pak: %v\n", syncErr)
		} else {
			for _, w := range syncWarnings {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
			}
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
//     now dangling - re-run the installer to refresh them, built from the
//     PROFILE-effective link method and recording it on the row
//     (relinkDeployedRow, #152) - not the game-level method, which would
//     "repair" a profile-level copy/hardlink override back to the game's.
//     Hardlink/copy deployments are untouched by the cache rename, so
//     they're left alone regardless. On failure, SetModDeployed (not
//     SaveInstalledMod - audit Finding 1 again) clears the flag without
//     touching file IDs.
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
	var relinkNotes []string
	if note == "" && mod.Deployed && mod.LinkMethod == domain.LinkSymlink {
		installErr, recordErr, undeployErr := relinkDeployedRow(cmd, svc, game, profile, mod)
		if undeployErr != nil {
			// Non-fatal (see relinkDeployedRow's doc) - but surfaced in
			// BOTH output modes (PR #154 Copilot): text here, and folded
			// into the row's note below (after the sibling pass, which
			// overwrites note wholesale), so a --json caller sees the
			// partial cleanup a human would be shown.
			relinkNotes = append(relinkNotes, fmt.Sprintf("undeploy before re-link: %v", undeployErr))
			if !jsonOutput {
				fmt.Printf("  Warning: undeploy %s: %v\n", mod.Name, undeployErr)
			}
		}
		if installErr != nil {
			mod.Deployed = false
			if saveErr := svc.SetModDeployed(mod.SourceID, mod.ID, mod.GameID, profile, false); saveErr != nil {
				relinkErr = fmt.Errorf("relinking deployed files: %w (also failed to clear deployed flag: %v)", installErr, saveErr)
			} else {
				relinkErr = fmt.Errorf("relinking deployed files: %w", installErr)
			}
		} else if recordErr != nil {
			// Non-fatal, like DeployProfile's own SetModLinkMethod failure
			// handling: the deployment itself is fixed; only the recorded
			// method is stale. Same two-mode surfacing as the undeploy
			// warning above.
			msg := fmt.Sprintf("could not record link method: %v", recordErr)
			relinkNotes = append(relinkNotes, msg)
			if !jsonOutput {
				fmt.Printf("  Warning: %s\n", msg)
			}
		}
	}

	// Sibling repair runs regardless of the primary re-link's own outcome
	// (above) - a sibling row's orphaning is caused by the cache rename,
	// not by anything that happens to the primary row's deployment.
	if renamed || (!oldExists && newExists && note == "") {
		note, siblingFailures = repairSiblingProfiles(cmd, svc, game, profile, mod, recorded, effective)
	}

	if len(relinkNotes) > 0 {
		joined := strings.Join(relinkNotes, "; ")
		if note != "" {
			note += "; " + joined
		} else {
			note = joined
		}
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
// A sibling whose ref is LOCKED in its OWN profile (checked fresh per
// sibling, since the lock lives in that sibling's profile YAML, not the
// primary's) is likewise left untouched, for the same reason the primary
// row's own repair refuses a locked ref (repairModVersion's caller,
// verify.go:362-377): the record IS that sibling's lock target, and
// rewriting it here would silently move what the lock means just as surely
// as rewriting the primary would - circumventing verify's own primary-row
// refusal by going around it through a sibling. Surfaced as its own
// warning naming the sibling profile, distinct from the "differs" decline.
// The warning names the MOD as locked (matching the primary refusal's own
// wording), not the profile - a profile isn't "locked", a ref in it is -
// and gives its remedies with an explicit -p <sibling> flag, since
// `lmm mod lock`/`lmm mod unlock` without one resolve against the
// active/-p profile (the PRIMARY here), and copy-pasting an unflagged
// remedy would move/clear the lock in the wrong profile. When the sibling
// was Deployed, the primary repair has already renamed the shared cache
// dir out from under it (repairSiblingProfiles' own doc above) and this
// decline bypasses the re-link that would otherwise follow - so the
// warning also says the sibling's deployment may be broken until the lock
// is moved or cleared, since `verify --fix -p <sibling>` will decline the
// same way until then.
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
// installer exactly like the primary row's would be - built from that
// sibling's OWN profile-effective link method (relinkDeployedRow, #152),
// since a link_method override lives per-profile and the sibling's may
// differ from both the game's and the primary profile's; on a per-sibling
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
// failed, and/or declined (for differing file selection or a lock - empty
// if none), for the caller to fold into repairModVersion's own note - the --json
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

	var repaired, failed, differs, locked, methodNotes, undeployWarns []string
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

		// #97: a sibling ref locked in ITS OWN profile refuses the same
		// rewrite the primary row's own repair refuses (verify.go:362-377) -
		// the record is that sibling's lock target, and rewriting it here
		// would silently move what the lock means, just as surely as
		// rewriting the primary would. Loaded fresh per-sibling since the
		// lock lives in that sibling's own profile YAML, not the primary's.
		if siblingProfile, perr := pm.Get(game.ID, p.Name); perr == nil {
			if ref := siblingProfile.FindRef(sibling.SourceID, sibling.ID); ref != nil && ref.Locked {
				locked = append(locked, p.Name)
				if !jsonOutput {
					// #142 round 5: also name -s (in addition to the -p this
					// warning already carried) - resolveSource must otherwise
					// disambiguate on its own whenever more than one source
					// is configured, which a copy-pasted remedy shouldn't
					// depend on.
					msg := fmt.Sprintf("  Warning: %s is locked at v%s in profile %s; run 'lmm mod lock -s %s -p %s %s <version>' or unlock with 'lmm mod unlock -s %s -p %s %s' instead of rewriting it", sibling.Name, ref.Version, p.Name, sibling.SourceID, p.Name, sibling.ID, sibling.SourceID, p.Name, sibling.ID)
					if sibling.Deployed {
						msg += "; its deployment may be broken until the lock is moved or cleared"
					}
					fmt.Println(msg)
				}
				continue
			}
		}
		// (A missing/unreadable sibling profile falls through - matches
		// ApplyUpdate/ApplyRollback's own precedent: a lock cannot exist in
		// an unloadable profile.)

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
			installErr, recordErr, undeployErr := relinkDeployedRow(cmd, svc, game, p.Name, sibling)
			if undeployErr != nil {
				// Non-fatal (see relinkDeployedRow's doc), but surfaced in
				// both output modes (PR #154 Copilot) via its own parts
				// entry below - reported BEFORE the install-failure branch
				// so it reaches the note either way. Not counted in
				// failedCount: with a successful install nothing is left
				// broken, and a failed one already counts itself.
				undeployWarns = append(undeployWarns, fmt.Sprintf("%s (%v)", p.Name, undeployErr))
				if !jsonOutput {
					fmt.Printf("  Warning: undeploy %s in profile %s: %v\n", sibling.Name, p.Name, undeployErr)
				}
			}
			if installErr != nil {
				if saveErr := svc.SetModDeployed(sibling.SourceID, sibling.ID, sibling.GameID, p.Name, false); saveErr != nil {
					failed = append(failed, fmt.Sprintf("%s (relinking deployed files: %v; also failed to clear deployed flag: %v)", p.Name, installErr, saveErr))
				} else {
					failed = append(failed, fmt.Sprintf("%s (relinking deployed files: %v)", p.Name, installErr))
				}
				if !jsonOutput {
					fmt.Printf("  Warning: could not repair profile %s: %v\n", p.Name, installErr)
				}
				// The version correction (DB + profile, above) stands, but
				// the deployment itself is still broken - report this as a
				// failure, not a clean "Repaired" success.
				continue
			}
			if recordErr != nil {
				// Non-fatal (DeployProfile's own precedent): the sibling's
				// deployment is fixed; only its recorded method is stale.
				// Not counted in failedCount - unlike the failures above,
				// nothing here is left broken on disk - but still surfaced
				// in both output modes via its own parts entry below.
				methodNotes = append(methodNotes, fmt.Sprintf("%s (%v)", p.Name, recordErr))
				if !jsonOutput {
					fmt.Printf("  Warning: could not record link method for profile %s: %v\n", p.Name, recordErr)
				}
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
	if len(locked) > 0 {
		parts = append(parts, fmt.Sprintf("locked in profile(s): %s (move the lock or unlock instead)", strings.Join(locked, ", ")))
	}
	if len(methodNotes) > 0 {
		parts = append(parts, fmt.Sprintf("could not record link method in profile(s): %s", strings.Join(methodNotes, ", ")))
	}
	if len(undeployWarns) > 0 {
		parts = append(parts, fmt.Sprintf("undeploy warning in profile(s): %s", strings.Join(undeployWarns, ", ")))
	}
	return strings.Join(parts, "; "), len(failed) + len(differs) + len(locked)
}

// relinkDeployedRow re-runs the installer for a row recorded as a symlink
// deployment whose links dangle after a cache re-key, building the installer
// from profileName's EFFECTIVE link method (profile > game > global, #81) -
// not the game-level one (#152): a profile-level copy/hardlink override
// would otherwise be "repaired" back to the game's method, re-introducing
// exactly the drift verify exists to fix. The method is resolved once (the
// same single-resolve shape as DeployProfile, internal/core/flows.go) and,
// on a successful install, recorded on the row via SetModLinkMethod exactly
// like DeployProfile records its own deploys - the row was just re-linked
// with the effective method, and leaving it claiming the old one would be a
// new record-vs-reality drift. The write is skipped when the method didn't
// change; on success mod.LinkMethod is updated in place.
//
// The helper does no reporting of its own - the caller owns it all, since
// the primary and sibling sites surface things differently. installErr is
// the Install failure (the caller also owns clearing Deployed);
// undeployErr is an Uninstall failure, which is deliberately NOT fatal
// (DeployProfile's own precedent): one that still lets Install succeed
// left nothing broken - every file was rewritten - and one that does break
// Install surfaces through installErr too, so undeployErr can accompany
// installErr as context. recordErr is a SetModLinkMethod failure after a
// SUCCESSFUL install, likewise non-fatal: the deployment itself is fixed,
// only the recorded method is stale, and failing the whole repair over it
// would misreport a fixed deployment as broken. installErr and recordErr
// are mutually exclusive. A GetEffectiveLinkMethod failure (#189: an invalid
// profile link_method) is reported as installErr too - it happens before
// anything is touched, same as any other reason no method could be
// resolved to install with.
func relinkDeployedRow(cmd *cobra.Command, svc *core.Service, game *domain.Game, profileName string, mod *domain.InstalledMod) (installErr, recordErr, undeployErr error) {
	method, err := svc.GetEffectiveLinkMethod(game, profileName)
	if err != nil {
		return err, nil, nil
	}
	installer := svc.NewInstallerWithLinker(game, svc.GetLinker(method))
	// Undeploy-then-install, the same shape DeployProfile uses
	// (internal/core/flows.go) and for the same reason: dst still holds
	// whatever the previous deployment left behind (here, the dangling
	// symlinks the cache re-key orphaned), and only the symlink and
	// hardlink linkers' Deploy clear an existing dst themselves - the copy
	// linker's OpenFile would follow (or trip over) a stale symlink instead
	// of replacing it.
	undeployErr = installer.Uninstall(cmd.Context(), game, &mod.Mod, profileName)
	if err := installer.Install(cmd.Context(), game, &mod.Mod, profileName); err != nil {
		return err, nil, undeployErr
	}
	if method != mod.LinkMethod {
		if err := svc.SetModLinkMethod(mod.SourceID, mod.ID, mod.GameID, profileName, method); err != nil {
			return nil, err, undeployErr
		}
		mod.LinkMethod = method
	}
	return nil, nil, undeployErr
}

// redownloadModFile re-downloads a single mod file and extracts it to the
// cache, then updates the checksum in the DB. persisted reports whether a
// checksum row was actually written: a download can succeed while yielding no
// checksum to store (e.g. a directory-source mod whose directory holds no
// regular files), and callers must not report "checksum populated" on the
// strength of a nil error alone (#164) - only when persisted is true.
func redownloadModFile(cmd *cobra.Command, svc *core.Service, game *domain.Game, profile string, mod *domain.InstalledMod, fileID string) (persisted bool, err error) {
	ctx := cmd.Context()
	files, err := svc.GetModFiles(ctx, mod.SourceID, sourceMappedMod(game, &mod.Mod))
	if err != nil {
		return false, fmt.Errorf("getting mod files: %w", err)
	}
	var downloadFile *domain.DownloadableFile
	for i := range files {
		if files[i].ID == fileID {
			downloadFile = &files[i]
			break
		}
	}
	if downloadFile == nil {
		return false, fmt.Errorf("file %s not found in mod", fileID)
	}
	result, err := svc.DownloadMod(ctx, mod.SourceID, game, &mod.Mod, downloadFile, nil)
	if err != nil {
		return false, err
	}
	if result.Checksum == "" {
		return false, nil
	}
	if err := svc.SaveFileChecksum(mod.SourceID, mod.ID, game.ID, profile, fileID, result.Checksum); err != nil {
		return false, fmt.Errorf("saving checksum: %w", err)
	}
	return true, nil
}
