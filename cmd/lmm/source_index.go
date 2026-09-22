package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"
)

// `lmm source index` and `lmm source index prune` (#410): the index surface
// of a source that searches a LOCAL copy of its catalogue - today,
// Thunderstore. The design put the footprint and the pruning under an
// `lmm cache` command lmm does not have; they live here instead, beside
// the index they are about (docs/plans/2026-09-10-thunderstore-design.md,
// the 2026-09-16 amendment).

var (
	sourceIndexSource  string
	sourceIndexRefresh bool
	sourceIndexAll     bool

	sourceIndexPruneAll    bool
	sourceIndexPruneDryRun bool
	sourceIndexPruneYes    bool
)

var sourceIndexCmd = &cobra.Command{
	Use:   "index",
	Short: "Show, rebuild or list the local search indexes of sources like Thunderstore",
	Long: `Show or rebuild the local search index a source keeps for the active game.

Thunderstore publishes each community's whole catalogue as one document and
has no search endpoint, so lmm keeps a copy under the cache directory and
searches that. It is built on the first search, refreshed every six hours,
and costs from a few megabytes to a couple of hundred for the largest
community. --refresh rebuilds it now - the command to reach for when a
package published in the last few hours does not show up.

--all lists every index on disk, for every game, with its size and the
games that use it; 'lmm source index prune' removes the ones nothing needs.

Examples:
  lmm source index --game lethal-company
  lmm source index --refresh
  lmm source index --all
  lmm source index prune --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if sourceIndexAll {
			if sourceIndexRefresh {
				return errors.New("--refresh rebuilds one game's index; it cannot be combined with --all")
			}
			return withService(cmd, func(ctx context.Context, svc *core.Service) error {
				return doSourceIndexList(ctx, svc, sourceIndexSource)
			})
		}
		return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
			return doSourceIndex(ctx, svc, game, sourceIndexSource, sourceIndexRefresh)
		})
	},
}

var sourceIndexPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove local source indexes nothing needs",
	Long: `Remove the local search indexes (Thunderstore's) that no game needs.

An index no game in games.yaml maps is removed. An index a game does map is
kept until it has gone 30 days without a refresh. --all removes every index
regardless, after asking (-y skips the question). Every index is a copy of a
public catalogue, so anything removed is rebuilt the next time it is
searched.

Pruning is fail-closed: nothing is removed if games.yaml cannot be read,
and only --all removes anything when games.yaml is missing, empty, or holds
anything lmm cannot read as a game (a mistyped key, a game indented out of
its block, a game whose profiles exist but which the file does not list);
no unused index is removed while a game maps the source to an identifier
lmm cannot use; an index refreshed after the prune decided to remove it is
kept; and a directory is only removed when it holds nothing but files lmm
provably wrote - never through a symbolic link, and never one lmm has no
permission to remove. --dry-run lists exactly what a real run would
remove, and removes nothing. A run that could not remove an index it set
out to exits 1.

Examples:
  lmm source index prune --dry-run
  lmm source index prune
  lmm source index prune --all -y`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withService(cmd, func(ctx context.Context, svc *core.Service) error {
			return doSourceIndexPrune(ctx, svc, core.IndexPruneOptions{
				SourceID: sourceIndexSource, All: sourceIndexPruneAll, DryRun: sourceIndexPruneDryRun,
			}, sourceIndexPruneYes)
		})
	},
}

func init() {
	sourceIndexCmd.PersistentFlags().StringVarP(&sourceIndexSource, "source", "s", "", "the source whose index to use (default: every source of the game that keeps one)")
	sourceIndexCmd.Flags().BoolVar(&sourceIndexRefresh, "refresh", false, "rebuild the index now, even if it is current")
	sourceIndexCmd.Flags().BoolVar(&sourceIndexAll, "all", false, "list every index on disk, for every game")

	sourceIndexPruneCmd.Flags().BoolVar(&sourceIndexPruneAll, "all", false, "remove every index, including ones a game uses")
	sourceIndexPruneCmd.Flags().BoolVar(&sourceIndexPruneDryRun, "dry-run", false, "list what would be removed, and remove nothing")
	sourceIndexPruneCmd.Flags().BoolVarP(&sourceIndexPruneYes, "yes", "y", false, "skip the confirmation --all asks for")

	sourceIndexCmd.AddCommand(sourceIndexPruneCmd)
	sourceCmd.AddCommand(sourceIndexCmd)
}

// doSourceIndex shows - or, with refresh, rebuilds - the index one of the
// game's sources keeps. With no source named it is the game's one indexed
// source; a game with several must name one, since the --json document is
// a single index.
func doSourceIndex(ctx context.Context, svc *core.Service, game *domain.Game, sourceID string, refresh bool) error {
	sourceID, status, err := indexedSourceFor(ctx, svc, game, sourceID)
	if err != nil {
		return err
	}
	name := sourceName(svc, sourceID)

	if !refresh {
		if jsonOutput {
			return emitJSON(status)
		}
		printIndexStatus(name, status, onDiskUnusable(ctx, svc, sourceID, status))
		return nil
	}

	var sink core.EventSink
	if !jsonOutput {
		sink = printIndexStep
	}
	report, err := svc.RefreshSourceIndex(ctx, sourceID, game.ID, true, sink)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(report)
	}
	fmt.Println(indexReportLine(name, report))
	return nil
}

// indexedSourceFor resolves which of the game's sources keeps the index
// this command is about, and reads its status.
func indexedSourceFor(ctx context.Context, svc *core.Service, game *domain.Game, sourceID string) (string, *core.IndexStatus, error) {
	if sourceID != "" {
		status, err := svc.SourceIndexStatus(ctx, sourceID, game.ID)
		if err != nil {
			return "", nil, err
		}
		if status == nil {
			return "", nil, fmt.Errorf("source %q keeps no local index; only a source like thunderstore does", sourceID)
		}
		return sourceID, status, nil
	}

	var found []string
	var statuses []*core.IndexStatus
	for _, id := range searchedSourceIDs(game, "") {
		status, err := svc.SourceIndexStatus(ctx, id, game.ID)
		if err != nil {
			return "", nil, err
		}
		if status != nil {
			found = append(found, id)
			statuses = append(statuses, status)
		}
	}
	switch len(found) {
	case 0:
		return "", nil, fmt.Errorf("none of %s's sources keeps a local index (Thunderstore is the one that does: 'lmm game edit %s --source thunderstore=<community>')", game.Name, game.ID)
	case 1:
		return found[0], statuses[0], nil
	default:
		return "", nil, fmt.Errorf("%s has several sources with a local index (%s); choose one with --source", game.Name, strings.Join(found, ", "))
	}
}

// onDiskUnusable is the listing's row for status's index when there is a
// directory for it but no index lmm can use - one an older lmm wrote, or a
// damaged one - or nil.
func onDiskUnusable(ctx context.Context, svc *core.Service, sourceID string, status *core.IndexStatus) *core.SourceIndexEntry {
	if status.Present {
		return nil
	}
	listing, err := svc.ListSourceIndexes(ctx, sourceID)
	if err != nil {
		return nil
	}
	for i, e := range listing.Indexes {
		if e.Game == status.Game && unusableOnDisk(e) {
			return &listing.Indexes[i]
		}
	}
	return nil
}

// unusableOnDisk reports a directory holding something that is not a usable
// index. An empty one - all a failed cold build leaves is its lock file - is
// no index at all, not a damaged one.
func unusableOnDisk(e core.SourceIndexEntry) bool {
	return e.Cached && !e.Present && e.Bytes > 0
}

// printIndexStatus is `lmm source index`'s plain-text view. unusable is the
// directory on disk when there is one but no usable index in it.
func printIndexStatus(name string, status *core.IndexStatus, unusable *core.SourceIndexEntry) {
	switch {
	case !status.Present && unusable != nil:
		// Not "no index yet" (T3 review F12): `--all` lists this directory
		// with its size, and the two views must agree.
		fmt.Printf("The %s index for %s on disk (%s) is one lmm cannot use - written by an older lmm, or damaged.\n", name, status.Game, humanBytes(unusable.Bytes))
		fmt.Println("The next search rebuilds it, or do it now with 'lmm source index --refresh'.")
	case !status.Present:
		fmt.Printf("No %s index for %s yet. It is built on the first search, or now with 'lmm source index --refresh'.\n", name, status.Game)
	default:
		fmt.Printf("%s index for %s\n", name, status.Game)
		fmt.Printf("  Packages: %d\n", status.Packages)
		updated := formatWhen(status.FetchedAt, time.Now())
		if status.Stale {
			updated += " (due for a refresh; the next search does it)"
		}
		fmt.Printf("  Updated:  %s\n", updated)
		fmt.Printf("  Size:     %s\n", humanBytes(status.Bytes))
	}
	if !status.RetryAt.IsZero() {
		fmt.Printf("\n%s\n", holdLine(name, "", status.RetryAt, status.HoldReason))
	}
}

// holdLine is one sentence for a hold: when lmm will next ask, and why not
// before (T3 review F3/F10).
func holdLine(name, game string, at time.Time, reason string) string {
	about := ""
	if game != "" {
		about = " about " + game
	}
	return fmt.Sprintf("Not asking %s%s again until %s (in %s): %s", name, about, clockAt(at), roughWait(time.Until(at)), reason)
}

// clockAt renders a moment against the reader's clock: the time alone
// within the next twelve hours, with the date otherwise.
func clockAt(at time.Time) string {
	if d := time.Until(at); d > -12*time.Hour && d < 12*time.Hour {
		return at.Local().Format("15:04:05")
	}
	return at.Local().Format("2006-01-02 15:04:05")
}

// roughWait is a wait to the second, and never negative.
func roughWait(d time.Duration) string {
	return max(d, 0).Round(time.Second).String()
}

// indexReportLine is one sentence for what a refresh did.
func indexReportLine(name string, report *core.IndexReport) string {
	took := (time.Duration(report.DurationMS) * time.Millisecond).Round(100 * time.Millisecond)
	switch report.Status {
	case core.IndexStatusBuilt:
		verb := "rebuilt, unchanged"
		if report.Changed {
			verb = "updated"
		}
		return fmt.Sprintf("%s index for %s %s: %d packages, %s, in %s.", name, report.Game, verb, report.Packages, humanBytes(report.Bytes), took)
	case core.IndexStatusStale:
		return fmt.Sprintf("%s index for %s could not be refreshed; still using the copy on disk (%d packages): %s",
			name, report.Game, report.Packages, strings.Join(report.Warnings, "; "))
	default:
		return fmt.Sprintf("%s index for %s is already current: %d packages, %s.", name, report.Game, report.Packages, humanBytes(report.Bytes))
	}
}

// doSourceIndexList is `lmm source index --all`.
func doSourceIndexList(ctx context.Context, svc *core.Service, sourceID string) error {
	listing, err := svc.ListSourceIndexes(ctx, sourceID)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(listing)
	}
	for _, w := range listing.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	for _, h := range listing.Holds {
		fmt.Println(holdLine(sourceName(svc, h.Source), h.Game, h.RetryAt, h.Reason))
	}
	if len(listing.Holds) > 0 {
		fmt.Println()
	}
	if len(listing.Indexes) == 0 {
		fmt.Println("No source keeps a local index yet.")
		return nil
	}

	var buf bytes.Buffer
	w := newDisplayWidthTableWriter(&buf)
	if _, err := fmt.Fprintln(w, "SOURCE\tINDEX\tPACKAGES\tSIZE\tUPDATED\tUSED BY"); err != nil {
		return fmt.Errorf("writing source index header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "------\t-----\t--------\t----\t-------\t-------"); err != nil {
		return fmt.Errorf("writing source index separator: %w", err)
	}
	var total int64
	var kept []string
	now := time.Now()
	for _, e := range listing.Indexes {
		packages, size, updated := "-", "-", "not built yet"
		// An empty directory - a failed cold build's lock file - is
		// listed as what it is: not built yet.
		if e.Cached && (e.Present || e.Bytes > 0) {
			packages = fmt.Sprintf("%d", e.Packages)
			if unusableOnDisk(e) {
				// A directory with no index lmm can use (review F12).
				packages = "unusable"
			}
			size = humanBytes(e.Bytes)
			updated = formatWhen(e.FetchedAt, now)
			total += e.Bytes
		}
		used := "-"
		if len(e.MappedBy) > 0 {
			used = strings.Join(e.MappedBy, ", ")
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Source, e.Game, packages, size, updated, used); err != nil {
			return fmt.Errorf("writing source index row: %w", err)
		}
		if e.KeepReason != "" {
			kept = append(kept, fmt.Sprintf("  %s/%s: prune keeps it: %s", e.Source, e.Game, e.KeepReason))
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}
	if err := printTable(&buf, 2, nil); err != nil {
		return err
	}
	if len(kept) > 0 {
		fmt.Printf("\n%s\n", strings.Join(kept, "\n"))
	}
	fmt.Printf("\nTotal on disk: %s\n", humanBytes(total))
	return nil
}

// doSourceIndexPrune is `lmm source index prune`. Every run previews first
// and then removes only what the preview listed, so what is shown (and,
// for --all, confirmed) is exactly what goes.
func doSourceIndexPrune(ctx context.Context, svc *core.Service, opts core.IndexPruneOptions, yes bool) error {
	preview, err := svc.PruneSourceIndexes(ctx, core.IndexPruneOptions{SourceID: opts.SourceID, All: opts.All, DryRun: true})
	if err != nil {
		return err
	}
	if opts.DryRun {
		if jsonOutput {
			return emitJSON(preview)
		}
		return printPruneReport(preview, true)
	}
	if opts.All && preview.Removed > 0 && !yes {
		if !jsonOutput {
			// The table alone: the question below is its summary, and
			// "nothing was removed (dry run)" right before it read as the
			// answer (T3 review F11).
			if err := printPruneReport(preview, false); err != nil {
				return err
			}
			fmt.Printf("\nRemove %d index(es), %s? They are rebuilt on the next search. [y/N] ", preview.Removed, humanBytes(preview.FreedBytes))
		}
		response, err := readPromptLine()
		if err != nil {
			return err
		}
		if response != "y" && response != "yes" {
			return ErrCancelled
		}
	}

	opts.Only = preview.RemovalKeys()
	report, err := svc.PruneSourceIndexes(ctx, opts)
	if err != nil {
		return err
	}
	if jsonOutput {
		if err := emitJSON(report); err != nil {
			return err
		}
	} else {
		if err := printPruneReport(report, true); err != nil {
			return err
		}
	}
	// An index that could not be removed is a failure the exit status must
	// carry (T3 review F11); the report has already said which and why.
	failed := 0
	for _, e := range report.Entries {
		if e.Action == core.IndexPruneFailed {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d index(es) could not be removed: %w", failed, ErrReported)
	}
	return nil
}

// printPruneReport renders a prune (or its preview) as a table and, with
// summary, the line that says what it came to.
func printPruneReport(report *core.IndexPruneReport, summary bool) error {
	for _, w := range report.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if len(report.Entries) == 0 {
		fmt.Println("No local source indexes on disk.")
		return nil
	}
	entries := append([]core.IndexPruneEntry(nil), report.Entries...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Game < entries[j].Game })

	var buf bytes.Buffer
	w := newDisplayWidthTableWriter(&buf)
	if _, err := fmt.Fprintln(w, "ACTION\tSOURCE\tINDEX\tSIZE\tWHY"); err != nil {
		return fmt.Errorf("writing source index prune header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "------\t------\t-----\t----\t---"); err != nil {
		return fmt.Errorf("writing source index prune separator: %w", err)
	}
	for _, e := range entries {
		action := e.Action
		if report.DryRun && action == core.IndexPruneRemove {
			action = "would remove"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", action, e.Source, e.Game, humanBytes(e.Bytes), e.Reason); err != nil {
			return fmt.Errorf("writing source index prune row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing source index prune table: %w", err)
	}
	if err := printTable(&buf, 2, nil); err != nil {
		return err
	}

	switch {
	case !summary:
	case report.DryRun:
		fmt.Printf("\nWould remove %d index(es), freeing %s. Nothing was removed (dry run).\n", report.Removed, humanBytes(report.FreedBytes))
	case report.Removed == 0:
		fmt.Println("\nNothing to remove.")
	default:
		fmt.Printf("\nRemoved %d index(es), freeing %s.\n", report.Removed, humanBytes(report.FreedBytes))
	}
	return nil
}

// formatWhen renders a timestamp with how long ago it was.
func formatWhen(at, now time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	return fmt.Sprintf("%s (%s ago)", at.Local().Format("2006-01-02 15:04"), roughAge(now.Sub(at)))
}

// roughAge is an age at the precision a person reads one.
func roughAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// capitalize upper-cases a sentence's first letter.
func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
