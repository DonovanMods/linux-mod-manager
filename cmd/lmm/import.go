package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	importProfile   string
	importSource    string
	importModID     string
	importForce     bool
	importDryRun    bool
	importSkipMatch bool
)

var importCmd = &cobra.Command{
	Use:   "import [archive-path]",
	Short: "Import mods from local files or scan mod_path",
	Long: `Import mods from local files or scan for untracked mods.

Two distinct modes, chosen by whether an archive path is given:

Scan mode (no arguments): scans the game's mod_path for files not yet
tracked by lmm, tries to match each one by name against the game's
configured sources (skip with --skip-match), and imports whatever is
left after confirmation. Useful for mods that were installed manually -
e.g. mods whose author has disabled API downloads. --skip-match only
applies to this mode. Every mod imported this way is
marked as requiring manual download (since lmm did not fetch it itself);
re-link it to a source with 'lmm mod edit --source' to clear that once
it can be checked for updates normally.

Archive mode (an archive path given): imports that one specific mod file,
deploying it and adding it to the profile. Pass --id (with --source, or
it resolves automatically when the game has exactly one configured
source, or prompts interactively when it has several) to fetch and
attach source metadata as part of the import. --dry-run previews it -
the archive is listed, never extracted, so nothing is written.

Either way, a mod that ends up unmatched to any remote source is
imported as local - it deploys and installs normally, but 'lmm update'
has nothing to check it against and will never notify about it.

Examples:
  lmm import --game hytale                    # Scan mod_path for untracked mods
  lmm import --game hytale --dry-run          # Preview what would be imported
  lmm import --game hytale --skip-match       # Scan without source lookup
  lmm import ./my-mod.zip --game skyrim-se    # Import specific archive
  lmm import ./mod-12345-1-0.7z --game skyrim-se --profile survival
  lmm import ./mod.zip --game skyrim-se --id 12345 --source curseforge`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().StringVarP(&importProfile, "profile", "p", "", "profile to import to (default: active profile)")
	importCmd.Flags().StringVarP(&importSource, "source", "s", "", "source for update tracking (default: auto-detect or local)")
	importCmd.Flags().StringVar(&importModID, "id", "", "mod ID for linking to source (source resolves automatically; see --source)")
	importCmd.Flags().BoolVarP(&importForce, "force", "f", false, "import without conflict prompts")
	importCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "preview what would be imported without making changes")
	importCmd.Flags().BoolVar(&importSkipMatch, "skip-match", false, "skip source lookup for untracked mods")

	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doImport(ctx, cmd, service, game, args)
	})
}

// doImport renders `lmm import <archive>`: it validates the argument,
// resolves --source when only --id was given (that resolution can prompt,
// so it stays here), plans the import, renders the plan's readout, answers
// the conflict question from it, and applies it - printing every line from
// the event stream and the returned result. The engine itself lives in
// internal/core/import_archive.go.
func doImport(ctx context.Context, cmd *cobra.Command, service *core.Service, game *domain.Game, args []string) error {
	profileName, err := resolveProfile(ctx, service, game.ID, importProfile)
	if err != nil {
		return err
	}

	// No args = scan mode
	if len(args) == 0 {
		return runImportScan(cmd, game, service, profileName)
	}

	// Single arg = import specific archive
	archivePath := args[0]

	// Validate archive exists
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("archive not found: %s", archivePath)
	}

	// If --id is provided without --source, resolve dynamically: a sole
	// configured source auto-selects, several prompt interactively - the
	// same resolveSource semantics deploy/search/update/mod already use.
	if importModID != "" && importSource == "" {
		var err error
		importSource, err = resolveSource(service, game, importSource, false)
		if err != nil {
			return err
		}
	}

	// This announcement precedes the core call, as it always has: a
	// nonexistent/unreadable archive fails inside ImportArchive, and the
	// pre-lift engine had already printed the line by then. Not under
	// --json: the document is the run's whole output (Ruling 15).
	if !jsonOutput {
		fmt.Printf("Importing: %s\n", archivePath)
	}

	opts := core.ImportArchiveOptions{
		SourceID:  importSource,
		ModID:     importModID,
		Force:     importForce,
		SkipHooks: noHooks,
		// AcceptConflicts is deliberately left false: the conflict prompt
		// below answers it. --force implies it in core, so a forced import
		// never reaches the prompt at all.
	}

	// progress prints every diagnostic and readout line at its exact point
	// of occurrence. Result.Warnings/.Notes carry the same strings for a
	// caller with no event stream, so they are never batch-printed below -
	// doing so would double-print. HookWarnings are the one exception: they
	// are not events at all, because the pre-lift engine collected them and
	// printed them together at the very end.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.ImportArchiveFetching, core.ImportArchiveDetected, core.ImportArchiveDeploying:
			fmt.Printf("\n%s\n", p.Detail)
		case core.ImportArchiveDetail:
			fmt.Printf("  %s\n", p.Detail)
		case core.ImportArchiveNote:
			if verbose {
				fmt.Printf("%s\n", p.Detail)
			}
		case core.ImportArchiveProfileNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.ImportArchiveWarning, core.InstallBeforeAllForced, core.InstallBeforeEachForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	sink := quietSink(progress)

	// The metadata-fetch progress line announces work PlanImportArchive is
	// about to do, so it prints HERE rather than arriving as an event from a
	// plan (plans emit nothing). core owns the condition so the two cannot
	// disagree about when the fetch happens.
	if sink != nil && core.ImportEnrichmentRuns(game, opts) {
		sink(core.StepEvent{
			Scope: core.Scope{Op: core.OpImport}, Phase: core.ImportArchiveFetching,
			Detail: "Fetching metadata from " + opts.SourceID + "...",
		})
	}

	plan, err := service.PlanImportArchive(ctx, game, profileName, archivePath, opts)
	if err != nil {
		return err
	}

	// Ruling 18 (#314): the readout - the plan's enrichment warnings and its
	// Mod/Source/ID/Version/Author/URL/Files block - is rendered ONCE, from
	// the plan, so it names the identity the apply will persist. Under
	// --json quietSink has already made this a no-op; the plan or the result
	// document carries the same facts.
	core.EmitImportArchiveReadout(plan, sink)

	if importDryRun {
		// Ruling 15: --dry-run --json is the Plan document.
		if jsonOutput {
			return emitJSON(plan)
		}
		renderImportArchivePlan(plan, game, profileName)
		return nil
	}

	// Ruling 1: the conflict decision is the frontend's, and since #314 it is
	// answered from the PLAN - no speculative ingest, no re-run, no second
	// readout (Ruling 18). --force skips the question entirely; core reads it
	// as AcceptConflicts, so a forced import never gets here.
	//
	// Ruling 15/2: under --json the prompt is unanswerable, so the same
	// *core.ConflictError core would have raised is returned and reportError
	// renders it as the envelope with details.conflicts - the contract
	// doInstall's identical guard gives `install --json` (unit P review,
	// Important 1).
	if len(plan.Conflicts) > 0 && !importForce {
		if jsonOutput {
			return &core.ConflictError{Conflicts: plan.Conflicts}
		}
		proceed, readErr := confirmInstallConflicts(ctx, service, game, profileName, plan.Conflicts)
		if readErr != nil {
			// A genuine stdin read failure, not an ordinary decline - see
			// confirmInstallConflicts' doc comment.
			return readErr
		}
		if !proceed {
			return fmt.Errorf("import cancelled")
		}
		opts.AcceptConflicts = true
	}

	result, err := service.ApplyImportArchive(ctx, game, profileName, plan, opts, sink)
	if err != nil {
		return err
	}

	// Ruling 15: the ImportArchiveResult document, which carries
	// HookWarnings, the deployed-file count and the linked source itself -
	// everything the readout below renders.
	if jsonOutput {
		return emitJSON(result)
	}

	// The accumulated, non-fatal after_each/after_all failures, printed
	// together here - the position the pre-lift tail's own batch print
	// occupied. Nothing in ImportArchive can fail once they are populated.
	for _, w := range result.HookWarnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	fmt.Printf("\n✓ Imported: %s\n", result.Mod.Name)
	// #197 postsmoke UX fix: see doInstall's identical fix (cmd/lmm/install.go)
	// - a DeployCompile ".exmodz" mod deploys zero files of its own by
	// design (validate+retain only).
	if game.DeployMode == domain.DeployCompile && result.Deployed == 0 {
		fmt.Println("  Installed (merged pak updated)")
	} else {
		fmt.Printf("  Files deployed: %d\n", result.Deployed)
	}
	fmt.Printf("  Added to profile: %s\n", profileName)

	if result.LinkedSource == domain.SourceLocal {
		fmt.Println("\nNote: Local mods won't receive update notifications.")
	}

	return nil
}

// renderImportArchivePlan prints `lmm import <archive> --dry-run`'s preview,
// straight after the readout core.EmitImportArchiveReadout already produced
// (so the mod's identity, version and file count are on screen above it).
//
// The vocabulary mirrors the live summary this replaces line for line -
// "Would import:" for "✓ Imported:", the same merged-pak wording for a
// DeployCompile mod that deploys zero files of its own - and follows the
// Phase 2 dry-run convention the rest of the command tree uses: a "Would ..."
// summary, --verbose expanding the lists, a hooks line, and scan mode's own
// closing "(dry run - no changes made)".
//
// A dry run does not prompt: there is nothing to confirm when nothing will
// change. The conflicts it would have prompted about are stated instead.
//
// Exit code: like every other --dry-run, a preview that renders successfully
// returns nil; an archive that cannot be planned at all is a
// PlanImportArchive error and fails normally.
func renderImportArchivePlan(plan *core.ImportArchivePlan, game *domain.Game, profileName string) {
	fmt.Printf("\nWould import: %s\n", plan.Mod.Name)
	// #197 postsmoke UX fix, mirrored from the live summary: a DeployCompile
	// ".exmodz" mod deploys zero files of its own by design.
	if game.DeployMode == domain.DeployCompile && len(plan.Files) == 0 {
		fmt.Println("  Would install (merged pak updated)")
	} else {
		fmt.Printf("  Would deploy %d file(s)\n", len(plan.Files))
		if verbose {
			for _, f := range plan.Files {
				fmt.Printf("    - %s\n", f)
			}
		}
	}
	fmt.Printf("  Would add to profile: %s\n", profileName)

	if len(plan.Conflicts) > 0 {
		fmt.Printf("  Would overwrite %d file(s) owned by other mods\n", len(plan.Conflicts))
		if verbose {
			for _, c := range plan.Conflicts {
				fmt.Printf("    - %s (%s)\n", c.RelativePath, domain.ModKey(c.CurrentSourceID, c.CurrentModID))
			}
		}
	}

	if e := plan.MergedArtifact; e != nil {
		if e.Action == core.MergedArtifactRemove {
			fmt.Println("  The profile's merged artifact would be removed afterwards")
		} else {
			fmt.Println("  The profile's merged artifact would be resynced afterwards")
		}
	}

	if len(plan.Hooks) > 0 {
		fmt.Printf("\nHooks that would run: %s\n", strings.Join(plan.Hooks, ", "))
	}

	if plan.LinkedSource == domain.SourceLocal {
		fmt.Println("\nNote: Local mods won't receive update notifications.")
	}

	fmt.Println("\n(dry run - no changes made)")
}

// runImportScan renders `lmm import`'s scan mode: it plans the adopt in
// core, prints the scan and its match outcomes, applies the metadata
// backfill (which the pre-lift engine performed - and reported - before the
// confirmation prompt, and kept on a decline), confirms, and applies the
// adoption. Every printed line comes from the plan, the results, or the
// event stream; the engine itself lives in internal/core/adopt.go.
func runImportScan(cmd *cobra.Command, game *domain.Game, service *core.Service, profileName string) error {
	ctx := cmd.Context()

	// The caveat and the "Scanning..." notice print BEFORE any core call, as
	// they always have for the scan failure this preserves exactly: a game
	// with a missing or unconfigured mod_path fails inside PlanAdopt, and the
	// pre-lift engine had already printed both lines by then.
	//
	// ONE recorded delta (Task 18 review, important 1; decisions log
	// 2026-08-28): core.ScanLocal reads the installed-mod set BEFORE it
	// scans, where the pre-lift engine read it before printing "Scanning".
	// So a DB read failure - and only that - now surfaces one line later
	// than it used to. Restoring exact parity would cost a redundant read
	// here and push engine sequencing back into cmd.
	//
	// core.LocalScan.ExtractModeWarning carries the same caveat rule for a
	// frontend that renders a finished plan instead of a live stream; it is
	// deliberately not read here, since it exists only after the scan has
	// already succeeded.
	// Not under --json: the document is the run's whole output (Ruling 15),
	// and core.LocalScan.ExtractModeWarning carries the same caveat for a
	// caller rendering the plan instead.
	if !jsonOutput {
		if game.DeployMode != domain.DeployCopy {
			fmt.Println("Note: Scan import for extract-mode games tracks mods in-place without caching.")
			fmt.Println("      Uninstall will only remove the database entry, not the files.")
			fmt.Println()
		}
		fmt.Printf("Scanning %s for untracked mods...\n", game.ModPath)
	}

	plan, err := service.PlanAdopt(ctx, game, profileName, core.AdoptOptions{
		SkipMatch: importSkipMatch,
		DryRun:    importDryRun,
	})
	if err != nil {
		return err
	}

	if !jsonOutput {
		fmt.Printf("Found %d files, %d untracked\n\n", len(plan.Scan.Tracked)+len(plan.Scan.Untracked), len(plan.Scan.Untracked))
	}

	// progress renders both applies' events at their point of occurrence.
	// AdoptSyncWarning is the only stderr line (and the reason
	// AdoptResult.Warnings is deliberately not printed as well - core carries
	// the same strings there for callers with no event stream).
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.AdoptBackfillNote, core.AdoptBackfilled:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.AdoptDuplicateSkipped, core.AdoptAdopted, core.AdoptFailed:
			fmt.Printf("  %s\n", p.Detail)
		case core.AdoptNote:
			if verbose {
				fmt.Printf("    %s\n", p.Detail)
			}
		case core.AdoptSyncWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	// Backfill metadata for already-tracked mods missing metadata. The plan
	// carries no candidates at all under --skip-match, so this whole block
	// stays silent there, as it always has.
	if !jsonOutput && len(plan.Scan.Backfill) > 0 {
		fmt.Printf("Backfilling metadata for %d mod(s)...\n", len(plan.Scan.Backfill))
	}
	backfill, err := service.ApplyAdoptBackfill(ctx, game, plan, quietSink(progress))
	if err != nil {
		return err
	}
	if !jsonOutput {
		if backfill.Backfilled > 0 {
			fmt.Printf("Updated metadata for %d existing mod(s)\n\n", backfill.Backfilled)
		} else if len(plan.Scan.Backfill) > 0 {
			fmt.Println("No metadata updates needed")
		}
	}

	if len(plan.Scan.Untracked) == 0 {
		// Nothing to adopt still owes a --json caller a document: the Plan
		// under --dry-run, otherwise the AdoptResult an empty adopt
		// produces (Ruling 15).
		if jsonOutput {
			if importDryRun {
				return emitJSON(plan)
			}
			return emitJSON(&core.AdoptResult{})
		}
		fmt.Println("All mods are already tracked!")
		return nil
	}

	if !jsonOutput && !plan.SkipMatch {
		fmt.Println("Looking up mods on configured sources...")
		for _, m := range plan.Matches {
			if m.Untracked.Mod == nil {
				continue
			}
			switch {
			case m.Error != "":
				if verbose {
					fmt.Printf("  %s: lookup failed: %s\n", m.Untracked.FileName, m.Error)
				}
			case m.Mod != nil:
				fmt.Printf("  ✓ %s -> %s (%s #%s)\n", m.Untracked.FileName, m.Mod.Name, m.Mod.SourceID, m.Mod.ID)
				// #139: a source-file resolution failure is non-fatal - the
				// adoption just stays marker-less.
				if m.FileError != "" && verbose {
					fmt.Printf("  %s: could not resolve source file: %s\n", m.Untracked.FileName, m.FileError)
				}
			default:
				fmt.Printf("  ○ %s -> local (no match)\n", m.Untracked.FileName)
			}
		}
		fmt.Println()
	}

	// Show summary and confirm
	if !jsonOutput {
		fmt.Printf("Ready to import %d mod(s):\n", len(plan.Scan.Untracked))
		for _, r := range plan.Scan.Untracked {
			if r.Mod != nil {
				sourceTag := "local"
				if r.MatchedSource != "" && r.MatchedSource != domain.SourceLocal {
					sourceTag = fmt.Sprintf("%s #%s", r.MatchedSource, r.Mod.ID)
				}
				fmt.Printf("  - %s (%s, v%s)\n", r.Mod.Name, sourceTag, r.Mod.Version)
			} else {
				fmt.Printf("  - %s (unknown)\n", r.FileName)
			}
		}
	}

	if importDryRun {
		// Ruling 15: --dry-run --json is the Plan document. Emitted here,
		// not straight after PlanAdopt, so the backfill apply a plain dry
		// run performs still runs on this path too.
		if jsonOutput {
			return emitJSON(plan)
		}
		fmt.Println("\n(dry run - no changes made)")
		return nil
	}

	// Confirm unless --force
	if !importForce {
		if !jsonOutput {
			fmt.Printf("\nImport these mods? [y/N]: ")
		}
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "y" && input != "yes" {
			return fmt.Errorf("import cancelled")
		}
	}

	result, err := service.ApplyAdopt(ctx, game, plan, quietSink(progress))
	if err != nil {
		return err
	}

	// Ruling 15: the AdoptResult document - the three counters below plus
	// the warnings the event stream carried.
	if jsonOutput {
		return emitJSON(result)
	}

	fmt.Printf("\nImported: %d, Skipped: %d, Failed: %d\n", result.Adopted, result.Skipped, result.Failed)
	return nil
}
