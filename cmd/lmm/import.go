package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

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

	// Set up installer for conflict checking and deployment
	linkMethod := service.GetEffectiveLinkMethod(game, profileName)
	installer := service.GetInstallerForProfile(game, profileName)

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
				conflictMod, _ := service.GetInstalledMod(sourceID, modID, game.ID, profileName)
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
		FileIDs:      []string{}, // Local imports don't have file IDs
	}

	if err := service.SaveInstalledMod(installedMod); err != nil {
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
		FileIDs:  []string{},
	}
	if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update profile: %v\n", err)
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
	fmt.Printf("  Files deployed: %d\n", result.FilesExtracted)
	fmt.Printf("  Added to profile: %s\n", profileName)

	if result.LinkedSource == domain.SourceLocal {
		fmt.Println("\nNote: Local mods won't receive update notifications.")
	}

	return nil
}
func runImportScan(cmd *cobra.Command, game *domain.Game, service *core.Service, profileName string) error {
	ctx := cmd.Context()

	// Warn about extract mode limitations
	if game.DeployMode != domain.DeployCopy {
		fmt.Println("Note: Scan import for extract-mode games tracks mods in-place without caching.")
		fmt.Println("      Uninstall will only remove the database entry, not the files.")
		fmt.Println()
	}

	// Get installed mods for this game/profile
	installedMods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// Create importer and scan
	importer := service.NewImporter(game)
	opts := core.ScanOptions{
		ProfileName: profileName,
		DryRun:      importDryRun,
	}

	fmt.Printf("Scanning %s for untracked mods...\n", game.ModPath)

	results, err := importer.ScanModPath(ctx, game, installedMods, opts)
	if err != nil {
		return fmt.Errorf("scanning mod_path: %w", err)
	}

	// Count untracked
	var untracked []core.ScanResult
	for _, r := range results {
		if !r.AlreadyTracked {
			untracked = append(untracked, r)
		}
	}

	fmt.Printf("Found %d files, %d untracked\n\n", len(results), len(untracked))

	// Backfill metadata for already-tracked mods missing metadata
	if !importSkipMatch {
		// Count mods needing backfill
		var needsBackfill int
		for _, im := range installedMods {
			if im.SourceID != domain.SourceLocal && im.SourceID != "" {
				if im.Author == "" || im.SourceURL == "" {
					needsBackfill++
				}
			}
		}
		var backfilled int
		if needsBackfill > 0 {
			fmt.Printf("Backfilling metadata for %d mod(s)...\n", needsBackfill)
		}
		for _, im := range installedMods {
			if im.SourceID == domain.SourceLocal || im.SourceID == "" {
				continue
			}
			// Skip if already has metadata
			if im.Author != "" && im.SourceURL != "" {
				continue
			}
			// Fetch fresh metadata from source
			sourceGameID, ok := game.SourceIDs[im.SourceID]
			if !ok {
				continue
			}
			mod, err := service.GetMod(ctx, im.SourceID, sourceGameID, im.ID)
			if err != nil {
				if verbose {
					fmt.Printf("  %s: metadata fetch failed: %v\n", im.Name, err)
				}
				continue
			}
			// Update fields that are missing
			updated := im
			if im.Author == "" && mod.Author != "" {
				updated.Author = mod.Author
			}
			if im.Summary == "" && mod.Summary != "" {
				updated.Summary = mod.Summary
			}
			if im.SourceURL == "" && mod.SourceURL != "" {
				updated.SourceURL = mod.SourceURL
			}
			if err := service.SaveInstalledMod(&updated); err != nil {
				if verbose {
					fmt.Printf("  %s: metadata save failed: %v\n", im.Name, err)
				}
				continue
			}
			backfilled++
			if verbose {
				fmt.Printf("  ✓ %s: metadata updated (author: %s)\n", im.Name, mod.Author)
			}
		}
		if backfilled > 0 {
			fmt.Printf("Updated metadata for %d existing mod(s)\n\n", backfilled)
		} else if needsBackfill > 0 {
			fmt.Println("No metadata updates needed")
		}
	}

	if len(untracked) == 0 {
		fmt.Println("All mods are already tracked!")
		return nil
	}

	// Try to match untracked mods against the game's configured sources
	if !importSkipMatch {
		fmt.Println("Looking up mods on configured sources...")
		for i := range untracked {
			if untracked[i].Mod == nil {
				continue
			}

			matched, err := tryMatchSources(ctx, service, game, untracked[i].Mod.Name)
			if err != nil {
				if verbose {
					fmt.Printf("  %s: lookup failed: %v\n", untracked[i].FileName, err)
				}
				continue
			}
			if matched != nil {
				untracked[i].Mod.ID = matched.ID
				untracked[i].Mod.SourceID = matched.SourceID
				untracked[i].Mod.Name = matched.Name
				untracked[i].Mod.Author = matched.Author
				untracked[i].Mod.Summary = matched.Summary
				untracked[i].Mod.SourceURL = matched.SourceURL
				untracked[i].Mod.PictureURL = matched.PictureURL
				untracked[i].Mod.GameID = matched.GameID
				untracked[i].MatchedSource = matched.SourceID
				fmt.Printf("  ✓ %s -> %s (%s #%s)\n", untracked[i].FileName, matched.Name, matched.SourceID, matched.ID)
			} else {
				fmt.Printf("  ○ %s -> local (no match)\n", untracked[i].FileName)
			}
		}
		fmt.Println()
	}

	// Show summary and confirm
	fmt.Printf("Ready to import %d mod(s):\n", len(untracked))
	for _, r := range untracked {
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

	// Import each untracked mod
	linkMethod := service.GetEffectiveLinkMethod(game, profileName)

	// Get current installed mods for duplicate checking
	currentMods, _ := service.GetInstalledMods(game.ID, profileName)

	var imported, failed, skipped int
	for _, r := range untracked {
		if r.Mod == nil {
			continue
		}

		// Check for duplicates before importing
		importer := service.NewImporter(game)
		if dup := importer.FindDuplicateMod(r.Mod.Name, currentMods); dup != nil {
			fmt.Printf("  ⊘ %s: skipped (duplicate of \"%s\")\n", r.FileName, dup.Name)
			skipped++
			continue
		}

		// For deploy_mode: copy, the mod is already in place
		// We just need to create a cache entry and register it
		err := importExistingMod(ctx, service, game, r, profileName, linkMethod)
		if err != nil {
			fmt.Printf("  ✗ %s: %v\n", r.FileName, err)
			failed++
			continue
		}
		fmt.Printf("  ✓ %s\n", r.Mod.Name)
		imported++

		// Add to currentMods so we catch duplicates within this batch
		currentMods = append(currentMods, domain.InstalledMod{Mod: *r.Mod})
	}

	fmt.Printf("\nImported: %d, Skipped: %d, Failed: %d\n", imported, skipped, failed)
	return nil
}

// tryMatchSources searches every source configured for game that declares
// search capability, in SourcesForGame's ID-sorted order (design §4.2:
// "curseforge" before "nexusmods" alphabetically, so typical two-built-in
// setups keep today's outcome), and returns the first source whose search
// turns up a result - the same "first non-empty result wins" acceptance
// rule tryMatchCurseForge used, unchanged (tighter scoring tracked in #27).
// Generalizes the old CurseForge-only lookup so any configured,
// search-capable source can supply scan-import matches. A per-source search
// failure does not abort the scan - remaining sources are still tried.
//
// Error semantics (PR #124 review round 1): a single source failing does
// not make the overall round a failure - any source that responds at all,
// even with zero results, proves a real search happened and "no match" is
// the honest outcome (nil, nil), not a stale error from an unrelated
// source that happened to fail first. An error is returned to the caller
// (for its verbose "lookup failed" notice) only when EVERY searchable
// source failed - lastErr then reports the most recent one. No
// search-capable sources configured is likewise a clean no-match, not an
// error (the loop never runs, so anySucceeded stays false but so does
// lastErr).
func tryMatchSources(ctx context.Context, service *core.Service, game *domain.Game, modName string) (*domain.Mod, error) {
	sources, err := service.SourcesForGame(game.ID)
	if err != nil {
		return nil, err
	}

	var lastErr error
	anySucceeded := false
	for _, src := range sources {
		if !source.CapabilitiesOf(src).Search {
			continue
		}

		searchResult, err := service.SearchMods(ctx, src.ID(), game.ID, modName, "", nil, 0, 0)
		if err != nil {
			lastErr = err
			continue
		}
		anySucceeded = true
		if len(searchResult.Mods) > 0 {
			// Return the first (best) match. Tighter scoring tracked in #27.
			return &searchResult.Mods[0], nil
		}
	}

	if anySucceeded {
		return nil, nil
	}
	return nil, lastErr
}

// importExistingMod registers an already-deployed mod in lmm
func importExistingMod(ctx context.Context, service *core.Service, game *domain.Game, r core.ScanResult, profileName string, linkMethod domain.LinkMethod) error {
	// For deploy_mode: copy, create cache entry by copying the file
	if game.DeployMode == domain.DeployCopy {
		gameCache := service.GetGameCache(game)
		cachePath := gameCache.ModPath(game.ID, r.Mod.SourceID, r.Mod.ID, r.Mod.Version)

		// Create cache directory
		if err := os.MkdirAll(cachePath, 0755); err != nil {
			return fmt.Errorf("creating cache: %w", err)
		}

		// Copy the file to cache using streaming to avoid memory spikes
		destPath := filepath.Join(cachePath, r.FileName)
		if err := copyFileStreaming(r.FilePath, destPath); err != nil {
			return fmt.Errorf("copying to cache: %w", err)
		}
	}

	// Save to database
	installedMod := &domain.InstalledMod{
		Mod:            *r.Mod,
		ProfileName:    profileName,
		UpdatePolicy:   domain.UpdateNotify,
		Enabled:        true,
		Deployed:       true,
		LinkMethod:     linkMethod,
		ManualDownload: true, // Scanned mods require manual download
		FileIDs:        []string{},
	}

	if err := service.SaveInstalledMod(installedMod); err != nil {
		return fmt.Errorf("saving to database: %w", err)
	}

	// Add to profile
	pm := getProfileManager(service)
	modRef := domain.ModReference{
		SourceID: r.Mod.SourceID,
		ModID:    r.Mod.ID,
		Version:  r.Mod.Version,
		FileIDs:  []string{},
	}
	if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
		// Non-fatal
		if verbose {
			fmt.Printf("    Warning: could not update profile: %v\n", err)
		}
	}

	return nil
}

// copyFileStreaming copies a file using streaming to avoid loading it all into memory
func copyFileStreaming(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying: %w", err)
	}

	return dstFile.Sync()
}
