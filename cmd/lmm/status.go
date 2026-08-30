package main

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current status",
	Long: `Show the current status including configured games, active profiles, and mod counts.

Examples:
  lmm status
  lmm status --game skyrim-se`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doStatus(ctx, service)
	})
}

func doStatus(ctx context.Context, service *core.Service) error {
	games := service.ListGames()

	if len(games) == 0 {
		if jsonOutput {
			// Service.Status over zero games is a trivial call whose Games
			// slice is empty-but-non-nil, so the document stays {"games": []}
			// without this branch hand-building one.
			report, err := service.Status(ctx)
			if err != nil {
				return err
			}
			return emitJSON(report)
		}
		fmt.Println("No games configured.")
		fmt.Println("\nUse 'lmm game add' to add a game.")
		return nil
	}

	// If a specific game is requested, show details for that game
	if gameID != "" {
		if jsonOutput {
			return showGameStatusJSON(ctx, service, gameID)
		}
		return showGameStatus(ctx, service, gameID)
	}

	// core.Status owns the per-game join (default game, profile names,
	// active-profile mod count); this command only renders it. Built here
	// rather than above the zero-games check so the `--game <id>` detail
	// path above never pays for a summary it doesn't print.
	report, err := service.Status(ctx)
	if err != nil {
		return err
	}

	if jsonOutput {
		return emitJSON(report)
	}

	// Show summary of all games
	fmt.Println("Configured Games:")
	fmt.Println()

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)

	if verbose {
		if _, err := fmt.Fprintln(w, "GAME\tID\tPATH\tLINK\tPROFILES\tMODS†"); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
		if _, err := fmt.Fprintln(w, "----\t--\t----\t----\t--------\t-----"); err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}
	} else {
		if _, err := fmt.Fprintln(w, "GAME\tPATH\tMODS†\tPROFILES"); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
		if _, err := fmt.Fprintln(w, "----\t----\t-----\t--------"); err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}
	}

	var totalMods int
	for _, summary := range report.Games {
		game := summary.Game
		totalMods += summary.ModCount

		// Mark default game
		gameName := game.Name
		if summary.IsDefault {
			gameName += " (default)"
		}

		// The last column (whichever count it is - MODS† in verbose,
		// PROFILES otherwise) is safe to color inline: text/tabwriter never
		// pads after the final cell, so this cell's inflated byte length
		// can't misalign any column after it (see printTable's doc
		// comment; do not do this for an interior column).
		if verbose {
			linkStr := summary.LinkMethod.String()
			if game.LinkMethodExplicit {
				linkStr += "*" // Indicate per-game override
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
				gameName,
				game.ID,
				truncate(game.InstallPath, 30),
				linkStr,
				len(summary.Profiles),
				colorCyan(strconv.Itoa(summary.ModCount)),
			); err != nil {
				return fmt.Errorf("writing row: %w", err)
			}
		} else {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%s\n",
				gameName,
				truncate(game.InstallPath, 40),
				summary.ModCount,
				colorCyan(strconv.Itoa(len(summary.Profiles))),
			); err != nil {
				return fmt.Errorf("writing row: %w", err)
			}
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}
	if err := printTable(&buf, 2, nil); err != nil {
		return fmt.Errorf("writing table: %w", err)
	}

	fmt.Println()
	if verbose {
		fmt.Println("* = per-game override")
	}
	fmt.Println("† = mods in active profile")

	fmt.Printf("\nTotal: %d game(s), %d mod(s) installed\n", len(report.Games), totalMods)

	return nil
}

func showGameStatusJSON(ctx context.Context, service *core.Service, gameID string) error {
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}
	st, err := service.GameStatus(ctx, game)
	if err != nil {
		return err
	}
	return emitJSON(st)
}

func showGameStatus(ctx context.Context, service *core.Service, gameID string) error {
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// core.GameStatus owns the whole assembly - profile list, the three-level
	// link-method resolution (#155/#81), the active profile's counts, its
	// last deploy and its pak-conversion failures (#221) - so this command
	// only renders it.
	st, err := service.GameStatus(ctx, game)
	if err != nil {
		return err
	}

	fmt.Printf("Game: %s\n", st.Name)
	fmt.Printf("  ID: %s\n", st.ID)
	fmt.Printf("  Install Path: %s\n", st.InstallPath)
	fmt.Printf("  Mod Path: %s\n", st.ModPath)

	// Show the effective link method for the active profile (the game's
	// default profile - the one deploys target): per-profile > per-game >
	// global default (#81). The global-default line stays --verbose-only.
	switch {
	case st.LinkMethodSource == "profile":
		fmt.Printf("  Link Method: %s (per-profile)\n", colorCyan(st.EffectiveLinkMethod.String()))
	case st.LinkMethodSource == "game":
		fmt.Printf("  Link Method: %s (per-game)\n", colorCyan(st.LinkMethod.String()))
	case verbose:
		fmt.Printf("  Link Method: %s (global default)\n", colorCyan(st.LinkMethod.String()))
	}

	// Show cache path
	if st.CachePath != "" {
		fmt.Printf("  Cache Path: %s (per-game)\n", st.CachePath)
	} else if verbose {
		fmt.Printf("  Cache Path: %s (global default)\n", st.ResolvedCachePath)
	}

	// Show source mappings in verbose mode
	if verbose && len(st.SourceIDs) > 0 {
		fmt.Println("  Sources:")
		for source, sourceGameID := range st.SourceIDs {
			fmt.Printf("    %s: %s\n", source, sourceGameID)
		}
	}

	fmt.Println()

	if len(st.Profiles) == 0 {
		fmt.Println("No profiles configured.")
		return nil
	}

	fmt.Println("Profiles:")
	for _, p := range st.Profiles {
		defaultMark := ""
		if p.IsDefault {
			defaultMark = colorGreen(" (active)")
		}
		fmt.Printf("  - %s%s: %s mod(s)\n", p.Name, defaultMark, colorCyan(strconv.Itoa(p.ModCount)))
	}

	// Show installed mods count for active profile
	if st.ActiveProfile != "" {
		fmt.Printf("\nActive Profile: %s\n", colorGreen(st.ActiveProfile))
		fmt.Printf("  Installed Mods: %s\n", colorCyan(strconv.Itoa(st.InstalledModCount)))

		if st.InstalledModCount > 0 {
			// Disabled is a routine, expected state (not an error), so it's
			// dimmed rather than red - accent, not alarm.
			disabled := st.InstalledModCount - st.EnabledModCount
			fmt.Printf("  Enabled: %s, Disabled: %s\n", colorGreen(strconv.Itoa(st.EnabledModCount)), colorDim(strconv.Itoa(disabled)))
		}

		deployDisplay := formatLastDeploy(st.LastDeploy)
		if st.LastDeploy == nil {
			// "Never deployed" is a routine, expected state for a freshly
			// added game - dimmed rather than red, same convention as a
			// disabled mod (accent, not alarm).
			deployDisplay = colorDim(deployDisplay)
		} else {
			deployDisplay = colorGreen(deployDisplay)
		}
		fmt.Printf("  Last Deploy: %s\n", deployDisplay)

		// #221 design §5: surface pak-conversion failures here too, not just
		// 'lmm verify' - only when there's actually something to report.
		if st.ConversionFailures > 0 {
			fmt.Printf("  pak conversion failures: %d (see 'lmm verify')\n", st.ConversionFailures)
		}
	}

	return nil
}

// formatLastDeploy renders a game's last-deploy timestamp for the CLI's
// plain-text status output: nil (never deployed) is "never"; otherwise an
// absolute, local "YYYY-MM-DD HH:MM" timestamp. Deliberately NOT a
// relative-age rendering - the CLI's output is scriptable/parseable, so a
// stable absolute format that doesn't change between two invocations a
// second apart is preferable to a coarse "3m ago" that would.
func formatLastDeploy(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}
