package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"lmm/internal/core"
	"lmm/internal/linker"

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
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	games := service.ListGames()

	if len(games) == 0 {
		fmt.Println("No games configured.")
		fmt.Println("\nUse 'lmm game add' to add a game.")
		return nil
	}

	// If a specific game is requested, show details for that game
	if gameID != "" {
		return showGameStatus(service, gameID)
	}

	// Otherwise show summary of all games
	fmt.Println("Configured Games:")
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GAME\tPATH\tPROFILES")
	fmt.Fprintln(w, "----\t----\t--------")

	lnk := linker.New(service.GetDefaultLinkMethod())
	pm := core.NewProfileManager(service.ConfigDir(), service.DB(), service.Cache(), lnk)

	for _, game := range games {
		profiles, _ := pm.List(game.ID)
		fmt.Fprintf(w, "%s\t%s\t%d\n",
			game.Name,
			truncate(game.InstallPath, 40),
			len(profiles),
		)
	}
	w.Flush()

	fmt.Printf("\nTotal: %d game(s) configured\n", len(games))

	return nil
}

func showGameStatus(service *core.Service, gameID string) error {
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	lnk := linker.New(service.GetDefaultLinkMethod())
	pm := core.NewProfileManager(service.ConfigDir(), service.DB(), service.Cache(), lnk)

	fmt.Printf("Game: %s\n", game.Name)
	fmt.Printf("  ID: %s\n", game.ID)
	fmt.Printf("  Install Path: %s\n", game.InstallPath)
	fmt.Printf("  Mod Path: %s\n", game.ModPath)
	fmt.Println()

	// Get profiles
	profiles, err := pm.List(gameID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles configured.")
		return nil
	}

	fmt.Println("Profiles:")
	for _, p := range profiles {
		defaultMark := ""
		if p.IsDefault {
			defaultMark = " (active)"
		}
		fmt.Printf("  - %s%s: %d mod(s)\n", p.Name, defaultMark, len(p.Mods))
	}

	// Show installed mods count for active profile
	defaultProfile, err := pm.GetDefault(gameID)
	if err == nil {
		mods, _ := service.GetInstalledMods(gameID, defaultProfile.Name)
		fmt.Printf("\nActive Profile: %s\n", defaultProfile.Name)
		fmt.Printf("  Installed Mods: %d\n", len(mods))

		// Count enabled vs disabled
		var enabled, disabled int
		for _, m := range mods {
			if m.Enabled {
				enabled++
			} else {
				disabled++
			}
		}
		if len(mods) > 0 {
			fmt.Printf("  Enabled: %d, Disabled: %d\n", enabled, disabled)
		}
	}

	return nil
}
