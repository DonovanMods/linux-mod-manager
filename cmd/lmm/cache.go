package main

// cache.go is `lmm cache [status]`, `lmm cache prune` and `lmm cache keep`
// (#678): the mod cache's size, the prune of old cached versions, and its
// setting. The policy - what a prune keeps - lives in
// internal/core/cache_prune.go, shared with `lmm serve`'s Storage section.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	cachePruneDryRun      bool
	cachePruneYes         bool
	cachePruneUninstalled bool
)

// cacheKeepRule is the keep rule, as every cache command words it.
const cacheKeepRule = `Per mod, the cache keeps every installed version (any profile's), plus the
newest N other versions, N = cache.keep_previous_versions in config.yaml
(default 3). On top of those it keeps every older version a profile or a
snapshot references: a row's rollback target (previous_version), a locked or
pinned version, a collection pin, another profile's version, a snapshot's.
A referenced version already among the newest N uses one of the N. So after
updates v1 to v5, the default keeps v5 (installed) and v4, v3 and v2.

0 keeps only the installed (and referenced) versions; unlimited (or a
negative count) never prunes. Delete a snapshot to let go of the versions
only it references.

A mod nothing references at all - not installed in any profile, named by
no profile or snapshot, such as one uninstalled with --keep-cache - is left
whole. 'lmm cache status' lists those as not installed anywhere, and only
'lmm cache prune --include-uninstalled' removes them.`

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Show and prune the mod cache",
	Long: `Show how much room the mod cache takes, and prune old versions of mods.

lmm keeps the files of every mod version it installs in its cache, so a
rollback or a profile switch needs no download. Without a limit, a mod that
updates every week fills it with versions nobody will go back to.

` + cacheKeepRule + `

lmm prunes a mod's old versions by itself after an update, a rollback, a
profile apply or switch, or a collection update changes its version. 'lmm
cache prune' prunes every mod at once. Without a subcommand, 'lmm cache' is
'lmm cache status'.

Examples:
  lmm cache
  lmm cache prune --dry-run
  lmm cache prune --game skyrim-se
  lmm cache keep 5`,
	Args: cobra.NoArgs,
	RunE: runCacheStatus,
}

var cacheStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the cache's size per game and what a prune would free",
	Long: `Show the mod cache's size per game, and how much a prune would free.

It reads the size of every cached file, so on a large cache it takes a
moment. Without --game it covers every game.

Examples:
  lmm cache status
  lmm cache status --game skyrim-se --json`,
	Args: cobra.NoArgs,
	RunE: runCacheStatus,
}

var cachePruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove old cached versions of mods",
	Long: `Remove old cached versions of mods from the cache.

` + cacheKeepRule + `

Without --game it prunes every game's cache. A game whose profiles,
installed records or snapshots lmm cannot read is left whole, and the
command says why: lmm never prunes a version it cannot tell is unused.

--dry-run lists exactly what would be removed, and how much room that
frees, without removing anything or prompting. Under --json without
--dry-run, pass --yes: a prompt cannot be answered.

--include-uninstalled also removes every cached version of each mod
nothing references (listed by 'lmm cache status' as not installed
anywhere).

Examples:
  lmm cache prune --dry-run
  lmm cache prune --game skyrim-se
  lmm cache prune --include-uninstalled --dry-run
  lmm cache prune --yes --json`,
	Args: cobra.NoArgs,
	RunE: runCachePrune,
}

var cacheKeepCmd = &cobra.Command{
	Use:   "keep [count|unlimited]",
	Short: "Show or set how many previous versions of each mod the cache keeps",
	Long: `Show or set cache.keep_previous_versions in config.yaml.

` + cacheKeepRule + `

Setting it prunes nothing by itself; the next update of a mod, or 'lmm
cache prune', applies it.

Examples:
  lmm cache keep
  lmm cache keep 5
  lmm cache keep 0
  lmm cache keep unlimited`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCacheKeep,
}

func init() {
	cachePruneCmd.Flags().BoolVar(&cachePruneDryRun, "dry-run", false, "list what would be removed without removing anything")
	cachePruneCmd.Flags().BoolVarP(&cachePruneYes, "yes", "y", false, "skip the confirmation prompt")
	cachePruneCmd.Flags().BoolVar(&cachePruneUninstalled, "include-uninstalled", false, "also remove every cached version of the mods nothing references (not installed anywhere)")
	cacheCmd.AddCommand(cacheStatusCmd, cachePruneCmd, cacheKeepCmd)
	rootCmd.AddCommand(cacheCmd)
}

// cacheGames resolves --game: that game, or nil for every game. The
// default game is NOT applied: the cache commands cover every game unless
// told otherwise.
func cacheGames(svc *core.Service) ([]*domain.Game, error) {
	if gameID == "" {
		return nil, nil
	}
	game, err := svc.GetGame(gameID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, gameID)
	}
	return []*domain.Game{game}, nil
}

func runCacheStatus(cmd *cobra.Command, _ []string) error {
	return withService(cmd, func(ctx context.Context, svc *core.Service) error {
		games, err := cacheGames(svc)
		if err != nil {
			return err
		}
		return doCacheStatus(ctx, svc, games)
	})
}

func doCacheStatus(ctx context.Context, svc *core.Service, games []*domain.Game) error {
	status, err := svc.CacheStatus(ctx, games)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(status)
	}
	renderCacheStatus(status)
	return nil
}

// keepRuleLine is the setting in one line.
func keepRuleLine(keep int) string {
	switch {
	case keep < 0:
		return "Keeps every cached version (cache.keep_previous_versions: unlimited)."
	case keep == 1:
		return "Keeps each mod's installed versions plus 1 previous version, and every version a profile or snapshot references."
	default:
		return fmt.Sprintf("Keeps each mod's installed versions plus %d previous versions, and every version a profile or snapshot references.", keep)
	}
}

func renderCacheStatus(status *core.CacheStatus) {
	if len(status.Games) == 0 {
		fmt.Println("No games are configured.")
		return
	}
	tw := newDisplayWidthTableWriter(os.Stdout)
	_, _ = fmt.Fprintln(tw, "GAME\tSIZE\tMODS\tVERSIONS\tRECLAIMABLE")
	for _, g := range status.Games {
		reclaim := "-"
		if g.Skipped != "" {
			reclaim = "unknown"
		} else if g.ReclaimableVersions > 0 {
			reclaim = fmt.Sprintf("%s (%s)", humanBytes(g.ReclaimableBytes), countOf(g.ReclaimableVersions, "version", "versions"))
		}
		name := g.GameID
		if len(g.SharedWith) > 0 {
			name += " (+" + strings.Join(g.SharedWith, ", ") + ")"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n", domain.SanitizeLine(name), humanBytes(g.Bytes), g.Mods, g.Versions, reclaim)
	}
	_ = tw.Flush()
	fmt.Printf("\nTotal: %s", humanBytes(status.Bytes))
	if status.ReclaimableVersions > 0 {
		fmt.Printf(", of which a prune frees %s (%s): run 'lmm cache prune'", humanBytes(status.ReclaimableBytes), countOf(status.ReclaimableVersions, "old version", "old versions"))
	}
	fmt.Println(".")
	fmt.Println(keepRuleLine(status.KeepPreviousVersions))
	renderUninstalled(status.Games)
	for _, g := range status.Games {
		if g.Skipped != "" {
			fmt.Fprintf(os.Stderr, "Warning: %s's cache would not be pruned: %s\n", g.GameID, g.Skipped)
		}
	}
}

// countOf is "1 thing" or "n things".
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func runCachePrune(cmd *cobra.Command, _ []string) error {
	return withService(cmd, func(ctx context.Context, svc *core.Service) error {
		games, err := cacheGames(svc)
		if err != nil {
			return err
		}
		return doCachePrune(ctx, svc, games)
	})
}

func doCachePrune(ctx context.Context, svc *core.Service, games []*domain.Game) error {
	plan, err := svc.PlanCachePrune(ctx, games, core.CachePruneOptions{IncludeUninstalled: cachePruneUninstalled})
	if err != nil {
		return err
	}
	// Ruling 15: --dry-run --json is the plan document.
	if cachePruneDryRun && jsonOutput {
		return emitJSON(plan)
	}
	if !jsonOutput {
		for _, s := range plan.Skipped {
			fmt.Fprintf(os.Stderr, "Warning: left %s's cache whole: %s\n", s.GameID, s.Reason)
		}
	}
	if len(plan.Entries) == 0 {
		if jsonOutput {
			return emitJSON(&core.CachePruneResult{Entries: []core.CachePruneEntry{}, Skipped: plan.Skipped})
		}
		if plan.KeepPreviousVersions < 0 {
			fmt.Println("Nothing to prune: cache.keep_previous_versions is unlimited.")
		} else {
			fmt.Println("Nothing to prune.")
		}
		printUninstalledKept(plan)
		return nil
	}
	if !jsonOutput {
		renderCachePrunePlan(plan)
	}
	if cachePruneDryRun {
		fmt.Println("\n(dry-run: nothing removed)")
		return nil
	}
	if !cachePruneYes {
		if !jsonOutput {
			fmt.Print("\nRemove them? [y/N] ")
		}
		response, err := readPromptLine()
		if err != nil {
			return err
		}
		if response != "y" && response != "yes" {
			return ErrCancelled
		}
	}
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.CachePruneRemoved:
			if verbose && !jsonOutput {
				fmt.Printf("  removed %s %s\n", domain.SanitizeLine(p.ModName), domain.SanitizeLine(p.ModVersion))
			}
		case core.CachePruneWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}
	result, err := svc.ApplyCachePrune(ctx, plan, quietSink(progress))
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}
	fmt.Printf("\n✓ Freed %s (%s).\n", humanBytes(result.Bytes), countOf(result.Versions, "old version", "old versions"))
	return nil
}

// renderCachePrunePlan lists a prune plan by game and mod.
func renderCachePrunePlan(plan *core.CachePrunePlan) {
	fmt.Println(keepRuleLine(plan.KeepPreviousVersions))
	game, mod := "", ""
	for _, e := range plan.Entries {
		if e.GameID != game {
			game, mod = e.GameID, ""
			fmt.Printf("\n%s:\n", domain.SanitizeLine(game))
		}
		if key := domain.ModKey(e.SourceID, e.ModID); key != mod {
			mod = key
			label := key
			if e.ModName != "" {
				label = e.ModName + " (" + key + ")"
			}
			fmt.Printf("  %s\n", domain.SanitizeLine(label))
		}
		version := e.Version
		if e.Variant != "" {
			version += " [" + e.Variant + "]"
		}
		if e.Uninstalled {
			version += " (not installed anywhere)"
		}
		fmt.Printf("    - %-24s %s\n", domain.SanitizeLine(version), humanBytes(e.Bytes))
	}
	fmt.Printf("\nWould free %s (%s).\n", humanBytes(plan.Bytes), countOf(plan.Versions, "old version", "old versions"))
	printUninstalledKept(plan)
}

// printUninstalledKept says, for a plan that leaves them whole, how many
// mods nothing references it keeps and how to remove them too.
func printUninstalledKept(plan *core.CachePrunePlan) {
	if plan.IncludeUninstalled || len(plan.Uninstalled) == 0 {
		return
	}
	var bytes int64
	for _, m := range plan.Uninstalled {
		bytes += m.Bytes
	}
	fmt.Printf("Kept: %s not installed anywhere (%s); --include-uninstalled removes them too.\n",
		countOf(len(plan.Uninstalled), "mod", "mods"), humanBytes(bytes))
}

// renderUninstalled lists, under the status table, the mods nothing
// references, with their size.
func renderUninstalled(games []core.CacheGameStatus) {
	for _, g := range games {
		if len(g.Uninstalled) == 0 {
			continue
		}
		fmt.Printf("\nNot installed anywhere in %s (kept; 'lmm cache prune --include-uninstalled' removes them):\n", domain.SanitizeLine(g.GameID))
		for _, m := range g.Uninstalled {
			fmt.Printf("  %s  %s (%s)\n", domain.SanitizeLine(domain.ModKey(m.SourceID, m.ModID)), humanBytes(m.Bytes), countOf(m.Versions, "version", "versions"))
		}
	}
}

// cacheKeepResult is `lmm cache keep --json`'s document: the setting, -1
// for unlimited.
type cacheKeepResult struct {
	KeepPreviousVersions int `json:"keep_previous_versions"`
}

func runCacheKeep(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, svc *core.Service) error {
		return doCacheKeep(ctx, svc, args)
	})
}

func doCacheKeep(ctx context.Context, svc *core.Service, args []string) error {
	if len(args) == 1 {
		n, err := parseKeepPrevious(args[0])
		if err != nil {
			return err
		}
		if err := svc.SetCacheKeepPreviousVersions(ctx, n); err != nil {
			return err
		}
	}
	keep, err := svc.CacheKeepPreviousVersions()
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(cacheKeepResult{KeepPreviousVersions: keep})
	}
	fmt.Println(keepRuleLine(keep))
	return nil
}

// parseKeepPrevious reads a count or "unlimited" (-1).
func parseKeepPrevious(arg string) (int, error) {
	if arg == "unlimited" {
		return -1, nil
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number of versions: give a count, or unlimited", arg)
	}
	return max(n, -1), nil
}

// printCachePrunePreview is a flow plan's line saying what the prune after
// it frees (#678); nothing when it frees nothing.
func printCachePrunePreview(indent string, s *core.CachePruneSummary) {
	if s != nil && s.Versions > 0 {
		fmt.Printf("%sCache: pruning afterwards %s\n", indent, s.Sentence())
	}
}

// printCachePruned is a flow result's line saying what its prune freed.
func printCachePruned(indent string, s *core.CachePruneSummary) {
	if s != nil && s.Versions > 0 {
		fmt.Printf("%sCache: %s\n", indent, s.FreedSentence())
	}
}

// cachePruneLine renders a flow's own prune events (#678) for an
// event-driven command: a warning on stderr, the freed line on stdout. It
// reports whether p was one.
func cachePruneLine(p flowLine) bool {
	switch p.Phase {
	case core.CachePruneWarning:
		fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
	case core.CachePruneDone:
		if !jsonOutput {
			fmt.Printf("  Cache: %s\n", p.Detail)
		}
	case core.CachePruneRemoved:
	default:
		return false
	}
	return true
}
