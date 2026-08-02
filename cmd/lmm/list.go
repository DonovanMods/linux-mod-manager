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
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed mods",
	Long: `List all mods installed in the specified game and profile.

Mods are printed in the profile's load order (see 'lmm profile reorder')
- the same order that decides merge precedence for a compiled/merged pak:
a mod later in the load order is merged later and wins conflicting rows.
A mod installed but missing from the load order is still shown (never
silently dropped), placed first since it has no claim to the final say.

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
		return doList(cmd, service, game)
	})
}

func doList(cmd *cobra.Command, service *core.Service, game *domain.Game) error {
	if listProfiles {
		return runListProfiles(cmd, service, game.ID, game.Name)
	}

	profileName, err := resolveProfile(service, game.ID, listProfile)
	if err != nil {
		return err
	}

	mods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	// #97: lock state lives on the profile YAML ref, not the DB row
	// GetInstalledMods above already returned - load it separately and key
	// by domain.ModKey for O(1) lookup per mod below (a precomputed map,
	// not per-row FindRef, since this loops over every mod). Tolerant of a
	// missing/unreadable profile.yaml (mirrors coreProvider.Overview's own
	// "_ =" shape, internal/tui/service_core.go): an unreadable profile just
	// means nothing shows as locked, not a failed listing.
	profileYAML, _ := config.LoadProfile(service.ConfigDir(), game.ID, profileName)
	lockedByKey := map[string]domain.ModReference{}
	if profileYAML != nil {
		for _, ref := range profileYAML.Mods {
			if ref.Locked {
				lockedByKey[domain.ModKey(ref.SourceID, ref.ModID)] = ref
			}
		}
	}

	// #201: display the profile's load order - the order that actually
	// decides merge precedence (later = merged later = wins) - not
	// installed_at (GetInstalledMods' own DB order), which has no
	// relationship to it. core.OrderByProfile, not the deploy-only
	// GetInstalledModsInProfileOrder seam: that one deliberately OMITS a
	// mod absent from the profile's load order (correct for deploy - an
	// untracked mod must never silently deploy), which would make such a
	// mod vanish from a listing instead of just being placed first (lowest
	// priority, since it has no claim to "final say"). OrderByProfile is
	// the same never-omitting seam the TUI's mod list already uses
	// (internal/tui/service_core.go's Overview) - reusing it here keeps the
	// CLI and TUI in agreement on what "the load order" looks like.
	mods = core.OrderByProfile(profileYAML, mods)

	if jsonOutput {
		out := listJSONOutput{GameID: game.ID, Profile: profileName, Mods: make([]listModJSON, len(mods))}
		for i, mod := range mods {
			sourceDisplay := mod.SourceID
			if mod.SourceID == domain.SourceLocal {
				sourceDisplay = "local"
			}
			lockedRef, locked := lockedByKey[domain.ModKey(mod.SourceID, mod.ID)]
			lockedVersion := ""
			if locked {
				lockedVersion = lockedRef.Version
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
				Locked:        locked,
				LockedVersion: lockedVersion,
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
		header = "ID\tNAME\tVERSION\tAUTHOR\tSOURCE\tENABLED\tDEPLOYED\tMETHOD\tPOLICY\tLOCKED"
		sep = "--\t----\t-------\t------\t------\t-------\t--------\t------\t------\t------"
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
			if lockedRef, ok := lockedByKey[domain.ModKey(mod.SourceID, mod.ID)]; ok {
				locked = lockedRef.Version
			}
			row = fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s", mod.ID, truncate(mod.Name, 40), mod.Version, truncate(author, 20), sourceDisplay, enabled, deployed, mod.LinkMethod.String(), policyToString(mod.UpdatePolicy), locked)
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
