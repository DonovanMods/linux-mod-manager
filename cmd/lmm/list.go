package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
)

var listProfile string

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed mods",
	Long: `List all mods installed in the specified game and profile.

Examples:
  lmm list --game skyrim-se
  lmm list --game skyrim-se --profile survival`,
	RunE: runList,
}

func init() {
	listCmd.Flags().StringVarP(&listProfile, "profile", "p", "", "profile to list (default: active profile)")

	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	profileName := profileOrDefault(listProfile)

	mods, err := service.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	if verbose {
		fmt.Printf("Installed mods in %s (profile: %s)\n", game.Name, profileName)
		// Show cache path if overridden
		if game.CachePath != "" {
			fmt.Printf("Cache: %s\n", game.CachePath)
		}
		fmt.Println()
	}

	if len(mods) == 0 {
		fmt.Println("No mods installed.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tVERSION\tSOURCE\tENABLED\tDEPLOYED\tMETHOD")
	fmt.Fprintln(w, "--\t----\t-------\t------\t-------\t--------\t------")

	for _, mod := range mods {
		enabled := "yes"
		if !mod.Enabled {
			enabled = "no"
		}
		deployed := "yes"
		if !mod.Deployed {
			deployed = "no"
		}
		sourceDisplay := mod.SourceID
		if mod.SourceID == domain.SourceLocal {
			sourceDisplay = "(local)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			mod.ID,
			truncate(mod.Name, 40),
			mod.Version,
			sourceDisplay,
			enabled,
			deployed,
			mod.LinkMethod.String(),
		)
	}
	w.Flush()

	if verbose {
		fmt.Printf("\nTotal: %d mod(s)\n", len(mods))
	}

	return nil
}
