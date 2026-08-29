package main

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var conflictsProfile string

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

--json emits {game_id, profile, conflicts: [{path, owner, also_in,
load_order_winner, stale}]}, where owner, each also_in entry and
load_order_winner are {key, name} objects; load_order_winner and stale
carry the same information as the human output's "Winner:" line and its
stale suffix.

Note: File tracking requires mods to be installed/deployed with lmm version 0.9.0+.
Older mods may need to be redeployed to track their files.

Examples:
  lmm conflicts --game skyrim-se
  lmm conflicts --game skyrim-se --profile survival`,
	RunE: runConflicts,
}

func init() {
	conflictsCmd.Flags().StringVarP(&conflictsProfile, "profile", "p", "", "profile (default: active profile)")

	rootCmd.AddCommand(conflictsCmd)
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
	profileName, err := resolveProfile(svc, game.ID, conflictsProfile)
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

	fmt.Printf("Found %d conflicting file(s):\n\n", len(conflicts))

	for _, c := range conflicts {
		fmt.Printf("  %s\n", c.Path)
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

	return nil
}
