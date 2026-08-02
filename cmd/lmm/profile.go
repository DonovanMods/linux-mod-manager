package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

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

Examples:
  lmm profile switch survival --game skyrim-se`,
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
confirmation prompt. Use --no-install to skip that entirely and just
save the profile, installing nothing. Use --force to overwrite an
existing profile with the same name instead of failing.

Examples:
  lmm profile import survival.yaml --game skyrim-se
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
If no name is given, uses the current/default profile.

Examples:
  lmm profile sync --game skyrim-se
  lmm profile sync survival --game skyrim-se`,
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
	profileApplyYes        bool
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

	// Import flags
	profileImportCmd.Flags().BoolVar(&profileImportForce, "force", false, "overwrite existing profile")
	profileImportCmd.Flags().BoolVar(&profileImportNoInstall, "no-install", false, "skip installing missing mods")

	// Apply flags
	profileApplyCmd.Flags().BoolVarP(&profileApplyYes, "yes", "y", false, "auto-confirm changes")

	// Reorder flags
	profileReorderCmd.Flags().StringVarP(&profileReorderProfile, "profile", "p", "", "profile (default: active profile)")

	rootCmd.AddCommand(profileCmd)
}

func getProfileManager(service *core.Service) *core.ProfileManager {
	return service.NewProfileManager()
}

func runProfileList(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileList(service, game)
	})
}

func doProfileList(service *core.Service, game *domain.Game) error {
	pm := getProfileManager(service)

	profiles, err := pm.List(game.ID)
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
		return doProfileCreate(service, game, args[0])
	})
}

func doProfileCreate(service *core.Service, game *domain.Game, name string) error {
	pm := getProfileManager(service)

	profile, err := pm.Create(game.ID, name)
	if err != nil {
		return fmt.Errorf("creating profile: %w", err)
	}

	fmt.Printf("✓ Created profile: %s\n", profile.Name)
	return nil
}

func runProfileDelete(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileDelete(service, game, args[0])
	})
}

func doProfileDelete(service *core.Service, game *domain.Game, name string) error {
	pm := getProfileManager(service)

	if err := pm.Delete(game.ID, name); err != nil {
		return fmt.Errorf("deleting profile: %w", err)
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

	if plan.AlreadyActive {
		fmt.Printf("Already on profile: %s\n", targetName)
		return nil
	}

	fmt.Printf("Switching to profile: %s\n\n", targetName)

	if plan.NoChanges {
		// No mod changes, just switch the default - ApplyProfileSwitch's
		// three loops are all empty, so this is exactly a SetDefault call.
		if _, err := service.ApplyProfileSwitch(ctx, game, plan, nil); err != nil {
			return err
		}
		fmt.Printf("✓ Switched to profile: %s\n", targetName)
		return nil
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
		for _, ref := range plan.ToInstall {
			fmt.Printf("  ↓ %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
		}
	}

	// Confirm
	fmt.Print("\nProceed? [Y/n]: ")
	input, err := readPromptLine()
	if err != nil {
		return err
	}
	if input != "" && input != "y" && input != "yes" {
		fmt.Println("Cancelled.")
		return nil
	}

	// progress prints every diagnostic and per-mod status line at its exact
	// point of occurrence, driven entirely by core.ApplyProfileSwitch's
	// progress events - doProfileSwitch never wrote to stderr, so every
	// printed diagnostic here is --verbose-gated stdout (a Note), matching
	// the SwitchResult.Notes display contract. result.Notes is never
	// separately batch-printed below: every entry has a corresponding event
	// here already.
	progress := func(p core.DeployProgress) {
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
		case core.SwitchInstallNote:
			if verbose {
				fmt.Printf("    %s\n", p.Detail)
			}
		}
	}

	if _, err := service.ApplyProfileSwitch(ctx, game, plan, progress); err != nil {
		// Diagnostics accumulated before a fatal error (ApplyProfileSwitch's
		// error-path convention returns them alongside it) were already
		// printed above, live, via progress - nothing left to print here.
		return err
	}

	fmt.Printf("\n✓ Switched to profile: %s\n", targetName)
	return nil
}

func runProfileExport(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileExport(service, game, args[0])
	})
}

func doProfileExport(service *core.Service, game *domain.Game, name string) error {
	pm := getProfileManager(service)

	data, err := pm.Export(game.ID, name)
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
// mapping). The prompt is wired as core.ProfileImportOptions.ConfirmInstall,
// called by ApplyImport at its own restored position (right after the
// profile is saved) - the same callback-at-the-CLI's-own-prompt-position
// precedent core.InstallOptions.ConfirmConflicts established; promptErr
// mirrors confirmInstallConflicts' own seam in install.go for propagating a
// genuine stdin read failure verbatim instead of collapsing it into a
// generic error.
func doProfileImport(ctx context.Context, service *core.Service, game *domain.Game, data []byte) error {
	plan, err := service.PlanImport(ctx, game, data)
	if err != nil {
		// PlanImport's only failure mode is a parse error, already wrapped
		// ("parsing profile: %w") - matching doProfileImport's own historical
		// wrapping exactly.
		return err
	}

	// Show summary - printed purely from the plan, matching the
	// pre-extraction CLI's preview exactly.
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

	toDownloadCount := len(plan.NeedsRedownload) + len(plan.Missing)

	// promptErr captures a genuine stdin read failure from the "Download and
	// install mods?" prompt (distinct from an ordinary decline - see
	// readPromptLine's own doc comment), so it can be propagated verbatim
	// below instead of collapsing into ApplyImport's generic error.
	var promptErr error
	declined := false

	opts := core.ProfileImportOptions{Force: profileImportForce, NoInstall: profileImportNoInstall}
	if toDownloadCount > 0 && !profileImportNoInstall {
		opts.ConfirmInstall = func(toDownload []domain.ModReference) bool {
			fmt.Print("\nDownload and install mods? [Y/n]: ")
			input, err := readPromptLine()
			if err != nil {
				promptErr = err
				return false
			}
			if input != "" && input != "y" && input != "yes" {
				declined = true
				fmt.Printf("Skipped. Use 'lmm profile apply %s' to install them later.\n", plan.Profile.Name)
				return false
			}
			return true
		}
	}

	// progress prints every diagnostic and status line at its exact point of
	// occurrence, driven entirely by core.ApplyImport's progress events -
	// including the sole diagnostic that also lands in result.Notes (see
	// core.ProfileImportResult's doc comment). Notes is never separately
	// batch-printed below: it has a corresponding event here already.
	progress := func(p core.DeployProgress) {
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

	result, err := service.ApplyImport(ctx, game, plan, opts, progress)
	// A genuine stdin read failure inside the ConfirmInstall closure must be
	// checked UNCONDITIONALLY, before anything else: the closure signals it
	// by returning false, which ApplyImport treats as an ordinary decline
	// and returns (result, nil) - so an `err != nil`-gated check would
	// swallow the failure and fall through to a spurious "--- Summary ---"
	// block. The pre-extraction CLI returned the error immediately after the
	// prompt, printing nothing further (fix wave 1, Important 1 - pinned by
	// TestDoProfileImport_PromptReadFailure_PropagatesErrorWithoutSummary).
	if promptErr != nil {
		return promptErr
	}
	if err != nil {
		// Diagnostics accumulated before a fatal error were already printed
		// above, live, via progress. ApplyImport's own error is already
		// appropriately wrapped (e.g. "importing profile: %w" for a failed
		// save) or bare (ctx cancellation) - no additional wrapping here.
		return err
	}

	switch {
	case profileImportNoInstall:
		if result.Skipped > 0 {
			fmt.Printf("\nSkipped installing %d mod(s). Use 'lmm profile apply %s' to install them later.\n", result.Skipped, result.ProfileName)
		}
	case declined:
		// The decline message was already printed inside the ConfirmInstall
		// closure above.
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
	pm := getProfileManager(service)

	// Determine profile name
	var profileName string
	if len(args) > 0 {
		profileName = args[0]
	} else {
		defaultProfile, err := pm.GetDefault(game.ID)
		if err != nil {
			profileName = "default"
		} else {
			profileName = defaultProfile.Name
		}
	}

	// Get current profile
	profile, err := pm.Get(game.ID, profileName)
	if err != nil {
		if err == domain.ErrProfileNotFound {
			// Create profile if it doesn't exist
			profile, err = pm.Create(game.ID, profileName)
			if err != nil {
				return fmt.Errorf("creating profile: %w", err)
			}
		} else {
			return fmt.Errorf("loading profile: %w", err)
		}
	}

	// Get installed mods from database
	installedMods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// Build set of installed mod references
	installedRefs := make(map[string]domain.ModReference)
	for _, im := range installedMods {
		if im.Enabled {
			key := im.SourceID + ":" + im.ID
			installedRefs[key] = domain.ModReference{
				SourceID: im.SourceID,
				ModID:    im.ID,
				Version:  im.Version,
				FileIDs:  im.FileIDs,
			}
		}
	}

	// Build set of profile mod references
	profileRefs := make(map[string]domain.ModReference)
	for _, mr := range profile.Mods {
		key := mr.SourceID + ":" + mr.ModID
		profileRefs[key] = mr
	}

	// Calculate differences
	var toAdd []domain.ModReference
	var toRemove []domain.ModReference
	var toUpdate []domain.ModReference // Mods that need FileIDs updated

	// Mods in DB but not in profile
	for key, ref := range installedRefs {
		if profileRef, exists := profileRefs[key]; !exists {
			toAdd = append(toAdd, ref)
		} else if len(ref.FileIDs) > 0 && len(profileRef.FileIDs) == 0 {
			// Mod exists in both but profile is missing FileIDs
			toUpdate = append(toUpdate, ref)
		}
	}

	// Mods in profile but not in DB (or disabled)
	for key, ref := range profileRefs {
		if _, exists := installedRefs[key]; !exists {
			toRemove = append(toRemove, ref)
		}
	}

	// Show changes
	if len(toAdd) == 0 && len(toRemove) == 0 && len(toUpdate) == 0 {
		fmt.Printf("Profile %s is already in sync.\n", profileName)
		return nil
	}

	fmt.Printf("Syncing profile: %s\n\n", profileName)

	if len(toAdd) > 0 {
		fmt.Println("Will add to profile:")
		for _, ref := range toAdd {
			// Try to get mod name from DB
			mod, _ := service.GetInstalledMod(ref.SourceID, ref.ModID, game.ID, profileName)
			if mod != nil {
				fmt.Printf("  + %s (%s:%s)\n", mod.Name, ref.SourceID, ref.ModID)
			} else {
				fmt.Printf("  + %s:%s\n", ref.SourceID, ref.ModID)
			}
		}
	}

	if len(toRemove) > 0 {
		fmt.Println("Will remove from profile:")
		for _, ref := range toRemove {
			fmt.Printf("  - %s:%s\n", ref.SourceID, ref.ModID)
		}
	}

	if len(toUpdate) > 0 {
		fmt.Println("Will update FileIDs for:")
		for _, ref := range toUpdate {
			mod, _ := service.GetInstalledMod(ref.SourceID, ref.ModID, game.ID, profileName)
			if mod != nil {
				fmt.Printf("  ~ %s (%s:%s)\n", mod.Name, ref.SourceID, ref.ModID)
			} else {
				fmt.Printf("  ~ %s:%s\n", ref.SourceID, ref.ModID)
			}
		}
	}

	// Confirm
	fmt.Print("\nProceed? [Y/n]: ")
	input, err := readPromptLine()
	if err != nil {
		return err
	}
	if input != "" && input != "y" && input != "yes" {
		fmt.Println("Cancelled.")
		return nil
	}

	// Apply changes
	for _, ref := range toAdd {
		if err := pm.AddMod(game.ID, profileName, ref); err != nil {
			if verbose {
				fmt.Printf("  Warning: %v\n", err)
			}
		}
	}

	for _, ref := range toRemove {
		if err := pm.RemoveMod(game.ID, profileName, ref.SourceID, ref.ModID); err != nil {
			if verbose {
				fmt.Printf("  Warning: %v\n", err)
			}
		}
	}

	// Update mods with FileIDs
	for _, ref := range toUpdate {
		if err := pm.UpsertMod(game.ID, profileName, ref); err != nil {
			if verbose {
				fmt.Printf("  Warning: could not update %s:%s: %v\n", ref.SourceID, ref.ModID, err)
			}
		}
	}

	// #197 postsmoke seam-audit fix: toAdd/toRemove change profile.Mods
	// MEMBERSHIP directly (AddMod/RemoveMod) - membership, not just the DB
	// Enabled flag, is what GetInstalledModsInProfileOrder (and so
	// enabledExmodzSources) requires, so this is a genuine merge-input
	// change with no other seam to catch it.
	if syncWarnings, syncErr := service.SyncMergedPak(ctx, game, profileName); syncErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not sync merged pak: %v\n", syncErr)
	} else {
		for _, w := range syncWarnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}

	fmt.Printf("✓ Synced profile: %s\n", profileName)
	return nil
}

func runProfileReorder(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileReorder(service, game, args)
	})
}

func doProfileReorder(service *core.Service, game *domain.Game, args []string) error {

	profileName, err := resolveProfile(service, game.ID, profileReorderProfile)
	if err != nil {
		return err
	}
	profile, err := config.LoadProfile(service.ConfigDir(), game.ID, profileName)
	if err != nil {
		return fmt.Errorf("loading profile: %w", err)
	}

	if len(args) == 0 {
		// Show current load order
		if len(profile.Mods) == 0 {
			fmt.Printf("No mods in profile %s.\n", profileName)
			return nil
		}
		installed, _ := service.GetInstalledMods(game.ID, profileName)
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

	// Set new order: args are mod IDs (or "source:modid") in desired order (first = lowest priority).
	// Key by sourceID:modID so mods from different sources with the same ModID are not overwritten.
	byKey := make(map[string]domain.ModReference)
	for _, ref := range profile.Mods {
		key := ref.SourceID + ":" + ref.ModID
		byKey[key] = ref
	}
	var newRefs []domain.ModReference
	seen := make(map[string]bool)
	for _, id := range args {
		var ref domain.ModReference
		var key string
		if strings.Contains(id, ":") {
			key = id
			var ok bool
			ref, ok = byKey[key]
			if !ok {
				return fmt.Errorf("mod %s not in profile", id)
			}
		} else {
			// Look up by ModID only; ambiguous if multiple sources have this ModID
			var matches []string
			for k, r := range byKey {
				if r.ModID == id {
					matches = append(matches, k)
				}
			}
			switch len(matches) {
			case 0:
				return fmt.Errorf("mod %s not in profile", id)
			case 1:
				key = matches[0]
				ref = byKey[key]
			default:
				return fmt.Errorf("ambiguous mod id %s (use source:modid): %s", id, strings.Join(matches, ", "))
			}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		newRefs = append(newRefs, ref)
	}
	// Append mods not mentioned in args (unchanged relative order)
	for _, ref := range profile.Mods {
		key := ref.SourceID + ":" + ref.ModID
		if !seen[key] {
			newRefs = append(newRefs, ref)
		}
	}

	if err := service.ReorderProfileMods(game.ID, profileName, newRefs); err != nil {
		return fmt.Errorf("reordering: %w", err)
	}
	fmt.Printf("✓ Load order updated for profile %s.\n", profileName)
	return nil
}

func runProfileApply(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileApply(ctx, service, game, args)
	})
}

func doProfileApply(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {

	pm := getProfileManager(service)

	// Determine profile name
	var profileName string
	if len(args) > 0 {
		profileName = args[0]
	} else {
		defaultProfile, err := pm.GetDefault(game.ID)
		if err != nil {
			profileName = "default"
		} else {
			profileName = defaultProfile.Name
		}
	}

	// Get the profile
	profile, err := pm.Get(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("profile not found: %s", profileName)
	}

	// Get installed mods from database
	installedMods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// Build lookup of installed mods
	installedByKey := make(map[string]*domain.InstalledMod)
	for i := range installedMods {
		key := installedMods[i].SourceID + ":" + installedMods[i].ID
		installedByKey[key] = &installedMods[i]
	}

	// Build set of profile mod keys
	profileKeys := make(map[string]domain.ModReference)
	for _, mr := range profile.Mods {
		key := mr.SourceID + ":" + mr.ModID
		profileKeys[key] = mr
	}

	// Calculate differences
	var toDisable []*domain.InstalledMod
	var toEnable []*domain.InstalledMod
	var toInstall []domain.ModReference
	needsRedownloadSet := make(map[string]bool) // Track which mods are re-downloads
	needsReplaceSet := make(map[string]bool)    // #96: mods needing Installer.Replace (already deployed at the wrong version), not a bare Install

	// Check installed mods against profile. Deterministic order: iterate
	// core.OrderByProfile(profile, installedMods) - not `for key, im :=
	// range installedByKey`, which iterates map order - keeping installedByKey
	// only for the membership lookup below.
	ordered := core.OrderByProfile(profile, installedMods)
	for i := range ordered {
		im := &ordered[i]
		key := im.SourceID + ":" + im.ID
		if _, inProfile := profileKeys[key]; !inProfile {
			// Installed but not in profile - disable it
			if im.Enabled {
				toDisable = append(toDisable, im)
			}
		} else {
			// In profile - make sure it's enabled and at the profile's version
			ref := profileKeys[key]
			if ref.Version != "" && im.Version != ref.Version {
				// #96 convergence: the profile names a different version
				// than the installed row - reinstall at the profile's
				// version (downgrades included), regardless of enabled
				// state. ref is passed as-is: its own FileIDs (if any)
				// describe the TARGET version; the installed row's
				// describe the wrong one.
				toInstall = append(toInstall, ref)
				needsRedownloadSet[key] = false // fresh target version: profile FileIDs, not the DB's
				needsReplaceSet[key] = im.Deployed
				continue
			}
			if !im.Enabled {
				// Check if cache exists before adding to toEnable
				if service.GetGameCache(game).Exists(game.ID, im.SourceID, im.ID, im.Version) {
					toEnable = append(toEnable, im)
				} else {
					// Cache missing - need to re-download
					toInstall = append(toInstall, domain.ModReference{
						SourceID: im.SourceID,
						ModID:    im.ID,
						Version:  im.Version,
						FileIDs:  im.FileIDs,
					})
					needsRedownloadSet[key] = true
				}
			}
		}
	}

	// Check profile mods against installed. Deterministic order: iterate
	// profile.Mods - not `for key, ref := range profileKeys`, which iterates
	// map order. seen guards the same dedup profileKeys gave for free.
	seen := make(map[string]bool, len(profile.Mods))
	for _, ref := range profile.Mods {
		key := ref.SourceID + ":" + ref.ModID
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, installed := installedByKey[key]; !installed {
			// In profile but not installed
			toInstall = append(toInstall, ref)
		}
	}

	// Show changes
	if len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0 {
		fmt.Printf("System already matches profile %s.\n", profileName)
		return nil
	}

	fmt.Printf("Applying profile: %s\n\n", profileName)

	if len(toDisable) > 0 {
		fmt.Printf("Will disable %d mod(s):\n", len(toDisable))
		for _, im := range toDisable {
			fmt.Printf("  - %s (%s)\n", im.Name, im.ID)
		}
	}

	if len(toEnable) > 0 {
		fmt.Printf("Will enable %d mod(s):\n", len(toEnable))
		for _, im := range toEnable {
			fmt.Printf("  + %s (%s)\n", im.Name, im.ID)
		}
	}

	if len(toInstall) > 0 {
		fmt.Printf("Will install %d mod(s):\n", len(toInstall))
		for _, ref := range toInstall {
			fmt.Printf("  ↓ %s:%s v%s\n", ref.SourceID, ref.ModID, ref.Version)
		}
	}

	// Confirm unless --yes
	if !profileApplyYes {
		fmt.Print("\nProceed? [Y/n]: ")
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "" && input != "y" && input != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	installer, err := service.GetInstallerForProfile(game, profileName)
	if err != nil {
		return err
	}

	// Disable mods
	for _, im := range toDisable {
		if err := installer.Uninstall(ctx, game, &im.Mod, profileName); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to undeploy %s: %v\n", im.Name, err)
			}
		}
		if err := service.SetModEnabled(im.SourceID, im.ID, game.ID, profileName, false); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to update %s: %v\n", im.Name, err)
			}
		}
		fmt.Printf("  ✓ Disabled: %s\n", im.Name)
	}

	// Enable mods
	for _, im := range toEnable {
		if err := installer.Install(ctx, game, &im.Mod, profileName); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to deploy %s: %v\n", im.Name, err)
			}
			continue
		}
		if err := service.SetModEnabled(im.SourceID, im.ID, game.ID, profileName, true); err != nil {
			if verbose {
				fmt.Printf("  Warning: failed to update %s: %v\n", im.Name, err)
			}
		}
		fmt.Printf("  ✓ Enabled: %s\n", im.Name)
	}

	// Install missing mods
	if len(toInstall) > 0 {
		fmt.Println("\nInstalling missing mods...")
		for _, ref := range toInstall {
			fmt.Printf("  Installing %s:%s...\n", ref.SourceID, ref.ModID)

			// Fetch mod details
			mod, err := service.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
			if err != nil {
				fmt.Printf("    Error: failed to fetch mod: %v\n", err)
				continue
			}

			// Get files
			files, err := service.GetModFiles(ctx, ref.SourceID, mod)
			if err != nil {
				fmt.Printf("    Error: failed to get files: %v\n", err)
				continue
			}

			if len(files) == 0 {
				fmt.Printf("    Error: no downloadable files\n")
				continue
			}

			// Select files to download - use stored FileIDs for re-downloads, or profile FileIDs for new installs
			key := ref.SourceID + ":" + ref.ModID
			var fileIDsToUse []string
			if needsRedownloadSet[key] {
				// Re-download: use DB-stored FileIDs (from ref, which was populated from im.FileIDs)
				fileIDsToUse = ref.FileIDs
			} else if len(ref.FileIDs) > 0 {
				// New install: use FileIDs from profile
				fileIDsToUse = ref.FileIDs
			}
			filesToDownload, err := selectFilesToDownload(files, fileIDsToUse, ref.Version)
			if err != nil {
				fmt.Printf("    Error: %v\n", err)
				continue
			}
			mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94

			// #96: cache-first - a convergence entry (or any other
			// already-cached-at-this-version reinstall) skips the download
			// step entirely once the stamped version is already cached.
			// Review finding 2: HasFileIDs (not bare Exists) - a version
			// directory can exist yet be only PARTIALLY populated by a
			// previous download run that broke off partway through a
			// multi-file mod; skipping on directory presence alone would
			// silently leave it that way forever. Round 2: the check is by
			// FILE ID (the per-file completion markers
			// commitStagedCacheWithMarker stamps), never by FileName - a
			// cache entry for an extracted archive holds member names that
			// match no DownloadableFile, so a name-based check would miss
			// every archive-based mod and redownload a complete cache.
			downloadedFileIDs := make([]string, 0, len(filesToDownload))
			for _, f := range filesToDownload {
				downloadedFileIDs = append(downloadedFileIDs, f.ID)
			}
			if !service.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, downloadedFileIDs) {
				// Download each file
				progressFn := func(p core.DownloadProgress) {
					if p.TotalBytes > 0 {
						fmt.Printf("\r    Downloading: %.1f%%", p.Percentage)
					}
				}

				downloadFailed := false
				for _, selectedFile := range filesToDownload {
					_, err = service.DownloadMod(ctx, ref.SourceID, game, mod, selectedFile, progressFn)
					if err != nil {
						fmt.Println()
						fmt.Printf("    Error: download failed: %v\n", err)
						downloadFailed = true
						break
					}
				}
				fmt.Println()

				if downloadFailed {
					continue
				}
			}

			// Deploy. #96: a mod already deployed at the wrong version must
			// be replaced (removing files the new version no longer serves),
			// not just have new files installed alongside stale ones -
			// mirrors ApplyUpdate's Installer.Replace semantics
			// (internal/core/flows.go). Review finding 4: guard the map
			// lookup with its own ok - needsReplaceSet[key] should never be
			// true without a corresponding installedByKey[key] row, but a
			// bare index expression would panic on a nil *InstalledMod if
			// that invariant were ever violated.
			//
			// Round 2: the deployed flag alone isn't enough - Installer.
			// Replace reads the OLD version's cache entry to work out which
			// files to retire and hard-fails with "old mod not in cache"
			// when it's been pruned, which would abort convergence outright
			// and leave the old deployment on disk. Only Replace when that
			// entry is still there; otherwise fall back to a bare Install,
			// exactly as core's ApplyProfileSwitch does. The caveat is the
			// same in both twins: without the old file list, files the new
			// version no longer serves stay behind as stale deployments
			// (`lmm verify` surfaces them) - strictly better than failing to
			// converge at all.
			if prev, ok := installedByKey[key]; ok && needsReplaceSet[key] &&
				service.GetGameCache(game).Exists(game.ID, prev.SourceID, prev.ID, prev.Version) {
				if err := installer.Replace(ctx, game, &prev.Mod, mod, profileName); err != nil {
					fmt.Printf("    Error: deploy failed: %v\n", err)
					continue
				}
			} else if err := installer.Install(ctx, game, mod, profileName); err != nil {
				fmt.Printf("    Error: deploy failed: %v\n", err)
				continue
			}

			// Save to DB. Normalize GameID to the lmm game (see comment on
			// the doProfileSwitch save site for why).
			installedMod := &domain.InstalledMod{
				Mod:          *mod,
				ProfileName:  profileName,
				UpdatePolicy: domain.UpdateNotify,
				Enabled:      true,
				Deployed:     true, // review finding 3: Install/Replace above just succeeded
				FileIDs:      downloadedFileIDs,
			}
			installedMod.Mod.GameID = game.ID
			if err := service.SaveInstalledMod(installedMod); err != nil {
				fmt.Printf("    Error: save failed: %v\n", err)
				continue
			}

			// Update profile with actual downloaded FileIDs
			modRef := domain.ModReference{
				SourceID: mod.SourceID,
				ModID:    mod.ID,
				Version:  mod.Version,
				FileIDs:  downloadedFileIDs,
			}
			if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
				if verbose {
					fmt.Printf("    Warning: could not update profile: %v\n", err)
				}
			}

			fmt.Printf("    ✓ Installed: %s\n", mod.Name)
		}
	}

	// #197 postsmoke seam-audit fix: doProfileApply is a bespoke
	// disable/enable/install reimplementation - like batchInstallMods, it
	// never went through a core seam that syncs the merged pak. Sync
	// failures are printed unconditionally, matching batchInstallMods'
	// loud-failure fix.
	if syncWarnings, syncErr := service.SyncMergedPak(ctx, game, profileName); syncErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not sync merged pak: %v\n", syncErr)
	} else {
		for _, w := range syncWarnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}

	fmt.Printf("\n✓ Applied profile: %s\n", profileName)
	return nil
}

// selectPrimaryFile returns the primary file from a list of downloadable files,
// or the first file if no primary is marked. Returns nil for empty slice.
func selectPrimaryFile(files []domain.DownloadableFile) *domain.DownloadableFile {
	if len(files) == 0 {
		return nil
	}
	for i := range files {
		if files[i].IsPrimary {
			return &files[i]
		}
	}
	return &files[0]
}

// errNoDownloadableFiles is returned when selectFilesToDownload is called with no files.
var errNoDownloadableFiles = fmt.Errorf("no downloadable files")

// errStoredFilesUnavailable mirrors internal/core/flows.go's sentinel of the
// same name (selectDeployFiles' would-be-fallback rejection, #95): this
// package can't import internal/core's unexported sentinel, so it's
// duplicated here for the same documented reason selectFilesToDownload
// itself duplicates selectDeployFiles - see the cross-reference comment on
// selectFilesToDownload below and on selectDeployFiles in flows.go.
var errStoredFilesUnavailable = errors.New("stored file(s) no longer available upstream")

// availableVersions mirrors internal/core/resolve.go's unexported helper of
// the same name: the distinct non-empty versions in files, in first-seen
// order - display material for core.ErrVersionNotFound.
func availableVersions(files []domain.DownloadableFile) []string {
	seen := make(map[string]bool, len(files))
	var out []string
	for _, f := range files {
		if f.Version == "" || seen[f.Version] {
			continue
		}
		seen[f.Version] = true
		out = append(out, f.Version)
	}
	return out
}

// anyFileHasVersion mirrors internal/core/resolve.go's unexported helper of
// the same name: reports whether at least one file carries version info -
// the gate between version-aware and legacy (FileIDs-only) behavior.
func anyFileHasVersion(files []domain.DownloadableFile) bool {
	for _, f := range files {
		if f.Version != "" {
			return true
		}
	}
	return false
}

// selectFilesToDownload picks files to download based on the recorded
// version (#96), stored FileIDs (for re-downloads), or primary file (for
// fresh installs). Mirrors internal/core/flows.go's selectVersionedDeployFiles
// with allowFallback=false (doProfileApply's only caller is deploy-class, so
// no allowFallback parameter is needed here) exactly - same precedence,
// byte-identical error wording, so callers/tests can't tell which package
// produced a given error. See the CANONICAL NOTE at
// internal/tui/service_core.go:254-261 for why this is a hand-duplicated
// twin rather than a shared helper (cmd/lmm is package main).
//
// version == "" (legacy refs) and version-less file lists (the #130 vacuous
// rule) fall through to the pre-#96 behavior unchanged: storedFileIDs found
// upstream win, storedFileIDs missing hard-fail via errStoredFilesUnavailable
// (#95 - no fallback, since silently substituting the primary file would
// install a file the caller never asked for), and no storedFileIDs at all
// falls back to the primary file. Otherwise: stored IDs win only while their
// effective version agrees with the record; drift and gone-IDs heal by
// exact-match resolution to the SAME version (never latest); unresolvable
// targets are hard per-mod errors naming the version - the "gone upstream"
// #95 wording only when the stored IDs themselves match nothing at all
// upstream, versus a distinct core.ErrVersionNotFound wrap when at least one
// stored ID IS still present upstream but the recorded version isn't (the
// classic pre-#94 mis-stamped row, which isn't a "gone" file - it's a wrong
// version record on a file that's still there).
func selectFilesToDownload(files []domain.DownloadableFile, storedFileIDs []string, version string) ([]*domain.DownloadableFile, error) {
	if version == "" || !anyFileHasVersion(files) {
		return selectFilesToDownloadLegacy(files, storedFileIDs)
	}
	var idSet map[string]bool
	if len(storedFileIDs) > 0 {
		idSet = make(map[string]bool, len(storedFileIDs))
		for _, id := range storedFileIDs {
			idSet[id] = true
		}
	}
	var found []*domain.DownloadableFile
	for i := range files {
		if idSet[files[i].ID] {
			found = append(found, &files[i])
		}
	}
	if len(found) > 0 && domain.EffectiveInstalledVersion(version, found) == version {
		return found, nil
	}
	var matches []*domain.DownloadableFile
	for i := range files {
		if files[i].Version == version {
			matches = append(matches, &files[i])
		}
	}
	if len(matches) == 0 {
		if len(storedFileIDs) > 0 {
			if len(found) > 0 {
				// At least one stored ID is still present upstream - the
				// files aren't gone, only the recorded version doesn't
				// match anything. Distinct from the #95 "gone" wording
				// below: this is a version-record problem, not a
				// missing-file problem, so it points at verify/update
				// instead of reinstall.
				return nil, fmt.Errorf("%w: installed file(s) (ID(s): %s) do not match recorded version %q, which is not available upstream - run 'lmm verify --fix' to correct the version record, or 'lmm update' to adopt the current version", core.ErrVersionNotFound, strings.Join(storedFileIDs, ", "), version)
			}
			return nil, fmt.Errorf("%w (file ID(s): %s; version %q not available) - reinstall the mod or run 'lmm update' to adopt the current version", errStoredFilesUnavailable, strings.Join(storedFileIDs, ", "), version)
		}
		return nil, fmt.Errorf("%w: version %q is not available upstream (available: %s) - edit the profile's version or reinstall", core.ErrVersionNotFound, version, strings.Join(availableVersions(files), ", "))
	}
	// This "stored subset, else primary, else best category priority, else
	// first" tail is the twin of internal/core/flows.go's pickVersionMatch,
	// shared there by selectVersionedDeployFiles (#96) and
	// selectUpdateDeployFiles (#143) - mirror any change to it here, and vice
	// versa (drift guard: TestSelectFilesToDownload_CategoryPriorityTieBreak
	// and its core-side parity test).
	if len(storedFileIDs) > 0 {
		var stored []*domain.DownloadableFile
		for _, m := range matches {
			if idSet[m.ID] {
				stored = append(stored, m)
			}
		}
		if len(stored) > 0 {
			return stored, nil
		}
	}
	for _, m := range matches {
		if m.IsPrimary {
			return []*domain.DownloadableFile{m}, nil
		}
	}
	best := 0
	for i := 1; i < len(matches); i++ {
		if fileCategoryPriority(matches[i].Category) < fileCategoryPriority(matches[best].Category) {
			best = i
		}
	}
	return []*domain.DownloadableFile{matches[best]}, nil
}

// selectFilesToDownloadLegacy is selectFilesToDownload's pre-#96 behavior,
// mirroring internal/core/flows.go's selectDeployFiles with
// allowFallback=false: when storedFileIDs is non-empty but none of it
// matches what the source currently offers, silently substituting the
// primary file would install a file the caller never asked for - exactly
// the silent-fallback bug #95 tracks - so this returns
// errStoredFilesUnavailable instead, wrapped with the missing IDs and a
// remediation hint.
func selectFilesToDownloadLegacy(files []domain.DownloadableFile, storedFileIDs []string) ([]*domain.DownloadableFile, error) {
	if len(files) == 0 {
		return nil, errNoDownloadableFiles
	}
	if len(storedFileIDs) > 0 {
		// Try to use stored file IDs
		found := findFilesByIDs(files, storedFileIDs)
		if len(found) > 0 {
			return found, nil
		}
		// No fallback (#95): a would-be primary-file substitution is an error.
		return nil, fmt.Errorf("%w (file ID(s): %s) - reinstall the mod or run 'lmm update' to adopt the current version", errStoredFilesUnavailable, strings.Join(storedFileIDs, ", "))
	}
	// Fresh install: use primary file
	p := selectPrimaryFile(files)
	if p == nil {
		return nil, errNoDownloadableFiles
	}
	return []*domain.DownloadableFile{p}, nil
}
