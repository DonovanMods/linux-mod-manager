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
	Status  string `json:"status"`         // ok, missing, no_checksum, file_count_mismatch, skipped, version_mismatch, version_unverifiable, stale_compile, stale_deployment, fixed_stale_deployment, conversion_failed, needs_reingest, fixed_needs_reingest
	Note    string `json:"note,omitempty"` // optional detail: a blocked cache rename, sibling-repair results, a --fix repair/redownload failure reason, a file-count-check lookup failure, a stale-deployment reason ("no longer provided by <source>/<mod>" | "dangling link into lmm cache"), a convergence per-item error (e.g. an unsafe deployed-file record skipped), a pak-conversion failure reason (conversion_failed), or why/whether a pak needed re-ingesting (needs_reingest / fixed_needs_reingest) - omitted when there's nothing extra to add
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
    ? PATH - STALE DEPLOYMENT (reason)  a game-dir file/link no installed mod
                                         provides anymore, or a dangling
                                         symlink into the lmm cache

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

    ? NAME - RECOMPILE NEEDED           the merged pak's inputs changed
                                         (base pak update, or missing from
                                         the game directory); run 'lmm
                                         update --all' to fix it

This check is entirely local (no source contacted) and applies to every
compiled mod regardless of source, including local imports. Use --fix to
repair it: it resyncs the profile's merged pak (recompiling and
redeploying it if needed), the same repair 'lmm update --all' applies.

For a pak-to-exmod-conversion game (#221), verify also surfaces two more
compile-only states:

    ? NAME - CONVERSION FAILED (reason)  the mod's prebuilt .pak could not
                                          be converted into the merged pak
                                          on the last sync; it stays
                                          raw-deployed instead. Fix the mod
                                          or run 'lmm mod convert <mod-id> off'
                                          to silence it.
    ? NAME (FILE) - NEEDS REINGEST       the mod's pak was cached before
                                          conversion support existed (no
                                          retained source); run 'lmm verify
                                          --fix' to re-ingest it, or
                                          re-import the archive for a local
                                          mod

CONVERSION FAILED is read straight from the merged pak's stored
fingerprint (the outcome of the last successful sync) - it is not
recomputed by verify itself, so it stays accurate even between syncs.
NEEDS REINGEST only fires for a convert-eligible pak (the game and the mod
both have conversion enabled) whose cache entry predates #221. --fix
re-ingests it via the same redownload path MISSING uses; a local/imported
mod has no source to redownload from and must be re-imported instead. A
successful --fix re-ingest flips the row to "fixed_needs_reingest" in
--json (a resolved problem, same convention as "fixed_stale_deployment"
below) instead of leaving it reading as still-outstanding in the very run
that just fixed it, and is not counted as a warning.

verify also reconciles the game directory's deployed state against
current reality (#168/#212): a deployed_files record no longer provided
by any installed mod, or a dangling symlink into the lmm cache with no
owning record at all, is reported as STALE DEPLOYMENT. Plain verify only
reports candidates - nothing is touched. With --fix, verify REMOVES
these: the stale deployed file/link itself, and any dangling lmm-cache
symlink with no record. This is provenance-gated - only content lmm
itself deployed or is tracking is ever touched, never a foreign file
just sitting in the game directory. Like the merged-pak staleness check
above, this is profile-wide and still runs even when you pass a specific
mod-id filter - and it runs even when the profile has no installed mods
at all, since a game dir can still hold stray lmm-deployed files after
everything is uninstalled (#217).

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
"version_unverifiable", "stale_compile", "stale_deployment",
"fixed_stale_deployment", "conversion_failed", "needs_reingest", or
"fixed_needs_reingest"; note adds detail where there's something extra to
say - a blocked cache rename, sibling-repair results, a --fix repair or
redownload failure's reason, why a successful re-download stored no
checksum, a file-count-check lookup failure, a --fix refusal on a locked
record ("locked"), a locked record's pending convergence detail, a
stale-deployment row's reason (populated on both "stale_deployment" and
"fixed_stale_deployment"), a pak's conversion-failure reason
("conversion_failed"), or why/whether a pak needed re-ingesting
(populated on both "needs_reingest" and "fixed_needs_reingest") - and is
omitted otherwise. issues counts MISSING files and VERSION MISMATCH rows
(a successful --fix repair of either decrements it back out; a locked
VERSION MISMATCH stays counted since --fix refuses it); warnings counts
everything else that isn't OK, including "stale_deployment",
"conversion_failed", and "needs_reingest" rows (never
"fixed_stale_deployment" or "fixed_needs_reingest" - a successful --fix
removal/re-ingest is a resolved problem, not an outstanding one, the same
convention as a successful re-download or version repair). Lock-pending-convergence rows
are informational only and count toward neither.

Examples:
  lmm verify --game skyrim-se           # Verify all mods
  lmm verify 12345 --game skyrim-se     # Verify specific mod
  lmm verify --fix --game skyrim-se     # Re-download missing files, populate checksums, repair version records`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyFix, "fix", false, "re-download missing files, fill missing checksums, repair version records, remove stale lmm-deployed files")
	verifyCmd.Flags().StringVarP(&verifyProfile, "profile", "p", "", "profile to verify (default: active profile)")

	rootCmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doVerify(cmd, svc, game, args)
	})
}

// doVerify is a thin renderer over the core verify engine (#224 Task 7):
// it resolves the profile/mod-filter, calls svc.Verify, and either streams
// core.VerifyEvent progress through renderVerifyEvent (text mode) or maps
// the final core.VerifyResult straight to the --json contract (json mode -
// events carry no rows of their own there, see the callback below). Every
// piece of verify's actual logic - the per-file/per-mod checks, the --fix
// repairs, the deploy-convergence sweep - now lives in
// internal/core/verify.go, verify_repair.go, and verify_helpers.go
// (#224 Tasks 2-6); this function owns none of it anymore.
func doVerify(cmd *cobra.Command, svc *core.Service, game *domain.Game, args []string) error {
	profile, err := resolveProfile(svc, game.ID, verifyProfile)
	if err != nil {
		return err
	}

	var modFilter string
	if len(args) > 0 {
		modFilter = args[0]
	}

	opts := core.VerifyOptions{Tier: core.VerifyFull, Fix: verifyFix, ModFilter: modFilter}
	result, err := svc.Verify(cmd.Context(), game, profile, opts, func(e core.Event) {
		ev, ok := e.(core.VerifyEvent)
		if !ok {
			return
		}
		if jsonOutput {
			if ev.Kind == core.VerifyEvSyncWarning {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", ev.Detail) // stderr never corrupts the JSON document
			}
			return // rows come from result.Findings below
		}
		renderVerifyEvent(ev)
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		// Always a non-nil slice (even with zero findings, e.g. an
		// empty-profile run with nothing stale to report) so --json's
		// "files" key encodes as [] rather than null.
		jsonFiles := make([]verifyFileJSON, 0, len(result.Findings))
		for _, f := range result.Findings {
			jsonFiles = append(jsonFiles, verifyFileJSON{ModID: f.ModID, ModName: f.ModName, FileID: f.FileID, Status: f.Status, Note: f.Note})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(verifyJSONOutput{GameID: game.ID, Profile: profile, Files: jsonFiles, Issues: result.Issues, Warnings: result.Warnings}); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return nil
	}

	if !result.HasFiles {
		// #217: the empty-profile path ran nothing but the deploy-
		// convergence sweep - no checksummed files means no issues are
		// possible, only whatever stale-deployment candidates (plain) or
		// fixes (--fix) that sweep found.
		if result.Warnings > 0 {
			fmt.Println()
			fmt.Printf("0 issue(s), %d warning(s) found.\n", result.Warnings)
			if !verifyFix {
				fmt.Println("Run with --fix to remove stale lmm-deployed files.")
			}
		}
		return nil
	}

	fmt.Println()

	if result.Checked == 0 && modFilter != "" {
		fmt.Printf("No files found for mod %s\n", modFilter)
		return nil
	}

	if result.Issues > 0 || result.Warnings > 0 {
		fmt.Printf("%d issue(s), %d warning(s) found.\n", result.Issues, result.Warnings)
		if !verifyFix {
			fmt.Println("Run with --fix to re-download missing files, populate missing checksums, repair version-record mismatches, and remove stale lmm-deployed files.")
		}
	} else {
		fmt.Println(colorGreen("All files verified OK."))
	}

	return nil
}

// renderVerifyEvent prints ev's text-mode line(s), reproducing every format
// string the pre-#224-Task-7 doVerify printed inline at the matching site -
// copied verbatim from the deleted CLI code, not from memory. Only reached
// in text mode (doVerify's progress callback keeps json mode from ever
// calling this except indirectly via VerifyEvSyncWarning's own stderr
// write, which is unconditional in BOTH modes - see below).
func renderVerifyEvent(ev core.VerifyEvent) {
	switch ev.Kind {
	case core.VerifyEvBegin:
		if ev.HasFiles {
			fmt.Println("Verifying cached mods...")
			fmt.Println()
		} else {
			fmt.Println("No installed mods to verify.")
		}

	case core.VerifyEvFinding:
		renderVerifyFinding(ev)

	case core.VerifyEvRepairDetail:
		if ev.Fixed {
			fmt.Printf("  %s\n", colorGreen(ev.Detail))
		} else {
			fmt.Printf("  %s\n", ev.Detail)
		}

	case core.VerifyEvSyncWarning:
		// Unconditional stderr write in both text and json mode (ported
		// from doVerify's fix-mode SyncMergedPak call, originally
		// cmd/lmm/verify.go:811-825) - doVerify's own callback already
		// handles this case for json mode before ever reaching
		// renderVerifyEvent, so this arm only actually fires in text mode,
		// but mirrors the exact same write for symmetry with the engine's
		// "always stderr, never stdout" contract for this event.
		fmt.Fprintf(os.Stderr, "Warning: %s\n", ev.Detail)

	case core.VerifyEvVerbose:
		if verbose {
			fmt.Printf("  (verbose) %s\n", ev.Detail)
		}

	case core.VerifyEvProgress:
		// Not rendered by the CLI (#224).
	}
}

// renderVerifyFinding prints ev.Finding's main line, keyed on
// Finding.Status plus whatever extras that status's site relies on
// (Recorded/Effective/Version/ExpectedCount/ChecksumPopulated - see
// VerifyEvent's own doc comment). Every branch is copied verbatim from the
// deleted CLI's inline fmt.Printf calls.
func renderVerifyFinding(ev core.VerifyEvent) {
	f := ev.Finding
	switch f.Status {
	case "skipped":
		renderVerifySkipped(f)

	case "file_count_mismatch":
		fmt.Printf("! %s - FILE COUNT MISMATCH (expected content from %d download(s), cache has 0 files)\n", f.ModName, ev.ExpectedCount)

	case "needs_reingest":
		fmt.Printf("%s %s (%s) - NEEDS REINGEST (%s)\n", colorYellow("?"), f.ModName, f.FileID, f.Note)

	case "missing":
		fmt.Printf("%s %s (%s) - MISSING (version %s not in cache)\n", colorRed("X"), f.ModName, f.FileID, ev.Version)

	case "no_checksum":
		fmt.Printf("%s %s (%s) - NO CHECKSUM\n", colorYellow("?"), f.ModName, f.FileID)

	case "ok":
		switch {
		case ev.ChecksumPopulated:
			// #164: only printed when a checksum was actually written by a
			// --fix redownload.
			fmt.Printf("%s %s (%s) - %s (checksum populated)\n", colorGreen("+"), f.ModName, f.FileID, colorGreen("OK"))
		case f.FileID != "":
			// Cache exists and a checksum was already stored - the plain
			// per-file OK line.
			fmt.Printf("%s %s (%s) - %s\n", colorGreen("+"), f.ModName, f.FileID, colorGreen("OK"))
		case f.Note != "":
			// #97: a locked ref whose recorded version hasn't yet
			// converged to the lock's target - informational, never
			// counted as an issue or warning.
			fmt.Printf("~ %s — %s — run 'lmm profile apply'\n", f.ModName, f.Note)
			// The quiet-OK case (matches, unlocked or already converged)
			// never reaches here at all - versionPass doesn't emit a
			// finding for it.
		}

	case "version_unverifiable":
		fmt.Printf("%s %s - VERSION UNVERIFIABLE (recorded file ID(s) no longer found upstream; reinstall the mod or run 'lmm update' to adopt the current version)\n", colorYellow("?"), f.ModName)

	case "version_mismatch":
		fmt.Printf("%s %s - VERSION MISMATCH (recorded %s, source reports %s)\n", colorRed("X"), f.ModName, ev.Recorded, ev.Effective)

	case "stale_compile":
		fmt.Printf("%s %s - RECOMPILE NEEDED (%s - run 'lmm update --all' to fix)\n", colorYellow("?"), f.ModName, f.Note)

	case "conversion_failed":
		fmt.Printf("  %s %s - CONVERSION FAILED (%s) - deploying raw; fix the mod or run 'lmm mod convert %s off' to silence\n", colorYellow("?"), f.ModName, f.Note, f.ModID)

	case "stale_deployment":
		// #168/#212 - FileID carries the deployed path (convergeDeployedFiles
		// reports per-path, not per-mod-file).
		fmt.Printf("%s %s - STALE DEPLOYMENT (%s)\n", colorYellow("?"), f.FileID, f.Note)

	case "fixed_stale_deployment":
		// The WHOLE line is green, keyed on this Status alone (no event
		// field involved) - unlike a version repair, which prints a plain
		// main line and a separate green sub-line, a --fix stale-deployment
		// removal has no main line of its own to have printed first.
		fmt.Println(colorGreen(fmt.Sprintf("Fixed: removed %s (%s)", f.FileID, f.Note)))
	}
}

// renderVerifySkipped disambiguates the several distinct "skipped" finding
// sites the verify engine emits (fileCountPrePass's two lookup failures,
// perFileWalk's unknown-mod row, versionPass's source-unreachable row,
// mergedPakStalenessPass's own check failure, and convergencePass's
// per-item failures) - all share one Status string but the pre-#224
// doVerify printed each with different wording. The dispatch is structural
// (which Finding fields are populated) except for three Note prefixes,
// which the emitting passes format deliberately for exactly this purpose:
// their text-mode line drops the prefix for a friendlier phrase (or reuses
// it verbatim), while their --json Note keeps the full formatted string.
// Order matters - the prefix checks must run before the two ModName-based
// fallbacks below them, since a genuine lookup-failure error string could
// otherwise coincidentally start the same way.
func renderVerifySkipped(f core.VerifyFinding) {
	switch {
	// perFileWalk: a checksum row references a mod no longer installed
	// (originally doVerify's "Unknown mod %s - SKIPPED").
	case f.Note == "" && f.FileID != "":
		fmt.Printf("? Unknown mod %s - SKIPPED\n", f.ModID)

	// versionPass: GetModFiles failed (source unreachable) - text mode
	// deliberately drops the error detail; --json's Note carries it.
	case strings.HasPrefix(f.Note, "could not check version: "):
		fmt.Printf("%s %s - could not check version (source unreachable)\n", colorYellow("?"), f.ModName)

	// convergencePass: a per-item convergeDeployedFiles failure - Note is
	// already the full "convergence: <err>" text.
	case strings.HasPrefix(f.Note, "convergence: "):
		fmt.Printf("%s %s\n", colorYellow("?"), f.Note)

	// mergedPakStalenessPass: CheckMergedPakStaleness itself failed - Note
	// is already the full message.
	case strings.HasPrefix(f.Note, "could not check merged pak staleness: "):
		fmt.Printf("%s %s\n", colorYellow("?"), f.Note)

	// fileCountPrePass: the installed-mod lookup itself failed (a genuine
	// DB error, not "not installed" - that's ErrModNotFound, a silent skip
	// the engine never reports at all) - ModName is blank since no mod row
	// was found to name.
	case f.ModName == "":
		fmt.Printf("%s Mod %s - could not check file count: %s\n", colorYellow("?"), f.ModID, f.Note)

	// fileCountPrePass: ListFiles on an existing cache dir failed.
	default:
		fmt.Printf("%s %s - could not check cached file count: %s\n", colorYellow("?"), f.ModName, f.Note)
	}
}
