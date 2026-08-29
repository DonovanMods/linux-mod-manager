package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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
	// UpdatePolicy is "notify", "auto", or "pinned". Emitted unconditionally,
	// not gated on --verbose: a JSON consumer has no other way to see that a
	// mod is held back from updates.
	UpdatePolicy string `json:"update_policy"`
	// Locked/LockedVersion (#97) are additive fields (JSON-contract-
	// additions-are-MINOR precedent): Locked is emitted unconditionally like
	// UpdatePolicy above, LockedVersion only when actually locked so an
	// unlocked mod's JSON row doesn't carry a stray empty string.
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
	// ConvertPaks (#221) is an additive field (JSON-contract-additions-are-MINOR
	// precedent): present only for merge-compile games.
	ConvertPaks *bool `json:"convert_paks,omitempty"`
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed mods",
	Long: `List all mods installed in the specified game and profile.

Mods are printed in the profile's load order (see 'lmm profile reorder')
- the same order that decides merge precedence for a compiled/merged pak:
a mod later in the load order is merged later and wins conflicting
fields on a shared data-table row (untouched fields from earlier mods
still survive). A mod installed but missing from the load order is
still shown (never silently dropped), placed first since it has no
claim to the final say.

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
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doList(ctx, cmd, service, game)
	})
}

func doList(ctx context.Context, cmd *cobra.Command, service *core.Service, game *domain.Game) error {
	if listProfiles {
		return runListProfiles(cmd, service, game.ID, game.Name)
	}

	profileName, err := resolveProfile(service, game.ID, listProfile)
	if err != nil {
		return err
	}

	// core.ListMods owns the whole join - the DB rows, the profile YAML's
	// lock state (#97), the load order (#201, OrderByProfile: a mod absent
	// from the order is placed first, never dropped) and per-mod pak-
	// conversion applicability (#221) - so this command only renders it.
	list, err := service.ListMods(ctx, game, profileName)
	if err != nil {
		return err
	}
	mods := list.Mods

	if jsonOutput {
		out := listJSONOutput{GameID: list.GameID, Profile: list.Profile, Mods: make([]listModJSON, len(mods))}
		for i, mod := range mods {
			sourceDisplay := mod.SourceID
			if mod.SourceID == domain.SourceLocal {
				sourceDisplay = "local"
			}
			out.Mods[i] = listModJSON{
				ID:            mod.ID,
				Name:          mod.Name,
				Version:       mod.Version,
				Source:        sourceDisplay,
				Enabled:       mod.Enabled,
				Deployed:      mod.Deployed,
				Method:        mod.LinkMethod.String(),
				UpdatePolicy:  policyToString(mod.UpdatePolicy),
				Locked:        mod.Locked,
				LockedVersion: mod.LockedVersion,
				ConvertPaks:   mod.ConvertPaks,
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

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	header := "ID\tNAME\tVERSION\tAUTHOR"
	sep := "--\t----\t-------\t------"
	if verbose {
		header = "ID\tNAME\tVERSION\tAUTHOR\tSOURCE\tENABLED\tDEPLOYED\tMETHOD\tPOLICY\tLOCKED\tCONVERT"
		sep = "--\t----\t-------\t------\t------\t-------\t--------\t------\t------\t------\t-------"
	}
	if _, err := fmt.Fprintln(w, header); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, sep); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	for _, mod := range mods {
		author := mod.Author
		if author == "" {
			author = "-"
		}
		var row string
		if verbose {
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
			locked := "-"
			if mod.Locked {
				locked = mod.LockedVersion
			}
			convert := "-"
			if mod.ConvertPaks != nil {
				if *mod.ConvertPaks {
					convert = "on"
				} else {
					convert = "off"
				}
			}
			row = fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s", mod.ID, truncate(mod.Name, 40), mod.Version, truncate(author, 20), sourceDisplay, enabled, deployed, mod.LinkMethod.String(), policyToString(mod.UpdatePolicy), locked, convert)
		} else {
			row = fmt.Sprintf("%s\t%s\t%s\t%s", mod.ID, truncate(mod.Name, 40), mod.Version, truncate(author, 20))
		}
		if _, err := fmt.Fprintln(w, row); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	// Row tinting reflects each mod's actual enabled/deployed state
	// regardless of --verbose: the non-verbose table doesn't SHOW the
	// ENABLED/DEPLOYED columns, but the mod's health is exactly as real
	// there as in the verbose table, and a user comparing `lmm list` against
	// `lmm list -v` should see the identical color for the identical mod
	// (#193 round 2 - the row-tint decision had only ever been wired up on
	// the verbose branch, so plain `lmm list` stayed uncolored while `-v`
	// wasn't).
	rowColor := func(i int) func(string) string {
		if i < 0 || i >= len(mods) {
			return nil
		}
		return modRowColor(mods[i].Enabled, mods[i].Deployed)
	}
	if err := printTable(&buf, 2, rowColor); err != nil {
		return fmt.Errorf("writing table: %w", err)
	}

	return nil
}

func runListProfiles(cmd *cobra.Command, service *core.Service, gameID, gameName string) error {
	pm := service.NewProfileManager()
	names, err := pm.ListNames(gameID)
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
		prof, err := pm.Get(gameID, name)
		if err == nil && prof.IsDefault {
			fmt.Printf("  %s (default)\n", name)
		} else {
			fmt.Printf("  %s\n", name)
		}
	}
	return nil
}
