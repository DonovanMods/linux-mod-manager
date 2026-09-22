package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/spf13/cobra"
)

var (
	searchSource   string
	searchLimit    int
	searchProfile  string
	searchCategory string
	searchTags     []string
	searchRefresh  bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for mods",
	Long: `Search for mods in the configured sources.

If --source is not specified, all configured sources for the game are
searched concurrently and the results are merged. Results already
installed in the target profile (-p/--profile, default: active profile)
are marked [installed].

A source that searches a LOCAL copy of its catalogue (Thunderstore) builds
that copy on the first search - a one-time wait of a few seconds, announced
on stderr - and refreshes it every six hours. --refresh rebuilds it before
searching, for a package published in the last few hours. Thunderstore
packages flagged NSFW are left out unless you ask for them with
--tag NSFW (or --category NSFW); --tag Deprecated finds deprecated ones.

Use --category and --tag to filter results; support varies by source.
--category is honored by NexusMods and CurseForge; --tag is currently
NexusMods only. Custom sources (directory/manifest/API) ignore both.
The values each source accepts are source-specific: NexusMods takes the
category NAME as it spells it ("Armour") and a tag name; CurseForge takes
a numeric category id. Steam Workshop treats both as REQUIRED TAGS - the
Workshop has no category concept distinct from tags - and needs your own
Steam Web API key ('lmm auth login steamworkshop'). A source you have not
signed in to is left out of an all-sources search rather than warned about
on every query ('lmm source list' shows which sources need a key); a search
that finds nothing names what it skipped and the login command, and
searching it directly with '-s' still reports that authentication is
required.

Examples:
  lmm search skyui --game skyrim-se
  lmm search "immersive armor" --game skyrim-se --source nexusmods
  lmm search "armor" --game skyrim-se --category Armour --tag lore-friendly
  lmm search "armor" --game skyrim-se --profile survival`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSearch,
}

func init() {
	searchCmd.Flags().StringVarP(&searchSource, "source", "s", "", "mod source to search (default: all configured sources)")
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 10, "maximum number of results")
	searchCmd.Flags().StringVarP(&searchProfile, "profile", "p", "", "profile to check for installed mods (default: active profile)")
	searchCmd.Flags().StringVar(&searchCategory, "category", "", "filter by category (NexusMods: the category name; CurseForge: its numeric id)")
	searchCmd.Flags().StringSliceVar(&searchTags, "tag", nil, "filter by tag (repeatable; source-specific)")
	searchCmd.Flags().BoolVar(&searchRefresh, "refresh", false, "rebuild a locally cached source index (Thunderstore) before searching")

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

// noSearchableSourcesNotice renders the honesty-notice text for an
// all-sources search whose AttemptedCount came back 0 (#58 item 3): the
// game DOES have configured sources (noSourcesConfiguredErr already guarded
// the zero-sources case above), but NONE of them support searching at all -
// silently rendering "No mods found." here would be indistinguishable from
// a capable source genuinely finding nothing, leaving the user with no hint
// that searching isn't possible here at all.
func noSearchableSourcesNotice(game *domain.Game) string {
	// Sentence-cased with terminal punctuation - this is a printed
	// user-facing notice, not a lowercase Go error value.
	return fmt.Sprintf("None of %s's sources support searching; install by ID instead.", game.Name)
}

// skippedUnauthenticatedNotice renders the one line an all-sources search
// owes the user when a searchable source was left out for want of a
// credential (#383): the source's own name and the command that fixes it.
// The skip itself stays silent on a search that DID find something - this
// is the empty-result path's explanation, not a standing notice on every
// query, which is the whole point of #383.
func skippedUnauthenticatedNotice(sourceIDs []string) string {
	verb, login := "was", "lmm auth login "+sourceIDs[0]
	if len(sourceIDs) > 1 {
		verb, login = "were", "lmm auth login <source>"
	}
	return fmt.Sprintf("%s %s skipped: not signed in (run: %s).",
		strings.Join(sourceIDs, ", "), verb, login)
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

// searchPageSize turns --limit into the page size requested from sources. A
// positive limit is requested verbatim so `--limit 30` can actually fetch 30
// results instead of being capped at each source's own default page size
// (20 — see internal/source/custom/search.go and internal/source/nexusmods)
// and then merely truncating an already-short list; there is no --page flag,
// so that default has otherwise been an invisible, unreachable ceiling. A
// non-positive limit (0, or the historical --limit -1 case) falls back to 0
// so sources keep applying their own default, matching prior behavior.
func searchPageSize(limit int) int {
	if limit > 0 {
		return limit
	}
	return 0
}

func doSearch(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	query := args[0]
	if len(args) > 1 {
		// Join multiple args as single query
		for _, arg := range args[1:] {
			query += " " + arg
		}
	}

	// Resolved up front so core.Search can mark already-installed hits; it
	// only reads the profile when there is something to mark.
	profileName, err := resolveProfile(ctx, service, game.ID, searchProfile)
	if err != nil {
		return err
	}

	opts := core.SearchOptions{
		Category: searchCategory,
		Tags:     searchTags,
		PageSize: searchPageSize(searchLimit),
		Limit:    searchLimit,
	}
	if searchSource == "" {
		// Guard: game must have at least one configured source
		if err := noSourcesConfiguredErr(game); err != nil {
			return err
		}
		if verbose {
			fmt.Printf("Searching for %q in %s (all sources)...\n", query, game.Name)
		}
	} else {
		sourceToUse, err := resolveSource(service, game, searchSource, false)
		if err != nil {
			return err
		}
		opts.SourceID = sourceToUse
		if verbose {
			fmt.Printf("Searching for %q in %s (%s)...\n", query, game.Name, sourceToUse)
		}
	}

	// A source that answers Search from a LOCAL index builds that index on
	// the first search and announces it itself, through the notices
	// withServiceOpts prints to stderr (#360 §2.7, #436). --refresh asks
	// for the rebuild up front instead.
	if searchRefresh {
		refreshSearchedIndexes(ctx, service, game, opts.SourceID)
	}

	// core.Search owns the search itself, the merge across sources and the
	// installed-mod join; this command only classifies the failure and
	// renders what came back.
	report, err := service.Search(ctx, game, profileName, query, opts)
	if err != nil {
		if opts.SourceID == "" {
			// No ErrAuthRequired special-case here: an all-sources failure's
			// joined error already names each source and its reason (including
			// auth), and a per-source auth hint lives in the warnings path.
			return fmt.Errorf("search failed: %w", err)
		}
		if notice, ok := capabilityGapNotice(opts.SourceID, err); ok {
			return errors.New(notice)
		}
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(opts.SourceID)
		}
		return fmt.Errorf("search failed: %w", err)
	}

	for _, w := range report.Warnings {
		fmt.Fprintf(os.Stderr, "warning: source %s: %v\n", w.SourceID, w.Err)
	}

	mods, totalResults := report.Mods, report.TotalResults

	if len(mods) == 0 {
		// #58 item 3: attemptedCount == 0 means NONE of the game's configured
		// sources support searching at all - a different condition from a
		// capable source legitimately finding nothing, which the plain "No
		// mods found." below would otherwise claim just as confidently for
		// both. honestNotice is "" for every other case (single-source
		// search, or an aggregate search that genuinely attempted and came
		// up empty), preserving the original message there.
		//
		// A source SKIPPED for want of a credential (#383) takes
		// precedence over both: it is searchable, so the notice above
		// would be a false capability claim, and it is the one actionable
		// fact about this empty result. It also covers the AttemptedCount
		// 0 case that skip produces on a game whose only searchable source
		// is that one - the shape `lmm init` builds for a Steam game.
		honestNotice := ""
		switch {
		case len(report.SkippedUnauthenticated) > 0:
			honestNotice = skippedUnauthenticatedNotice(report.SkippedUnauthenticated)
		case report.AttemptedCount == 0:
			honestNotice = noSearchableSourcesNotice(game)
		}

		if jsonOutput {
			// One-document-on-stdout invariant (mirrors `update --json`'s own
			// contract): stdout must stay a single parseable JSON document
			// regardless of this notice, so it goes to stderr instead.
			if honestNotice != "" {
				fmt.Fprintln(os.Stderr, honestNotice)
			}
			return emitJSON(report)
		}

		if honestNotice != "" {
			fmt.Println(honestNotice)
		} else {
			fmt.Println("No mods found.")
		}
		return nil
	}

	if jsonOutput {
		// report.Mods already carries service.Search's own --limit-capped
		// slice; TotalResults stays the untruncated count (SearchReport's
		// doc comment). Final review, Important #3 / #302: the cap moved
		// into core so this command and `lmm serve` render the identical
		// document for the same call, instead of mutating the result after
		// the fact.
		return emitJSON(report)
	}

	// Print results. The UPDATED column (#433) appears only when at least
	// one hit carries a date: a source that reports none (and a search
	// over only such sources) adds no column of dashes.
	showUpdated := false
	for _, mod := range mods {
		if displayAge(mod.UpdatedAt, cliNow()) != "" {
			showUpdated = true
			break
		}
	}
	header, separator := "ID\tNAME\tAUTHOR\tVERSION\t", "--\t----\t------\t-------\t"
	if showUpdated {
		header, separator = header+"UPDATED\t", separator+"-------\t"
	}
	var buf bytes.Buffer
	w := newDisplayWidthTableWriter(&buf)
	if _, err := fmt.Fprintln(w, header+"SOURCE\t"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, separator+"------\t"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}

	// installedRows tracks each row's installed state in iteration order, so
	// the whole row (not just the marker) can be green-tinted post-Flush -
	// #193's richer palette (a cell-only accent read as too subtle in smoke
	// feedback). Plain "[installed]" text is fed into the table writer; the
	// row-level color wraps the already-padded line, matching printTable's
	// "color only after Flush" contract.
	var installedRows []bool
	for _, mod := range mods {
		installedMark := ""
		installed := mod.Installed
		if installed {
			installedMark = "[installed]"
		}
		installedRows = append(installedRows, installed)
		// displayModVersion, never the raw field: since Tier 2 (#269 W2) a
		// hit can be a Steam Workshop item, whose Version is the 19-digit
		// content id. core.SearchHit.External is stamped for exactly this -
		// a catalog document has no installed row to read the fact from.
		cells := []string{
			mod.ID,
			truncate(mod.Name, 40),
			truncate(core.AuthorText(&mod.Mod), 20), // #420: the persona name where one resolved
			displayModVersion(mod.External, mod.Version, mod.UpdatedAt),
		}
		if showUpdated {
			// "-", not a blank cell, for an undated hit beside dated ones:
			// the column says this source reported no date.
			cells = append(cells, cmp.Or(displayAge(mod.UpdatedAt, cliNow()), "-"))
		}
		cells = append(cells, mod.SourceID, installedMark)
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}
	rowColor := func(i int) func(string) string {
		if i >= 0 && i < len(installedRows) && installedRows[i] {
			return colorGreen
		}
		return nil
	}
	if err := printTable(&buf, 2, rowColor); err != nil {
		return fmt.Errorf("writing table: %w", err)
	}

	if verbose {
		fmt.Printf("\nShowing %d of %d results.\n", len(mods), totalResults)
	}

	return nil
}

// truncate shortens a string to maxLen characters, adding "..." if
// truncated. It counts runes, not bytes, so a non-ASCII name (a Steam
// persona name, #420) is never cut through the middle of a character.
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-3]) + "..."
}

// refreshSearchedIndexes rebuilds the local index of every source this
// search will ask that keeps one (`lmm search --refresh`). Progress and the
// outcome go to STDERR, like every other index notice, so `--json` keeps
// its single document; a refresh that fails is reported and the search
// goes ahead on whatever is cached, where the search's own error or
// warning says the rest.
func refreshSearchedIndexes(ctx context.Context, service *core.Service, game *domain.Game, sourceID string) {
	for _, id := range searchedSourceIDs(game, sourceID) {
		status, err := service.SourceIndexStatus(ctx, id, game.ID)
		if err != nil || status == nil {
			continue // no index to refresh; the search reports the rest
		}
		report, err := service.RefreshSourceIndex(ctx, id, game.ID, true, printIndexStep)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: refreshing the %s index for %s failed: %v\n", sourceName(service, id), status.Game, err)
			continue
		}
		fmt.Fprintln(os.Stderr, indexReportLine(sourceName(service, id), report))
	}
}

// printIndexStep prints an explicit refresh's start line to stderr; the
// per-thousand progress ticks and the done tick are left to the summary
// line the caller prints from the report.
func printIndexStep(e core.Event) {
	if step, ok := e.(core.StepEvent); ok && step.Phase == core.IndexRefreshStarted {
		fmt.Fprintf(os.Stderr, "%s...\n", capitalize(step.Detail))
	}
}

// searchedSourceIDs names the sources this search will actually ask, in a
// stable order: the one named by --source, or every source the game maps.
func searchedSourceIDs(game *domain.Game, sourceID string) []string {
	if sourceID != "" {
		return []string{sourceID}
	}
	ids := make([]string, 0, len(game.SourceIDs))
	for id := range game.SourceIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// sourceName is a source's display name, falling back to its id - this is
// notice text, not a place to fail.
func sourceName(service *core.Service, id string) string {
	if src, err := service.GetSource(id); err == nil {
		return src.Name()
	}
	return id
}
