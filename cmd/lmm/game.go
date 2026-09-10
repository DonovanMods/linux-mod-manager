package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var gameCmd = &cobra.Command{
	Use:   "game",
	Short: "Game management commands",
	Long: `Commands for managing game configurations: which games are known to
lmm, their install/mod paths and configured sources (games.yaml), and
the default game used when --game/-g is omitted.

Use 'lmm game add' to configure a game interactively, 'lmm game detect'
to find Steam installs automatically, or 'lmm game list' to see what's
already configured.`,
}

var gameSetDefaultCmd = &cobra.Command{
	Use:   "set-default <game-id>",
	Short: "Set the default game",
	Long: `Set the default game so you don't have to specify --game for every command.

Examples:
  lmm game set-default skyrim-se
  lmm game set-default starrupture`,
	Args: cobra.ExactArgs(1),
	RunE: runGameSetDefault,
}

var gameShowDefaultCmd = &cobra.Command{
	Use:   "show-default",
	Short: "Show the current default game",
	Long: `Display the currently configured default game, or a note that none is
set.

Examples:
  lmm game show-default`,
	Args: cobra.NoArgs,
	RunE: runGameShowDefault,
}

var gameClearDefaultCmd = &cobra.Command{
	Use:   "clear-default",
	Short: "Clear the default game setting",
	Long: `Remove the default game setting.

Every command that needs a game (install, search, list, and so on) then
requires an explicit --game/-g flag until a new default is set.

Examples:
  lmm game clear-default`,
	Args: cobra.NoArgs,
	RunE: runGameClearDefault,
}

var gameDetectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Detect Steam games and add them to config",
	Long: `Scan Steam libraries for moddable games and optionally add them to games.yaml.

Prompts for which games to add (e.g. 1,2 or all or none). Every listed
row is numbered, curated games first, and the prompt takes either a row
number or a game's Steam app id - the app id does not shift when the
listing widens. A game already configured (present in games.yaml) is
marked "[configured]" and is excluded from the default "all" selection,
since it needs no re-offering - but it stays listed, and you can still
name it explicitly to re-add/repair it (this replays the same games.yaml
+ default-profile overwrite 'lmm game add' always performs, so a repair
also resets the default profile's mod list). Each added game gets a
source mapping, the symlink link method, and an empty default profile;
edit games.yaml afterwards for anything more specific, including the
NexusMods slug if none was detected.

Use --all or --select to decide non-interactively (required under
--json, which never reads stdin): --all selects every not-yet-configured
game, the same set the interactive "all" answer selects; --select takes
the same values the prompt accepts (e.g. "1,2" or "1,1133870"),
including already-configured games for a repair.

A game whose Steam Workshop manifest shows items already downloaded is
mapped to the 'steamworkshop' source automatically, so 'lmm import
--workshop' can track them; --no-workshop suppresses that. Such a game is
LISTED even when lmm has no curated entry for it - the downloaded items
are what say it is moddable - with its item count beside it, and
selecting it configures it straight from the detection, exactly as 'lmm
game add --from-detected <app-id>' would.

--include-unknown also lists every OTHER installed Steam game, in the same
uncurated section (#206). Nothing tells lmm where those keep their mods,
so selecting one is refused with a pointer to 'lmm game add
--from-detected <app-id>', which prefills the name, install path, game id
and a default mod path and asks only for the source. Under --json,
--include-unknown with neither --all nor --select emits the detect LISTING
document (every candidate, known and unknown) instead of prompting: search
first, add second. A plain --json scan (no --include-unknown) carries the
listing's default rows only - pass the flag to see the rest, exactly as on
a terminal.

If a plain scan finds no known games but this machine has OTHER installed
Steam games lmm has no known-games entry for, it says so and names
--include-unknown, instead of reporting "No moddable Steam games found" -
the game is right there, just not curated yet. --all/--select in that same
situation - nothing here is selectable, so nothing gets added - carries a
warning saying so (under --json too) instead of a silent empty success.

Examples:
  lmm game detect
  lmm game detect --all
  lmm game detect --select 1,3
  lmm game detect --include-unknown
  lmm game detect --include-unknown --json
  lmm game detect --no-workshop`,
	Args: cobra.NoArgs,
	RunE: runGameDetect,
}

var (
	gameDetectAll            bool
	gameDetectSelect         string
	gameDetectIncludeUnknown bool
	gameDetectNoWorkshop     bool
)

func init() {
	gameCmd.AddCommand(gameSetDefaultCmd)
	gameCmd.AddCommand(gameShowDefaultCmd)
	gameCmd.AddCommand(gameClearDefaultCmd)
	gameCmd.AddCommand(gameDetectCmd)

	gameDetectCmd.Flags().BoolVar(&gameDetectAll, "all", false, "select every not-yet-configured detected game without prompting")
	gameDetectCmd.Flags().StringVar(&gameDetectSelect, "select", "", "comma-separated row numbers or Steam app ids to add/repair without prompting (see the printed listing)")
	gameDetectCmd.Flags().BoolVar(&gameDetectNoWorkshop, "no-workshop", false,
		"do not map games with subscribed Steam Workshop items to the steamworkshop source")
	gameDetectCmd.Flags().BoolVar(&gameDetectIncludeUnknown, "include-unknown", false,
		"also list installed games that are not in the known-games list (add one with 'game add --from-detected')")
	gameDetectCmd.MarkFlagsMutuallyExclusive("all", "select")

	rootCmd.AddCommand(gameCmd)
}

func runGameSetDefault(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doGameSetDefault(cmd, service, args[0])
	})
}

func doGameSetDefault(cmd *cobra.Command, service *core.Service, newDefault string) error {
	game, err := service.GetGame(newDefault)
	if err != nil {
		return fmt.Errorf("game not found: %s", newDefault)
	}

	if err := service.SetDefaultGame(cmd.Context(), newDefault); err != nil {
		return err
	}

	// Ruling 15: the SettingsResult document, in place of the console line.
	if jsonOutput {
		return emitJSON(&core.SettingsResult{DefaultGame: newDefault})
	}

	cmd.Printf("Default game set to: %s (%s)\n", game.Name, newDefault)
	return nil
}

// runGameShowDefault resolves the configured default game ID config-only
// (getServiceConfig + ServiceConfig.DefaultGame, no DB open), matching the
// pre-#309 behaviour: a malformed games.yaml or any other service-open
// failure must not turn a successful "no default"/"bare ID" readout into a
// hard error, and querying the default game must not create lmm.db/cache/
// as a read-only side effect (task A review round 1, Important 1/2). A
// service is opened only when a default IS set, and only best-effort, to
// enrich the id with its game's Name - a lookup or open failure keeps the
// bare-ID fallback doGameShowDefault already renders for an unresolvable
// Name.
func runGameShowDefault(cmd *cobra.Command, args []string) error {
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	id, err := svcCfg.DefaultGame(cmd.Context())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	info := &core.DefaultGame{}
	if id != "" {
		info.Set = true
		info.ID = id
		if service, err := initService(cmd.Context()); err == nil {
			defer closeService(service)
			if game, err := service.GetGame(id); err == nil {
				info.Name = game.Name
			}
		}
	}

	return doGameShowDefault(cmd, info)
}

// doGameShowDefault renders an already-resolved DefaultGame query (#309).
// Ruling 17 (recorded plain-text delta): the plain lines move from stderr -
// an accident of cmd.Println/Printf, which write to Command.OutOrStderr() -
// to stdout via cmd.OutOrStdout(); the bytes themselves are unchanged.
func doGameShowDefault(cmd *cobra.Command, info *core.DefaultGame) error {
	if jsonOutput {
		return emitJSON(info)
	}

	out := cmd.OutOrStdout()
	if !info.Set {
		//nolint:errcheck // best-effort console write
		_, _ = fmt.Fprintln(out, "No default game set")
		//nolint:errcheck // best-effort console write
		_, _ = fmt.Fprintln(out, "Use 'lmm game set-default <game-id>' to set one")
		return nil
	}
	if info.Name != "" {
		//nolint:errcheck // best-effort console write
		_, _ = fmt.Fprintf(out, "Default game: %s (%s)\n", info.Name, info.ID)
		return nil
	}
	//nolint:errcheck // best-effort console write
	_, _ = fmt.Fprintf(out, "Default game: %s\n", info.ID)
	return nil
}

func runGameClearDefault(cmd *cobra.Command, args []string) error {
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	defaultGame, err := svcCfg.DefaultGame(cmd.Context())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if defaultGame == "" {
		// Already clear is not an error, and a --json caller is still owed
		// the resulting state - which is the same empty default a real
		// clear produces.
		if jsonOutput {
			return emitJSON(&core.SettingsResult{})
		}
		cmd.Println("No default game was set")
		return nil
	}

	if err := svcCfg.ClearDefaultGame(cmd.Context()); err != nil {
		return err
	}

	// Ruling 15: the resulting state, not the "was: X" verb - see
	// core.SettingsResult's doc comment.
	if jsonOutput {
		return emitJSON(&core.SettingsResult{})
	}

	cmd.Printf("Cleared default game (was: %s)\n", defaultGame)
	return nil
}

func runGameDetect(cmd *cobra.Command, args []string) error {
	if !jsonOutput {
		cmd.Println("Scanning Steam libraries...")
	}
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	// The scan itself always goes wide (#206 Important 3): whether the
	// unknown rows get PRINTED or SELECTABLE still follows
	// gameDetectIncludeUnknown (below, in doGameDetect) exactly as before -
	// this only lets doGameDetect tell "nothing installed at all" apart from
	// "installed, but none of it is in the known-games list" so a plain scan
	// with only the latter can say so instead of a flat "found nothing".
	games, warnings, err := app.DetectGames(cmd.Context(), svcCfg.ConfigDir,
		app.DetectOptions{IncludeUnknown: true, NoWorkshop: gameDetectNoWorkshop})
	if err != nil {
		return fmt.Errorf("detecting games: %w", err)
	}
	// Ruling 15: nothing but the document under --json, so the scan's
	// warnings are carried INTO it (GameDetectResult.Warnings) rather than
	// printed to stderr and lost.
	if !jsonOutput {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}

	svc, err := initService(cmd.Context())
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer closeService(svc)

	reader := bufio.NewReader(os.Stdin)
	return doGameDetect(cmd.Context(), cmd, reader, svc, games, warnings)
}

// doGameDetect drives the interactive detect-and-select flow against an
// already-detected games list, so it can be tested without a real Steam
// library scan. service's ConfigDir is used both for the existing-games
// lookup (to mark/exclude already-configured games, #205 item 2) and for
// saving newly selected ones.
func doGameDetect(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, games []domain.DetectedGame, detectWarnings []string) error {
	// --include-unknown under --json, with no selection flag, is a pure
	// query: the LISTING document is the answer, exactly as `game add
	// --query` without --pick emits its catalog document rather than
	// refusing for want of a choice. One document on stdout either way
	// (Ruling 15), and it is the CLI's counterpart to GET
	// /api/v1/games/detect?all=1.
	if jsonOutput && gameDetectIncludeUnknown && !gameDetectAll && gameDetectSelect == "" {
		listing, err := service.GameDetectListing(ctx, games, detectWarnings,
			core.GameDetectListingOptions{IncludeUnknown: true})
		if err != nil {
			return err
		}
		return emitJSON(listing)
	}

	if len(games) == 0 {
		if jsonOutput {
			return emitJSON(&core.GameDetectResult{Warnings: detectWarnings})
		}
		cmd.Println("No moddable Steam games found.")
		return nil
	}

	// The rows this listing shows, curated first: every known-games match,
	// then the uncurated candidates the listing rule keeps (#368 - a game
	// with Steam Workshop items already downloaded, or everything left
	// under --include-unknown). `hidden` is what neither half kept, which
	// is what the "nothing here is selectable" message counts.
	listed, curatedCount, hidden := gameDetectRows(games, gameDetectIncludeUnknown)

	existingGames, err := service.LoadGamesFromDisk()
	if err != nil {
		return fmt.Errorf("loading games: %w", err)
	}

	// The listing is the prompt's own context; under --json there is no
	// prompt (Ruling 2 decides the selection from --all/--select or fails)
	// and no console text may sit beside the document.
	if !jsonOutput {
		printDetectedGames(cmd, listed, curatedCount, existingGames)
	}

	// A row nothing can configure without asking more of the user is
	// listed, never selected: `lmm game add --from-detected` is the flow
	// that collects a source (#368).
	if !anyAddable(listed) {
		// Nothing here is selectable, so there is no prompt to print and
		// no answer to read - the unknown section above already said what
		// to do next (when --include-unknown asked for it).
		unselectable := countUnaddable(games)
		if jsonOutput {
			result := &core.GameDetectResult{Warnings: detectWarnings}
			if msg := unknownOnlyDetectMessage(unselectable); msg != "" {
				// #206 review Minor 11: --all/--select under --json with
				// only uncurated games installed used to report an empty
				// success ({"saved":[],"profiles":[],"warnings":[]}) - the
				// user asked to add everything and was told nothing,
				// silently.
				result.Warnings = append(append([]string(nil), detectWarnings...), msg)
			}
			return emitJSON(result)
		}
		if !gameDetectIncludeUnknown && len(hidden) > 0 {
			// #206 Important 3: the game is right there, just not curated -
			// "No moddable Steam games found" would be a dead end for
			// exactly the user this feature exists for. Only for the
			// !gameDetectIncludeUnknown case: with the flag already on, the
			// unknown section above already said everything there is to
			// say, so repeating it here would be redundant.
			cmd.Println(unknownOnlyDetectMessage(unselectable))
			return nil
		}
		return nil
	}
	games = listed
	line, err := gameDetectAnswer(cmd, reader, len(games))
	if err != nil {
		return err
	}

	indices, err := gameDetectSelectionIndices(line, games, existingGames)
	if err != nil {
		return err
	}
	if len(indices) == 0 {
		if jsonOutput {
			return emitJSON(&core.GameDetectResult{Warnings: detectWarnings})
		}
		if line == "all" || line == "a" {
			cmd.Println("All detected games are already configured. No new games added.")
		} else {
			cmd.Println("No games added.")
		}
		return nil
	}

	selected := make([]domain.DetectedGame, len(indices))
	for i, n := range indices {
		selected[i] = games[n-1]
	}

	applied, result, applyErr := applyDetectSelection(ctx, service, selected)
	// The curated half goes through ApplyGameDetect, which converts and
	// persists one game at a time and stops at the first failing game
	// (conversion or persistence); result.Profiles holds exactly the games
	// that fully completed (games.yaml write + default profile),
	// one-for-one with `applied`'s leading entries in the same order - so
	// this prints "Added:" for precisely the games doGameDetect's old
	// interleaved loop would have printed before hitting the same error.
	//
	// The scan's warnings lead: they happened before anything this result
	// reports. Merged in on both the success and the partial-failure path,
	// so a --json error envelope's details carries them too.
	result.Warnings = append(append([]string(nil), detectWarnings...), result.Warnings...)
	if jsonOutput {
		if applyErr != nil {
			return &core.GameDetectPartialError{Err: applyErr, Result: result}
		}
		return emitJSON(result)
	}
	for i := range result.Profiles {
		cmd.Printf("Added: %s (%s)\n", applied[i].Name, applied[i].Slug)
	}
	return applyErr
}

// applyDetectSelection persists one detect selection, which since #368 can
// hold two kinds of row.
//
//   - A CURATED row is configured from its known-games entry, in one gated
//     ApplyGameDetect call over the whole curated subset - the batch
//     semantics (one mutation slot for the lot, stop at the first failure)
//     that call has always had.
//   - An UNCURATED row - listed because detection prefilled a source map
//     for it, today #269's `steamworkshop: <appid>` - has no curated entry
//     to configure from, so it takes exactly the path `lmm game add
//     --from-detected <app-id>` takes: core.GameSpecFromDetected, then
//     AddGame. The CLI derives no slug, mod path or source map of its own.
//
// It returns the rows it attempted, in the order their outcomes land in
// result (curated first, then uncurated - not the order they were typed),
// so the caller can name each added game beside its result row.
func applyDetectSelection(ctx context.Context, service *core.Service, selected []domain.DetectedGame) ([]domain.DetectedGame, *core.GameDetectResult, error) {
	curated, uncurated := splitDetectedGames(selected)
	applied := append(append([]domain.DetectedGame(nil), curated...), uncurated...)

	result := &core.GameDetectResult{}
	if len(curated) > 0 {
		var err error
		result, err = service.ApplyGameDetect(ctx, curated)
		if err != nil {
			return applied, result, err
		}
	}
	for _, g := range uncurated {
		entry, err := service.AddGame(ctx, core.GameSpecFromDetected(g, core.GameSpec{}))
		if err != nil {
			return applied, result, fmt.Errorf("adding detected game %s: %w", g.Slug, err)
		}
		// AddGame creates the same "default" profile ApplyGameDetect does,
		// so the two halves report identically.
		result.Saved = append(result.Saved, entry.ID)
		result.Profiles = append(result.Profiles, entry.ID+"/default")
	}
	return applied, result, nil
}

// gameDetectAnswer resolves the selection line gameDetectSelectionIndices
// parses: --all/--select decide it non-interactively (in that priority -
// mutually exclusive by the flag definition, so both set never reaches
// here) with no prompt printed or read at all; otherwise it prints the
// prompt and reads an answer via readPromptLineFrom, the CLI's one choke
// point for the non-interactive rule (v2 Phase 3 Ruling 2) - under --json
// with neither flag, that call returns core.ErrConfirmationRequired without
// ever touching reader. count is the number of known (selectable) rows the
// listing above just printed - the prompt's own range must match it (#206
// review Minor 6: a fixed "[1,2/all/none]" advertised an index that did not
// exist whenever the count was not exactly 2, most confusingly with
// --include-unknown's unnumbered rows sitting right above it), using the
// same "1-N" shape gameDetectSelectionIndices' own error already does.
func gameDetectAnswer(cmd *cobra.Command, reader *bufio.Reader, count int) (string, error) {
	switch {
	case gameDetectAll:
		return "all", nil
	case gameDetectSelect != "":
		return gameDetectSelect, nil
	default:
		if !jsonOutput {
			cmd.Printf("Add games to config? [1-%d/app id/all/none]: ", count)
		}
		return readPromptLineFrom(reader)
	}
}

// splitDetectedGames separates a scan into the curated rows (the
// known-games matches) and the rest (#206). Order is preserved within each
// half, so the printed numbering is exactly the numbering
// core.GameDetectListing assigns.
func splitDetectedGames(games []domain.DetectedGame) (known, unknown []domain.DetectedGame) {
	for _, g := range games {
		if g.Known {
			known = append(known, g)
			continue
		}
		unknown = append(unknown, g)
	}
	return known, unknown
}

// gameDetectRows orders a scan into the rows the CLI lists and numbers
// (#368): every curated row first, then the uncurated ones the listing
// keeps - domain.DetectedGame.Listable by default, everything under
// --include-unknown. curatedCount is where the second section starts, and
// hidden is what neither half kept.
//
// Curated first, then continuous numbering over the whole thing, is what
// makes the printed list and the prompt agree: before #368 the uncurated
// rows were printed unnumbered and the prompt rejected the only handle
// they had (their Steam app id), so a listed row could not be chosen at
// all. Numbers therefore shift when --include-unknown widens the list,
// which is exactly why the prompt also takes an app id: that never shifts.
func gameDetectRows(games []domain.DetectedGame, includeUnknown bool) (listed []domain.DetectedGame, curatedCount int, hidden []domain.DetectedGame) {
	known, unknown := splitDetectedGames(games)
	listed = append(listed, known...)
	for _, g := range unknown {
		if includeUnknown || g.Listable() {
			listed = append(listed, g)
			continue
		}
		hidden = append(hidden, g)
	}
	return listed, len(known), hidden
}

// anyAddable reports whether any listed row can actually be configured
// from the prompt - i.e. whether there is a prompt to print at all.
func anyAddable(listed []domain.DetectedGame) bool {
	for _, g := range listed {
		if g.Addable() {
			return true
		}
	}
	return false
}

// countUnaddable counts the scan's rows a detect selection cannot
// configure, which is what unknownOnlyDetectMessage reports on. A
// Workshop-bearing row is NOT one of them since #368 - detection prefilled
// its source - so it is not counted as something the user still has to go
// elsewhere for.
func countUnaddable(games []domain.DetectedGame) int {
	n := 0
	for _, g := range games {
		if !g.Addable() {
			n++
		}
	}
	return n
}

// printDetectedGames renders the numbered listing the prompt reads against:
// the curated rows, then (separated by a blank line and its own header) the
// uncurated ones, numbered continuously through both. curatedCount is where
// the second section begins.
//
// The uncurated header still names `lmm game add --from-detected <app-id>`:
// that flow is the only way to configure a row detection found no source
// for, and it is also what a user wants when the mod path or source needs
// correcting.
func printDetectedGames(cmd *cobra.Command, listed []domain.DetectedGame, curatedCount int, existingGames map[string]*domain.Game) {
	if len(listed) == 0 {
		return
	}
	cmd.Printf("Found %d moddable game(s):\n", len(listed))
	for i, g := range listed {
		if i == curatedCount {
			if i > 0 {
				cmd.Println()
			}
			cmd.Printf("Installed but not in the known-games list - pick one by number or app id here, or add it with `lmm game add --from-detected <app-id>`:\n")
		}
		printDetectedGameRow(cmd, i+1, g, existingGames, i >= curatedCount)
	}
}

// printDetectedGameRow renders one listed row. showAppID is set for the
// uncurated section, where the app id is the handle that does not shift
// when the scan finds one more game - and the one `lmm game add
// --from-detected` takes.
func printDetectedGameRow(cmd *cobra.Command, n int, g domain.DetectedGame, existingGames map[string]*domain.Game, showAppID bool) {
	marker := ""
	if _, ok := existingGames[g.Slug]; ok {
		marker = " " + colorGreen("[configured]")
	}
	appID := ""
	if showAppID && g.SteamAppID != "" {
		appID = "  app id " + g.SteamAppID
	}
	cmd.Printf("  %d. %s (%s)%s%s\n", n, g.Name, g.Slug, appID, marker)
	cmd.Printf("      Path: %s\n", g.InstallPath)
	if g.WorkshopItems > 0 {
		cmd.Printf("      Steam Workshop: %d %s\n", g.WorkshopItems, pluralItems(g.WorkshopItems))
	}
}

// pluralItems is the one-word plural the Workshop count line needs; "1
// items" in the listing a user reads before choosing is exactly the kind of
// sloppiness that makes a count look fabricated.
func pluralItems(n int) string {
	if n == 1 {
		return "item"
	}
	return "items"
}

// unknownOnlyDetectMessage names what to do when a detect scan found
// installed games but NONE of them are known - so there is nothing
// selectable, plain-text or --json (#206 Important 3, review Minor 11) -
// or "" when unknownCount is 0, meaning there is nothing to say at all.
// Wording depends on whether --include-unknown was already given: if not,
// it names the flag that would list them; if so, the caller already saw
// (or, under --json with no --all/--select, would see via the LISTING
// document) every candidate, but --all/--select still selected nothing
// since none of them have a known-games entry to select, so this points at
// the per-game add path instead.
func unknownOnlyDetectMessage(unknownCount int) string {
	if unknownCount == 0 {
		return ""
	}
	if gameDetectIncludeUnknown {
		return fmt.Sprintf("%d installed game(s) are not in the known-games list and cannot be added by --all/--select; add them individually with `lmm game add --from-detected <app-id>`.", unknownCount)
	}
	return fmt.Sprintf("%d installed game(s) are not in the known-games list; run `lmm game detect --include-unknown` to add them.", unknownCount)
}

// gameDetectSelectionIndices parses the detect prompt's answer into the
// 1-based indices into games to add/repair. games is the LISTED set, in
// printed order (gameDetectRows): curated rows first, then the uncurated
// ones the listing kept.
//
// Each comma-separated part is a row NUMBER or a Steam APP ID (#368). Both
// spellings are offered because neither is sufficient alone: the number is
// what the listing prints beside the row and what --select has always
// taken, but it shifts when --include-unknown widens the list, while the
// app id is stable and is the value the uncurated section prints and `lmm
// game add --from-detected` takes. A number that IS a valid row number
// wins over an app id that happens to have the same digits - real Steam
// app ids start well above any plausible row count, and the row is
// reachable by its app id either way.
//
// A row nothing can configure - uncurated, and detection found no source
// for it - is refused by name, pointing at the flow that asks for the
// source, rather than writing an unusable games.yaml entry.
//
// "all"/"a" defaults to every NOT-yet-configured, addable game (#205 item
// 2): a game already in games.yaml doesn't need re-offering by default,
// since silently re-selecting it would replay doGameDetect's unconditional
// games.yaml + default-profile overwrite against a game the user already
// set up - possibly wiping its default profile's installed-mod list for no
// reason the user asked for. An explicit selection (e.g. "2,5") is NOT
// filtered: naming an already-configured game is how a user deliberately
// repairs/re-adds it, mirroring the same overwrite 'lmm game add' has
// always performed unconditionally (it has no existing-ID guard either) -
// #205 asks only for visibility into what's already configured, not a
// merge-preserving repair.
//
// A duplicate is refused: with two spellings for one row it is easy to
// name the same game twice by accident, and the second add would fail on
// ErrGameExists after the first had already been written. core's own
// SelectDetectedGames refuses one for the same reason.
func gameDetectSelectionIndices(line string, games []domain.DetectedGame, existingGames map[string]*domain.Game) ([]int, error) {
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" || line == "n" || line == "none" {
		return nil, nil
	}
	var indices []int
	if line == "all" || line == "a" {
		for i, g := range games {
			if _, ok := existingGames[g.Slug]; ok {
				continue
			}
			if !g.Addable() {
				continue
			}
			indices = append(indices, i+1)
		}
		return indices, nil
	}
	seen := make(map[int]bool, len(games))
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		n, err := gameDetectSelector(part, games)
		if err != nil {
			return nil, err
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate selection: %q (row %d is already selected)", part, n)
		}
		seen[n] = true
		indices = append(indices, n)
	}
	return indices, nil
}

// gameDetectSelector resolves one part of a selection line to a 1-based row
// number, or says what would have been accepted.
func gameDetectSelector(part string, games []domain.DetectedGame) (int, error) {
	n, numErr := strconv.Atoi(part)
	if numErr != nil || n < 1 || n > len(games) {
		n = 0
		for i, g := range games {
			if g.SteamAppID != "" && g.SteamAppID == part {
				n = i + 1
				break
			}
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("invalid selection: %q (use a row number 1-%d, a Steam app id, all, or none)", part, len(games))
	}
	if g := games[n-1]; !g.Addable() {
		return 0, fmt.Errorf("invalid selection: %q - %s (Steam app id %s) is not in the known-games list and detection found no mod source for it; add it with `lmm game add --from-detected %s`, which asks for the source",
			part, g.Name, g.SteamAppID, g.SteamAppID)
	}
	return n, nil
}
