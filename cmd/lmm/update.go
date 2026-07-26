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

var updateCmd = &cobra.Command{
	Use:   "update [mod-id]",
	Short: "Check for or apply mod updates",
	Long: `Check for available updates or update specific mods.

Without arguments, checks all installed mods for updates.
With a mod ID, updates that specific mod.

Examples:
  lmm update --game skyrim-se                    # Check all mods for updates
  lmm update 12345 --game skyrim-se              # Update specific mod
  lmm update --game skyrim-se --all              # Apply all available updates
  lmm update --game skyrim-se --dry-run          # Show what would update`,
	Args: cobra.MaximumNArgs(1),
	RunE: runUpdate,
}

var updateRollbackCmd = &cobra.Command{
	Use:   "rollback <mod-id>",
	Short: "Rollback a mod to its previous version",
	Long: `Rollback a mod to the version before the last update.

The previous version must still be available in the cache.

Examples:
  lmm update rollback 12345 --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runUpdateRollback,
}

func init() {
	updateCmd.Flags().StringVarP(&updateSource, "source", "s", "", "mod source (default: first configured source alphabetically)")
	updateCmd.Flags().StringVarP(&updateProfile, "profile", "p", "", "profile to check (default: active profile)")
	updateCmd.Flags().BoolVar(&updateAll, "all", false, "apply all available updates")
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "show what would update without applying")
	updateCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "continue even if hooks fail")

	updateRollbackCmd.Flags().StringVarP(&updateSource, "source", "s", "", "mod source (default: first configured source alphabetically)")
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
		fmt.Println("No mods installed.")
		return nil
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
					fmt.Printf("%s is a local mod — no remote source to check for updates.\n", candidates[0].Name)
					return nil
				}
				return fmt.Errorf("mod %s in profile %s belongs to source %q, not %q; retry with --source %s",
					modID, profileName, candidates[0].SourceID, updateSource, candidates[0].SourceID)
			default:
				sources := make([]string, 0, len(candidates))
				for _, c := range candidates {
					sources = append(sources, c.SourceID)
				}
				sort.Strings(sources) // deterministic regardless of install order
				return fmt.Errorf("mod %s is in profile %s under multiple sources (%s); retry with --source to choose (local mods cannot be update-checked)",
					modID, profileName, strings.Join(sources, ", "))
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

	if len(updates) == 0 {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			skips := core.CountUpdateSkips(installed)
			if err := enc.Encode(updateJSONOutput{
				GameID: game.ID, Profile: profileName, Updates: []updateModJSON{},
				Skipped: updateSkippedJSON{Pinned: skips.Pinned, Local: skips.Local},
			}); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
		}
		// "All mods are up to date" would be false if the only reason there is
		// nothing to report is that every mod was skipped. An empty profile
		// returned earlier, so len(installed) is non-zero here.
		skips := core.CountUpdateSkips(installed)
		if skips.Total() == len(installed) {
			printSkipped(skips)
			return nil
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
		return nil
	}

	if jsonOutput {
		skips := core.CountUpdateSkips(installed)
		out := updateJSONOutput{
			GameID: game.ID, Profile: profileName, Updates: make([]updateModJSON, len(updates)),
			Skipped: updateSkippedJSON{Pinned: skips.Pinned, Local: skips.Local},
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
		return nil
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
		return nil
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

	return nil
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
			fmt.Printf("%s is pinned at v%s and was not checked.\n", mod.Name, mod.Version)
			fmt.Printf("Unpin with: lmm mod set-update %s --notify\n", mod.ID)
			return nil
		}
		fmt.Printf("%s is already up to date (v%s).\n", mod.Name, mod.Version)
		return nil
	}

	update := updates[0]
	oldVersion := mod.Version
	newVersion := update.NewVersion
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

	if updateDryRun {
		fmt.Println("(dry-run: no changes applied)")
		return nil
	}

	if err := applyUpdate(ctx, service, game, update, profileName); err != nil {
		return err
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
			if verbose {
				fmt.Printf("\r  Downloading: %.1f%%", p.Percent)
			}
		case core.UpdateDownloadDone:
			if verbose {
				fmt.Println()
			}
		case core.UpdateBeforeEachForced, core.UpdateWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.UpdateNote:
			if verbose {
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

	fmt.Printf("Rolling back %s %s → %s...\n", mod.Name, mod.Version, mod.PreviousVersion)

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
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		}
	}

	result, err := service.ApplyRollback(ctx, game, profileName, mod.SourceID, mod.ID, opts, progress)
	if err != nil {
		return err
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
