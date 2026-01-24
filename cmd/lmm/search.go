package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"lmm/internal/domain"

	"github.com/spf13/cobra"
)

var (
	searchSource string
	searchLimit  int
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for mods",
	Long: `Search for mods in the configured sources.

Examples:
  lmm search skyui --game skyrim-se
  lmm search "immersive armor" --game skyrim-se --source nexusmods`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSearch,
}

func init() {
	searchCmd.Flags().StringVarP(&searchSource, "source", "s", "nexusmods", "mod source to search")
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 10, "maximum number of results")

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
	defer service.Close()

	// Verify game exists
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	if verbose {
		fmt.Printf("Searching for \"%s\" in %s (%s)...\n", query, game.Name, searchSource)
	}

	ctx := context.Background()
	mods, err := service.SearchMods(ctx, searchSource, gameID, query)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return fmt.Errorf("NexusMods requires authentication.\nRun 'lmm auth login' to authenticate")
		}
		return fmt.Errorf("search failed: %w", err)
	}

	if len(mods) == 0 {
		fmt.Println("No mods found.")
		return nil
	}

	// Limit results
	if len(mods) > searchLimit {
		mods = mods[:searchLimit]
	}

	// Print results
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tAUTHOR\tVERSION\tDOWNLOADS")
	fmt.Fprintln(w, "--\t----\t------\t-------\t---------")

	for _, mod := range mods {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n",
			mod.ID,
			truncate(mod.Name, 40),
			truncate(mod.Author, 20),
			mod.Version,
			mod.Downloads,
		)
	}
	w.Flush()

	if verbose {
		fmt.Printf("\nShowing %d of %d results.\n", len(mods), len(mods))
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
