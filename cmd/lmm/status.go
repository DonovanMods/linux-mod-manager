package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(statusJSONOutput{Games: []statusGameJSON{}}); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
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
	report := service.Status(ctx)

	if jsonOutput {
		return outputStatusJSON(report)
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

type statusJSONOutput struct {
	Games []statusGameJSON `json:"games"`
}

type statusGameJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	InstallPath string   `json:"install_path"`
	ModPath     string   `json:"mod_path"`
	LinkMethod  string   `json:"link_method"`
	Profiles    []string `json:"profiles"`
	ModCount    int      `json:"mod_count"`
	IsDefault   bool     `json:"is_default,omitempty"`
}

func outputStatusJSON(report *core.StatusReport) error {
	out := statusJSONOutput{Games: make([]statusGameJSON, 0, len(report.Games))}
	for _, summary := range report.Games {
		out.Games = append(out.Games, statusGameJSON{
			ID:          summary.Game.ID,
			Name:        summary.Game.Name,
			InstallPath: summary.Game.InstallPath,
			ModPath:     summary.Game.ModPath,
			LinkMethod:  summary.LinkMethod.String(),
			Profiles:    summary.Profiles,
			ModCount:    summary.ModCount,
			IsDefault:   summary.IsDefault,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
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
	profileList := make([]statusProfileJSON, len(st.Profiles))
	for i, p := range st.Profiles {
		profileList[i] = statusProfileJSON{Name: p.Name, ModCount: p.ModCount, IsDefault: p.IsDefault}
	}
	out := statusGameDetailJSON{
		ID:                  st.Game.ID,
		Name:                st.Game.Name,
		InstallPath:         st.Game.InstallPath,
		ModPath:             st.Game.ModPath,
		LinkMethod:          st.LinkMethod.String(),
		EffectiveLinkMethod: st.EffectiveLinkMethod.String(),
		LinkMethodSource:    st.LinkMethodSource,
		Profiles:            profileList,
		CachePath:           st.CachePath,
		ActiveProfile:       st.ActiveProfile,
		InstalledModCount:   st.InstalledModCount,
		EnabledModCount:     st.EnabledModCount,
		LastDeploy:          st.LastDeploy,
		ConversionFailures:  st.ConversionFailures,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

type statusGameDetailJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	InstallPath string `json:"install_path"`
	ModPath     string `json:"mod_path"`
	// LinkMethod is the GAME-level resolution (game-explicit or global
	// default) - deliberately NOT the profile-effective method, for JSON
	// contract stability (#155): consumers written before per-profile
	// overrides existed read this key as the game's setting, so its meaning
	// stays put and the profile-aware fields below are additive instead.
	LinkMethod string `json:"link_method"`
	// EffectiveLinkMethod is what a deploy into the active profile actually
	// uses (profile > game > global) - the JSON twin of the text output's
	// Link Method line. LinkMethodSource says which level won: "profile",
	// "game", or "global". Both are always present; with no profile
	// override they equal LinkMethod and its level.
	EffectiveLinkMethod string              `json:"effective_link_method"`
	LinkMethodSource    string              `json:"link_method_source"`
	CachePath           string              `json:"cache_path"`
	Profiles            []statusProfileJSON `json:"profiles"`
	ActiveProfile       string              `json:"active_profile,omitempty"`
	InstalledModCount   int                 `json:"installed_mod_count,omitempty"`
	EnabledModCount     int                 `json:"enabled_mod_count,omitempty"`
	// LastDeploy is nil for a profile that has never been deployed. Kept
	// omitempty (task-4-brief.md / lmm-repo-conventions' JSON-contract-
	// additions-are-MINOR precedent): an unset field, not a null or zero
	// time, is what makes this an additive change existing consumers can
	// ignore entirely.
	LastDeploy *time.Time `json:"last_deploy,omitempty"`
	// ConversionFailures is the active profile's count of pak-conversion
	// failures (#221 design §5) - mods whose prebuilt .pak could not be
	// converted into the merged pak on the last sync and stay raw-deployed
	// instead ('lmm verify' reports each one by name). Zero/omitted for a
	// non-DeployCompile game or a profile with none.
	ConversionFailures int `json:"conversion_failures,omitempty"`
}

type statusProfileJSON struct {
	Name      string `json:"name"`
	ModCount  int    `json:"mod_count"`
	IsDefault bool   `json:"is_default"`
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

	fmt.Printf("Game: %s\n", st.Game.Name)
	fmt.Printf("  ID: %s\n", st.Game.ID)
	fmt.Printf("  Install Path: %s\n", st.Game.InstallPath)
	fmt.Printf("  Mod Path: %s\n", st.Game.ModPath)

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
	if st.Game.CachePath != "" {
		fmt.Printf("  Cache Path: %s (per-game)\n", st.Game.CachePath)
	} else if verbose {
		fmt.Printf("  Cache Path: %s (global default)\n", st.CachePath)
	}

	// Show source mappings in verbose mode
	if verbose && len(st.Game.SourceIDs) > 0 {
		fmt.Println("  Sources:")
		for source, sourceGameID := range st.Game.SourceIDs {
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
