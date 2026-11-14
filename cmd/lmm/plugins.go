package main

// plugins.go is `lmm plugins` (#617, design docs/plans/2026-11-10-
// collections-design.md §5 "Frontends"): a Bethesda game's plugin order -
// list, enable, disable, move, sort, and the profile's LOOT-userlist-shaped
// rules. Every mutation is core's PlanPluginEdit/ApplyPluginEdit pair, so
// --dry-run prints the plan and nothing is written; the web UI's Plugins
// tab drives the same pair.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	pluginsProfile    string
	pluginsDryRun     bool
	pluginsBefore     string
	pluginsAfter      string
	pluginsFirst      bool
	pluginsLast       bool
	pluginsRuleAfter  []string
	pluginsRuleReq    []string
	pluginsRuleGroup  string
	pluginsClearGroup bool
	pluginsGroupAfter []string
)

var pluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "Show and change a Bethesda game's plugin load order",
	Long: `Show and change the plugin load order of a Bethesda game (a game on
the bethesda adapter): which .esm, .esp and .esl files the game loads, in
what order, and which are active.

Plugin order is a second axis beside mod order. Mod order decides which
mod's file wins when two ship the same path; plugin order decides the
order the game loads its plugins in. lmm works the plugin order out from
the profile's plugins: block (the order you chose and your rules), the
plugins' own masters, and the plugins deployed, and writes it to the
game's Plugins.txt on every deploy.

Only the plugins lmm deploys are the profile's to order or switch. The
game's own plugins always load first, and a plugin lmm did not deploy
(installed by hand, or by the game's Creations menu) keeps the place and
the state the game gave it.

Every command that changes something takes --dry-run, which prints what
it would do and writes nothing. -p/--profile picks the profile (default:
the active one); only the active profile's plugins are deployed, so a
change to another profile is recorded and written to Plugins.txt when it
is deployed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var pluginsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the effective plugin load order",
	Long: `List the plugin load order lmm writes to the game's Plugins.txt, first
loaded first: each plugin's state, its flags (M master, L light, D
medium), the masters it needs, what placed it, and the mod that ships it.

The "placed by" column says why a plugin is where it is: the game (its own
plugins, always first), order (the profile's plugin order), new (not in
the order yet: it loads last), rule (a profile rule holds it after a
plugin the order puts later), master (one of its masters does), or
external (not deployed by lmm; it keeps its place).

Problems the game would not survive - a missing master, too many plugins
active, a cycle in the rules - are listed after the order.`,
	Example: `  lmm plugins list
  lmm plugins list -p survival --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withGameService(cmd, doPluginsList)
	},
}

var pluginsEnableCmd = &cobra.Command{
	Use:   "enable <plugin>...",
	Short: "Activate plugins",
	Long: `Activate one or more plugins lmm deploys, in the profile's plugin order
and, for the active profile, in the game's Plugins.txt.`,
	Example: `  lmm plugins enable "Optional Thing.esp"
  lmm plugins enable A.esp B.esp --dry-run`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginEdit(cmd, core.PluginEdit{Op: core.PluginOpEnable, Plugins: args})
	},
}

var pluginsDisableCmd = &cobra.Command{
	Use:   "disable <plugin>...",
	Short: "Deactivate plugins",
	Long: `Deactivate one or more plugins lmm deploys. The plugin stays deployed
and keeps its place in the order; the game just does not load it.`,
	Example: `  lmm plugins disable "Optional Thing.esp"`,
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginEdit(cmd, core.PluginEdit{Op: core.PluginOpDisable, Plugins: args})
	},
}

var pluginsMoveCmd = &cobra.Command{
	Use:   "move <plugin> (--before <plugin> | --after <plugin> | --first | --last)",
	Short: "Move a plugin in the load order",
	Long: `Move a plugin lmm deploys before or after another one, or to the top or
the end of the order. The profile records the whole order as you see it in
'lmm plugins list', so nothing else moves.

A rule or a master can hold a plugin where it is: a plugin always loads
after its masters, and after the plugins your rules put before it. The
move is still recorded, and --dry-run says what holds the plugin.`,
	Example: `  lmm plugins move Patch.esp --after SkyUI_SE.esp
  lmm plugins move "Optional Thing.esp" --last --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		edit := core.PluginEdit{Op: core.PluginOpMove, Plugin: args[0], Before: pluginsBefore, After: pluginsAfter}
		if pluginsFirst {
			edit.To = core.PluginMoveFirst
		}
		if pluginsLast {
			edit.To = core.PluginMoveLast
		}
		return runPluginEdit(cmd, edit)
	},
}

var pluginsSortCmd = &cobra.Command{
	Use:   "sort",
	Short: "Record the effective plugin order and write Plugins.txt",
	Long: `Work out the plugin order from the profile's order, its rules and the
plugins' masters, record it in the profile - new plugins included, where
they load - and write the game's Plugins.txt now.

A deploy already writes Plugins.txt in this order; sort is how you keep
it in the profile, so the order is yours to change from there, and how
you write the list without a deploy. It needs the active profile, whose
plugins are deployed. A cycle in the rules is refused.`,
	Example: `  lmm plugins sort --dry-run
  lmm plugins sort`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginEdit(cmd, core.PluginEdit{Op: core.PluginOpSort})
	},
}

var pluginsRulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Show and change the profile's plugin ordering rules",
	Long: `Show and change the profile's plugin rules, which take the shape of a
LOOT userlist: a plugin can load after other plugins (after), require
them (req: it loads after them, and lmm reports one that is not active),
or belong to a group; a group can load after other groups. Every plugin
no rule places is in the group named default.

Rules are applied on every deploy. A rule that would make the order
impossible (a cycle) is refused.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var pluginsRulesListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List the profile's plugin rules",
	Example: `  lmm plugins rules list`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withGameService(cmd, doPluginRulesList)
	},
}

var pluginsRulesAddCmd = &cobra.Command{
	Use:   "add <plugin> [--after <plugin>]... [--req <plugin>]... [--group <group>]",
	Short: "Add to a plugin's rule",
	Long: `Add to a plugin's rule: plugins it loads after (--after), plugins it
requires (--req), or the group it is in (--group, which must exist: add
it with 'lmm plugins rules group add'). Each flag can be repeated. A rule
may name a plugin that is not deployed yet; it applies once it is.`,
	Example: `  lmm plugins rules add Patch.esp --after SkyUI_SE.esp
  lmm plugins rules add Patch.esp --group "Late Loaders" --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginEdit(cmd, core.PluginEdit{Op: core.PluginOpRuleAdd,
			Rule: domain.PluginRule{Name: args[0], Group: pluginsRuleGroup, After: pluginsRuleAfter, Req: pluginsRuleReq}})
	},
}

var pluginsRulesRemoveCmd = &cobra.Command{
	Use:   "remove <plugin> [--after <plugin>]... [--req <plugin>]... [--clear-group]",
	Short: "Remove a plugin's rule, or part of it",
	Long: `Remove a plugin's whole rule, or with --after, --req or --clear-group
only those parts of it. A rule left with nothing in it is removed.`,
	Example: `  lmm plugins rules remove Patch.esp
  lmm plugins rules remove Patch.esp --after SkyUI_SE.esp`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginEdit(cmd, core.PluginEdit{Op: core.PluginOpRuleRemove, ClearGroup: pluginsClearGroup,
			Rule: domain.PluginRule{Name: args[0], After: pluginsRuleAfter, Req: pluginsRuleReq}})
	},
}

var pluginsRulesGroupCmd = &cobra.Command{
	Use:   "group",
	Short: "Add or remove plugin rule groups",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

var pluginsRulesGroupAddCmd = &cobra.Command{
	Use:   "add <group> [--after <group>]...",
	Short: "Add a group, or groups it loads after",
	Long: `Add a plugin group, or more groups an existing one loads after. Every
plugin of the group loads after every plugin of each group named with
--after ("default" is the group of every plugin no rule places).`,
	Example: `  lmm plugins rules group add "Late Loaders" --after default`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginEdit(cmd, core.PluginEdit{Op: core.PluginOpGroupAdd, Group: domain.PluginGroup{Name: args[0], After: pluginsGroupAfter}})
	},
}

var pluginsRulesGroupRemoveCmd = &cobra.Command{
	Use:   "remove <group>",
	Short: "Remove a group no rule uses",
	Long: `Remove a plugin group. A group a plugin rule puts a plugin in, or
another group loads after, is refused: change those first.`,
	Example: `  lmm plugins rules group remove "Late Loaders"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginEdit(cmd, core.PluginEdit{Op: core.PluginOpGroupRemove, Group: domain.PluginGroup{Name: args[0]}})
	},
}

func init() {
	pluginsCmd.PersistentFlags().StringVarP(&pluginsProfile, "profile", "p", "", "profile (default: active profile)")
	for _, c := range []*cobra.Command{pluginsEnableCmd, pluginsDisableCmd, pluginsMoveCmd, pluginsSortCmd,
		pluginsRulesAddCmd, pluginsRulesRemoveCmd, pluginsRulesGroupAddCmd, pluginsRulesGroupRemoveCmd} {
		c.Flags().BoolVar(&pluginsDryRun, "dry-run", false, "print what would change without changing anything")
	}
	pluginsMoveCmd.Flags().StringVar(&pluginsBefore, "before", "", "move it just before this plugin")
	pluginsMoveCmd.Flags().StringVar(&pluginsAfter, "after", "", "move it just after this plugin")
	pluginsMoveCmd.Flags().BoolVar(&pluginsFirst, "first", false, "move it to the top of the order")
	pluginsMoveCmd.Flags().BoolVar(&pluginsLast, "last", false, "move it to the end of the order")
	pluginsMoveCmd.MarkFlagsMutuallyExclusive("before", "after", "first", "last")
	pluginsMoveCmd.MarkFlagsOneRequired("before", "after", "first", "last")

	for _, c := range []*cobra.Command{pluginsRulesAddCmd, pluginsRulesRemoveCmd} {
		c.Flags().StringArrayVar(&pluginsRuleAfter, "after", nil, "a plugin it loads after (repeatable)")
		c.Flags().StringArrayVar(&pluginsRuleReq, "req", nil, "a plugin it requires (repeatable)")
	}
	pluginsRulesAddCmd.Flags().StringVar(&pluginsRuleGroup, "group", "", "the group it is in")
	pluginsRulesRemoveCmd.Flags().BoolVar(&pluginsClearGroup, "clear-group", false, "take it out of its group")
	pluginsRulesGroupAddCmd.Flags().StringArrayVar(&pluginsGroupAfter, "after", nil, "a group it loads after (repeatable)")

	pluginsRulesGroupCmd.AddCommand(pluginsRulesGroupAddCmd, pluginsRulesGroupRemoveCmd)
	pluginsRulesCmd.AddCommand(pluginsRulesListCmd, pluginsRulesAddCmd, pluginsRulesRemoveCmd, pluginsRulesGroupCmd)
	pluginsCmd.AddCommand(pluginsListCmd, pluginsEnableCmd, pluginsDisableCmd, pluginsMoveCmd, pluginsSortCmd, pluginsRulesCmd)
	rootCmd.AddCommand(pluginsCmd)
}

func doPluginsList(ctx context.Context, svc *core.Service, game *domain.Game) error {
	profile, err := resolveProfile(ctx, svc, game.ID, pluginsProfile)
	if err != nil {
		return err
	}
	report, err := svc.PluginOrder(ctx, game, profile)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(report)
	}
	fmt.Printf("Plugin order for %s (profile %s)\n", game.ID, report.Profile)
	renderPluginOrder(report)
	return nil
}

func doPluginRulesList(ctx context.Context, svc *core.Service, game *domain.Game) error {
	profile, err := resolveProfile(ctx, svc, game.ID, pluginsProfile)
	if err != nil {
		return err
	}
	report, err := svc.ProfilePluginRules(ctx, game, profile)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(report)
	}
	fmt.Printf("Plugin rules for %s (profile %s)\n", game.ID, report.Profile)
	renderPluginRules(report.Rules)
	return nil
}

func runPluginEdit(cmd *cobra.Command, edit core.PluginEdit) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doPluginEdit(ctx, svc, game, edit)
	})
}

func doPluginEdit(ctx context.Context, svc *core.Service, game *domain.Game, edit core.PluginEdit) error {
	profile, err := resolveProfile(ctx, svc, game.ID, pluginsProfile)
	if err != nil {
		return err
	}
	plan, err := svc.PlanPluginEdit(ctx, game, profile, edit)
	if err != nil {
		return err
	}
	if pluginsDryRun {
		if jsonOutput {
			return emitJSON(plan)
		}
		renderPluginEditPlan(plan)
		return nil
	}
	res, err := svc.ApplyPluginEdit(ctx, game, plan)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(res)
	}
	for _, c := range res.Changes {
		fmt.Printf("  ✓ %s\n", c)
	}
	for _, n := range res.Notes {
		fmt.Printf("Note: %s\n", n)
	}
	if !res.Changed {
		fmt.Println("No change to the profile.")
	}
	if res.List == nil {
		fmt.Printf("%s is not the active profile: recorded only; its Plugins.txt is written when it is deployed.\n", res.Profile)
		return nil
	}
	for _, f := range res.List.Findings {
		fmt.Fprintf(os.Stderr, "Warning: plugins: %s\n", f.Message)
	}
	renderPluginList(res.List)
	return nil
}

// renderPluginEditPlan prints a plugin edit's plan under a "(dry run)"
// header, with the order it would leave.
func renderPluginEditPlan(plan *core.PluginEditPlan) {
	fmt.Printf("Plugin edit for profile %s (dry run)\n\n", plan.Profile)
	if plan.NoChanges {
		fmt.Println("No change: the profile already says this.")
	}
	for _, c := range plan.Changes {
		fmt.Printf("  ~ %s\n", c)
	}
	for _, n := range plan.Notes {
		fmt.Printf("Note: %s\n", n)
	}
	fmt.Println()
	if plan.Active {
		fmt.Println("The game's Plugins.txt would be written in this order:")
	} else {
		fmt.Printf("%s is not the active profile: the change would be recorded only.\n", plan.Profile)
	}
	renderPluginOrder(plan.Order)
}

// pluginFlags is an entry's flags column: M master, L light, D medium.
func pluginFlags(e core.PluginOrderEntry) string {
	var b strings.Builder
	if e.Master {
		b.WriteString("M")
	}
	if e.Light {
		b.WriteString("L")
	}
	if e.Medium {
		b.WriteString("D")
	}
	return b.String()
}

// pluginPlacedBy is an entry's "placed by" column.
func pluginPlacedBy(e core.PluginOrderEntry) string {
	switch e.Placement {
	case core.PluginPlacementImplicit:
		return "game"
	case core.PluginPlacementExternal:
		return "external"
	case core.PluginPlacementRule, core.PluginPlacementMaster:
		parts := make([]string, 0, len(e.Constraints))
		for _, c := range e.Constraints {
			switch c.Reason {
			case core.PluginEdgeAfter:
				parts = append(parts, "rule: after "+c.From)
			case core.PluginEdgeReq:
				parts = append(parts, "rule: requires "+c.From)
			case core.PluginEdgeGroup:
				parts = append(parts, "rule: group "+c.Group)
			default:
				parts = append(parts, "master: "+c.From)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ", ")
		}
		return e.Placement
	}
	return e.Placement
}

// pluginMasters is an entry's masters column, a missing one marked.
func pluginMasters(e core.PluginOrderEntry) string {
	parts := make([]string, 0, len(e.Masters))
	for _, m := range e.Masters {
		missing := false
		for _, x := range e.MissingMasters {
			if strings.EqualFold(x, m) {
				missing = true
			}
		}
		if missing {
			parts = append(parts, colorRed(m+" (missing)"))
		} else {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, ", ")
}

// renderPluginOrder prints a PluginOrderReport: the order as a table, the
// order entries not deployed, the rule count and the findings.
func renderPluginOrder(report *core.PluginOrderReport) {
	if report.ListFile != "" {
		fmt.Printf("Plugins.txt: %s\n", report.ListFile)
	}
	if !report.Active {
		fmt.Printf("%s is not the active profile: its plugins are not deployed, so lmm cannot work out their effective order.\n", report.Profile)
	}
	if !report.Sorted {
		fmt.Println(colorRed("The rules form a cycle, so this is the profile's order with no rule applied; nothing is written until it is fixed."))
	}
	fmt.Println()
	if len(report.Plugins) == 0 && report.Active {
		fmt.Println("No plugins deployed.")
	}
	if len(report.Plugins) > 0 {
		w := newDisplayWidthTableWriter(os.Stdout)
		_, _ = fmt.Fprintln(w, "  #\tON\tPLUGIN\tFLAGS\tPLACED BY\tMASTERS\tMOD")
		for _, e := range report.Plugins {
			on := ""
			if e.Active {
				on = "*"
			}
			mod := e.ModName
			if mod == "" && e.ModID != "" {
				mod = domain.ModKey(e.SourceID, e.ModID)
			}
			_, _ = fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Position, on, e.Name, pluginFlags(e), pluginPlacedBy(e), pluginMasters(e), mod)
		}
		_ = w.Flush()
	}
	if len(report.NotDeployed) > 0 {
		fmt.Println()
		if report.Active {
			fmt.Println("In the profile's order, not deployed now (kept for when they are):")
		} else {
			fmt.Println("The profile's plugin order:")
		}
		for _, e := range report.NotDeployed {
			state := ""
			if !e.Enabled {
				state = " (disabled)"
			}
			fmt.Printf("  %s%s\n", e.Name, state)
		}
	}
	if n := len(report.Rules.Plugins) + len(report.Rules.Groups); n > 0 {
		fmt.Printf("\n%d rule(s) and group(s): lmm plugins rules list\n", n)
	}
	if len(report.Findings) > 0 {
		fmt.Println()
		for _, f := range report.Findings {
			label := colorYellow("warning")
			if f.Severity == core.PluginSeverityError {
				label = colorRed("error")
			}
			fmt.Printf("%s: %s\n", label, f.Message)
		}
	}
}

// renderPluginRules prints a profile's plugin rules.
func renderPluginRules(rules domain.PluginRules) {
	if rules.IsZero() {
		fmt.Println("\nNo rules. Add one with: lmm plugins rules add <plugin> --after <plugin>")
		return
	}
	if len(rules.Groups) > 0 {
		fmt.Println("\nGroups:")
		for _, g := range rules.Groups {
			line := "  " + g.Name
			if len(g.After) > 0 {
				line += " - after " + strings.Join(g.After, ", ")
			}
			fmt.Println(line)
		}
	}
	if len(rules.Plugins) > 0 {
		fmt.Println("\nPlugins:")
		for _, r := range rules.Plugins {
			var parts []string
			if r.Group != "" {
				parts = append(parts, "group "+r.Group)
			}
			if len(r.After) > 0 {
				parts = append(parts, "after "+strings.Join(r.After, ", "))
			}
			if len(r.Req) > 0 {
				parts = append(parts, "requires "+strings.Join(r.Req, ", "))
			}
			fmt.Printf("  %s - %s\n", r.Name, strings.Join(parts, "; "))
		}
	}
}
