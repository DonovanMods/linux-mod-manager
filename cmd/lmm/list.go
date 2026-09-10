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

A mod that is not enabled, or enabled but not yet deployed, is marked in
a STATE column; the column appears only when there is such a mod, and
-v/--verbose replaces it with the full ENABLED/DEPLOYED pair.

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
	external, disabled, offState := 0, 0, 0
	for _, m := range mods {
		if m.External {
			external++
		}
		if !m.Enabled {
			disabled++
		}
		if modStateLabel(m) != "" {
			offState++
		}
	}
	fmt.Printf("Installed mods in %s (profile: %s) — %d mod(s)", game.Name, profileName, len(mods))
	if external > 0 {
		// #269: said here rather than left to the reader to infer from the
		// EXTERNAL markers, so the count and its explanation arrive together.
		fmt.Printf(", %d tracked from Steam", external)
	}
	if disabled > 0 {
		// #397, same reasoning: the count above includes mods that are off,
		// so it says how many rather than leaving the reader to tally the
		// STATE column.
		fmt.Printf(", %d disabled", disabled)
	}
	fmt.Println()
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
	// #397: the default view was ID/NAME/VERSION/AUTHOR, so a disabled,
	// undeployed mod rendered identically to a live one. Row tinting
	// carried the state already, but colour is gone under --no-color, in a
	// pipe, and for anyone who cannot see it. The column follows the
	// EXTERNAL rule below - present only when there is something to say -
	// so a profile whose mods are all live keeps the shape it has always
	// had. Under --verbose the ENABLED/DEPLOYED columns say it in full.
	if !verbose && offState > 0 {
		header += "\tSTATE"
		sep += "\t-----"
	}
	// #269: the EXTERNAL column appears only when the profile actually has
	// such a mod, so every existing listing keeps the shape it has always
	// had.
	if external > 0 {
		header += "\tEXTERNAL"
		sep += "\t--------"
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
		// #269: the version shown for an external mod is its revision DATE,
		// not the 19-digit Steam content id its Version field carries - that
		// id is the item's version identity, and no human-facing surface
		// prints it as a version (the design's approval note).
		version := displayModVersion(mod.External, mod.Version, mod.UpdatedAt)
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
			// #269/#392: lmm never links or deploys an external mod - it
			// tracks the item where Steam put it, which `import --workshop`
			// says in its own preamble. METHOD reads "-", the same way
			// LOCKED and CONVERT already do for a column that does not apply
			// to a row, and DEPLOYED names who does have the files there.
			linkMethod := mod.LinkMethod.String()
			if mod.External {
				linkMethod = "-"
				deployed = "Steam"
			}
			sourceDisplay := mod.SourceID
			if mod.SourceID == domain.SourceLocal {
				sourceDisplay = "(local)"
			}
			locked := "-"
			if mod.Locked {
				// The same rule the VERSION column above follows, and for a
				// stronger reason: a lock target has no date to fall back on
				// (displayLockTarget), so an external row says only THAT it
				// is locked. Without this the two columns of one row
				// disagreed - a date on the left, the content id on the right.
				if target := displayLockTarget(mod.External, mod.LockedVersion); target != "" {
					locked = mod.LockedVersion
				} else {
					locked = "yes"
				}
			}
			convert := "-"
			if mod.ConvertPaks != nil {
				if *mod.ConvertPaks {
					convert = "on"
				} else {
					convert = "off"
				}
			}
			row = fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s", mod.ID, truncate(mod.Name, 40), version, truncate(author, 20), sourceDisplay, enabled, deployed, linkMethod, policyToString(mod.UpdatePolicy), locked, convert)
		} else {
			row = fmt.Sprintf("%s\t%s\t%s\t%s", mod.ID, truncate(mod.Name, 40), version, truncate(author, 20))
			if offState > 0 {
				state := modStateLabel(mod)
				if state == "" {
					state = "-"
				}
				row += "\t" + state
			}
		}
		if external > 0 {
			marker := "-"
			if mod.External {
				marker = "EXTERNAL"
			}
			row += "\t" + marker
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

// modStateLabel names a mod's state when it is NOT the ordinary one
// (enabled and deployed), and returns "" when it is (#397).
//
// Disabled wins over undeployed: a disabled mod is undeployed as a
// CONSEQUENCE, and naming the cause is what tells the reader which command
// to reach for.
func modStateLabel(mod core.ModListing) string {
	switch {
	case !mod.Enabled:
		return "disabled"
	case !mod.Deployed:
		return "not deployed"
	default:
		return ""
	}
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
