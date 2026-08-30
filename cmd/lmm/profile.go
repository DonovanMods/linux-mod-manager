package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage mod profiles",
	Long: `Manage mod profiles for organizing different mod configurations.

Profiles allow you to maintain different sets of mods for the same game.
For example, you might have a "vanilla plus" profile and a "total conversion" profile.`,
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all profiles",
	Long: `List all profiles for the specified game.

Examples:
  lmm profile list --game skyrim-se`,
	RunE: runProfileList,
}

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new profile",
	Long: `Create a new empty profile for the specified game.

Examples:
  lmm profile create survival --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileCreate,
}

var profileDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a profile",
	Long: `Delete a profile and its configuration.

Note: This does not remove the installed mods, only the profile configuration.

Examples:
  lmm profile delete old-profile --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileDelete,
}

var profileSwitchCmd = &cobra.Command{
	Use:   "switch <name>",
	Short: "Switch to a different profile",
	Long: `Switch to a different profile, deploying its mods to the game directory.

This will undeploy mods from the current profile and deploy mods from the new profile.
Prompts for confirmation before making any changes; pass -y/--yes to skip the prompt.

Examples:
  lmm profile switch survival --game skyrim-se
  lmm profile switch survival --game skyrim-se --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileSwitch,
}

var profileExportCmd = &cobra.Command{
	Use:   "export <name>",
	Short: "Export a profile",
	Long: `Export a profile to a portable YAML file.

The exported file can be shared with others or used as a backup.

Examples:
  lmm profile export survival --game skyrim-se > survival.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileExport,
}

var profileImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import a profile",
	Long: `Import a profile from a YAML file.

Missing mods are downloaded and installed automatically, after a
confirmation prompt; pass -y/--yes to skip the prompt and answer yes. Use
--no-install to skip that entirely and just save the profile, installing
nothing. Use --force to overwrite an existing profile with the same name
instead of failing.

Examples:
  lmm profile import survival.yaml --game skyrim-se
  lmm profile import survival.yaml --game skyrim-se --yes
  lmm profile import survival.yaml --game skyrim-se --no-install
  lmm profile import survival.yaml --game skyrim-se --force`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileImport,
}

var profileSyncCmd = &cobra.Command{
	Use:   "sync [name]",
	Short: "Sync profile to match installed mods",
	Long: `Update the profile YAML to match currently installed/enabled mods in the database.

Use this if the profile got out of sync, or to migrate from pre-profile installs.
If no name is given, uses the current/default profile. Prompts for
confirmation before making any changes; pass -y/--yes to skip the prompt.

Examples:
  lmm profile sync --game skyrim-se
  lmm profile sync survival --game skyrim-se
  lmm profile sync survival --game skyrim-se --yes`,
	Args: cobra.MaximumNArgs(1),
	RunE: runProfileSync,
}

var profileReorderCmd = &cobra.Command{
	Use:   "reorder [mod-id ...]",
	Short: "View or change load order",
	Long: `View or change the load order of mods in a profile (first = lowest priority).

With no arguments, prints the current load order.
With mod IDs as arguments, sets the new order (first ID = lowest priority).
Mods not listed are appended at the end. A mod ID shared by mods from
different sources is ambiguous; qualify it as "source:modid" instead.

Use -p/--profile to target a profile other than the active one - this
flag belongs to 'reorder' itself, distinct from the game's active profile
used by other 'profile' subcommands.

Examples:
  lmm profile reorder --game skyrim-se
  lmm profile reorder --game skyrim-se --profile survival
  lmm profile reorder 12345 67890 11111 --game skyrim-se`,
	Args: cobra.ArbitraryArgs,
	RunE: runProfileReorder,
}

var profileApplyCmd = &cobra.Command{
	Use:   "apply [name]",
	Short: "Apply profile to system",
	Long: `Make the system match the profile by installing/enabling/disabling mods.

Use this after manually editing a profile YAML to apply those changes.
If no name is given, uses the current/default profile. Prompts for
confirmation before making any changes; pass -y/--yes to skip the prompt.

Examples:
  lmm profile apply --game skyrim-se
  lmm profile apply survival --game skyrim-se
  lmm profile apply survival --game skyrim-se --yes`,
	Args: cobra.MaximumNArgs(1),
	RunE: runProfileApply,
}

var (
	profileImportForce     bool
	profileImportNoInstall bool
	profileImportYes       bool
	profileApplyYes        bool
	profileApplyDryRun     bool
	profileSwitchYes       bool
	profileSwitchDryRun    bool
	profileSyncYes         bool
	profileSyncDryRun      bool
	profileReorderProfile  string
)

func init() {
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileCreateCmd)
	profileCmd.AddCommand(profileDeleteCmd)
	profileCmd.AddCommand(profileSwitchCmd)
	profileCmd.AddCommand(profileExportCmd)
	profileCmd.AddCommand(profileImportCmd)
	profileCmd.AddCommand(profileSyncCmd)
	profileCmd.AddCommand(profileReorderCmd)
	profileCmd.AddCommand(profileApplyCmd)

	// Switch flags
	profileSwitchCmd.Flags().BoolVarP(&profileSwitchYes, "yes", "y", false, "auto-confirm the switch")
	profileSwitchCmd.Flags().BoolVar(&profileSwitchDryRun, "dry-run", false, "print what the switch would do without changing anything")

	// Import flags
	profileImportCmd.Flags().BoolVar(&profileImportForce, "force", false, "overwrite existing profile")
	profileImportCmd.Flags().BoolVar(&profileImportNoInstall, "no-install", false, "skip installing missing mods")
	profileImportCmd.Flags().BoolVarP(&profileImportYes, "yes", "y", false, "auto-confirm downloading/installing missing mods")

	// Sync flags
	profileSyncCmd.Flags().BoolVarP(&profileSyncYes, "yes", "y", false, "auto-confirm the sync")
	profileSyncCmd.Flags().BoolVar(&profileSyncDryRun, "dry-run", false, "print what the sync would do without changing anything")

	// Apply flags
	profileApplyCmd.Flags().BoolVarP(&profileApplyYes, "yes", "y", false, "auto-confirm changes")
	profileApplyCmd.Flags().BoolVar(&profileApplyDryRun, "dry-run", false, "print what the apply would do without changing anything")

	// Reorder flags
	profileReorderCmd.Flags().StringVarP(&profileReorderProfile, "profile", "p", "", "profile (default: active profile)")

	rootCmd.AddCommand(profileCmd)
}

func getProfileManager(service *core.Service) *core.ProfileManager {
	return service.NewProfileManager()
}

func runProfileList(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileList(ctx, service, game)
	})
}

func doProfileList(ctx context.Context, service *core.Service, game *domain.Game) error {
	pm := getProfileManager(service)

	profiles, err := pm.List(ctx, game.ID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tMODS\tDEFAULT"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "----\t----\t-------"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	for _, p := range profiles {
		defaultMark := ""
		if p.IsDefault {
			defaultMark = "*"
		}
		if _, err := fmt.Fprintf(w, "%s\t%d\t%s\n", p.Name, len(p.Mods), defaultMark); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	return nil
}

func runProfileCreate(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileCreate(ctx, service, game, args[0])
	})
}

func doProfileCreate(ctx context.Context, service *core.Service, game *domain.Game, name string) error {
	pm := getProfileManager(service)

	profile, err := pm.Create(ctx, game.ID, name)
	if err != nil {
		return fmt.Errorf("creating profile: %w", err)
	}

	// Ruling 15: the ProfileResult document - the profile as created.
	if jsonOutput {
		return emitJSON(&core.ProfileResult{Profile: *profile})
	}

	fmt.Printf("✓ Created profile: %s\n", profile.Name)
	return nil
}

func runProfileDelete(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileDelete(ctx, service, game, args[0])
	})
}

func doProfileDelete(ctx context.Context, service *core.Service, game *domain.Game, name string) error {
	pm := getProfileManager(service)

	// Under --json the document reports WHICH profile was deleted, which
	// means reading it before it is gone; the read happens only on that
	// path so the plain path's behaviour (and its single Delete call) is
	// unchanged. A load failure here is not fatal: Delete below reports a
	// missing profile authoritatively, and a readable-but-unparseable
	// profile still deletes - the document then names it and nothing else.
	var deleted domain.Profile
	if jsonOutput {
		if p, err := pm.Get(ctx, game.ID, name); err == nil {
			deleted = *p
		} else {
			deleted = domain.Profile{Name: name, GameID: game.ID}
		}
	}

	if err := pm.Delete(ctx, game.ID, name); err != nil {
		return fmt.Errorf("deleting profile: %w", err)
	}

	// Ruling 15: the ProfileResult document - the profile as it stood
	// immediately before the delete (see core.ProfileResult).
	if jsonOutput {
		return emitJSON(&core.ProfileResult{Profile: deleted})
	}

	fmt.Printf("✓ Deleted profile: %s\n", name)
	return nil
}

func runProfileSwitch(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileSwitch(ctx, service, game, args[0])
	})
}

// doProfileSwitch owns plan-printing, the "Proceed?" confirmation prompt,
// and event-driven console output; the diff computation and execution live
// in core.PlanProfileSwitch/core.ApplyProfileSwitch (see the task report).
// The prompt deliberately stays here rather than in core: core never blocks
// on user input.
func doProfileSwitch(ctx context.Context, service *core.Service, game *domain.Game, targetName string) error {
	plan, err := service.PlanProfileSwitch(ctx, game, targetName)
	if err != nil {
		return err
	}

	// Ruling 15: --dry-run --json is the Plan document, emitted before any
	// preview - including the already-active and no-changes readouts, both
	// of which the plan itself states (AlreadyActive/NoChanges).
	if profileSwitchDryRun && jsonOutput {
		return emitJSON(plan)
	}

	if plan.AlreadyActive {
		if jsonOutput {
			// A run that changes nothing still owes a document: the
			// Result an empty switch produces.
			return emitJSON(&core.SwitchResult{})
		}
		fmt.Printf("Already on profile: %s\n", targetName)
		return nil
	}

	if !jsonOutput {
		if profileSwitchDryRun {
			fmt.Printf("Switch plan for profile %q (dry run)\n\n", targetName)
		} else {
			fmt.Printf("Switching to profile: %s\n\n", targetName)
		}
	}

	if plan.NoChanges {
		// No mod changes, just switch the default - ApplyProfileSwitch's
		// three loops are all empty, so this is exactly a SetDefault call.
		// A dry run must not make even that one write.
		if profileSwitchDryRun {
			return nil
		}
		result, err := service.ApplyProfileSwitch(ctx, game, plan, nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return emitJSON(result)
		}
		fmt.Printf("✓ Switched to profile: %s\n", targetName)
		return nil
	}

	// The preview is the plan rendered for the human about to confirm it;
	// under --json the document is the whole output (Ruling 15).
	if !jsonOutput {
		if len(plan.ToDisable) > 0 {
			fmt.Printf("Will disable %d mod(s):\n", len(plan.ToDisable))
			for _, im := range plan.ToDisable {
				fmt.Printf("  - %s (%s)\n", im.Name, im.ID)
			}
		}

		if len(plan.ToEnable) > 0 {
			fmt.Printf("Will enable %d mod(s):\n", len(plan.ToEnable))
			for _, im := range plan.ToEnable {
				fmt.Printf("  + %s (%s)\n", im.Name, im.ID)
			}
		}

		if len(plan.ToInstall) > 0 {
			fmt.Printf("Will install %d mod(s):\n", len(plan.ToInstall))
			for _, ref := range plan.ToInstall {
				fmt.Printf("  ↓ %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
			}
		}
	}

	// A dry run stops here: it has shown the plan and must change nothing.
	if profileSwitchDryRun {
		return nil
	}

	// Confirm unless --yes
	if !profileSwitchYes {
		if !jsonOutput {
			fmt.Print("\nProceed? [Y/n]: ")
		}
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "" && input != "y" && input != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// progress prints every diagnostic and per-mod status line at its exact
	// point of occurrence, driven entirely by core.ApplyProfileSwitch's
	// events. result.Notes is never separately batch-printed below: every
	// Notes entry (the disable/enable loops' --verbose-gated warnings) has
	// a corresponding event here already. The install loop's UpsertMod
	// refusal is a SwitchInstallWarning now, not a Note (#294, Ruling 5's
	// class extension - Task 13b) - it reaches the user unconditionally
	// through result.Warnings below instead of a case here.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.SwitchDisableNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.SwitchDisabled:
			fmt.Printf("  ✓ Disabled: %s\n", p.ModName)
		case core.SwitchEnableNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.SwitchEnabled:
			fmt.Printf("  ✓ Enabled: %s\n", p.ModName)
		case core.SwitchInstalling:
			fmt.Println("\nInstalling missing mods...")
		case core.SwitchInstallingMod:
			fmt.Printf("  Installing %s:%s...\n", p.SourceID, p.ModID)
		case core.SwitchInstallError:
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.SwitchDownloading:
			fmt.Printf("\r    Downloading: %.1f%%", p.Percent)
		case core.SwitchDownloadFailed:
			fmt.Println()
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.SwitchDownloadDone:
			fmt.Println()
		case core.SwitchInstalled:
			fmt.Printf("    ✓ Installed: %s\n", p.ModName)
		}
	}

	result, err := service.ApplyProfileSwitch(ctx, game, plan, quietSink(progress))
	if err != nil {
		// Task 13 review round 1, Important 1: ApplyProfileSwitch's
		// error-path convention returns diagnostics accumulated before the
		// failure alongside it, but the #294 install-loop warning below
		// lives on result.Warnings, not on a live progress event - it was
		// never printed above, so it must be surfaced here or it is
		// silently dropped on the fatal path. Unit Q review M3: under
		// --json stderr is off limits (Ruling 15), so the warnings ride the
		// error into the envelope's "details" instead of vanishing.
		if result != nil && len(result.Warnings) > 0 {
			if jsonOutput {
				return &profileWarningsError{err: err, warnings: result.Warnings}
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
			}
		}
		return err
	}

	// Ruling 15: the SwitchResult document, which carries Warnings itself.
	if jsonOutput {
		return emitJSON(result)
	}

	// #197 postsmoke fix / #294 Ruling 5's class extension (Task 13b):
	// SwitchResult.Warnings (unconditional stderr, unlike .Notes above) -
	// the install loop's refused UpsertMod (a LOCKED profile ref, #143),
	// then a merged-pak sync failure for the target profile. Previously
	// this whole result was discarded.
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	fmt.Printf("\n✓ Switched to profile: %s\n", targetName)
	return nil
}

func runProfileExport(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileExport(ctx, service, game, args[0])
	})
}

func doProfileExport(ctx context.Context, service *core.Service, game *domain.Game, name string) error {
	pm := getProfileManager(service)

	data, err := pm.Export(ctx, game.ID, name)
	if err != nil {
		return fmt.Errorf("exporting profile: %w", err)
	}

	fmt.Print(string(data))
	return nil
}

func runProfileImport(cmd *cobra.Command, args []string) error {
	filePath := args[0]
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileImport(ctx, service, game, data)
	})
}

// doProfileImport owns preview-printing, the "Download and install mods?"
// confirmation prompt, and event-driven console output; the categorization,
// save, and download/install loop all live in core.PlanImport/
// core.ApplyImport (Phase 6b Task 8 - see the task report for the full
// mapping).
//
// Ruling 7 delta (v2 Phase 3): the prompt now runs BEFORE ApplyImport and
// its answer travels as core.ProfileImportOptions.Install - the decision is
// fully derivable from the plan (NeedsRedownload/Missing), so core never
// calls back into the frontend (Ruling 1). The visible consequence is
// ordering: the prompt (and, on a decline, its "Skipped." line) now precede
// "✓ Imported profile: <name>" instead of following it - the profile is no
// longer saved before the question is asked.
func doProfileImport(ctx context.Context, service *core.Service, game *domain.Game, data []byte) error {
	plan, err := service.PlanImport(ctx, game, data)
	if err != nil {
		// PlanImport's only failure mode is a parse error, already wrapped
		// ("parsing profile: %w") - matching doProfileImport's own historical
		// wrapping exactly.
		return err
	}

	// Show summary - printed purely from the plan, matching the
	// pre-extraction CLI's preview exactly. Not under --json: the document
	// is the run's whole output (Ruling 15).
	if !jsonOutput {
		fmt.Printf("Importing profile: %s\n\n", plan.Profile.Name)
		totalMods := len(plan.Installed) + len(plan.NeedsRedownload) + len(plan.Missing)
		fmt.Printf("Found %d mod(s) in profile.\n", totalMods)
		if len(plan.Installed) > 0 {
			fmt.Printf("  ✓ %d already installed\n", len(plan.Installed))
		}
		if len(plan.NeedsRedownload) > 0 {
			fmt.Printf("  ⚠ %d cache missing, need re-download:\n", len(plan.NeedsRedownload))
			for _, ref := range plan.NeedsRedownload {
				fmt.Printf("    - %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
			}
		}
		if len(plan.Missing) > 0 {
			fmt.Printf("  ↓ %d need to be downloaded:\n", len(plan.Missing))
			for _, ref := range plan.Missing {
				fmt.Printf("    - %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
			}
		}
	}

	toDownloadCount := len(plan.NeedsRedownload) + len(plan.Missing)

	declined := false

	opts := core.ProfileImportOptions{Force: profileImportForce, NoInstall: profileImportNoInstall}
	if toDownloadCount > 0 && !profileImportNoInstall {
		if profileImportYes {
			opts.Install = true
		} else {
			if !jsonOutput {
				fmt.Print("\nDownload and install mods? [Y/n]: ")
			}
			input, err := readPromptLine()
			if err != nil {
				// A genuine stdin read failure, not an ordinary decline (see
				// readPromptLine's own doc comment): propagate it verbatim and
				// print nothing further - and, now that the prompt precedes
				// Apply, without saving the profile either.
				return err
			}
			if input != "" && input != "y" && input != "yes" {
				declined = true
				fmt.Printf("Skipped. Use 'lmm profile apply %s' to install them later.\n", plan.Profile.Name)
				// Unreachable under --json: readPromptLine above refuses to
				// read stdin there (Ruling 2), so a decline is impossible.
			} else {
				opts.Install = true
			}
		}
	}

	// progress prints every diagnostic and status line at its exact point of
	// occurrence, driven entirely by core.ApplyImport's progress events -
	// including the sole diagnostic that also lands in result.Notes (see
	// core.ProfileImportResult's doc comment). Notes is never separately
	// batch-printed below: it has a corresponding event here already.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.ImportSaved:
			fmt.Printf("\n✓ Imported profile: %s\n", p.ModName)
		case core.ImportInstalling:
			fmt.Println("\nDownloading and installing mods...")
		case core.ImportModInstalling:
			fmt.Printf("  Installing %s:%s...\n", p.SourceID, p.ModID)
		case core.ImportDownloading:
			fmt.Printf("\r    Downloading: %.1f%%", p.Percent)
		case core.ImportModFailed:
			if strings.HasPrefix(p.Detail, "download failed:") {
				fmt.Println()
			}
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.ImportDownloadDone:
			fmt.Println()
		case core.ImportModInstalled:
			fmt.Printf("    ✓ Installed: %s\n", p.ModName)
		case core.ImportNote:
			if verbose {
				fmt.Printf("    %s\n", p.Detail)
			}
		}
	}

	result, err := service.ApplyImport(ctx, game, plan, opts, quietSink(progress))
	if err != nil {
		// Diagnostics accumulated before a fatal error were already printed
		// above, live, via progress. ApplyImport's own error is already
		// appropriately wrapped (e.g. "importing profile: %w" for a failed
		// save) or bare (ctx cancellation) - no additional wrapping here.
		return err
	}

	// Ruling 15: the ProfileImportResult document, which carries Warnings
	// and the Installed/Failed/Skipped counters the summary below renders.
	if jsonOutput {
		return emitJSON(result)
	}

	// #197 postsmoke fix: result.Warnings was never read - a merged-pak
	// sync failure only ever reached the ImportNote progress event above
	// (--verbose-gated), so it was silent by default. Print unconditionally
	// as the loud backstop, matching applyRecompile's identical fix (M4).
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	switch {
	case profileImportNoInstall:
		if result.Skipped > 0 {
			fmt.Printf("\nSkipped installing %d mod(s). Use 'lmm profile apply %s' to install them later.\n", result.Skipped, result.ProfileName)
		}
	case declined:
		// The decline message was already printed at the prompt above.
	case toDownloadCount == 0:
		// Nothing to install - the pre-extraction CLI's early-out never
		// printed anything further in this case either.
	default:
		fmt.Printf("\n--- Summary ---\n")
		fmt.Printf("Installed: %d\n", result.Installed)
		if result.Failed > 0 {
			fmt.Printf("Failed: %d\n", result.Failed)
		}
	}

	return nil
}

func runProfileSync(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileSync(ctx, service, game, args)
	})
}

func doProfileSync(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	profileName := profileSyncTarget(ctx, service, game, args)

	plan, err := service.PlanProfileSync(ctx, game, profileName)
	if err != nil {
		return err
	}

	// Ruling 15: --dry-run --json is the Plan document, emitted before any
	// preview - the plan itself states NoChanges/Missing.
	if profileSyncDryRun && jsonOutput {
		return emitJSON(plan)
	}

	if plan.NoChanges {
		// The pre-lift engine called pm.Create unconditionally before ever
		// computing the diff, so a missing profile always got a profile.yaml
		// even with nothing to sync into it. ApplyProfileSync still must be
		// reached for that side effect - it creates on plan.Missing
		// regardless of the buckets. A dry run makes no such write.
		var result *core.ProfileSyncResult
		if plan.Missing && !profileSyncDryRun {
			var err error
			if result, err = service.ApplyProfileSync(ctx, game, plan, nil); err != nil {
				return err
			}
		}
		if jsonOutput {
			if result == nil {
				result = &core.ProfileSyncResult{}
			}
			return emitJSON(result)
		}
		fmt.Printf("Profile %s is already in sync.\n", profileName)
		return nil
	}

	// The preview is the plan rendered for the human about to confirm it;
	// under --json the document is the whole output (Ruling 15).
	if !jsonOutput {
		if profileSyncDryRun {
			fmt.Printf("Sync plan for profile %q (dry run)\n\n", profileName)
		} else {
			fmt.Printf("Syncing profile: %s\n\n", profileName)
		}

		if len(plan.ToAdd) > 0 {
			fmt.Println("Will add to profile:")
			for _, ref := range plan.ToAdd {
				if name, ok := plan.Names[domain.ModKey(ref.SourceID, ref.ModID)]; ok {
					fmt.Printf("  + %s (%s:%s)\n", name, ref.SourceID, ref.ModID)
				} else {
					fmt.Printf("  + %s:%s\n", ref.SourceID, ref.ModID)
				}
			}
		}

		if len(plan.ToRemove) > 0 {
			fmt.Println("Will remove from profile:")
			for _, ref := range plan.ToRemove {
				fmt.Printf("  - %s:%s\n", ref.SourceID, ref.ModID)
			}
		}

		if len(plan.ToUpdate) > 0 {
			fmt.Println("Will update FileIDs for:")
			for _, ref := range plan.ToUpdate {
				if name, ok := plan.Names[domain.ModKey(ref.SourceID, ref.ModID)]; ok {
					fmt.Printf("  ~ %s (%s:%s)\n", name, ref.SourceID, ref.ModID)
				} else {
					fmt.Printf("  ~ %s:%s\n", ref.SourceID, ref.ModID)
				}
			}
		}
	}

	// A dry run stops here: it has shown the plan and must change nothing.
	if profileSyncDryRun {
		return nil
	}

	// Confirm unless --yes
	if !profileSyncYes {
		if !jsonOutput {
			fmt.Print("\nProceed? [Y/n]: ")
		}
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "" && input != "y" && input != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// progress prints the add/remove loops' --verbose-only diagnostics at
	// their exact point of occurrence, driven by core.ApplyProfileSync's
	// events - the same 2-space "  Warning: ..." rendering for both buckets
	// (see core's profile_sync.go). The toUpdate loop's refusal is a
	// SyncUpdateWarning since #294 (Ruling 5) and arrives on
	// result.Warnings instead, alongside the end-of-apply merged-pak
	// warnings - unconditional, printed below.
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.SyncAddNote, core.SyncRemoveNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		}
	}

	result, err := service.ApplyProfileSync(ctx, game, plan, quietSink(progress))
	if err != nil {
		// Task 13 review round 1, Important 1: the #294 warning below lives
		// on result.Warnings, not a live progress event, so it was never
		// printed live - it must be surfaced here or it is silently dropped
		// on the fatal path (ctx cancellation is the only reachable fatal
		// error once a toUpdate entry has already warned). Unit Q review
		// M3: under --json the envelope's "details" carries them, since
		// Ruling 15 forbids the stderr line.
		if result != nil && len(result.Warnings) > 0 {
			if jsonOutput {
				return &profileWarningsError{err: err, warnings: result.Warnings}
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
			}
		}
		return err
	}

	// Ruling 15: the ProfileSyncResult document, which carries Warnings
	// itself.
	if jsonOutput {
		return emitJSON(result)
	}

	// #197 postsmoke fix / #294 (Ruling 5): result.Warnings (unconditional
	// stderr, unlike the --verbose-gated warnings above) - the toUpdate
	// loop's refused UpsertMod (a LOCKED profile ref, #143), then a
	// merged-pak sync failure for the profile.
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	fmt.Printf("✓ Synced profile: %s\n", profileName)
	return nil
}

// profileSyncTarget resolves which profile `lmm profile sync` acts on: the
// positional argument when given, else the game's default profile, else the
// literal "default" (an unreadable default is not an error here - the
// plan's own profile lookup handles a missing profile via Plan.Missing).
func profileSyncTarget(ctx context.Context, service *core.Service, game *domain.Game, args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	defaultProfile, err := getProfileManager(service).GetDefault(ctx, game.ID)
	if err != nil {
		return "default"
	}
	return defaultProfile.Name
}

func runProfileReorder(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileReorder(ctx, service, game, args)
	})
}

func doProfileReorder(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {

	profileName, err := resolveProfile(ctx, service, game.ID, profileReorderProfile)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		profile, err := getProfileManager(service).Get(ctx, game.ID, profileName)
		if err != nil {
			return fmt.Errorf("loading profile: %w", err)
		}
		// Ruling 15: the load-order READOUT is the same profile document a
		// reorder emits - profile.Mods IS the load order, in order.
		if jsonOutput {
			return emitJSON(&core.ProfileResult{Profile: *profile})
		}
		// Show current load order
		if len(profile.Mods) == 0 {
			fmt.Printf("No mods in profile %s.\n", profileName)
			return nil
		}
		installed, _ := service.GetInstalledMods(ctx, game.ID, profileName)
		nameByKey := make(map[string]string)
		for i := range installed {
			key := installed[i].SourceID + ":" + installed[i].ID
			nameByKey[key] = installed[i].Name
		}
		fmt.Printf("Load order for %s (first = lowest priority):\n", profileName)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "#\tMOD_ID\tNAME"); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
		for i, ref := range profile.Mods {
			key := ref.SourceID + ":" + ref.ModID
			name := nameByKey[key]
			if name == "" {
				name = "(unknown)"
			}
			if _, err := fmt.Fprintf(w, "%d\t%s\t%s\n", i+1, ref.ModID, name); err != nil {
				return fmt.Errorf("writing row: %w", err)
			}
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("flushing output: %w", err)
		}
		return nil
	}

	// args are mod IDs (or "source:modid") in desired order (first = lowest priority).
	newRefs, err := service.ResolveReorder(ctx, game, profileName, args)
	if err != nil {
		return err
	}

	if err := service.ReorderProfileMods(ctx, game.ID, profileName, newRefs); err != nil {
		return fmt.Errorf("reordering: %w", err)
	}

	// Ruling 15: the ProfileResult document - the profile as it now stands,
	// re-read so the document reports what was persisted rather than what
	// was requested.
	if jsonOutput {
		profile, err := getProfileManager(service).Get(ctx, game.ID, profileName)
		if err != nil {
			return fmt.Errorf("loading profile: %w", err)
		}
		return emitJSON(&core.ProfileResult{Profile: *profile})
	}

	fmt.Printf("✓ Load order updated for profile %s.\n", profileName)
	return nil
}

func runProfileApply(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileApply(ctx, service, game, args)
	})
}

// doProfileApply owns profile-name resolution, plan-printing, the
// "Proceed?" confirmation prompt and event-driven console output; the diff,
// the source resolution and the disable/enable/install execution live in
// core.PlanProfileApply/core.ApplyProfileApply (#290). The prompt
// deliberately stays here rather than in core: core never blocks on user
// input.
func doProfileApply(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	profileName := profileApplyTarget(ctx, service, game, args)

	plan, err := service.PlanProfileApply(ctx, game, profileName)
	if err != nil {
		return err
	}

	// Ruling 15: --dry-run --json is the Plan document, emitted before any
	// preview is rendered.
	if profileApplyDryRun && jsonOutput {
		return emitJSON(plan)
	}

	if plan.NoChanges {
		if jsonOutput {
			// Nothing to do is still a run that owes a document: the
			// Result an apply with no work produces.
			return emitJSON(&core.ProfileApplyResult{})
		}
		fmt.Printf("System already matches profile %s.\n", profileName)
		return nil
	}

	// The preview is the plan rendered for the human about to confirm it.
	// Under --json it is not printed at all (the document is the whole
	// output, Ruling 15); under --dry-run the header names it a dry run,
	// matching `deploy --dry-run`'s "<verb> plan for profile %q (dry run)".
	if !jsonOutput {
		if profileApplyDryRun {
			fmt.Printf("Apply plan for profile %q (dry run)\n\n", profileName)
		} else {
			fmt.Printf("Applying profile: %s\n\n", profileName)
		}

		if len(plan.ToDisable) > 0 {
			fmt.Printf("Will disable %d mod(s):\n", len(plan.ToDisable))
			for _, im := range plan.ToDisable {
				fmt.Printf("  - %s (%s)\n", im.Name, im.ID)
			}
		}

		if len(plan.ToEnable) > 0 {
			fmt.Printf("Will enable %d mod(s):\n", len(plan.ToEnable))
			for _, im := range plan.ToEnable {
				fmt.Printf("  + %s (%s)\n", im.Name, im.ID)
			}
		}

		if len(plan.ToInstall) > 0 {
			fmt.Printf("Will install %d mod(s):\n", len(plan.ToInstall))
			for _, entry := range plan.ToInstall {
				fmt.Printf("  ↓ %s:%s v%s\n", entry.Ref.SourceID, entry.Ref.ModID, entry.Ref.Version)
			}
		}
	}

	// A dry run stops here: it has shown the plan and must change nothing.
	if profileApplyDryRun {
		return nil
	}

	// Confirm unless --yes
	if !profileApplyYes {
		if !jsonOutput {
			fmt.Print("\nProceed? [Y/n]: ")
		}
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "" && input != "y" && input != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// progress prints every diagnostic and per-mod status line at its exact
	// point of occurrence, driven entirely by core.ApplyProfileApply's
	// events - the same Switch* phase vocabulary doProfileSwitch renders,
	// because the two flows print the same lines (see core's profile_apply.go).
	// Everything here is stdout; result.Notes is never batch-printed below
	// since every entry already has an event here. The install loop's
	// UpsertMod refusal is a SwitchInstallWarning, not the --verbose-only
	// SwitchInstallNote it used to be (ApplyProfileSwitch's identical
	// refusal followed suit in Task 13b) - it reaches the user
	// unconditionally through result.Warnings below instead of a case here
	// (printing it here as well would duplicate it).
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.SwitchDisableNote, core.SwitchEnableNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.SwitchDisabled:
			fmt.Printf("  ✓ Disabled: %s\n", p.ModName)
		case core.SwitchEnabled:
			fmt.Printf("  ✓ Enabled: %s\n", p.ModName)
		case core.SwitchInstalling:
			fmt.Println("\nInstalling missing mods...")
		case core.SwitchInstallingMod:
			fmt.Printf("  Installing %s:%s...\n", p.SourceID, p.ModID)
		case core.SwitchInstallError:
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.SwitchDownloading:
			fmt.Printf("\r    Downloading: %.1f%%", p.Percent)
		case core.SwitchDownloadFailed:
			fmt.Println()
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.SwitchDownloadDone:
			fmt.Println()
		case core.SwitchInstalled:
			fmt.Printf("    ✓ Installed: %s\n", p.ModName)
		}
	}

	result, err := service.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, quietSink(progress))
	if err != nil {
		// Task 13 review round 1, Important 1: the #294 warning above lives
		// on result.Warnings, not a live progress event, so it was never
		// printed live - it must be surfaced here or it is silently dropped
		// on the fatal path (ctx cancellation is the only reachable fatal
		// error once a ToInstall entry has already warned). Unit Q review
		// M3: under --json the envelope's "details" carries them, since
		// Ruling 15 forbids the stderr line.
		if result != nil && len(result.Warnings) > 0 {
			if jsonOutput {
				return &profileWarningsError{err: err, warnings: result.Warnings}
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
			}
		}
		return err
	}

	// Ruling 15: the ProfileApplyResult document, which carries Warnings
	// itself - so the unconditional stderr line below is not printed.
	if jsonOutput {
		return emitJSON(result)
	}

	// #197 postsmoke fix / #294 (Ruling 5): result.Warnings (unconditional
	// stderr, unlike the --verbose-gated Notes above) - the install loop's
	// refused UpsertMod (a LOCKED profile ref, #143), then a merged-pak sync
	// failure for the profile.
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	fmt.Printf("\n✓ Applied profile: %s\n", profileName)
	return nil
}

// profileApplyTarget resolves which profile `lmm profile apply` acts on: the
// positional argument when given, else the game's default profile, else the
// literal "default" (an unreadable default is not an error here - the plan's
// own profile lookup reports a missing profile).
func profileApplyTarget(ctx context.Context, service *core.Service, game *domain.Game, args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	defaultProfile, err := getProfileManager(service).GetDefault(ctx, game.ID)
	if err != nil {
		return "default"
	}
	return defaultProfile.Name
}

// profileWarningsError carries the diagnostics `lmm profile apply`/`sync`/
// `switch` accumulated before a fatal error into the --json error envelope's
// "details" field (unit Q review, M3). Plain text prints them to stderr, but
// Ruling 15 keeps stderr empty under --json and reportError's envelope only
// carries data for a typed error - so without this wrapper the #294 warnings
// reached neither stream, leaving the DB-vs-profile divergence #294 exists
// to expose silent on exactly this path.
//
// Only constructed when there is at least one warning, so a fatal run with
// nothing to report still produces the bare {"error": ...} envelope.
// Follows the core.ConflictError / gameDetectPartialError convention
// (jsonout.go): Unwrap exposes err for errors.Is/As, Details() any is the
// unnamed interface errorDetails picks up automatically.
type profileWarningsError struct {
	err      error
	warnings []string
}

// Error returns the wrapped fatal failure's own message, so plain text and
// the envelope's "error" field are unchanged by the wrapping.
func (e *profileWarningsError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped fatal error for errors.Is/As.
func (e *profileWarningsError) Unwrap() error { return e.err }

// Details returns the accumulated warnings for the --json error envelope's
// "details" field.
func (e *profileWarningsError) Details() any { return profileWarningsDetails{Warnings: e.warnings} }

// profileWarningsDetails is profileWarningsError's wire shape: a named type
// rather than a map so the "warnings" key is part of the JSON contract and
// matches the same key on the ProfileApplyResult/ProfileSyncResult/
// SwitchResult documents a SUCCESSFUL --json run emits.
type profileWarningsDetails struct {
	Warnings []string `json:"warnings"`
}
