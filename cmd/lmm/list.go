package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
)

var listProfile string
var listProfiles bool

type listJSONOutput struct {
	GameID  string        `json:"game_id"`
	Profile string        `json:"profile"`
	Mods    []listModJSON `json:"mods"`
}

type listModJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	Enabled  bool   `json:"enabled"`
	Deployed bool   `json:"deployed"`
	Method   string `json:"link_method"`
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed mods",
	Long: `List all mods installed in the specified game and profile.

Use --profiles to list profile names for the game instead of mods.

Examples:
  lmm list --game skyrim-se
  lmm list --game skyrim-se --profile survival
  lmm list --game skyrim-se --profiles`,
	RunE: runList,
}

func init() {
	listCmd.Flags().StringVarP(&listProfile, "profile", "p", "", "profile to list (default: active profile)")
	listCmd.Flags().BoolVar(&listProfiles, "profiles", false, "list profile names for the game instead of mods")

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
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	if listProfiles {
		return runListProfiles(cmd, service, gameID, game.Name)
	}

	profileName := profileOrDefault(listProfile)

	mods, err := service.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	if jsonOutput {
		out := listJSONOutput{GameID: gameID, Profile: profileName, Mods: make([]listModJSON, len(mods))}
		for i, mod := range mods {
			sourceDisplay := mod.SourceID
			if mod.SourceID == domain.SourceLocal {
				sourceDisplay = "local"
			}
			out.Mods[i] = listModJSON{
				ID:       mod.ID,
				Name:     mod.Name,
				Version:  mod.Version,
				Source:   sourceDisplay,
				Enabled:  mod.Enabled,
				Deployed: mod.Deployed,
				Method:   mod.LinkMethod.String(),
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return nil
	}

	if len(mods) == 0 {
		fmt.Println("No mods installed.")
		return nil
	}

	// Always show total count (no longer requires --verbose)
	fmt.Printf("Installed mods in %s (profile: %s) — %d mod(s)\n", game.Name, profileName, len(mods))
	if verbose && game.CachePath != "" {
		fmt.Printf("Cache: %s\n", game.CachePath)
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tVERSION\tSOURCE\tENABLED\tDEPLOYED\tMETHOD"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "--\t----\t-------\t------\t-------\t--------\t------"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

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
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			mod.ID,
			truncate(mod.Name, 40),
			mod.Version,
			sourceDisplay,
			enabled,
			deployed,
			mod.LinkMethod.String(),
		); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	return nil
}

func runListProfiles(cmd *cobra.Command, service interface{ ConfigDir() string }, gameID, gameName string) error {
	names, err := config.ListProfiles(service.ConfigDir(), gameID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	if jsonOutput {
		type listProfilesJSON struct {
			GameID   string   `json:"game_id"`
			Profiles []string `json:"profiles"`
		}
		out := listProfilesJSON{GameID: gameID, Profiles: names}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return nil
	}

	if len(names) == 0 {
		fmt.Printf("No profiles for %s.\n", gameName)
		return nil
	}

	fmt.Printf("Profiles for %s (%s):\n", gameName, gameID)
	for _, name := range names {
		prof, err := config.LoadProfile(service.ConfigDir(), gameID, name)
		if err == nil && prof.IsDefault {
			fmt.Printf("  %s (default)\n", name)
		} else {
			fmt.Printf("  %s\n", name)
		}
	}
	return nil
}
