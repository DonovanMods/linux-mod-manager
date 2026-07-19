package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

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
	GameID   string          `json:"game_id"`
	Query    string          `json:"query"`
	Mods     []searchModJSON `json:"mods"`
	Warnings []string        `json:"warnings,omitempty"`
}

type searchModJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Author    string `json:"author"`
	Version   string `json:"version"`
	Source    string `json:"source"`
	Installed bool   `json:"installed"`
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for mods",
	Long: `Search for mods in the configured sources.

If --source is not specified, all configured sources for the game are
searched concurrently and the results are merged.

Examples:
  lmm search skyui --game skyrim-se
  lmm search "immersive armor" --game skyrim-se --source nexusmods`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSearch,
}

func init() {
	searchCmd.Flags().StringVarP(&searchSource, "source", "s", "", "mod source to search (default: all configured sources)")
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 10, "maximum number of results")
	searchCmd.Flags().StringVarP(&searchProfile, "profile", "p", "", "profile to check for installed mods (default: active profile)")
	searchCmd.Flags().StringVar(&searchCategory, "category", "", "filter by category (source-specific ID or name)")
	searchCmd.Flags().StringSliceVar(&searchTags, "tag", nil, "filter by tag (repeatable; source-specific)")

	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doSearch(ctx, service, game, args)
	})
}

// noSourcesConfiguredErr returns an error if the game has no configured sources.
// Used by the aggregate search path (when --source is not specified) to provide
// the same diagnostic as resolveSource in the single-source path.
func noSourcesConfiguredErr(game *domain.Game) error {
	if len(game.SourceIDs) == 0 {
		return fmt.Errorf("no mod sources configured for %s; add sources with 'lmm game add' or edit games.yaml", game.Name)
	}
	return nil
}

// capabilityGapNotice turns an ErrNotSupported search failure into a clean
// one-line notice (design §7) instead of a wrapped-error dump. ok is false
// for every other error.
func capabilityGapNotice(sourceID string, err error) (string, bool) {
	if !errors.Is(err, source.ErrNotSupported) {
		return "", false
	}
	return fmt.Sprintf("source %q does not support searching; install by ID instead: lmm install --source %s --id <mod-id>", sourceID, sourceID), true
}

// limitResults truncates mods to at most limit entries for display. A
// non-positive limit (e.g. --limit 0 or a negative value) leaves mods
// untouched instead of truncating to nothing or panicking on a negative
// slice bound (mods[:-1]).
func limitResults(mods []domain.Mod, limit int) []domain.Mod {
	if limit > 0 && len(mods) > limit {
		mods = mods[:limit]
	}
	return mods
}

func doSearch(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	query := args[0]
	if len(args) > 1 {
		// Join multiple args as single query
		for _, arg := range args[1:] {
			query += " " + arg
		}
	}

	var mods []domain.Mod
	var warnings []core.SourceWarning
	var totalResults int

	if searchSource == "" {
		// Guard: game must have at least one configured source
		if err := noSourcesConfiguredErr(game); err != nil {
			return err
		}

		if verbose {
			fmt.Printf("Searching for %q in %s (all sources)...\n", query, game.Name)
		}
		agg, err := service.SearchAllSources(ctx, game.ID, query, searchCategory, searchTags, 0, 0)
		if err != nil {
			// No ErrAuthRequired special-case here: an all-sources failure's
			// joined error already names each source and its reason (including
			// auth), and a per-source auth hint lives in the warnings path.
			return fmt.Errorf("search failed: %w", err)
		}
		mods, warnings = agg.Mods, agg.Warnings
		totalResults = len(mods)
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: source %s: %v\n", w.SourceID, w.Err)
		}
	} else {
		sourceToUse, err := resolveSource(game, searchSource, false)
		if err != nil {
			return err
		}
		if verbose {
			fmt.Printf("Searching for %q in %s (%s)...\n", query, game.Name, sourceToUse)
		}
		searchResult, err := service.SearchMods(ctx, sourceToUse, game.ID, query, searchCategory, searchTags, 0, 0)
		if err != nil {
			if notice, ok := capabilityGapNotice(sourceToUse, err); ok {
				return errors.New(notice)
			}
			if errors.Is(err, domain.ErrAuthRequired) {
				return authPromptError(sourceToUse)
			}
			return fmt.Errorf("search failed: %w", err)
		}
		mods = searchResult.Mods
		totalResults = len(mods)
	}

	warningStrs := make([]string, len(warnings))
	for i, w := range warnings {
		warningStrs[i] = fmt.Sprintf("source %s: %v", w.SourceID, w.Err)
	}

	if len(mods) == 0 {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			out := searchJSONOutput{GameID: game.ID, Query: query, Mods: []searchModJSON{}, Warnings: warningStrs}
			if err := enc.Encode(out); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
		}
		fmt.Println("No mods found.")
		return nil
	}

	// Get installed mods to mark already-installed ones (source-aware: a mod
	// ID is only unique within its source, so key on both).
	profileName := profileOrDefault(searchProfile)
	installedMods, _ := service.GetInstalledMods(game.ID, profileName)
	installedKeys := make(map[string]bool)
	for _, im := range installedMods {
		installedKeys[domain.ModKey(im.SourceID, im.ID)] = true
	}

	// Apply result limit for display (totalResults captured earlier per-branch for "Showing X of Y")
	mods = limitResults(mods, searchLimit)

	if jsonOutput {
		out := searchJSONOutput{GameID: game.ID, Query: query, Mods: make([]searchModJSON, len(mods)), Warnings: warningStrs}
		for i, mod := range mods {
			out.Mods[i] = searchModJSON{
				ID:        mod.ID,
				Name:      mod.Name,
				Author:    mod.Author,
				Version:   mod.Version,
				Source:    mod.SourceID,
				Installed: installedKeys[domain.ModKey(mod.SourceID, mod.ID)],
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
	if _, err := fmt.Fprintln(w, "ID\tNAME\tAUTHOR\tVERSION\tSOURCE\t"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "--\t----\t------\t-------\t------\t"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	for _, mod := range mods {
		installedMark := ""
		if installedKeys[domain.ModKey(mod.SourceID, mod.ID)] {
			installedMark = "[installed]"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			mod.ID,
			truncate(mod.Name, 40),
			truncate(mod.Author, 20),
			mod.Version,
			mod.SourceID,
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
