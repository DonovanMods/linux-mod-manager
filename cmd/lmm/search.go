package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	searchSource   string
	searchLimit    int
	searchProfile  string
	searchCategory string
	searchTags     []string
)

type searchJSONOutput struct {
	GameID string          `json:"game_id"`
	Query  string          `json:"query"`
	Mods   []searchModJSON `json:"mods"`
}

type searchModJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Author    string `json:"author"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for mods",
	Long: `Search for mods in the configured sources.

If --source is not specified, uses the first configured source for the game.

Examples:
  lmm search skyui --game skyrim-se
  lmm search "immersive armor" --game skyrim-se --source nexusmods`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSearch,
}

func init() {
	searchCmd.Flags().StringVarP(&searchSource, "source", "s", "", "mod source to search (default: first configured source for game)")
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 10, "maximum number of results")
	searchCmd.Flags().StringVarP(&searchProfile, "profile", "p", "", "profile to check for installed mods (default: active profile)")
	searchCmd.Flags().StringVar(&searchCategory, "category", "", "filter by category (source-specific ID or name)")
	searchCmd.Flags().StringSliceVar(&searchTags, "tag", nil, "filter by tag (repeatable; source-specific)")

	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	query := args[0]
	if len(args) > 1 {
		// Join multiple args as single query
		for _, arg := range args[1:] {
			query += " " + arg
		}
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

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	// Determine source: use flag if set, otherwise first configured source
	sourceToUse, err := resolveSource(game, searchSource, false)
	if err != nil {
		return err
	}

	if verbose {
		fmt.Printf("Searching for \"%s\" in %s (%s)...\n", query, game.Name, sourceToUse)
	}

	ctx := context.Background()
	mods, err := service.SearchMods(ctx, sourceToUse, gameID, query, searchCategory, searchTags)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return fmt.Errorf("authentication required; run 'lmm auth login %s' to authenticate", sourceToUse)
		}
		return fmt.Errorf("search failed: %w", err)
	}

	if len(mods) == 0 {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(searchJSONOutput{GameID: gameID, Query: query, Mods: []searchModJSON{}}); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
		}
		fmt.Println("No mods found.")
		return nil
	}

	// Get installed mods to mark already-installed ones
	profileName := profileOrDefault(searchProfile)
	installedMods, _ := service.GetInstalledMods(gameID, profileName)
	installedIDs := make(map[string]bool)
	for _, im := range installedMods {
		if im.SourceID == sourceToUse {
			installedIDs[im.ID] = true
		}
	}

	// Capture total count before limiting for "Showing X of Y"
	totalResults := len(mods)
	if len(mods) > searchLimit {
		mods = mods[:searchLimit]
	}

	if jsonOutput {
		out := searchJSONOutput{GameID: gameID, Query: query, Mods: make([]searchModJSON, len(mods))}
		for i, mod := range mods {
			out.Mods[i] = searchModJSON{
				ID:        mod.ID,
				Name:      mod.Name,
				Author:    mod.Author,
				Version:   mod.Version,
				Installed: installedIDs[mod.ID],
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return nil
	}

	// Print results
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tAUTHOR\tVERSION\t"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "--\t----\t------\t-------\t"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	for _, mod := range mods {
		installedMark := ""
		if installedIDs[mod.ID] {
			installedMark = "[installed]"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			mod.ID,
			truncate(mod.Name, 40),
			truncate(mod.Author, 20),
			mod.Version,
			installedMark,
		); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	if verbose {
		fmt.Printf("\nShowing %d of %d results.\n", len(mods), totalResults)
	}

	return nil
}

// truncate shortens a string to maxLen, adding "..." if truncated
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
