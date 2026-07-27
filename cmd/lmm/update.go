package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	updateSource  string
	updateProfile string
	updateAll     bool
	updateDryRun  bool
	updateForce   bool
)

type updateJSONOutput struct {
	GameID  string          `json:"game_id"`
	Profile string          `json:"profile"`
	Updates []updateModJSON `json:"updates"`
	// Skipped reports installed mods that were never checked, so a consumer
	// can tell "nothing to update" apart from "nothing was looked at". An
	// empty updates array means both, and only this distinguishes them.
	Skipped updateSkippedJSON `json:"skipped"`
	// Error is set when the check itself failed. An empty updates array
	// otherwise means "nothing to update"; with this set it means the answer
	// is unknown. Omitted on success so the common document stays unchanged.
	Error string `json:"error,omitempty"`
}

type updateSkippedJSON struct {
	Pinned int `json:"pinned"`
	Local  int `json:"local"`
}

type updateModJSON struct {
	ModID        string `json:"mod_id"`
	Name         string `json:"name"`
	Current      string `json:"current_version"`
	Available    string `json:"available_version"`
	UpdatePolicy string `json:"update_policy"`
}

// singleUpdateJSON is the one-document --json result of `lmm update <mod-id>`
// and `lmm update rollback <mod-id>`. It is deliberately NOT updateJSONOutput:
// that document reports a *check* over many mods (updates[], skipped counts);
// this reports a *single applied event*, so an updates[] array here would imply
// a batch that never happened. Emitted exactly once, on stdout, at the end of
// the operation. Failures never reach this type — they return before any
// document is written, and Execute renders {"error":...} instead, keeping the
// one-document-on-stdout invariant without ErrReported.
type singleUpdateJSON struct {
	ModID       string `json:"mod_id"`
	Name        string `json:"name"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version,omitempty"` // omitted for up_to_date / skipped
	Changelog   string `json:"changelog,omitempty"`
	// Status: "updated" | "up_to_date" | "skipped" | "available" | "rolled_back"
	Status string `json:"status"`
	// Reason qualifies status=="skipped": "pinned" | "local". Omitted otherwise.
	Reason string `json:"reason,omitempty"`
}

// emitSingleUpdateJSON writes doc as the sole JSON document on stdout,
// 2-space indented to match updateJSONOutput's existing formatting.
func emitSingleUpdateJSON(doc singleUpdateJSON) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encoding json: %w", err)
	}
	return nil
}

var updateCmd = &cobra.Command{
	Use:   "update [mod-id]",
	Short: "Check for or apply mod updates",
	Long: `Check for available updates or update specific mods.

Without arguments, checks all installed mods for updates and prints a
table (auto-update-policy mods are then applied automatically; pass --all
to apply every available update). With a mod ID, checks and updates just
that mod. If the same mod ID is installed from more than one source in
the profile, use -s/--source to disambiguate (the error names the sources
in conflict). -s/--source is resolved once up front either way, so on a
game with more than one configured source it also avoids an interactive
source prompt for the bulk check.

If the update check itself fails partway through (e.g. a source outage),
whatever was learned before the failure is still printed and the command
exits non-zero rather than silently claiming success.

--json prints exactly one JSON document to stdout, in one of two shapes:
  - Bulk check (no mod ID): {game_id, profile, updates: [...], skipped:
    {pinned, local}, error?}. error is present when the check itself
    failed partway through; updates/skipped still reflect whatever was
    learned first.
  - Single mod (a mod ID given) or 'update rollback': {mod_id, name,
    from_version, to_version, changelog, status, reason}. status is one
    of "updated", "up_to_date", "skipped", "available" (--dry-run), or
    "rolled_back"; reason is set only when status is "skipped" ("pinned"
    or "local").

Examples:
  lmm update --game skyrim-se                    # Check all mods for updates
  lmm update 12345 --game skyrim-se              # Update specific mod
  lmm update 12345 --game skyrim-se --source nexusmods  # Disambiguate by source
  lmm update --game skyrim-se --all              # Apply all available updates
  lmm update --game skyrim-se --dry-run          # Show what would update`,
	Args: cobra.MaximumNArgs(1),
	RunE: runUpdate,
}

var updateRollbackCmd = &cobra.Command{
	Use:   "rollback <mod-id>",
	Short: "Rollback a mod to its previous version",
	Long: `Rollback a mod to the version before the last update.

The previous version must still be available in the cache. If the same
mod ID is installed from more than one source in the profile, use
-s/--source to disambiguate.

--json prints the single-mod document (see 'lmm update --help') with
status "rolled_back".

Examples:
  lmm update rollback 12345 --game skyrim-se
  lmm update rollback 12345 --game skyrim-se --source nexusmods`,
	Args: cobra.ExactArgs(1),
	RunE: runUpdateRollback,
}

func init() {
	updateCmd.Flags().StringVarP(&updateSource, "source", "s", "", "mod source (default: the sole configured source; prompts when several are configured)")
	updateCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile to check (default: active profile)")
	updateCmd.Flags().BoolVar(&updateAll, "all", false, "apply all available updates")
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "show what would update without applying")
	updateCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "continue even if hooks fail")

	updateRollbackCmd.Flags().StringVarP(&updateSource, "source", "s", "", "mod source (default: the sole configured source; prompts when several are configured)")
	updateRollbackCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile (default: active profile)")
	updateRollbackCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "continue even if hooks fail")

	updateCmd.AddCommand(updateRollbackCmd)
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doUpdate(ctx, service, game, args)
	})
}

func doUpdate(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	// Resolve source: use flag if set, otherwise first configured source
	var err error
	updateSource, err = resolveSource(game, updateSource, false)
	if err != nil {
		return err
	}

	// Determine profile
	profileName, err := resolveProfile(service, game.ID, updateProfile)
	if err != nil {
		return err
	}

	// Get installed mods
	installed, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("failed to get installed mods: %w", err)
	}

	if len(installed) == 0 {
		switch {
		case jsonOutput && len(args) > 0:
			// Fall through to the single-mod lookup below: with nothing
			// installed, candidates stays empty and it reports the same
			// "not found in profile" error as any other absent mod.
		case jsonOutput:
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			out := updateJSONOutput{
				GameID: game.ID, Profile: profileName, Updates: []updateModJSON{},
				Skipped: updateSkippedJSON{},
			}
			if err := enc.Encode(out); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
		default:
			fmt.Println("No mods installed.")
			return nil
		}
	}

	// If specific mod ID provided, update just that mod
	if len(args) > 0 {
		modID := args[0]
		var targetMod *domain.InstalledMod
		// Mod IDs are only unique within a source, so the same ID can appear
		// more than once in a profile. Collect every candidate rather than
		// keeping one: naming an arbitrary source would be wrong roughly half
		// the time. Matching on source as well as ID is also what used to make
		// a local mod look absent from its own profile.
		var candidates []*domain.InstalledMod
		for i := range installed {
			if installed[i].ID != modID {
				continue
			}
			if installed[i].SourceID == updateSource {
				targetMod = &installed[i]
				break
			}
			candidates = append(candidates, &installed[i])
		}
		if targetMod == nil {
			switch len(candidates) {
			case 0:
				return fmt.Errorf("mod %s not found in profile %s", modID, profileName)
			case 1:
				if candidates[0].SourceID == domain.SourceLocal {
					// Present, just not checkable — informational, not an error.
					if jsonOutput {
						return emitSingleUpdateJSON(singleUpdateJSON{
							ModID: modID, Name: candidates[0].Name, FromVersion: candidates[0].Version, Status: "skipped", Reason: "local",
						})
					}
					fmt.Printf("%s is a local mod — no remote source to check for updates.\n", candidates[0].Name)
					return nil
				}
				return fmt.Errorf("mod %s in profile %s belongs to source %q, not %q; retry with --source %s",
					modID, profileName, candidates[0].SourceID, updateSource, candidates[0].SourceID)
			default:
				sources := make([]string, 0, len(candidates))
				hasLocal := false
				for _, c := range candidates {
					sources = append(sources, c.SourceID)
					if c.SourceID == domain.SourceLocal {
						hasLocal = true
					}
				}
				sort.Strings(sources) // deterministic regardless of install order
				// The local caveat only earns its place when one of the
				// candidates actually is local; on a purely remote ambiguity
				// (nexusmods vs curseforge) it is noise.
				caveat := ""
				if hasLocal {
					caveat = " (local mods cannot be update-checked)"
				}
				return fmt.Errorf("mod %s is in profile %s under multiple sources (%s); retry with --source to choose%s",
					modID, profileName, strings.Join(sources, ", "), caveat)
			}
		}

		return applySingleUpdate(ctx, service, game, targetMod, profileName)
	}

	if verbose {
		fmt.Printf("Checking %d mod(s) for updates in %s (profile: %s)...\n", len(installed), game.Name, profileName)
		ctx = context.WithValue(ctx, domain.UpdateProgressContextKey, domain.UpdateProgressFunc(func(n, total int, name string) {
			fmt.Fprintf(os.Stderr, "  %d/%d: %s\n", n, total, truncate(name, 60))
		}))
	}

	// Check for updates (partial results returned even when some mods fail to fetch)
	updater := service.NewUpdater()
	updates, checkErr := updater.CheckUpdates(ctx, game, installed)
	if checkErr != nil {
		if errors.Is(checkErr, domain.ErrAuthRequired) {
			return authPromptError(updateSource)
		}
		// Surface warning but continue to show partial updates
		fmt.Fprintf(os.Stderr, "Warning: %v\n", checkErr)
	}

	// finish is returned at every exit below rather than bailing out early: a
	// partial check still has results worth printing and auto-updates worth
	// applying, so the non-zero exit has to come after that work, not instead
	// of it. ErrReported because the failure is already on stderr (or in the
	// JSON document) and Execute must not print it twice.
	finish := func() error {
		if checkErr != nil {
			return fmt.Errorf("update check incomplete: %w", ErrReported)
		}
		return nil
	}

	if len(updates) == 0 {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			skips := core.CountUpdateSkips(installed)
			out := updateJSONOutput{
				GameID: game.ID, Profile: profileName, Updates: []updateModJSON{},
				Skipped: updateSkippedJSON{Pinned: skips.Pinned, Local: skips.Local},
			}
			if checkErr != nil {
				out.Error = checkErr.Error()
			}
			if err := enc.Encode(out); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return finish()
		}
		// "All mods are up to date" would be false if the only reason there is
		// nothing to report is that every mod was skipped. An empty profile
		// returned earlier, so len(installed) is non-zero here.
		skips := core.CountUpdateSkips(installed)
		if skips.Total() == len(installed) {
			printSkipped(skips)
			return finish()
		}
		// A failed check produces no updates too. Claiming currency here would
		// repeat the defect this whole command's reporting was fixed for: the
		// warning goes to stderr, so a caller reading stdout would see only a
		// false success.
		if checkErr != nil {
			fmt.Println("Update check did not complete — see the warning above. No results to report.")
		} else {
			fmt.Println("All mods are up to date.")
		}
		if skips.Total() > 0 {
			fmt.Println()
			printSkipped(skips)
		}
		return finish()
	}

	if jsonOutput {
		skips := core.CountUpdateSkips(installed)
		out := updateJSONOutput{
			GameID: game.ID, Profile: profileName, Updates: make([]updateModJSON, len(updates)),
			Skipped: updateSkippedJSON{Pinned: skips.Pinned, Local: skips.Local},
		}
		if checkErr != nil {
			out.Error = checkErr.Error()
		}
		for i, u := range updates {
			out.Updates[i] = updateModJSON{
				ModID:        u.InstalledMod.ID,
				Name:         u.InstalledMod.Name,
				Current:      u.InstalledMod.Version,
				Available:    u.NewVersion,
				UpdatePolicy: policyToString(u.InstalledMod.UpdatePolicy),
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return finish()
	}

	// Display available updates with policy
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(w, "MOD\tCURRENT\tAVAILABLE\tPOLICY\n"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintf(w, "---\t-------\t---------\t------\n"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	var autoUpdates []domain.Update
	for _, update := range updates {
		policyStr := policyToString(update.InstalledMod.UpdatePolicy)
		if update.InstalledMod.UpdatePolicy == domain.UpdateAuto {
			policyStr += " ✓"
			autoUpdates = append(autoUpdates, update)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			truncate(update.InstalledMod.Name, 40),
			update.InstalledMod.Version,
			update.NewVersion,
			policyStr,
		); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	fmt.Printf("\n%d update(s) available.\n", len(updates))
	if skips := core.CountUpdateSkips(installed); skips.Total() > 0 {
		fmt.Println()
		printSkipped(skips)
	}

	// Show changelogs where available
	var withChangelog []domain.Update
	for _, u := range updates {
		if u.Changelog != "" {
			withChangelog = append(withChangelog, u)
		}
	}
	if len(withChangelog) > 0 {
		fmt.Println("\nChangelogs:")
		for _, u := range withChangelog {
			cl := core.CleanChangelog(u.Changelog)
			const maxChangelog = 800
			if len(cl) > maxChangelog {
				cl = cl[:maxChangelog] + "\n..."
			}
			fmt.Printf("\n  %s (%s → %s):\n", u.InstalledMod.Name, u.InstalledMod.Version, u.NewVersion)
			for _, line := range strings.Split(strings.TrimSpace(cl), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}

	// Dry run mode - just show what would happen
	if updateDryRun {
		if len(autoUpdates) > 0 {
			fmt.Printf("\nWould auto-update %d mod(s):\n", len(autoUpdates))
			for _, u := range autoUpdates {
				fmt.Printf("  - %s %s → %s\n", u.InstalledMod.Name, u.InstalledMod.Version, u.NewVersion)
			}
		}
		fmt.Println("\nUse without --dry-run to apply updates.")
		return finish()
	}

	// Apply auto-updates
	if len(autoUpdates) > 0 {
		fmt.Printf("\nApplying %d auto-update(s)...\n", len(autoUpdates))
		for _, update := range autoUpdates {
			if err := applyUpdate(ctx, service, game, update, profileName); err != nil {
				fmt.Printf("  ✗ %s: %v\n", update.InstalledMod.Name, err)
			} else {
				fmt.Printf("  ✓ %s %s → %s\n", update.InstalledMod.Name, update.InstalledMod.Version, update.NewVersion)
			}
		}
	}

	// If --all flag, apply all remaining updates
	if updateAll {
		var notifyUpdates []domain.Update
		for _, update := range updates {
			if update.InstalledMod.UpdatePolicy != domain.UpdateAuto {
				notifyUpdates = append(notifyUpdates, update)
			}
		}

		if len(notifyUpdates) > 0 {
			fmt.Printf("\nApplying %d remaining update(s)...\n", len(notifyUpdates))
			for _, update := range notifyUpdates {
				if err := applyUpdate(ctx, service, game, update, profileName); err != nil {
					fmt.Printf("  ✗ %s: %v\n", update.InstalledMod.Name, err)
				} else {
					fmt.Printf("  ✓ %s %s → %s\n", update.InstalledMod.Name, update.InstalledMod.Version, update.NewVersion)
				}
			}
		}
	}

	return finish()
}

func applySingleUpdate(ctx context.Context, service *core.Service, game *domain.Game, mod *domain.InstalledMod, profileName string) error {
	// Check for update for this specific mod
	updater := service.NewUpdater()
	updates, err := updater.CheckUpdates(ctx, game, []domain.InstalledMod{*mod})
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(updateSource)
		}
		return fmt.Errorf("failed to check update: %w", err)
	}

	if len(updates) == 0 {
		// A pinned mod is filtered out before the source is queried, so no
		// version comparison ever happened — reporting it as up to date would
		// claim currency that was never checked.
		if mod.UpdatePolicy == domain.UpdatePinned {
			if jsonOutput {
				return emitSingleUpdateJSON(singleUpdateJSON{
					ModID: mod.ID, Name: mod.Name, FromVersion: mod.Version, Status: "skipped", Reason: "pinned",
				})
			}
			fmt.Printf("%s is pinned at v%s and was not checked.\n", mod.Name, mod.Version)
			fmt.Printf("Unpin with: lmm mod set-update %s --notify\n", mod.ID)
			return nil
		}
		if jsonOutput {
			return emitSingleUpdateJSON(singleUpdateJSON{
				ModID: mod.ID, Name: mod.Name, FromVersion: mod.Version, Status: "up_to_date",
			})
		}
		fmt.Printf("%s is already up to date (v%s).\n", mod.Name, mod.Version)
		return nil
	}

	update := updates[0]
	oldVersion := mod.Version
	newVersion := update.NewVersion
	if !jsonOutput {
		fmt.Printf("Updating %s %s → %s...\n", mod.Name, oldVersion, newVersion)
		if update.Changelog != "" {
			cl := core.CleanChangelog(update.Changelog)
			const maxChangelog = 500
			if len(cl) > maxChangelog {
				cl = cl[:maxChangelog] + "..."
			}
			fmt.Println("Changelog:")
			for _, line := range strings.Split(strings.TrimSpace(cl), "\n") {
				fmt.Printf("  %s\n", line)
			}
			fmt.Println()
		}
	}

	if updateDryRun {
		if jsonOutput {
			// Cleaned like the human output (upstream changelogs are HTML),
			// but untruncated - the 500-char cap is a display concern.
			return emitSingleUpdateJSON(singleUpdateJSON{
				ModID: mod.ID, Name: mod.Name, FromVersion: oldVersion, ToVersion: newVersion,
				Changelog: core.CleanChangelog(update.Changelog), Status: "available",
			})
		}
		fmt.Println("(dry-run: no changes applied)")
		return nil
	}

	if err := applyUpdate(ctx, service, game, update, profileName); err != nil {
		return err
	}

	if jsonOutput {
		return emitSingleUpdateJSON(singleUpdateJSON{
			ModID: mod.ID, Name: mod.Name, FromVersion: oldVersion, ToVersion: newVersion,
			Changelog: core.CleanChangelog(update.Changelog), Status: "updated",
		})
	}

	fmt.Printf("\n✓ Updated: %s %s → %s\n", mod.Name, oldVersion, newVersion)
	fmt.Println("  Previous version preserved for rollback")
	return nil
}

// applyUpdate applies upd to an installed mod: resolve -> call
// Service.ApplyUpdate -> print from its progress events. progress prints
// every diagnostic at its exact point of occurrence, driven entirely by
// core.ApplyUpdate's progress events - reproducing the pre-extraction CLI's
// exact console positioning (download progress, forced-hook warnings,
// after_each hook warnings, and the --verbose-gated link-method note).
func applyUpdate(ctx context.Context, service *core.Service, game *domain.Game, upd domain.Update, profileName string) error {
	opts := core.UpdateOptions{
		Hooks:       getResolvedHooks(service, game, profileName),
		HookRunner:  getHookRunner(service),
		HookContext: makeHookContext(game),
		Force:       updateForce,
	}

	progress := func(p core.DeployProgress) {
		switch p.Phase {
		case core.UpdateDownloading:
			if verbose && !jsonOutput {
				fmt.Printf("\r  Downloading: %.1f%%", p.Percent)
			}
		case core.UpdateDownloadDone:
			if verbose && !jsonOutput {
				fmt.Println()
			}
		case core.UpdateBeforeEachForced, core.UpdateWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.UpdateNote:
			if verbose && !jsonOutput {
				fmt.Printf("  %s\n", p.Detail)
			}
		}
	}

	_, err := service.ApplyUpdate(ctx, game, profileName, upd, opts, progress)
	return err
}

func runUpdateRollback(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doUpdateRollback(ctx, service, game, args[0])
	})
}

// doUpdateRollback resolves the target mod -> prints its own
// "Rolling back %s %s → %s..." header (using its own GetInstalledMod call,
// which also reproduces doUpdateRollback's pre-extraction guard errors
// verbatim: "mod not found: %s" and, before ApplyRollback is ever called,
// the same PreviousVersion/cache-existence checks ApplyRollback repeats
// internally - so the header never prints when either guard would fail,
// matching the pre-extraction ordering exactly) -> calls
// Service.ApplyRollback, printing from its progress events exactly like
// applyUpdate does for ApplyUpdate (forced-hook warnings, after_each hook
// warnings, and the --verbose-gated link-method note all reuse the SAME
// UpdateBeforeEachForced/UpdateWarning/UpdateNote phases) -> prints the
// final "✓ Rolled back: ..." footer from the result's own fields.
func doUpdateRollback(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	// Resolve source: use flag if set, otherwise first configured source
	var err error
	updateSource, err = resolveSource(game, updateSource, false)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, updateProfile)
	if err != nil {
		return err
	}

	// Get the installed mod - kept CLI-side for the header below (see this
	// function's doc comment); ApplyRollback fetches it again internally.
	mod, err := service.GetInstalledMod(updateSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	if mod.PreviousVersion == "" {
		return fmt.Errorf("no previous version available for rollback")
	}

	// Check if previous version exists in cache
	if !service.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, mod.PreviousVersion) {
		return fmt.Errorf("previous version %s not found in cache", mod.PreviousVersion)
	}

	if !jsonOutput {
		fmt.Printf("Rolling back %s %s → %s...\n", mod.Name, mod.Version, mod.PreviousVersion)
	}

	opts := core.RollbackOptions{
		Hooks:       getResolvedHooks(service, game, profileName),
		HookRunner:  getHookRunner(service),
		HookContext: makeHookContext(game),
		Force:       updateForce,
	}

	progress := func(p core.DeployProgress) {
		switch p.Phase {
		case core.UpdateBeforeEachForced, core.UpdateWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.UpdateNote:
			if verbose && !jsonOutput {
				fmt.Printf("  %s\n", p.Detail)
			}
		}
	}

	result, err := service.ApplyRollback(ctx, game, profileName, mod.SourceID, mod.ID, opts, progress)
	if err != nil {
		return err
	}

	if jsonOutput {
		return emitSingleUpdateJSON(singleUpdateJSON{
			ModID: mod.ID, Name: result.ModName, FromVersion: result.FromVersion, ToVersion: result.ToVersion,
			Status: "rolled_back",
		})
	}

	fmt.Printf("\n✓ Rolled back: %s %s → %s\n", result.ModName, result.FromVersion, result.ToVersion)
	return nil
}

// printSkipped notes the mods CheckUpdates filtered out, if any. No-op at zero
// so the common case stays quiet.
//
// Pinned and local get separate lines because the remedies differ: a pin is a
// reversible choice, a local mod has no remote and never will.
//
// Emits no leading blank line: when every mod is skipped this is the whole
// output, and a leading newline would render as a stray blank first line.
// Callers with preceding output add their own separator.
func printSkipped(skips core.UpdateSkips) {
	if skips.Pinned > 0 {
		fmt.Printf("%d pinned mod%s skipped — see `lmm list -v`.\n", skips.Pinned, plural(skips.Pinned))
	}
	if skips.Local > 0 {
		fmt.Printf("%d local mod%s skipped (no remote source to check).\n", skips.Local, plural(skips.Local))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func policyToString(policy domain.UpdatePolicy) string {
	switch policy {
	case domain.UpdateAuto:
		return "auto"
	case domain.UpdatePinned:
		return "pinned"
	default:
		return "notify"
	}
}
