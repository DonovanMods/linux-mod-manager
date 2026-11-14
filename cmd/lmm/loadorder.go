package main

// loadorder.go is `lmm load-order` (#620): a game's file-based load orders -
// Cyberpunk 2077's archive order, written to archive/pc/mod/modlist.txt -
// listed, and changed through core's PlanFileLoadOrderEdit/
// ApplyFileLoadOrderEdit pair, so --dry-run prints the plan and writes
// nothing. The web UI's load-order tab drives the same pair.

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	loadOrderProfile string
	loadOrderID      string
	loadOrderDryRun  bool
	loadOrderBefore  string
	loadOrderAfter   string
	loadOrderFirst   bool
	loadOrderLast    bool
)

var loadOrderCmd = &cobra.Command{
	Use:   "load-order",
	Short: "Show and change a game's file load order (Cyberpunk 2077's archives)",
	Long: `Show and change the order a game loads a set of mod files in, where the
game reads that order from a list file: Cyberpunk 2077 (a game on the
cyberpunk2077 adapter) loads the .archive files in archive/pc/mod in the
order archive/pc/mod/modlist.txt gives, and an archive listed earlier wins
a conflict with one listed later.

This is a third ordering axis beside mod order (which mod's file wins when
two ship the same path) and a Bethesda game's plugin order. The profile
records the order in its load_orders: block. It is opt-in: a profile with
no order leaves modlist.txt to the game and to you. The first 'move'
records the whole order as 'list' shows it, and from then on lmm writes
modlist.txt on every deploy; 'reset' clears the order and hands the file
back as it was before lmm wrote it.

Only the archives lmm deploys are the profile's to move. An archive lmm
did not deploy is never reordered: lmm lists each one where the game loads
it now, keeping its order among the others. An archive lmm deploys that the
order does not name yet goes in next to the archive it sorts after
alphabetically, which is where the game would load it with no list.

-p/--profile picks the profile (default: the active one); --id picks the
load order when a game has more than one.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var loadOrderListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the effective file load order",
	Long: `List each of the game's file load orders as lmm would write it, first
loaded first: the archive, what placed it, and the mod that ships it.

The "placed by" column says why an archive is where it is: order (the
profile's order), default (not in the order yet: its alphabetical place),
or external (not deployed by lmm; it keeps its place in the list).`,
	Example: `  lmm load-order list
  lmm load-order list -p coop --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withGameService(cmd, doLoadOrderList)
	},
}

var loadOrderMoveCmd = &cobra.Command{
	Use:   "move <file> (--before <file> | --after <file> | --first | --last)",
	Short: "Move a file in the load order",
	Long: `Move an archive lmm deploys before or after another one, or to the top
or the end of the order. The profile records the whole order as 'lmm
load-order list' shows it, so nothing else moves, and - for the active
profile - modlist.txt is written now.`,
	Example: `  lmm load-order move "! fixes.archive" --first
  lmm load-order move basegame_b.archive --before basegame_a.archive --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		edit := core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: args[0], Before: loadOrderBefore, After: loadOrderAfter}
		if loadOrderFirst {
			edit.To = core.LoadOrderMoveFirst
		}
		if loadOrderLast {
			edit.To = core.LoadOrderMoveLast
		}
		return runLoadOrderEdit(cmd, edit)
	},
}

var loadOrderResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Clear the profile's file load order",
	Long: `Clear the profile's order: lmm stops writing the list, and puts back the
modlist.txt the game had before lmm first wrote it (keeping what you
changed in it since), or removes the one lmm created.`,
	Example: `  lmm load-order reset --dry-run
  lmm load-order reset`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLoadOrderEdit(cmd, core.FileLoadOrderEdit{Op: core.LoadOrderOpReset})
	},
}

func init() {
	loadOrderCmd.PersistentFlags().StringVarP(&loadOrderProfile, "profile", "p", "", "profile (default: active profile)")
	loadOrderCmd.PersistentFlags().StringVar(&loadOrderID, "id", "", "the load order (default: the game's only one)")
	for _, c := range []*cobra.Command{loadOrderMoveCmd, loadOrderResetCmd} {
		c.Flags().BoolVar(&loadOrderDryRun, "dry-run", false, "print what would change without changing anything")
	}
	loadOrderMoveCmd.Flags().StringVar(&loadOrderBefore, "before", "", "move it just before this file")
	loadOrderMoveCmd.Flags().StringVar(&loadOrderAfter, "after", "", "move it just after this file")
	loadOrderMoveCmd.Flags().BoolVar(&loadOrderFirst, "first", false, "move it to the top of the order")
	loadOrderMoveCmd.Flags().BoolVar(&loadOrderLast, "last", false, "move it to the end of the order")
	loadOrderMoveCmd.MarkFlagsMutuallyExclusive("before", "after", "first", "last")
	loadOrderMoveCmd.MarkFlagsOneRequired("before", "after", "first", "last")

	loadOrderCmd.AddCommand(loadOrderListCmd, loadOrderMoveCmd, loadOrderResetCmd)
	rootCmd.AddCommand(loadOrderCmd)
}

func doLoadOrderList(ctx context.Context, svc *core.Service, game *domain.Game) error {
	profile, err := resolveProfile(ctx, svc, game.ID, loadOrderProfile)
	if err != nil {
		return err
	}
	report, err := svc.FileLoadOrders(ctx, game, profile)
	if err != nil {
		return err
	}
	if loadOrderID != "" {
		one, err := svc.FileLoadOrder(ctx, game, profile, loadOrderID)
		if err != nil {
			return err
		}
		report.Orders = []core.FileLoadOrderReport{*one}
	}
	if jsonOutput {
		return emitJSON(report)
	}
	for i := range report.Orders {
		if i > 0 {
			fmt.Println()
		}
		o := &report.Orders[i]
		fmt.Printf("%s load order for %s (profile %s)\n", o.Label, game.ID, o.Profile)
		renderLoadOrder(o)
	}
	return nil
}

func runLoadOrderEdit(cmd *cobra.Command, edit core.FileLoadOrderEdit) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doLoadOrderEdit(ctx, svc, game, edit)
	})
}

func doLoadOrderEdit(ctx context.Context, svc *core.Service, game *domain.Game, edit core.FileLoadOrderEdit) error {
	profile, err := resolveProfile(ctx, svc, game.ID, loadOrderProfile)
	if err != nil {
		return err
	}
	if edit.ID == "" {
		edit.ID = loadOrderID
	}
	plan, err := svc.PlanFileLoadOrderEdit(ctx, game, profile, edit)
	if err != nil {
		return err
	}
	if loadOrderDryRun {
		if jsonOutput {
			return emitJSON(plan)
		}
		fmt.Printf("Load-order edit for profile %s (dry run)\n\n", plan.Profile)
		if plan.NoChanges {
			fmt.Println("No change: the profile already says this.")
		}
		for _, c := range plan.Changes {
			fmt.Printf("  ~ %s\n", c)
		}
		fmt.Println()
		switch {
		case !plan.Active:
			fmt.Printf("%s is not the active profile: the change would be recorded only.\n", plan.Profile)
		case plan.Order.Recorded:
			fmt.Printf("The game's %s would be written in this order:\n", listFileName(plan.Order))
		default:
			fmt.Printf("The profile would order nothing: lmm would hand %s back to the game.\n", listFileName(plan.Order))
		}
		renderLoadOrder(plan.Order)
		return nil
	}
	res, err := svc.ApplyFileLoadOrderEdit(ctx, game, plan)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(res)
	}
	for _, c := range res.Changes {
		fmt.Printf("  ✓ %s\n", c)
	}
	if !res.Changed {
		fmt.Println("No change to the profile.")
	}
	if !res.Order.Active {
		fmt.Printf("%s is not the active profile: recorded only; its list is written when it is deployed.\n", res.Profile)
		return nil
	}
	if res.List != nil {
		for _, f := range res.List.Findings {
			fmt.Fprintf(os.Stderr, "Warning: load order: %s\n", f.Message)
		}
		renderLoadOrderResults([]core.FileLoadOrderResult{*res.List})
	}
	return nil
}

// listFileName is the list file's name for a report, as a message names it.
func listFileName(o *core.FileLoadOrderReport) string {
	if o.ListFile != "" {
		return o.ListFile
	}
	return "list file"
}

// renderLoadOrder prints a FileLoadOrderReport: the order as a table, the
// entries not deployed, the files the list does not name, and the
// findings.
func renderLoadOrder(o *core.FileLoadOrderReport) {
	if o.ListFile != "" {
		fmt.Printf("List: %s\n", o.ListFile)
	}
	if o.FirstWins {
		fmt.Println("An archive higher in the order wins a conflict with one lower down.")
	}
	if !o.Active {
		fmt.Printf("%s is not the active profile: its files are not deployed, so lmm cannot work out their effective order.\n", o.Profile)
	} else if !o.Recorded {
		fmt.Println("The profile orders nothing: the game uses its own order, shown here for lmm's files. 'lmm load-order move' records one.")
	}
	fmt.Println()
	if len(o.Files) == 0 && o.Active {
		fmt.Println("No files deployed.")
	}
	if len(o.Files) > 0 {
		w := newDisplayWidthTableWriter(os.Stdout)
		_, _ = fmt.Fprintln(w, "  #\tFILE\tPLACED BY\tMOD")
		for _, e := range o.Files {
			mod := e.ModName
			if mod == "" && e.ModID != "" {
				mod = domain.ModKey(e.SourceID, e.ModID)
			}
			_, _ = fmt.Fprintf(w, "  %d\t%s\t%s\t%s\n", e.Position, e.Name, e.Placement, mod)
		}
		_ = w.Flush()
	}
	if len(o.NotDeployed) > 0 {
		fmt.Println()
		if o.Active {
			fmt.Println("In the profile's order, not deployed now (kept for when they are):")
		} else {
			fmt.Println("The profile's order:")
		}
		for _, n := range o.NotDeployed {
			fmt.Printf("  %s\n", n)
		}
	}
	if len(o.Findings) > 0 {
		fmt.Println()
		for _, f := range o.Findings {
			label := colorYellow("warning")
			if f.Severity == core.PluginSeverityError {
				label = colorRed("error")
			}
			fmt.Printf("%s: %s\n", label, f.Message)
		}
	}
}

// renderLoadOrderResults prints what a deploy, a purge or an edit did to
// the game's file load-order lists (#620): a line each, and the file's
// path. The findings themselves were printed as they happened, as
// warnings, so they are not repeated here.
func renderLoadOrderResults(results []core.FileLoadOrderResult) {
	for _, res := range results {
		var line string
		switch res.Status {
		case core.PluginListWritten, core.PluginListUnchanged:
			verb := "written"
			if res.Status == core.PluginListUnchanged {
				verb = "unchanged"
			}
			line = fmt.Sprintf("Load order %s %s: %d file(s)", res.ID, verb, res.Files)
			if res.External > 0 {
				line += fmt.Sprintf(" (%d kept that lmm did not deploy)", res.External)
			}
		case core.PluginListRestored:
			line = fmt.Sprintf("Load order %s: the list you had before lmm wrote it is back", res.ID)
		case core.PluginListRemoved:
			line = fmt.Sprintf("Load order %s: list removed (there was none before lmm wrote it)", res.ID)
		default:
			line = colorYellow(fmt.Sprintf("Load order %s not written", res.ID)) + " - see the warnings above"
		}
		if res.HasErrors() && res.Status != core.PluginListSkipped {
			line += " - " + colorRed("with errors, see the warnings above")
		}
		fmt.Println(line)
		if res.ListFile != "" {
			fmt.Printf("  %s\n", res.ListFile)
		}
	}
}

// printCollectionLoadOrders summarises the file load orders a collection
// import saves on the profile (#620): each order's size, its first files,
// the mod each comes from when the source says, and whether lmm writes it.
// The full list is the --json plan's profile.load_orders.
func printCollectionLoadOrders(orders []core.CollectionLoadOrderSummary, game *domain.Game) {
	const shown = 10
	for _, o := range orders {
		label := o.Label
		if label == "" {
			label = o.ID
		}
		fmt.Printf("\n%s load order (%s): %d file(s)\n", label, o.ID, len(o.Files))
		for i, f := range o.Files {
			if i == shown {
				fmt.Printf("    … and %d more (the --json plan lists them all)\n", len(o.Files)-shown)
				break
			}
			if f.Mod != "" {
				fmt.Printf("  %3d. %s  (%s)\n", i+1, f.Name, f.Mod)
			} else {
				fmt.Printf("  %3d. %s\n", i+1, f.Name)
			}
		}
		if o.Managed {
			fmt.Printf("  Saved on the profile and written to %s's list at every deploy.\n", game.Name)
		} else {
			fmt.Printf("  %s's adapter does not write this load order, so the import is refused for it.\n", game.Name)
		}
	}
}
