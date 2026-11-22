package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	conflictsProfile    string
	conflictsAll        bool
	conflictsAllCurrent bool
)

var conflictsCmd = &cobra.Command{
	Use:   "conflicts",
	Short: "Show all file conflicts in the current profile",
	Long: `Display all file conflicts in the current profile.

A conflict occurs when multiple mods want to deploy the same file path.
The mod listed as "owner" is the one whose file is currently deployed;
the "winner" is the mod that would own the file after a fresh deploy,
per the profile's load order (later mods override earlier ones). When
the two disagree, the "Winner:" line is suffixed "(stale — redeploy to
apply)" - the deployed file is out of date with the current load order
until you redeploy.

A conflict you intend - the winner is meant to override the other mods'
files - can be marked known with "lmm conflicts ack <winner> <loser>"
(see "lmm conflicts ack --help"). Known conflicts are folded into a count;
--all lists them too. A collection profile marks the conflicts between
its own mods known when it is imported.

--json emits {game_id, profile, conflicts: [{path, owner, also_in,
load_order_winner, stale, known}]}, where owner, each also_in entry and
load_order_winner are {key, name} objects; load_order_winner and stale
carry the same information as the human output's "Winner:" line and its
stale suffix, and known says the conflict is marked as intended. --json
always lists every conflict, known or not.

Note: File tracking requires mods to be installed/deployed with lmm version 0.9.0+.
Older mods may need to be redeployed to track their files.

Examples:
  lmm conflicts --game skyrim-se
  lmm conflicts --game skyrim-se --profile survival
  lmm conflicts --game skyrim-se --all`,
	Args: cobra.NoArgs,
	RunE: runConflicts,
}

var conflictsAckCmd = &cobra.Command{
	Use:   "ack [<winner> <loser>]",
	Short: "Mark a mod pair's conflict as known (intended)",
	Long: `Mark the conflict between two mods as known: <winner>'s files are meant
to override <loser>'s, on every path the two share - including one they
come to share after an update. Each mod is "source:modid", or a bare mod ID
when only one mod in the profile has it.

The mark records the winner. If a reorder makes <loser> win again, the
conflict is reported again, because what was intended no longer holds.
Marking the pair the other way round replaces the old mark.

--all-current marks every conflict "lmm conflicts" lists now, as it
stands: each file's winner over every other mod that has it.

--json emits {game_id, profile, added, removed, known_conflicts}, each a
list of {winner, loser, origin} (origin "user" or "collection").

Examples:
  lmm conflicts ack 4521 1187 --game cyberpunk2077
  lmm conflicts ack nexusmods:4521 nexusmods:1187 --game cyberpunk2077
  lmm conflicts ack --all-current --game cyberpunk2077`,
	RunE: runConflictsAck,
}

var conflictsUnackCmd = &cobra.Command{
	Use:   "unack <winner> <loser>",
	Short: "Unmark a mod pair's known conflict",
	Long: `Remove the known mark from the conflict between two mods, so it is
reported again. The two mods can be named either way round; a pair a
collection marked is unmarked too, and stays unmarked when the collection
is updated.

--json emits the same document as "lmm conflicts ack --json".

Examples:
  lmm conflicts unack 4521 1187 --game cyberpunk2077`,
	Args: cobra.ExactArgs(2),
	RunE: runConflictsUnack,
}

func init() {
	conflictsCmd.PersistentFlags().StringVarP(&conflictsProfile, "profile", "p", "", "profile (default: active profile)")
	conflictsCmd.Flags().BoolVar(&conflictsAll, "all", false, "list known (intended) conflicts too")
	conflictsAckCmd.Flags().BoolVar(&conflictsAllCurrent, "all-current", false, "mark every conflict listed now as known, as it stands")

	conflictsCmd.AddCommand(conflictsAckCmd, conflictsUnackCmd)
	rootCmd.AddCommand(conflictsCmd)
}

func runConflictsAck(cmd *cobra.Command, args []string) error {
	switch {
	case conflictsAllCurrent && len(args) > 0:
		return errors.New("--all-current marks every present conflict: name no mods with it")
	case !conflictsAllCurrent && len(args) != 2:
		return errors.New("name the two mods - <winner> <loser> - or use --all-current")
	}
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doConflictsAck(ctx, svc, game, args)
	})
}

// doConflictsAck marks args' pair known, or with --all-current every
// present conflict, and renders what it did.
func doConflictsAck(ctx context.Context, svc *core.Service, game *domain.Game, args []string) error {
	profileName, err := resolveProfile(ctx, svc, game.ID, conflictsProfile)
	if err != nil {
		return err
	}
	var result *core.KnownConflictsResult
	if conflictsAllCurrent {
		result, err = svc.MarkCurrentConflictsKnown(ctx, game, profileName)
	} else {
		result, err = svc.MarkConflictsKnown(ctx, game, profileName, []core.ConflictPair{{Winner: args[0], Loser: args[1]}})
	}
	if err != nil {
		return err
	}
	if err := renderKnownConflictsResult(result, "ack", args); err != nil || jsonOutput || conflictsAllCurrent {
		return err
	}
	return noteStillContested(ctx, svc, game, profileName, result.Added)
}

// noteStillContested says when a pair just marked leaves files listed: a
// file a third mod also has is known only once its winner is marked over
// every other mod that has it (#668 review).
func noteStillContested(ctx context.Context, svc *core.Service, game *domain.Game, profileName string, added domain.KnownConflicts) error {
	if len(added) == 0 {
		return nil
	}
	conflicts, err := svc.GetProfileConflicts(ctx, game, profileName)
	if err != nil {
		return nil // the mark is made; this note is only a hint
	}
	for _, e := range added {
		n := 0
		for _, c := range conflicts {
			if c.Known || c.LoadOrderWinner.Key != e.Winner {
				continue
			}
			for _, m := range append([]core.ConflictModRef{c.Owner}, c.AlsoIn...) {
				if m.Key == e.Loser {
					n++
					break
				}
			}
		}
		if n > 0 {
			fmt.Printf("%d file(s) %s and %s share are still listed: another mod has them too. Mark %s over it as well, or accept every conflict as it stands with `lmm conflicts ack --all-current`.\n",
				n, e.Winner, e.Loser, e.Winner)
		}
	}
	return nil
}

func runConflictsUnack(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doConflictsUnack(ctx, svc, game, args)
	})
}

// doConflictsUnack unmarks args' pair and renders what it did.
func doConflictsUnack(ctx context.Context, svc *core.Service, game *domain.Game, args []string) error {
	profileName, err := resolveProfile(ctx, svc, game.ID, conflictsProfile)
	if err != nil {
		return err
	}
	result, err := svc.UnmarkConflictsKnown(ctx, game, profileName, []core.ConflictPair{{Winner: args[0], Loser: args[1]}})
	if err != nil {
		return err
	}
	return renderKnownConflictsResult(result, "unack", args)
}

// renderKnownConflictsResult prints what an ack or unack did: a line per
// entry it added (naming the opposite mark it replaced) or removed, or a
// line saying it changed nothing.
func renderKnownConflictsResult(result *core.KnownConflictsResult, verb string, args []string) error {
	if jsonOutput {
		return emitJSON(result)
	}
	if len(result.Added) == 0 && len(result.Removed) == 0 {
		switch {
		case verb == "unack":
			fmt.Printf("Nothing to unmark: no known conflict between %s and %s.\n", args[0], args[1])
		case len(args) == 2:
			fmt.Printf("Already known: %s overrides %s.\n", args[0], args[1])
		default:
			fmt.Println("Nothing to mark: no conflict is unresolved.")
		}
		return nil
	}
	replaced := map[string]bool{}
	for _, e := range result.Added {
		if result.Removed.Has(e.Loser, e.Winner) {
			replaced[e.Loser+"\x00"+e.Winner] = true
		}
	}
	for _, e := range result.Removed {
		if !replaced[e.Winner+"\x00"+e.Loser] {
			fmt.Printf("Unmarked: %s overrides %s\n", e.Winner, e.Loser)
		}
	}
	for _, e := range result.Added {
		if replaced[e.Loser+"\x00"+e.Winner] {
			fmt.Printf("Marked as known: %s overrides %s (it was marked the other way round)\n", e.Winner, e.Loser)
			continue
		}
		fmt.Printf("Marked as known: %s overrides %s\n", e.Winner, e.Loser)
	}
	return nil
}

func runConflicts(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doConflicts(ctx, svc, game)
	})
}

// doConflicts renders core's GetProfileConflicts for the resolved profile.
// The empty-mods vs no-conflicts distinction is presentation-only, so the
// installed-mod count check stays here rather than in core.
func doConflicts(ctx context.Context, svc *core.Service, game *domain.Game) error {
	profileName, err := resolveProfile(ctx, svc, game.ID, conflictsProfile)
	if err != nil {
		return err
	}

	mods, err := svc.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// The report is the whole --json document in all three branches below;
	// a nil Conflicts slice encodes as [], so "no installed mods" and "no
	// conflicts found" emit the same empty document they always did while
	// the text branches keep their two distinct sentences.
	report := &core.ConflictReport{GameID: game.ID, Profile: profileName}

	if len(mods) == 0 {
		if jsonOutput {
			return emitJSON(report)
		}
		fmt.Println("No installed mods.")
		return nil
	}

	conflicts, err := svc.GetProfileConflicts(ctx, game, profileName)
	if err != nil {
		// #455: a refused adapter is reported as itself - its sentence
		// already names the game and the fix, and --json carries the game
		// and the adapter - the same answer GET /api/v1/conflicts gives.
		var refused *core.AdapterRefusedError
		if errors.As(err, &refused) {
			return err
		}
		return fmt.Errorf("getting conflicts: %w", err)
	}
	report.Conflicts = conflicts

	if len(conflicts) == 0 {
		if jsonOutput {
			return emitJSON(report)
		}
		fmt.Println("No conflicts found.")
		return nil
	}

	if jsonOutput {
		return emitJSON(report)
	}

	// #668: the known (intended) conflicts are folded into a count unless
	// --all asks for them; the unresolved ones come first either way.
	var unresolved, known []core.ProfileConflict
	for _, c := range conflicts {
		if c.Known {
			known = append(known, c)
		} else {
			unresolved = append(unresolved, c)
		}
	}
	if len(unresolved) > 0 {
		fmt.Printf("Found %d conflicting file(s):\n\n", len(unresolved))
		printConflicts(unresolved)
	} else {
		fmt.Println("No unresolved conflicts.")
	}
	switch {
	case len(known) == 0:
	case conflictsAll:
		if len(unresolved) == 0 {
			fmt.Println()
		}
		fmt.Printf("Known conflicts (%d), marked as intended:\n\n", len(known))
		printConflicts(known)
	case len(known) == 1:
		fmt.Println("1 known conflict not shown (--all lists them).")
	default:
		fmt.Printf("%d known conflicts not shown (--all lists them).\n", len(known))
	}

	return nil
}

// printConflicts prints each conflict's block: its path, owner, the other
// providers, and the load-order winner.
func printConflicts(conflicts []core.ProfileConflict) {
	for _, c := range conflicts {
		fmt.Printf("  %s\n", domain.DisplayDeployPath(c.Root, c.Path))
		fmt.Printf("    Owner: %s\n", c.Owner.Name)
		fmt.Printf("    Also in: ")
		for i, m := range c.AlsoIn {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(m.Name)
		}
		fmt.Println()
		winner := c.LoadOrderWinner.Name
		if c.Stale {
			winner += " " + colorYellow("(stale — redeploy to apply)")
		}
		fmt.Printf("    Winner: %s\n", winner)
		fmt.Println()
	}
}
