package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
e.g. mods whose author has disabled API downloads. --dry-run and
--skip-match only apply to this mode. Every mod imported this way is
marked as requiring manual download (since lmm did not fetch it itself);
re-link it to a source with 'lmm mod edit --source' to clear that once
it can be checked for updates normally.

Archive mode (an archive path given): imports that one specific mod file,
deploying it and adding it to the profile. Pass --id (with --source, or
it resolves automatically when the game has exactly one configured
source, or prompts interactively when it has several) to fetch and
attach source metadata as part of the import.

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

func doImport(ctx context.Context, cmd *cobra.Command, service *core.Service, game *domain.Game, args []string) error {
	profileName, err := resolveProfile(service, game.ID, importProfile)
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

	// Create importer
	importer := service.NewImporter(game)

	// Set up import options
	opts := core.ImportOptions{
		SourceID:    importSource,
		ModID:       importModID,
		ProfileName: profileName,
	}

	fmt.Printf("Importing: %s\n", archivePath)

	// Import the archive
	result, err := importer.Import(ctx, archivePath, game, opts)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}

	// Save pre-enrichment values for cache rename
	preEnrichVersion := result.Mod.Version
	preEnrichID := result.Mod.ID

	// Enrich with source metadata when --id was provided
	var resolvedFile *domain.DownloadableFile
	if importModID != "" && importSource != "" && importSource != domain.SourceLocal {
		sourceGameID, ok := game.SourceIDs[importSource]
		if !ok {
			fmt.Fprintf(os.Stderr, "Warning: source %s is not configured for this game; skipping metadata fetch\n", importSource)
		} else {
			fmt.Printf("\nFetching metadata from %s...\n", importSource)
			mod, err := service.GetMod(ctx, importSource, sourceGameID, importModID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not fetch metadata: %v\n", err)
			} else {
				// Apply metadata from source, keeping local file info
				result.Mod.Name = mod.Name
				result.Mod.Author = mod.Author
				result.Mod.Summary = mod.Summary
				result.Mod.SourceURL = mod.SourceURL
				result.Mod.PictureURL = mod.PictureURL
				if mod.Version != "" && result.Mod.Version == "unknown" {
					result.Mod.Version = mod.Version
				}

				// #139: resolve which of the source's files this archive is
				// (exact filename first, else the sole file at the imported
				// version - the user asserted the mod identity via --id), so
				// the cache entry can be marker-stamped and the row records
				// real FileIDs. Non-fatal: an offline/failed listing keeps
				// today's marker-less import.
				file, ferr := service.ResolveImportedFile(ctx, importSource, mod, filepath.Base(archivePath), result.Mod.Version, true)
				if ferr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not resolve source file for archive: %v\n", ferr)
				} else if file != nil {
					resolvedFile = file
					// The matched file's own version is authoritative - adopt
					// it so the cache entry, DB row, and marker all agree with
					// what future source-side resolutions will report.
					if file.Version != "" && file.Version != result.Mod.Version {
						result.Mod.Version = file.Version
					}
				}
			}
		}
	}

	// If enrichment changed the version or ID, rename the cache entry
	gameCache := service.GetGameCache(game)
	needsCacheRename := preEnrichVersion != result.Mod.Version || preEnrichID != result.Mod.ID
	if needsCacheRename {
		oldPath := gameCache.ModPath(game.ID, result.Mod.SourceID, preEnrichID, preEnrichVersion)
		newPath := gameCache.ModPath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
		if err := os.MkdirAll(filepath.Dir(newPath), 0755); err == nil {
			if err := os.Rename(oldPath, newPath); err != nil && verbose {
				fmt.Printf("Warning: could not rename cache entry: %v\n", err)
			}
		}
	}

	// #139: stamp the resolved file's completion marker onto the (final,
	// post-rename) cache entry. Non-fatal, and the FileIDs are recorded on
	// the row/ref below even if stamping fails - the row's file identity is
	// resolved either way; a missing marker only costs the one redundant
	// redownload today's imports always pay.
	importedFileIDs := []string{}
	if resolvedFile != nil {
		importedFileIDs = []string{resolvedFile.ID}
		if err := service.MarkImportedFileComplete(ctx, game, result.Mod, resolvedFile.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not mark cache entry complete: %v\n", err)
		}
	}
	// #197 C1 fix: a DeployCompile ".exmodz" import retains its source under
	// RetainedFileID (the archive's own filename - Import's only stable
	// identity), which is NEVER resolvedFile.ID (a real source file ID, or
	// nothing at all without --id). Without folding it into FileIDs too,
	// enabledMergeSources can never find this mod's retained source - it
	// silently never participates in any merge, forever, and is invisible
	// to update/verify since it's excluded from both sides of the
	// staleness fingerprint as well.
	if result.RetainedFileID != "" {
		found := false
		for _, id := range importedFileIDs {
			if id == result.RetainedFileID {
				found = true
				break
			}
		}
		if !found {
			importedFileIDs = append(importedFileIDs, result.RetainedFileID)
		}
	}

	// Show detection results
	fmt.Printf("\nMod: %s\n", result.Mod.Name)
	fmt.Printf("  Source: %s\n", result.LinkedSource)
	fmt.Printf("  ID: %s\n", result.Mod.ID)
	if result.Mod.Version != "unknown" {
		fmt.Printf("  Version: %s\n", result.Mod.Version)
	}
	if result.Mod.Author != "" {
		fmt.Printf("  Author: %s\n", result.Mod.Author)
	}
	if result.Mod.SourceURL != "" {
		fmt.Printf("  URL: %s\n", result.Mod.SourceURL)
	}
	if result.AutoDetected {
		fmt.Println("  (auto-detected from filename)")
	}
	fmt.Printf("  Files: %d\n", result.FilesExtracted)

	// Set up installer for conflict checking and deployment. The installer is
	// built from the already-resolved method so both stay consistent (and the
	// profile file is only read once).
	linkMethod, err := service.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return err
	}
	installer := service.NewInstallerWithLinker(game, service.GetLinker(linkMethod))

	// Check for conflicts (unless --force)
	if !importForce {
		conflicts, err := installer.GetConflicts(ctx, game, result.Mod, profileName)
		if err != nil {
			if verbose {
				fmt.Printf("Warning: could not check conflicts: %v\n", err)
			}
		} else if len(conflicts) > 0 {
			fmt.Printf("\n⚠ File conflicts detected:\n")

			// Group conflicts by mod
			modConflicts := make(map[string][]string) // "sourceID:modID" -> []paths
			for _, c := range conflicts {
				key := c.CurrentSourceID + ":" + c.CurrentModID
				modConflicts[key] = append(modConflicts[key], c.RelativePath)
			}

			for key, paths := range modConflicts {
				parts := strings.SplitN(key, ":", 2)
				sourceID, modID := parts[0], parts[1]

				// Try to get mod name
				conflictMod, _ := service.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
				modName := modID
				if conflictMod != nil {
					modName = conflictMod.Name
				}

				fmt.Printf("  From %s (%s):\n", modName, modID)
				maxShow := 5
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
				return fmt.Errorf("import cancelled")
			}
		}
	}

	// Set up hooks
	hookRunner := getHookRunner(service)
	resolvedHooks := getResolvedHooks(service, game, profileName)
	hookCtx := makeHookContext(game)
	var hookErrors []error

	// Run install.before_all hook (for single mod import)
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeAll != "" {
		hookCtx.HookName = "install.before_all"
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeAll, hookCtx); err != nil {
			if !importForce {
				return fmt.Errorf("install.before_all hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_all hook failed (forced): %v\n", err)
		}
	}

	// Run install.before_each hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.BeforeEach != "" {
		hookCtx.HookName = "install.before_each"
		hookCtx.ModID = result.Mod.ID
		hookCtx.ModName = result.Mod.Name
		hookCtx.ModVersion = result.Mod.Version
		if _, err := hookRunner.Run(ctx, resolvedHooks.Install.BeforeEach, hookCtx); err != nil {
			if !importForce {
				return fmt.Errorf("install.before_each hook failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Warning: install.before_each hook failed (forced): %v\n", err)
		}
	}

	// Deploy to game directory
	fmt.Println("\nDeploying to game directory...")

	if err := installer.Install(ctx, game, result.Mod, profileName); err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	// Save to database
	installedMod := &domain.InstalledMod{
		Mod:          *result.Mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
		FileIDs:      importedFileIDs, // empty unless resolved against the source (#139)
	}

	if err := service.SaveInstalledMod(ctx, installedMod); err != nil {
		return fmt.Errorf("failed to save mod: %w", err)
	}

	// Add mod to profile
	pm := getProfileManager(service)

	// Ensure profile exists, create if needed
	if _, err := pm.Get(game.ID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(game.ID, profileName); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not create profile: %v\n", err)
				}
			}
		}
	}

	// Add or update mod in profile
	modRef := domain.ModReference{
		SourceID: result.Mod.SourceID,
		ModID:    result.Mod.ID,
		Version:  result.Mod.Version,
		FileIDs:  importedFileIDs,
	}
	if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update profile: %v\n", err)
		}
	}

	// #197 I3/C1 fix: a DeployCompile ".exmodz" import deploys zero files of
	// its own (validate+retain only) - without this, the imported mod's
	// content never reaches the game directory until some OTHER flow
	// happens to sync the merged pak.
	if syncWarnings, syncErr := service.SyncMergedPak(ctx, game, profileName); syncErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not sync merged pak: %v\n", syncErr)
	} else {
		for _, w := range syncWarnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}

	// Run install.after_each hook
	if hookRunner != nil && resolvedHooks != nil && resolvedHooks.Install.AfterEach != "" {
		hookCtx.HookName = "install.after_each"
		hookCtx.ModID = result.Mod.ID
		hookCtx.ModName = result.Mod.Name
		hookCtx.ModVersion = result.Mod.Version
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

	fmt.Printf("\n✓ Imported: %s\n", result.Mod.Name)
	// #197 postsmoke UX fix: see doInstall's identical fix (cmd/lmm/install.go)
	// - a DeployCompile ".exmodz" mod deploys zero files of its own by
	// design (validate+retain only).
	if game.DeployMode == domain.DeployCompile && result.FilesExtracted == 0 {
		fmt.Println("  Installed (merged pak updated)")
	} else {
		fmt.Printf("  Files deployed: %d\n", result.FilesExtracted)
	}
	fmt.Printf("  Added to profile: %s\n", profileName)

	if result.LinkedSource == domain.SourceLocal {
		fmt.Println("\nNote: Local mods won't receive update notifications.")
	}

	return nil
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
	if game.DeployMode != domain.DeployCopy {
		fmt.Println("Note: Scan import for extract-mode games tracks mods in-place without caching.")
		fmt.Println("      Uninstall will only remove the database entry, not the files.")
		fmt.Println()
	}
	fmt.Printf("Scanning %s for untracked mods...\n", game.ModPath)

	plan, err := service.PlanAdopt(ctx, game, profileName, core.AdoptOptions{
		SkipMatch: importSkipMatch,
		DryRun:    importDryRun,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Found %d files, %d untracked\n\n", len(plan.Scan.Tracked)+len(plan.Scan.Untracked), len(plan.Scan.Untracked))

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
	if len(plan.Scan.Backfill) > 0 {
		fmt.Printf("Backfilling metadata for %d mod(s)...\n", len(plan.Scan.Backfill))
	}
	backfill, err := service.ApplyAdoptBackfill(ctx, game, plan, progress)
	if err != nil {
		return err
	}
	if backfill.Backfilled > 0 {
		fmt.Printf("Updated metadata for %d existing mod(s)\n\n", backfill.Backfilled)
	} else if len(plan.Scan.Backfill) > 0 {
		fmt.Println("No metadata updates needed")
	}

	if len(plan.Scan.Untracked) == 0 {
		fmt.Println("All mods are already tracked!")
		return nil
	}

	if !plan.SkipMatch {
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

	if importDryRun {
		fmt.Println("\n(dry run - no changes made)")
		return nil
	}

	// Confirm unless --force
	if !importForce {
		fmt.Printf("\nImport these mods? [y/N]: ")
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "y" && input != "yes" {
			return fmt.Errorf("import cancelled")
		}
	}

	result, err := service.ApplyAdopt(ctx, game, plan, progress)
	if err != nil {
		return err
	}

	fmt.Printf("\nImported: %d, Skipped: %d, Failed: %d\n", result.Adopted, result.Skipped, result.Failed)
	return nil
}
