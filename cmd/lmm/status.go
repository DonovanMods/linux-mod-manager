package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

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

	// Load config to check for default game
	cfg, _ := config.Load(service.ConfigDir())

	// Show summary of all games
	fmt.Println("Configured Games:")
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if verbose {
		fmt.Fprintln(w, "GAME\tID\tPATH\tLINK\tPROFILES\tMODS†")
		fmt.Fprintln(w, "----\t--\t----\t----\t--------\t-----")
	} else {
		fmt.Fprintln(w, "GAME\tPATH\tMODS†\tPROFILES")
		fmt.Fprintln(w, "----\t----\t-----\t--------")
	}

	lnk := linker.New(service.GetDefaultLinkMethod())
	pm := core.NewProfileManager(service.ConfigDir(), service.DB(), service.Cache(), lnk)

	var totalMods int
	for _, game := range games {
		profiles, _ := pm.List(game.ID)

		// Get mod count from active profile
		var modCount int
		if defaultProfile, err := pm.GetDefault(game.ID); err == nil {
			mods, _ := service.GetInstalledMods(game.ID, defaultProfile.Name)
			modCount = len(mods)
			totalMods += modCount
		}

		// Mark default game
		gameName := game.Name
		if cfg != nil && cfg.DefaultGame == game.ID {
			gameName += " (default)"
		}

		if verbose {
			linkMethod := service.GetGameLinkMethod(game)
			linkStr := linkMethod.String()
			if game.LinkMethodExplicit {
				linkStr += "*" // Indicate per-game override
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
				gameName,
				game.ID,
				truncate(game.InstallPath, 30),
				linkStr,
				len(profiles),
				modCount,
			)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\n",
				gameName,
				truncate(game.InstallPath, 40),
				modCount,
				len(profiles),
			)
		}
	}
	w.Flush()

	fmt.Println()
	if verbose {
		fmt.Println("* = per-game override")
	}
	fmt.Println("† = mods in active profile")

	fmt.Printf("\nTotal: %d game(s), %d mod(s) installed\n", len(games), totalMods)

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

	// Show link method
	linkMethod := service.GetGameLinkMethod(game)
	if game.LinkMethodExplicit {
		fmt.Printf("  Link Method: %s (per-game)\n", linkMethod)
	} else if verbose {
		fmt.Printf("  Link Method: %s (global default)\n", linkMethod)
	}

	// Show cache path
	cachePath := service.GetGameCachePath(game)
	if game.CachePath != "" {
		fmt.Printf("  Cache Path: %s (per-game)\n", game.CachePath)
	} else if verbose {
		fmt.Printf("  Cache Path: %s (global default)\n", cachePath)
	}

	// Show source mappings in verbose mode
	if verbose && len(game.SourceIDs) > 0 {
		fmt.Println("  Sources:")
		for source, sourceGameID := range game.SourceIDs {
			fmt.Printf("    %s: %s\n", source, sourceGameID)
		}
	}

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
