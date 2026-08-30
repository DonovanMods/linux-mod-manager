package main

import (
	"bytes"
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"
)

var listProfile string
var listProfiles bool

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
		return runListProfiles(ctx, cmd, service, game.ID, game.Name)
	}

	profileName, err := resolveProfile(ctx, service, game.ID, listProfile)
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
		return emitJSON(list)
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

// runListProfiles takes the caller's ctx rather than reading cmd.Context():
// cobra leaves that nil unless Execute/ExecuteC/SetContext has run, which is
// a live trap for any test that builds a bare &cobra.Command{} (task-18
// review, Minor 6). cmd stays for the flag/output plumbing only.
func runListProfiles(ctx context.Context, cmd *cobra.Command, service *core.Service, gameID, gameName string) error {
	pm := service.NewProfileManager()
	profiles, err := service.ListProfileNames(ctx, gameID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}
	names := profiles.Profiles

	if jsonOutput {
		return emitJSON(profiles)
	}

	if len(names) == 0 {
		fmt.Printf("No profiles for %s.\n", gameName)
		return nil
	}

	fmt.Printf("Profiles for %s (%s):\n", gameName, gameID)
	for _, name := range names {
		prof, err := pm.Get(ctx, gameID, name)
		if err == nil && prof.IsDefault {
			fmt.Printf("  %s (default)\n", name)
		} else {
			fmt.Printf("  %s\n", name)
		}
	}
	return nil
}
