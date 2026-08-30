package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

// confirmInstallConflicts prints the file conflicts installing the mod would
// cause and prompts the user to confirm overwriting them, at the position it
// has always occupied: AFTER the mod is downloaded/extracted to cache and
// BEFORE it is deployed - the earliest point an uncached mod's conflicts can
// be detected at all (see core.InstallPlan.Conflicts' doc comment for why a
// pre-download plan can never see them).
//
// Since v2 Phase 3 (Ruling 1) core does not call this: ApplyInstall/
// ImportArchive return *core.ConflictError from that same position, and
// doInstall/doImport prompt here and then re-run Apply with
// AcceptConflicts set. conflicts is therefore always the non-empty,
// freshly-computed list that error carries, never plan.Conflicts.
//
// Ruling 7 delta: because accepting re-runs Apply, the download step's
// console lines print a second time between this prompt and "Deploying to
// game directory..." - the task report lists the exact sequence.
//
// Returns false to decline (the caller returns the pre-lift "installation
// cancelled" / "import cancelled" error, unchanged - see readErr below for
// the one case that is NOT a plain decline). readErr, if non-nil, is a
// genuine stdin read failure (readPromptLine's own doc comment: distinct
// from EOF, which is treated as an ordinary empty/declined answer) - the
// caller must propagate THIS error verbatim rather than letting it collapse
// into the generic cancellation text, matching the pre-extraction CLI's own
// `if err != nil { return err }` before its y/N check.
func confirmInstallConflicts(ctx context.Context, service *core.Service, game *domain.Game, profileName string, conflicts []core.Conflict) (proceed bool, readErr error) {
	fmt.Printf("\n⚠ File conflicts detected:\n")

	modConflicts := make(map[string][]string)
	for _, c := range conflicts {
		key := domain.ModKey(c.CurrentSourceID, c.CurrentModID)
		modConflicts[key] = append(modConflicts[key], c.RelativePath)
	}

	for key, paths := range modConflicts {
		parts := strings.SplitN(key, ":", 2)
		sourceID, modID := parts[0], parts[1]

		conflictMod, _ := service.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
		modName := modID
		if conflictMod != nil {
			modName = conflictMod.Name
		}

		fmt.Printf("  From %s (%s):\n", modName, modID)
		const maxShow = 5
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
		return false, err
	}
	return input == "y" || input == "yes", nil
}

// installFileIDList parses --file's comma-separated value into clean,
// deduplicated (first occurrence wins, order preserved) file IDs - nil when
// the flag is unset - the shared source for both selectInstallFiles'
// CLI-side matching and core.InstallOptions.TargetFileIDs (#140). Deduping
// here keeps a repeated ID (--file 9,9) from reaching SaveInstalledMod's
// PK-constrained installed_mod_files INSERTs, which would fail the install
// only after download and deploy.
func installFileIDList() []string {
	if installFileID == "" {
		return nil
	}
	seen := make(map[string]bool)
	var ids []string
	for _, fid := range strings.Split(installFileID, ",") {
		if fid = strings.TrimSpace(fid); fid != "" && !seen[fid] {
			seen[fid] = true
			ids = append(ids, fid)
		}
	}
	return ids
}

// selectInstallFiles applies the --file flag, single-file shortcut, --yes default,
// or interactive prompt to choose which downloadable files to install.
// validate, when non-nil, enforces variant-exclusivity rules (#211 -
// Service.ValidateInstallFileSelection): the --file path returns its error
// as-is (an explicit flag isn't retried), while the interactive path prints
// it and re-prompts. nil skips validation entirely (no caller currently
// needs that, but it keeps the signature honest for a future one).
func selectInstallFiles(files []domain.DownloadableFile, validate func([]domain.DownloadableFile) error) ([]*domain.DownloadableFile, error) {
	return selectInstallFilesFrom(os.Stdin, files, validate)
}

// selectInstallFilesFrom is the testable core of selectInstallFiles.
func selectInstallFilesFrom(r io.Reader, files []domain.DownloadableFile, validate func([]domain.DownloadableFile) error) ([]*domain.DownloadableFile, error) {
	runValidate := func(selected []*domain.DownloadableFile) error {
		if validate == nil {
			return nil
		}
		sel := make([]domain.DownloadableFile, len(selected))
		for i, f := range selected {
			sel[i] = *f
		}
		return validate(sel)
	}

	// Direct file ID(s) via --file flag
	if installFileID != "" {
		var selected []*domain.DownloadableFile
		for _, fid := range installFileIDList() {
			found := false
			for i := range files {
				if files[i].ID == fid {
					selected = append(selected, &files[i])
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("file ID %s not found", fid)
			}
		}
		if err := runValidate(selected); err != nil {
			return nil, err
		}
		return selected, nil
	}

	if len(files) == 1 {
		selected := []*domain.DownloadableFile{&files[0]}
		if err := runValidate(selected); err != nil {
			return nil, err
		}
		return selected, nil
	}

	// Find primary file index for default
	defaultChoice := 1
	for i := range files {
		if files[i].IsPrimary {
			defaultChoice = i + 1
			break
		}
	}

	if installYes {
		selected := []*domain.DownloadableFile{&files[defaultChoice-1]}
		if err := runValidate(selected); err != nil {
			return nil, err
		}
		return selected, nil
	}

	// The listing exists for the prompt that follows it; under --json
	// readMultiSelectionLine refuses to read stdin (Ruling 2), so printing
	// it would only put console text beside the error envelope.
	if !jsonOutput {
		fmt.Println("\nAvailable files:")
		for i, f := range files {
			fmt.Println(installFileRow(i, f))
		}
	}

	// reader is created ONCE and shared across every attempt below (both
	// readMultiSelectionLine's own invalid-format retry and the
	// validation retry here) - re-wrapping the same underlying r in a
	// fresh bufio.Reader per attempt (as calling promptMultiSelectionFrom
	// per-iteration would) silently drops whatever that attempt's Reader
	// had buffered past the line it returned, losing later input lines
	// (e.g. this function's own re-prompt test's second line).
	reader := bufio.NewReader(r)
	for {
		selections, retry, err := readMultiSelectionLine(reader, "Select file(s) (e.g., 1 or 1,3 or 1-3)", defaultChoice, len(files))
		if err != nil {
			return nil, err
		}
		if retry {
			continue
		}
		selected := make([]*domain.DownloadableFile, 0, len(selections))
		for _, sel := range selections {
			selected = append(selected, &files[sel-1])
		}
		if err := runValidate(selected); err != nil {
			fmt.Printf("Invalid selection: %v\n", err)
			continue
		}
		return selected, nil
	}
}

// searchAndSelectMods runs an interactive paginated search for query and returns
// the user's selection. If only one match exists or installYes is set, it auto-
// selects without prompting. Returns ErrCancelled if the user types 'q'.
func searchAndSelectMods(ctx context.Context, service *core.Service, gameID, source, query, profileName string) ([]*domain.Mod, error) {
	const displayPageSize = 10

	// Ruling 15: the header announces an interactive search whose listing
	// and prompt below are already gated - under --json the run's whole
	// output is the one document, and this line is the only thing that ever
	// printed ahead of it (unit P review, Important 2).
	if !jsonOutput {
		fmt.Printf("Searching for \"%s\"...\n\n", query)
	}

	searchResult, err := service.SearchMods(ctx, source, gameID, query, "", nil, 0, displayPageSize)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return nil, authPromptError(source)
		}
		return nil, fmt.Errorf("search failed: %w", err)
	}
	if len(searchResult.Mods) == 0 {
		return nil, fmt.Errorf("no mods found matching \"%s\"", query)
	}

	// Mark already-installed mods in the listing
	installedMods, _ := service.GetInstalledMods(ctx, gameID, profileName)
	installedIDs := make(map[string]bool)
	for _, im := range installedMods {
		if im.SourceID == source {
			installedIDs[im.ID] = true
		}
	}

	// Trivial selections
	if len(searchResult.Mods) == 1 || installYes {
		return []*domain.Mod{&searchResult.Mods[0]}, nil
	}

	// Non-interactive rule (Ruling 2): more than one match with no -y/--yes
	// to auto-pick the first has no other deciding flag - --id names a mod
	// directly and skips search entirely, so it's the other way out.
	if jsonOutput {
		return nil, confirmationRequiredVia("pass -y/--yes to auto-select the first result, or --id to install a specific mod directly")
	}

	// Interactive paginated selection
	currentPage := 0
	currentResult := searchResult
	reader := bufio.NewReader(os.Stdin)

	for {
		mods := currentResult.Mods
		for i, m := range mods {
			installedMark := ""
			if installedIDs[m.ID] {
				installedMark = " [installed]"
			}
			fmt.Printf("  [%d] %s v%s by %s (ID: %s)%s\n", i+1, m.Name, m.Version, m.Author, m.ID, installedMark)
		}

		hasMore := false
		if currentResult.TotalCount > 0 {
			remaining := currentResult.TotalCount - (currentPage+1)*displayPageSize
			if remaining > 0 {
				hasMore = true
				fmt.Printf("  [n] Next page (%d more)\n", remaining)
			}
		} else if len(mods) == displayPageSize {
			hasMore = true
			fmt.Printf("  [n] Next page\n")
		}
		if currentPage > 0 {
			fmt.Printf("  [p] Previous page\n")
		}
		fmt.Printf("  [q] Cancel\n")

		fmt.Printf("\nSelect mod(s) (e.g., 1 or 1,3,5 or 1-3) [1]: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading input: %w", err)
		}
		input = strings.TrimSpace(input)

		if input == "q" || input == "Q" {
			return nil, ErrCancelled
		}
		if (input == "n" || input == "N") && hasMore {
			currentPage++
			currentResult, err = service.SearchMods(ctx, source, gameID, query, "", nil, currentPage, displayPageSize)
			if err != nil {
				return nil, fmt.Errorf("search failed: %w", err)
			}
			if len(currentResult.Mods) == 0 {
				fmt.Println("No more results.")
				currentPage--
				currentResult, err = service.SearchMods(ctx, source, gameID, query, "", nil, currentPage, displayPageSize)
				if err != nil {
					return nil, fmt.Errorf("search failed: %w", err)
				}
			}
			fmt.Println()
			continue
		}
		if (input == "p" || input == "P") && currentPage > 0 {
			currentPage--
			currentResult, err = service.SearchMods(ctx, source, gameID, query, "", nil, currentPage, displayPageSize)
			if err != nil {
				return nil, fmt.Errorf("search failed: %w", err)
			}
			fmt.Println()
			continue
		}

		if input == "" {
			input = "1"
		}
		selections, err := parseRangeSelection(input, len(mods))
		if err != nil {
			fmt.Printf("Invalid selection: %v\n", err)
			continue
		}
		var selectedMods []*domain.Mod
		for _, sel := range selections {
			selectedMods = append(selectedMods, &currentResult.Mods[sel-1])
		}
		return selectedMods, nil
	}
}

var (
	installSource       string
	installProfile      string
	installVersion      string
	installModID        string
	installFileID       string
	installYes          bool
	installShowArchived bool
	skipVerify          bool
	installForce        bool
	installNoDeps       bool
)

var installCmd = &cobra.Command{
	Use:   "install [query]",
	Short: "Install a mod",
	Long: `Install a mod from a configured source.

A search query finds the mod interactively; --id skips the search and
fetches the mod directly by its source-specific ID. Combine --id with
--file to also skip the interactive file-selection prompt, installing the
exact file(s) you name (comma-separated for more than one).

Use -s/--source to pick which configured source to search or fetch from.
If omitted and the game has more than one configured source, you are
prompted to choose (or, with -y, the first source alphabetically is used
automatically).

The mod is added to the specified profile, or the active profile if
--profile is not given.

Dependencies are automatically resolved and installed. Use --no-deps to skip.

When selecting files interactively, you can choose multiple files (e.g.,
main + optional patches) using comma-separated values or ranges: 1,3,5 or
1-3 or 1,3-5. Archived/old-version files are hidden by default; pass
--show-archived to list and select from them too.

Examples:
  lmm install "ore stack" --game starrupture
  lmm install "skyui" --game skyrim-se --profile survival
  lmm install --id 12345 --game skyrim-se
  lmm install --id 12345 --file 67890 --game skyrim-se   # skip search and file prompt
  lmm install "mod name" -g skyrim-se -y       # Auto-select and auto-confirm
  lmm install "mod name" -g skyrim-se --no-deps  # Skip dependencies`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInstall,
}

func init() {
	installCmd.Flags().StringVarP(&installSource, "source", "s", "", "mod source (default: the sole configured source; prompts when several are configured, -y picks the first alphabetically)")
	installCmd.Flags().StringVarP(&installProfile, "profile", "p", "", "profile to install to (default: active profile)")
	installCmd.Flags().StringVar(&installVersion, "version", "", "specific version to install (default: latest; archived files are searched automatically)")
	installCmd.Flags().StringVar(&installModID, "id", "", "mod ID (skips search)")
	installCmd.Flags().StringVar(&installFileID, "file", "", "file ID(s), comma-separated (skips file selection)")
	installCmd.Flags().BoolVarP(&installYes, "yes", "y", false, "auto-select first/primary option (no prompts)")
	installCmd.Flags().BoolVar(&installShowArchived, "show-archived", false, "show archived/old files")
	installCmd.Flags().BoolVar(&skipVerify, "skip-verify", false, "skip checksum storage and display")
	installCmd.Flags().BoolVarP(&installForce, "force", "f", false, "install without conflict prompts")
	installCmd.Flags().BoolVar(&installNoDeps, "no-deps", false, "skip automatic dependency installation")

	rootCmd.AddCommand(installCmd)
}

// installFileRow renders one chooser row. The Description suffix (#211) is
// what tells the user WHY a variant is recommended - e.g. Icarus's
// "mergeable EXMOD - recommended" vs "prebuilt PAK"; sources that leave it
// empty render exactly as before.
func installFileRow(index int, f domain.DownloadableFile) string {
	sizeStr := formatSize(f.Size)
	defaultMark := ""
	if f.IsPrimary {
		defaultMark = " <- default"
	}
	row := fmt.Sprintf("  [%d] %s (%s, %s)", index+1, displayFileLabel(f), f.Category, sizeStr)
	if f.Description != "" {
		row += " - " + f.Description
	}
	return row + defaultMark
}

func displayFileLabel(file domain.DownloadableFile) string {
	name := strings.TrimSpace(file.Name)
	fileName := strings.TrimSpace(file.FileName)

	if fileName == "" {
		return name
	}
	if name == "" {
		return fileName
	}
	if strings.ContainsAny(fileName, `/\`) {
		return name
	}
	if looksOpaqueFileName(fileName) {
		return name
	}
	return fileName
}

func looksOpaqueFileName(fileName string) bool {
	if filepath.Ext(fileName) != "" {
		return false
	}
	if strings.Count(fileName, "-") < 4 {
		return false
	}

	compact := strings.ReplaceAll(fileName, "-", "")
	if len(compact) < 24 {
		return false
	}

	for _, r := range compact {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}

	return true
}

func runInstall(cmd *cobra.Command, args []string) error {
	// Either query or --id is required
	if len(args) == 0 && installModID == "" {
		return fmt.Errorf("either a search query or --id is required")
	}
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doInstall(ctx, service, game, args)
	})
}

func doInstall(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	// A --file value that parses to ZERO IDs (only commas/whitespace) fails
	// fast, before any search or fetch - otherwise it would silently degrade
	// into "no --file at all": selectInstallFiles' flag branch would return
	// an empty selection with no error, and the batch path's TargetFileIDs
	// pin would vanish (#140 review).
	if installFileID != "" && len(installFileIDList()) == 0 {
		return fmt.Errorf("--file %q contains no file IDs", installFileID)
	}

	// Resolve source: flag if set; else the sole configured source, an
	// interactive prompt when several are configured, or the first
	// alphabetically under --yes.
	var err error
	installSource, err = resolveSource(service, game, installSource, installYes)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, installProfile)
	if err != nil {
		return err
	}

	// Get the mod to install (by --id or interactive search) - unchanged,
	// CLI-side.
	var mod *domain.Mod
	if installModID != "" {
		if verbose {
			fmt.Printf("Fetching mod %s from %s...\n", installModID, installSource)
		}
		mod, err = service.GetMod(ctx, installSource, game.ID, installModID)
		if err != nil {
			if errors.Is(err, domain.ErrAuthRequired) {
				return authPromptError(installSource)
			}
			return fmt.Errorf("failed to fetch mod: %w", err)
		}
	} else {
		selectedMods, err := searchAndSelectMods(ctx, service, game.ID, installSource, args[0], profileName)
		if err != nil {
			return err
		}
		if len(selectedMods) > 1 {
			return installMultipleMods(ctx, service, game, selectedMods, profileName)
		}
		mod = selectedMods[0]
	}

	// Every human-facing line below is suppressed under --json: the run
	// emits exactly one document (Ruling 15).
	if !jsonOutput {
		fmt.Printf("\nSelected: %s v%s by %s\n", mod.Name, mod.Version, mod.Author)

		if !installNoDeps && mod.SourceID != domain.SourceLocal {
			fmt.Println("\nResolving dependencies...")
		}
	}

	// PlanInstall resolves dependencies, files (its own non-interactive
	// default), conflicts (against whatever is already cached), and any
	// existing installed row - all read-only. --no-deps/local-mod dep
	// skipping and interactive/--file file selection are deliberately NOT
	// part of PlanInstall (see its doc comment); both are applied to the
	// plan below, CLI-side, before ApplyInstall ever runs.
	plan, err := service.PlanInstall(ctx, game, profileName, installSource, mod.ID, installShowArchived)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(installSource)
		}
		return err
	}

	if installNoDeps || mod.SourceID == domain.SourceLocal {
		plan.Dependencies = nil
		plan.MissingDependencies = nil
		plan.CycleDetected = false
		plan.DependencyWarnings = nil
	}

	// If there are dependencies to install (or unresolvable ones to warn
	// about), show the plan and confirm.
	if len(plan.Dependencies) > 0 || len(plan.MissingDependencies) > 0 || len(plan.DependencyWarnings) > 0 {
		if !jsonOutput {
			showInstallPlan(plan)
		}

		if !installYes {
			if !jsonOutput {
				fmt.Printf("\nInstall %d mod(s)? [Y/n]: ", len(plan.Dependencies)+1)
			}
			input, err := readPromptLine()
			if err != nil {
				return err
			}
			if input == "n" || input == "no" {
				return fmt.Errorf("installation cancelled")
			}
		}
	}

	// Fix wave 1 (dep-path fidelity): a dependency-having install must
	// reproduce cmd/lmm/install.go's pre-extraction batchInstallMods
	// mechanics byte-for-byte for the WHOLE list, primary included - never
	// the single-mod code below (interactive/--file file selection, the
	// blocking conflict prompt) - matching doInstall's own pre-extraction
	// early return ("if len(modsToInstall) > 1: return
	// installModsWithDeps(...)"), which never reached any of that either.
	// See doInstallBatch's own doc comment and task-2-report.md's "Fix wave
	// 1" entry for the full review trace this restores.
	// --version and --file apply to the named mod only (#96/#140);
	// dependencies install at latest (#96 decision 6).
	if len(plan.Dependencies) > 0 {
		return doInstallBatch(ctx, service, game, plan, profileName)
	}

	// Get available files for the PRIMARY mod - unchanged, CLI-side:
	// PlanInstall's own Files already picked its non-interactive default;
	// interactive/--file selection below overrides plan.Files with exactly
	// what the user chose (see InstallPlan.Files' doc comment - ApplyInstall
	// installs exactly plan.Files, no re-selection).
	files, err := service.GetModFiles(ctx, installSource, mod)
	if err != nil {
		return fmt.Errorf("failed to get mod files: %w", err)
	}
	if installVersion != "" {
		// #96: resolve --version against the RAW list (archived included -
		// a version pin usually names an archived file). The matches become
		// the selection pool for --file / --yes / the interactive prompt.
		files, err = core.ResolveVersionFiles(installSource, files, installVersion)
		if err != nil {
			return err
		}
	} else {
		files = core.FilterAndSortFiles(files, installShowArchived)
	}
	if len(files) == 0 {
		return fmt.Errorf("no downloadable files available for this mod")
	}

	// validateFileSelection is the CLI-side enforcement of #211's
	// variant-exclusivity rule (Service.ValidateInstallFileSelection):
	// the interactive path re-prompts on a mixed pak+exmodz pick, --file
	// hard-errors immediately - both friendlier/earlier than the identical
	// backstop core.ApplyInstall applies to plan.Files right before this
	// override (internal/core/flows.go ~line 3671).
	validateFileSelection := func(sel []domain.DownloadableFile) error {
		return service.ValidateInstallFileSelection(plan.SourceID, sel)
	}
	selectedFiles, err := selectInstallFiles(files, validateFileSelection)
	if err != nil {
		return err
	}
	plan.Files = make([]domain.DownloadableFile, len(selectedFiles))
	for i, f := range selectedFiles {
		plan.Files[i] = *f
	}

	// Show selected files - unchanged, CLI-side.
	if !jsonOutput {
		if len(selectedFiles) == 1 {
			fmt.Printf("\nFile: %s\n", displayFileLabel(*selectedFiles[0]))
		} else {
			fmt.Printf("\nFiles (%d):\n", len(selectedFiles))
			for _, f := range selectedFiles {
				fmt.Printf("  - %s\n", displayFileLabel(*f))
			}
		}
	}

	opts := core.InstallOptions{
		// TargetVersion/TargetFileIDs are resolved in core for the STRICT
		// path too (#140): plan.Files above already reflects the version
		// pool sub-selection (interactive/--file/-y), which core keeps
		// verbatim, so this is normally a no-op cross-check - but it makes
		// the options describe the user's actual request (the #143 lock
		// gate judges the same pins the install honors) instead of relying
		// on the CLI-side plan.Files override alone.
		TargetVersion: installVersion,
		TargetFileIDs: installFileIDList(),
		SkipVerify:    skipVerify,
		Force:         installForce,
		SkipHooks:     noHooks,
		// AcceptConflicts is deliberately left false: the conflict prompt
		// below answers it. --force implies it in core, so a forced install
		// never reaches the prompt at all.
	}

	// progress prints every diagnostic and status line at its exact point
	// of occurrence, driven entirely by core.ApplyInstall's progress events
	// - including diagnostics that also land in result.Warnings/.Notes (see
	// core.InstallResult's doc comment). Those slices are never separately
	// batch-printed below: every entry has a corresponding event here, so
	// doing so would double-print.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.InstallBeforeAllForced, core.InstallBeforeEachForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.InstallDownloadStarted:
			if p.Total > 1 {
				fmt.Printf("\n[%d/%d] Downloading %s...\n", p.Index, p.Total, displayFileLabel(*p.File))
			} else {
				fmt.Printf("\nDownloading %s...\n", displayFileLabel(*p.File))
			}
		case core.InstallDownloading:
			if p.TotalBytes > 0 {
				bar := progressBar(p.Percent, 30)
				fmt.Printf("\r  [%s] %.1f%% (%s / %s)", bar, p.Percent, formatSize(p.Downloaded), formatSize(p.TotalBytes))
			} else {
				fmt.Printf("\r  Downloaded %s", formatSize(p.Downloaded))
			}
		case core.InstallDownloadDone:
			fmt.Println()
		case core.InstallDownloadFailed:
			if strings.Contains(p.Detail, "third-party downloads") && mod.SourceURL != "" {
				fmt.Println()
				fmt.Println("  ⚠  This mod author has disabled API downloads.")
				fmt.Println("  To install manually:")
				fmt.Println()
				fmt.Printf("    1. Download from: %s\n", mod.SourceURL)
				fmt.Printf("    2. Import:        lmm import <downloaded-file> --id %s\n", mod.ID)
				fmt.Println()
			}
		case core.InstallChecksumComputed:
			fmt.Printf("  Checksum: %s\n", truncateChecksum(p.Detail))
		case core.InstallCompiling:
			fmt.Printf("\nRetaining %s for merge...\n", displayFileLabel(*p.File))
		case core.InstallExtracting:
			fmt.Println("\nExtracting to cache...")
		case core.InstallDeploying:
			fmt.Println("Deploying to game directory...")
		case core.InstallNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.InstallMergedPakSyncFailed:
			// The event carries the RAW error (#288: the multi-select path
			// words this line differently) - this path's historical wording
			// is the "syncing merged pak: " phrase core used to bake in.
			fmt.Fprintf(os.Stderr, "Warning: syncing merged pak: %s\n", p.Detail)
		case core.InstallWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.ApplyInstall(ctx, game, plan, opts, quietSink(progress))

	// Ruling 1: an unaccepted file conflict comes back as a typed error, not
	// a callback into this prompt. ApplyInstall stopped before it deployed
	// or wrote anything (the download is cached), so accepting is simply a
	// re-run of the same Apply with AcceptConflicts set.
	//
	// Ruling 15/2: under --json the prompt is unanswerable, so the
	// *core.ConflictError is returned untouched and reportError renders it
	// as the envelope with details.conflicts - the conflicts as data, which
	// is strictly more than the prompt's own summary showed. --force is how
	// a non-interactive caller accepts them (core reads it as
	// AcceptConflicts, so a forced install never gets here at all).
	var conflictErr *core.ConflictError
	if errors.As(err, &conflictErr) && !jsonOutput {
		proceed, readErr := confirmInstallConflicts(ctx, service, game, profileName, conflictErr.Conflicts)
		if readErr != nil {
			// A genuine stdin read failure, not an ordinary decline - see
			// confirmInstallConflicts' doc comment. Propagate the real
			// error instead of the generic "installation cancelled".
			return readErr
		}
		if !proceed {
			return fmt.Errorf("installation cancelled")
		}
		opts.AcceptConflicts = true
		result, err = service.ApplyInstall(ctx, game, plan, opts, quietSink(progress))
	}
	if err != nil {
		// Diagnostics accumulated before a fatal error (ApplyInstall's
		// error-path convention returns them alongside it) were already
		// printed above, live, via progress - nothing left to print here.
		return err
	}

	// Ruling 15: the InstallResult document, in place of the readout below.
	if jsonOutput {
		return emitJSON(result)
	}

	fmt.Printf("\n✓ Installed: %s v%s\n", mod.Name, mod.Version)
	// #197 postsmoke UX fix: a DeployCompile ".exmodz" mod deploys zero
	// files of its own by design (validate+retain only - it participates
	// in the profile's shared merged pak instead, synced separately
	// above) - "Files deployed: 0" read as a failure, not the correct,
	// expected outcome it actually is. Copilot review (#200): that sync is
	// non-fatal, so this line unconditionally claimed "merged pak updated"
	// even when it had just failed loudly on stderr above - thread the
	// actual outcome instead of asserting success either way.
	switch {
	case game.DeployMode != domain.DeployCompile || result.FilesDeployed != 0:
		fmt.Printf("  Files deployed: %d\n", result.FilesDeployed)
	case result.MergedPakSyncFailed:
		fmt.Println("  Installed; merged pak sync FAILED — see warning above")
	default:
		fmt.Println("  Installed (merged pak updated)")
	}
	fmt.Printf("  Added to profile: %s\n", profileName)

	return nil
}

// doInstallBatch executes plan's dependency-present install via
// ApplyInstall's restored BATCH-path semantics (Fix wave 1 - see
// task-2-report.md's "Fix wave 1 (dep-path fidelity)" entry for the full
// review trace this fixes): every mod - each dependency, then the primary -
// is treated COMPLETELY identically, reproducing cmd/lmm/install.go's
// pre-extraction batchInstallMods console output byte-for-byte (git show
// 5243286:cmd/lmm/install.go, lines ~1175-1347). In particular, unlike
// doInstall's own single-mod code above: NO interactive file selection
// (always the primary-or-first file, re-resolved per mod - though an
// explicit --file pins the NAMED mod's selection via opts.TargetFileIDs,
// #140, exactly as --version pins its version), NO blocking conflict
// prompt (a non-blocking inline "⚠ N file conflict(s)" warning only) -
// doInstall's pre-extraction early return never reached either of those
// for a dependency-having install, so this function must not either. Must
// never read stdin - the caller's "Install N mod(s)?" confirm prompt (run
// before this is ever called) is the only legitimate stdin read anywhere
// in this path.
func doInstallBatch(ctx context.Context, service *core.Service, game *domain.Game, plan *core.InstallPlan, profileName string) error {
	if !jsonOutput {
		fmt.Printf("\nInstalling %d mod(s)...\n", len(plan.Dependencies)+1)
	}

	opts := core.InstallOptions{
		// TargetVersion/TargetFileIDs pin --version/--file to the named/
		// primary mod ONLY - see their doc comments. Dependencies still
		// install at latest with their own auto-picked primary file (#96
		// decision 6); a version or file ID that doesn't resolve for the
		// primary aborts the whole install before any dependency is
		// touched. Previously --file was silently ignored on this path
		// (#140, the #93 silent-flag class).
		TargetVersion: installVersion,
		TargetFileIDs: installFileIDList(),
		SkipVerify:    skipVerify,
		Force:         installForce,
		SkipHooks:     noHooks,
	}

	// pendingCompileDeps buffers the display names of DeployCompile
	// zero-file dependencies as InstallDepInstalled events arrive live -
	// their "merged pak updated" claim can't be verified until the SINGLE
	// end-of-batch sync (inside ApplyInstall) actually runs, which happens
	// after every per-dep event has already streamed. Printed once
	// ApplyInstall returns with the real outcome (#197 postsmoke Copilot
	// review fix, #200).
	var pendingCompileDeps []string

	// progress prints every diagnostic and status line at its exact point
	// of occurrence, driven entirely by core.ApplyInstall's BATCH-path
	// progress events - reproducing batchInstallMods' console output
	// byte-for-byte. See each Install* constant's doc comment (in
	// internal/core/flows.go, starting at InstallDepInstalling) for the
	// exact text/semantics being restored here.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.InstallBeforeAllForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.InstallDepInstalling:
			fmt.Printf("\n[%d/%d] Installing: %s v%s\n", p.Index, p.Total, p.ModName, p.ModVersion)
		case core.InstallDepReinstalling:
			fmt.Printf("  Removing previous installation...\n")
		case core.InstallDepFileSelected:
			fmt.Printf("  File: %s\n", displayFileLabel(*p.File))
		case core.InstallDepDownloading:
			bar := progressBar(p.Percent, 20)
			fmt.Printf("\r  [%s] %.1f%%", bar, p.Percent)
		case core.InstallDepDownloadDone:
			fmt.Println()
		case core.InstallDepSkipped:
			// Detail already carries its restored, failure-type-specific,
			// fully-prefixed text verbatim ("Skipped: ..." for a hook
			// failure, "Error: ..." for everything else) - see
			// InstallDepSkipped's doc comment.
			fmt.Printf("  %s\n", p.Detail)
		case core.InstallLockRefusal:
			// Detail is the refusal SENTENCE only (#288); this path has
			// always printed the ErrModLocked-wrapped error, so it puts the
			// sentinel back. The multi-select path prints the bare sentence.
			fmt.Printf("  Skipped: %v: %s\n", core.ErrModLocked, p.Detail)
		case core.InstallChecksumComputed:
			fmt.Printf("  Checksum: %s\n", truncateChecksum(p.Detail))
		case core.InstallDepConflictWarning:
			fmt.Printf("  ⚠ %s\n", p.Detail)
		case core.InstallDepInstalled:
			// #197 postsmoke UX fix: see the single-mod path's identical
			// fix above - a DeployCompile ".exmodz" dependency deploys
			// zero files of its own by design. The "merged pak updated"
			// half of that claim can't be printed yet (see
			// pendingCompileDeps above) - deferred until ApplyInstall
			// returns and the batch's one sync attempt is known to have
			// succeeded or failed.
			if game.DeployMode == domain.DeployCompile && p.FilesExtracted == 0 {
				pendingCompileDeps = append(pendingCompileDeps, p.ModName)
			} else {
				fmt.Printf("  ✓ Installed (%d files)\n", p.FilesExtracted)
			}
		case core.InstallNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.InstallChecksumSaveFailed:
			// Flush, not indented (#288) - the multi-select path indents it.
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.InstallMergedPakSyncFailed:
			fmt.Fprintf(os.Stderr, "Warning: syncing merged pak: %s\n", p.Detail)
		case core.InstallWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.ApplyInstall(ctx, game, plan, opts, quietSink(progress))
	if err != nil {
		// Diagnostics accumulated before a fatal error (install.before_all,
		// forced) were already printed above, live, via progress - nothing
		// left to print here. Unlike the STRICT path, an ordinary per-mod
		// failure in the BATCH path never reaches here - it's recorded in
		// result.Failed/Skipped and printed via the terminal Summary below
		// instead (see InstallDepSkipped). The one BATCH-path failure that
		// DOES reach here: --version not resolving for the named/primary
		// mod (opts.TargetVersion) - #96 decision "abort the whole install,
		// a per-mod Failed line isn't loud enough for a version the user
		// explicitly requested" - surfaced before any dependency is
		// touched, so nothing has been installed yet on this path either.
		return err
	}

	// Ruling 15: the InstallResult document - Installed/Failed/Skipped and
	// MergedPakSyncFailed are the data behind every line below.
	if jsonOutput {
		return emitJSON(result)
	}

	// The batch's one merged-pak sync attempt (inside ApplyInstall) has
	// now happened - print the deferred per-dependency completion lines
	// pendingCompileDeps buffered above, with the outcome finally known.
	for _, name := range pendingCompileDeps {
		if result.MergedPakSyncFailed {
			fmt.Printf("  ✓ %s: installed; merged pak sync FAILED — see warning above\n", name)
		} else {
			fmt.Printf("  ✓ %s: Installed (merged pak updated)\n", name)
		}
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Installed: %d\n", len(result.Installed))
	if len(result.Failed) > 0 {
		fmt.Printf("Failed: %d (%s)\n", len(result.Failed), strings.Join(installedRefNames(result.Failed), ", "))
	}

	return nil
}

// promptMultiSelection prompts the user to select one or more numbers
// Accepts formats like: "1", "1,3,5", "1-3", "1..3", "1,3-5"
func promptMultiSelection(prompt string, defaultChoice, max int) ([]int, error) {
	return promptMultiSelectionFrom(os.Stdin, prompt, defaultChoice, max)
}

// promptMultiSelectionFrom is the testable core of promptMultiSelection
func promptMultiSelectionFrom(r io.Reader, prompt string, defaultChoice, max int) ([]int, error) {
	reader := bufio.NewReader(r)

	for {
		selections, retry, err := readMultiSelectionLine(reader, prompt, defaultChoice, max)
		if err != nil {
			return nil, err
		}
		if retry {
			continue
		}
		return selections, nil
	}
}

// readMultiSelectionLine prints prompt and reads/parses a single selection
// line from reader - the shared attempt-level logic behind both
// promptMultiSelectionFrom's own invalid-format retry loop and
// selectInstallFilesFrom's validation retry loop (#211). reader is a
// *bufio.Reader (not io.Reader) so callers that loop across multiple
// attempts pass the SAME instance every time: bufio.Reader.fill() eagerly
// buffers everything available from the underlying stream on first read,
// so wrapping it afresh per attempt (as calling promptMultiSelectionFrom
// itself in a loop would) silently discards any input beyond the first
// line the moment that attempt's Reader is discarded.
//
// retry=true means an invalid-format message was already printed and the
// caller should prompt again; err is either a genuine read failure or
// ErrCancelled, both of which the caller returns immediately.
func readMultiSelectionLine(reader *bufio.Reader, prompt string, defaultChoice, max int) (selections []int, retry bool, err error) {
	if jsonOutput {
		return nil, false, core.ErrConfirmationRequired
	}
	fmt.Printf("\n%s (q to cancel) [%d]: ", prompt, defaultChoice)
	input, err := reader.ReadString('\n')
	if err != nil {
		return nil, false, fmt.Errorf("reading input: %w", err)
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return []int{defaultChoice}, false, nil
	}
	if input == "q" || input == "Q" {
		return nil, false, ErrCancelled
	}

	selections, err = parseRangeSelection(input, max)
	if err != nil {
		fmt.Printf("Invalid selection: %v\n", err)
		return nil, true, nil
	}

	return selections, false, nil
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

// installMultipleMods installs `lmm install <query>`'s multi-select
// selection: core plans the whole batch (PlanInstallMany), core executes it
// (ApplyInstall's batch branch), and this function does nothing but render
// the resulting event stream. It replaces the hand-rolled batchInstallMods
// engine that used to live here (v2 Phase 2 Unit H, #288) - hooks, lock
// gating, download, deploy, persistence and the merged-pak sync all moved
// into internal/core, so `lmm serve` can drive the same flow.
//
// The wording below is this path's own, not doInstallBatch's, wherever the
// two frozen contracts differ - the lock refusal (printed unwrapped here,
// ErrModLocked-prefixed there), a failed checksum save (indented here,
// flush there) and a failed merged-pak sync ("could not sync merged pak"
// here, "syncing merged pak" there). Core emits the fact; each frontend
// owns the sentence. Every other line is identical to doInstallBatch's, as
// it always was - the two paths render the same BATCH engine.
func installMultipleMods(ctx context.Context, service *core.Service, game *domain.Game, mods []*domain.Mod, profileName string) error {
	plan, err := service.PlanInstallMany(ctx, game, profileName, mods, installShowArchived)
	if err != nil {
		return err
	}

	if !jsonOutput {
		fmt.Printf("\nInstalling %d mod(s)...\n", len(plan.Batch))
	}

	opts := core.InstallOptions{
		// No TargetVersion/TargetFileIDs: --version/--file name a single
		// mod, and this path is reached only from a multi-mod search
		// selection, which has never had a per-mod file pin to honor.
		SkipVerify: skipVerify,
		Force:      installForce,
		SkipHooks:  noHooks,
	}

	// pendingCompileMods buffers the display names of DeployCompile
	// zero-file mods as InstallDepInstalled events arrive live - their
	// "merged pak updated" claim can't be verified until the batch's SINGLE
	// end-of-install sync (inside ApplyInstall) has actually run, which
	// happens after every per-mod event has already streamed. Printed once
	// ApplyInstall returns with the real outcome (#197 postsmoke Copilot
	// review fix, #200).
	var pendingCompileMods []string

	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.InstallBeforeAllForced:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.InstallDepInstalling:
			fmt.Printf("\n[%d/%d] Installing: %s v%s\n", p.Index, p.Total, p.ModName, p.ModVersion)
		case core.InstallDepReinstalling:
			fmt.Printf("  Removing previous installation...\n")
		case core.InstallDepFileSelected:
			fmt.Printf("  File: %s\n", displayFileLabel(*p.File))
		case core.InstallDepDownloading:
			bar := progressBar(p.Percent, 20)
			fmt.Printf("\r  [%s] %.1f%%", bar, p.Percent)
		case core.InstallDepDownloadDone:
			fmt.Println()
		case core.InstallDepSkipped:
			// Detail already carries its failure-type-specific, fully
			// prefixed text verbatim ("Skipped: ..." for a hook failure,
			// "Error: ..." for everything else).
			fmt.Printf("  %s\n", p.Detail)
		case core.InstallLockRefusal:
			fmt.Printf("  Skipped: %s\n", p.Detail)
		case core.InstallChecksumComputed:
			fmt.Printf("  Checksum: %s\n", truncateChecksum(p.Detail))
		case core.InstallDepConflictWarning:
			fmt.Printf("  ⚠ %s\n", p.Detail)
		case core.InstallDepInstalled:
			// A DeployCompile ".exmodz" mod deploys zero files of its own by
			// design (validate+retain only; the sync below is what actually
			// deploys it) - see pendingCompileMods above.
			if game.DeployMode == domain.DeployCompile && p.FilesExtracted == 0 {
				pendingCompileMods = append(pendingCompileMods, p.ModName)
			} else {
				fmt.Printf("  ✓ Installed (%d files)\n", p.FilesExtracted)
			}
		case core.InstallNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.InstallChecksumSaveFailed:
			fmt.Fprintf(os.Stderr, "  Warning: %s\n", p.Detail)
		case core.InstallMergedPakSyncFailed:
			// Printed unconditionally, never --verbose-gated: if this had
			// failed loudly the first time, the #197 postsmoke bug (content
			// silently missing from the game) would have been noticed
			// immediately.
			fmt.Fprintf(os.Stderr, "Warning: could not sync merged pak: %s\n", p.Detail)
		case core.InstallWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.ApplyInstall(ctx, game, plan, opts, quietSink(progress))
	if err != nil {
		// Diagnostics accumulated before a fatal error were already printed
		// above, live, via progress. No ordinary per-mod failure reaches
		// here - the batch engine records those in result.Failed/Skipped and
		// the Summary below reports them; only a fatal one does (an
		// unforced install.before_all failure, a profile that can't be
		// created, an unresolvable link method, or a stale plan).
		return err
	}

	// Ruling 15: the InstallResult document (see doInstallBatch's twin).
	if jsonOutput {
		return emitJSON(result)
	}

	// The batch's one merged-pak sync attempt (inside ApplyInstall) has now
	// happened - print the deferred per-mod completion lines with the
	// outcome finally known.
	for _, name := range pendingCompileMods {
		if result.MergedPakSyncFailed {
			fmt.Printf("  ✓ %s: installed; merged pak sync FAILED — see warning above\n", name)
		} else {
			fmt.Printf("  ✓ %s: Installed (merged pak updated)\n", name)
		}
	}

	fmt.Printf("\n--- Summary ---\n")
	fmt.Printf("Installed: %d\n", len(result.Installed))
	if len(result.Failed) > 0 {
		fmt.Printf("Failed: %d (%s)\n", len(result.Failed), strings.Join(installedRefNames(result.Failed), ", "))
	}

	return nil
}

// parseRangeSelection parses a selection string like "1,3-5,8" or "1..3"
// Returns sorted, unique slice of integers in range [1, max]
func parseRangeSelection(input string, max int) ([]int, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty selection")
	}

	seen := make(map[int]bool)
	var result []int

	// Split by comma
	parts := strings.Split(input, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check for range (either "-" or "..")
		var rangeStart, rangeEnd int
		var err error

		if strings.Contains(part, "..") {
			// Handle ".." range
			rangeParts := strings.Split(part, "..")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			rangeStart, err = strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[0])
			}
			rangeEnd, err = strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[1])
			}
		} else if strings.Contains(part, "-") && !strings.HasPrefix(part, "-") {
			// Handle "-" range (but not negative numbers)
			rangeParts := strings.SplitN(part, "-", 2)
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			rangeStart, err = strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[0])
			}
			rangeEnd, err = strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[1])
			}
		} else {
			// Single number
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", part)
			}
			rangeStart = n
			rangeEnd = n
		}

		// Validate range
		if rangeStart > rangeEnd {
			return nil, fmt.Errorf("invalid range: start %d > end %d", rangeStart, rangeEnd)
		}
		if rangeStart < 1 || rangeEnd > max {
			return nil, fmt.Errorf("selection out of range (1-%d): %s", max, part)
		}

		// Add to result
		for i := rangeStart; i <= rangeEnd; i++ {
			if !seen[i] {
				seen[i] = true
				result = append(result, i)
			}
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid selections")
	}

	// Sort for consistent output
	sort.Ints(result)
	return result, nil
}

// showInstallPlan displays PlanInstall's resolved dependency tree (install
// order: dependencies first, target last) and any missing/cyclic-dependency
// warnings - byte-identical to the pre-refit CLI's own showInstallPlan
// (which took a locally-resolved dependency list), now sourced from
// *core.InstallPlan since dependency resolution itself moved into
// Service.PlanInstall - see the task report.
func showInstallPlan(plan *core.InstallPlan) {
	fmt.Printf("\nDependency tree (install order):\n")
	i := 1
	for _, dep := range plan.Dependencies {
		fmt.Printf("  %d. %s v%s (ID: %s) [dependency]\n", i, dep.Name, dep.Version, dep.ID)
		i++
	}
	fmt.Printf("  %d. %s v%s (ID: %s) [target]\n", i, plan.Mod.Name, plan.Mod.Version, plan.Mod.ID)

	if plan.CycleDetected {
		fmt.Fprintf(os.Stderr, "\n⚠ Warning: Circular dependency detected among dependencies; install order is best-effort.\n")
	}

	if len(plan.MissingDependencies) > 0 {
		fmt.Printf("\n⚠ Warning: %d dependency(ies) not available on source:\n", len(plan.MissingDependencies))
		for _, ref := range plan.MissingDependencies {
			fmt.Printf("  - %s (may require manual install)\n", domain.ModKey(ref.SourceID, ref.ModID))
		}
	}

	// #52 item 10: a GetDependencies failure that WASN'T just "this source
	// lacks the capability" - matching the existing "Warning: %s" stderr
	// style used elsewhere in this file (e.g. line ~522/558/620/649).
	// core.DependencyWarning carries SourceID/ModID/Message as structured
	// data (v2 Phase 3 Task 2, #301); this reconstructs the historical
	// "<sourceID:modID>: <error>" line byte-for-byte.
	for _, w := range plan.DependencyWarnings {
		fmt.Fprintf(os.Stderr, "Warning: %s: %s\n", domain.ModKey(w.SourceID, w.ModID), w.Message)
	}
}

// truncateChecksum returns a display-friendly checksum (first 12 chars + "...").
func truncateChecksum(checksum string) string {
	if len(checksum) > 12 {
		return checksum[:12] + "..."
	}
	return checksum
}

// installedRefNames extracts each entry's Name - InstallResult.Installed/
// Failed carry structured core.InstalledRef data (v2 Phase 3 Task 2, #301);
// the terminal summary only ever displays the name.
func installedRefNames(refs []core.InstalledRef) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return names
}
