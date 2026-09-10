package main

// snapshot.go is `lmm snapshot create|list|restore|delete` (#350) - the
// last unchecked item on the README's roadmap since v1.
//
// Everything here is prompts and rendering. The engine (what a snapshot
// records, what a restore does, what it refuses) lives in
// internal/core/snapshot.go and snapshot_restore.go, shared with `lmm
// serve`'s own Snapshots surface.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Record and restore a game's mod arrangement",
	Long: `Record a named point you can bring a game back to.

A snapshot is metadata, not a copy of your mods: the profile (with its load
order and locks), the installed versions and settings, the deployed files
with their checksums, and a reference to the originals lmm has stored. The
mod files themselves are already in the cache, so a snapshot costs
kilobytes.

The originals are the part lmm cannot reconstruct any other way. Whenever a
deploy or a profile override would replace a file lmm did not put there -
stock game content, or a file another tool left - the original is copied to
$XDG_DATA_HOME/lmm/snapshots/<game>/_originals/ first, once, and a restore
puts it back. Deleting a snapshot never deletes those; they are the only
copy.

Examples:
  lmm snapshot create --game skyrim-se --name before-total-conversion
  lmm snapshot list --game skyrim-se
  lmm snapshot restore before-total-conversion --game skyrim-se --dry-run
  lmm snapshot restore before-total-conversion --game skyrim-se
  lmm snapshot delete before-total-conversion --game skyrim-se

Set 'auto_snapshot: true' in config.yaml to record one automatically before
every deploy, profile switch and update. It is off by default because
hashing a large deployed tree on every deploy is a real cost.`,
}

var (
	snapshotProfile      string
	snapshotName         string
	snapshotYes          bool
	snapshotRestoreDry   bool
	snapshotRestoreForce bool
	snapshotNoSafety     bool
)

var snapshotCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Record the current arrangement under a name",
	Long: `Record the current arrangement of a game and profile under a name.

Without --name the snapshot is named after the moment it was taken. A name
already in use is refused rather than overwritten - a snapshot is the only
copy of an arrangement you asked lmm to remember.

Examples:
  lmm snapshot create --game skyrim-se
  lmm snapshot create --game skyrim-se --name before-total-conversion
  lmm snapshot create --game skyrim-se --profile survival --name known-good`,
	Args: cobra.NoArgs,
	RunE: runSnapshotCreate,
}

var snapshotListCmd = &cobra.Command{
	Use:   "list",
	Short: "List a game's snapshots, newest first",
	Long: `List a game's snapshots, newest first.

The size shown is the snapshot document plus the originals it would put
back. Originals are shared between snapshots of the same game, so the sizes
do not add up to a disk total.

Examples:
  lmm snapshot list --game skyrim-se`,
	Args: cobra.NoArgs,
	RunE: runSnapshotList,
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore <name>",
	Short: "Bring a game back to a recorded snapshot",
	Long: `Bring a game back to a recorded snapshot.

Five stages, in order: the current deployment is undeployed, every original
lmm has stored is put back (each verified against its recorded checksum),
the recorded profile is written and made the active one, the mods it lists
are installed at their recorded versions, and the ones the snapshot recorded
as enabled are deployed - downgrades included. A version the source can no
longer serve is reported as a refusal, in the preview, before anything is
touched; it is never a quiet partial restore.

Restoring a snapshot taken while ANOTHER profile was active switches you
back: that profile's deployment is undeployed first, and the snapshot's own
profile becomes the active one.

A snapshot of the CURRENT state is recorded first, so a restore is itself
reversible. Pass --no-safety-snapshot to skip that.

Use --dry-run to see the whole plan without changing anything.

Examples:
  lmm snapshot restore before-total-conversion --game skyrim-se --dry-run
  lmm snapshot restore before-total-conversion --game skyrim-se
  lmm snapshot restore known-good --game skyrim-se --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runSnapshotRestore,
}

var snapshotDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a snapshot",
	Long: `Delete a snapshot's record.

The stored originals are NOT deleted: they are shared by every snapshot of
the game and are the only copy of files lmm replaced. Removing them is a
deliberate act, done by hand.

Examples:
  lmm snapshot delete before-total-conversion --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runSnapshotDelete,
}

func init() {
	snapshotCreateCmd.Flags().StringVarP(&snapshotProfile, "profile", "p", "", "profile to record (default: active profile)")
	snapshotCreateCmd.Flags().StringVar(&snapshotName, "name", "", "name for the snapshot (default: the current date and time)")

	snapshotRestoreCmd.Flags().BoolVar(&snapshotRestoreDry, "dry-run", false, "print what the restore would do without changing anything")
	snapshotRestoreCmd.Flags().BoolVarP(&snapshotYes, "yes", "y", false, "skip the confirmation prompt")
	snapshotRestoreCmd.Flags().BoolVarP(&snapshotRestoreForce, "force", "f", false, "continue even if hooks fail")
	snapshotRestoreCmd.Flags().BoolVar(&snapshotNoSafety, "no-safety-snapshot", false, "do not record the current state before restoring")

	snapshotCmd.AddCommand(snapshotCreateCmd)
	snapshotCmd.AddCommand(snapshotListCmd)
	snapshotCmd.AddCommand(snapshotRestoreCmd)
	snapshotCmd.AddCommand(snapshotDeleteCmd)
	rootCmd.AddCommand(snapshotCmd)
}

func runSnapshotCreate(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doSnapshotCreate(ctx, service, game)
	})
}

// doSnapshotCreate resolves the profile and the name, records the snapshot,
// and renders it.
func doSnapshotCreate(ctx context.Context, service *core.Service, game *domain.Game) error {
	profileName, err := resolveProfile(ctx, service, game.ID, snapshotProfile)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(snapshotName)
	if name == "" {
		// core owns the format, so `lmm snapshot create` and the web UI's
		// "Snapshot now" button name the same gesture the same way.
		name = core.DefaultSnapshotName(time.Now())
	}

	result, err := service.CreateSnapshot(ctx, game, profileName, name)
	if err != nil {
		return err
	}
	// Ruling 15: the document is the run's whole output.
	if jsonOutput {
		return emitJSON(result)
	}
	printSnapshotCreated(result, game)
	return nil
}

// printSnapshotCreated renders a create: what it is called, what it
// recorded, and where the file is - the last so a user can copy it
// somewhere safe.
func printSnapshotCreated(result *core.SnapshotResult, game *domain.Game) {
	fmt.Printf("Recorded snapshot %s for %s (profile: %s)\n", result.Name, game.Name, result.Profile)
	fmt.Printf("  %d mod(s), %d deployed file(s), %d stored original(s)\n",
		result.Mods, result.DeployedFiles, result.Originals)
	fmt.Printf("  %s\n", result.Path)
}

func runSnapshotList(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doSnapshotList(ctx, service, game)
	})
}

// doSnapshotList renders the listing as a table, newest first. A snapshot
// file that could not be read is a stderr warning, never a swallowed row:
// the others are still restorable and the broken one is what needs saying.
func doSnapshotList(ctx context.Context, service *core.Service, game *domain.Game) error {
	listing, err := service.ListSnapshots(ctx, game.ID)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(listing)
	}
	for _, warning := range listing.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
	}
	if len(listing.Snapshots) == 0 {
		fmt.Printf("No snapshots for %s. Record one with 'lmm snapshot create --game %s'.\n", game.Name, game.ID)
		return nil
	}
	fmt.Printf("Snapshots for %s:\n\n", game.Name)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tTAKEN\tPROFILE\tMODS\tFILES\tORIGINALS\tSIZE"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	for _, row := range listing.Snapshots {
		name := row.Name
		if row.Auto {
			name += " (auto)"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
			name, row.CreatedAt.Local().Format("2006-01-02 15:04"), row.Profile,
			row.Mods, row.DeployedFiles, row.Originals, humanBytes(row.SizeBytes)); err != nil {
			return fmt.Errorf("writing row %s: %w", row.Name, err)
		}
	}
	return w.Flush()
}

func runSnapshotDelete(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doSnapshotDelete(ctx, service, game, args[0])
	})
}

// doSnapshotDelete removes one snapshot's record and says what it did NOT
// remove - the stored originals, which are the only copy of the files lmm
// replaced.
func doSnapshotDelete(ctx context.Context, service *core.Service, game *domain.Game, name string) error {
	result, err := service.DeleteSnapshot(ctx, game.ID, name)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}
	fmt.Printf("Deleted snapshot %s.\n", result.Name)
	fmt.Println("The stored originals were kept - they are the only copy of the files lmm replaced.")
	return nil
}

func runSnapshotRestore(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doSnapshotRestore(ctx, service, game, args[0])
	})
}

// doSnapshotRestore plans the restore, renders it, asks, and applies it -
// printing every line from the event stream at its point of occurrence, the
// same adapter pattern doDeploy and doPurge use.
func doSnapshotRestore(ctx context.Context, service *core.Service, game *domain.Game, name string) error {
	plan, err := service.PlanSnapshotRestore(ctx, game, name)
	if err != nil {
		return err
	}

	// Ruling 15: --dry-run --json is the Plan document and nothing else.
	if snapshotRestoreDry && jsonOutput {
		return emitJSON(plan)
	}
	if snapshotRestoreDry {
		renderSnapshotRestorePlan(plan, game)
		return nil
	}

	if !snapshotYes {
		if !jsonOutput {
			renderSnapshotRestorePlan(plan, game)
			fmt.Print("\nContinue? [y/N] ")
		}
		response, err := readPromptLine()
		if err != nil {
			return err
		}
		if response != "y" && response != "yes" {
			return ErrCancelled
		}
	}

	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.DeployPurging:
			fmt.Printf("\nUndeploying the current mods...\n\n")
		case core.PurgeModPurged:
			fmt.Printf("  ✓ %s\n", p.ModName)
		case core.SnapshotRestoringOriginals:
			fmt.Printf("\nPutting back %d stored original(s)...\n\n", p.Total)
		case core.SnapshotOriginalRestored:
			fmt.Printf("  ✓ %s\n", p.Detail)
		case core.SnapshotOriginalSkipped:
			fmt.Printf("  ✗ %s\n", p.Detail)
		case core.SnapshotConverging:
			fmt.Printf("\nRestoring the recorded mods...\n\n")
		case core.SwitchInstalled, core.DeployDeployed:
			fmt.Printf("  ✓ %s\n", p.ModName)
		case core.SnapshotNote, core.PurgeNote, core.DeployNote:
			if verbose {
				fmt.Printf("  %s\n", p.Detail)
			}
		case core.SnapshotWarning, core.PurgeWarning, core.DeployWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}

	result, err := service.ApplySnapshotRestore(ctx, game, plan, core.SnapshotRestoreOptions{
		SkipHooks:        noHooks,
		Force:            snapshotRestoreForce,
		NoSafetySnapshot: snapshotNoSafety,
	}, quietSink(progress))
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}
	renderSnapshotRestoreResult(result)
	return nil
}

// renderSnapshotRestorePlan prints the plan: what goes away, what comes
// back, and - first, because it is the only part the user may want to stop
// for - what cannot be restored at all.
func renderSnapshotRestorePlan(plan *core.SnapshotRestorePlan, game *domain.Game) {
	fmt.Printf("Restore %s to snapshot %s (taken %s)\n",
		game.Name, plan.Snapshot, plan.CreatedAt.Local().Format("2006-01-02 15:04"))
	fmt.Printf("Profile: %s\n", plan.Profile)
	if plan.ActiveProfile != "" {
		// The restore carries the switch (review finding 2), so say so
		// BEFORE the counts: it is the part of the plan a user reading
		// "restore" would not otherwise expect.
		fmt.Printf("The active profile will change from %s to %s, and %d of its mod(s) will be undeployed.\n",
			plan.ActiveProfile, plan.Profile, len(plan.ToPurgeActive))
	}

	if len(plan.Refusals) > 0 {
		fmt.Printf("\n%d mod(s) CANNOT be restored:\n", len(plan.Refusals))
		for _, r := range plan.Refusals {
			fmt.Printf("  ✗ %s: %s\n", snapshotModLabel(r.Name, r.SourceID, r.ModID), r.Reason)
		}
	}

	fmt.Printf("\nWill undeploy %d mod(s).\n", len(plan.ToPurge)+len(plan.ToPurgeActive))
	if len(plan.External) > 0 {
		// #269: named, not counted into the line above - lmm undeploys none
		// of them, and a preview that said it would is a promise it never
		// keeps.
		fmt.Printf("%d Steam Workshop item(s) are left alone: %s\n",
			len(plan.External), strings.Join(plan.External, ", "))
	}

	restorable, unavailable := 0, 0
	for _, o := range plan.Originals {
		if o.Status == core.SnapshotOriginalRestorable {
			restorable++
		} else {
			unavailable++
		}
	}
	fmt.Printf("Will put back %d stored original(s).\n", restorable)
	if unavailable > 0 {
		fmt.Printf("%d stored original(s) cannot be put back:\n", unavailable)
		for _, o := range plan.Originals {
			if o.Status != core.SnapshotOriginalRestorable {
				fmt.Printf("  ✗ %s: %s\n", o.RelativePath, o.Reason)
			}
		}
	}

	// #386: the count and the list below it must describe the same set. The
	// count is of mods lmm will actually restore; the list also prints every
	// external item, which lmm leaves exactly as Steam has it - so the
	// header says both numbers rather than heading five bullets with "3".
	restorableMods, externalMods := 0, 0
	for _, m := range plan.Mods {
		switch {
		case m.Error != "":
		case m.External:
			externalMods++
		default:
			restorableMods++
		}
	}
	header := fmt.Sprintf("Will restore %d mod(s) at their recorded versions", restorableMods)
	if externalMods > 0 {
		header += fmt.Sprintf(", and leave %d Steam Workshop item(s) as Steam has them", externalMods)
	}
	fmt.Println(header + ":")
	for _, m := range plan.Mods {
		if m.Error != "" {
			continue
		}
		// #269: an external row is listed so the preview accounts for the
		// whole profile, with what lmm will do to it - nothing - said in
		// place of the "(will download)" it would otherwise imply. Its
		// version goes through displayModVersion because Steam's is a
		// 19-digit content id.
		detail := ""
		switch {
		case m.External && m.ExternalMissing:
			detail = " (Steam Workshop item - Steam no longer has it on disk)"
		case m.External:
			detail = " (Steam Workshop item - left as Steam has it)"
		case !m.Cached:
			detail = " (will download)"
		}
		fmt.Printf("  - %s %s%s\n", snapshotModLabel(m.Name, m.SourceID, m.ModID),
			displayModVersion(m.External, m.Version, m.UpdatedAt), detail)
	}
	if plan.ProfileChanged {
		fmt.Printf("\nThe profile %s will be rewritten from the snapshot.\n", plan.Profile)
	}
}

// renderSnapshotRestoreResult prints what actually happened. Anything the
// restore could not do is printed after the counts, never omitted: a
// partial restore that reads like a complete one is the failure mode this
// whole feature exists to avoid.
func renderSnapshotRestoreResult(result *core.SnapshotRestoreResult) {
	fmt.Printf("\nRestored %s.\n", result.Snapshot)
	if result.SwitchedFrom != "" {
		fmt.Printf("  Active profile: %s (was %s)\n", result.Profile, result.SwitchedFrom)
	}
	fmt.Printf("  Undeployed: %d\n", result.Purged)
	fmt.Printf("  Originals put back: %d\n", result.OriginalsRestored)
	fmt.Printf("  Installed: %d, replaced: %d, deployed: %d\n", result.Installed, result.Replaced, result.Deployed)

	// #386: the restore keeps the download and the installed_mods row of a
	// mod the snapshot's profile does not list - defensible, but it means
	// `lmm list` counts one more mod than the restored profile has. Say so
	// here, where the difference is created.
	if len(result.LeftInstalled) > 0 {
		names := make([]string, 0, len(result.LeftInstalled))
		for _, m := range result.LeftInstalled {
			names = append(names, snapshotModLabel(m.Name, m.SourceID, m.ModID))
		}
		fmt.Printf("  %d mod(s) left installed but disabled: %s\n", len(names), strings.Join(names, ", "))
	}

	if len(result.OriginalsSkipped) > 0 {
		fmt.Printf("\n%d original(s) could NOT be put back:\n", len(result.OriginalsSkipped))
		for _, o := range result.OriginalsSkipped {
			fmt.Printf("  ✗ %s: %s\n", o.RelativePath, o.Reason)
		}
	}
	if len(result.Refused) > 0 {
		fmt.Printf("\n%d mod(s) could NOT be restored:\n", len(result.Refused))
		for _, r := range result.Refused {
			fmt.Printf("  ✗ %s: %s\n", snapshotModLabel(r.Name, r.SourceID, r.ModID), r.Reason)
		}
	}
	if result.SafetySnapshot != "" {
		fmt.Printf("\nThe state before this restore was recorded as %s.\n", result.SafetySnapshot)
	}
}

// snapshotModLabel renders a mod as its name when there is one, and as its
// source:id identity when there is not - a mod whose source cannot be
// reached has no name to print.
func snapshotModLabel(name, sourceID, modID string) string {
	if name != "" {
		return name
	}
	return domain.ModKey(sourceID, modID)
}

// humanBytes renders a byte count at the largest unit that stays readable -
// the same rule the web UI's own formatter uses.
func humanBytes(n int64) string {
	if n < 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value, unit := float64(n), 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", n, units[0])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}
