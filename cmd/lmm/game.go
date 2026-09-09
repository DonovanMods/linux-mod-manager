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
	Long: `Scan Steam libraries for known moddable games and optionally add them to games.yaml.

Prompts for which games to add (e.g. 1,2 or all or none). A game already
configured (present in games.yaml) is marked "[configured]" and is
excluded from the default "all" selection, since it needs no re-offering
- but it stays listed, and you can still name its number explicitly to
re-add/repair it (this replays the same games.yaml + default-profile
overwrite 'lmm game add' always performs, so a repair also resets the
default profile's mod list). Each added game gets a NexusMods source
mapping, the symlink link method, and an empty default profile; edit
games.yaml afterwards for anything more specific, including the
NexusMods slug if none was detected.

Use --all or --select to decide non-interactively (required under
--json, which never reads stdin): --all selects every not-yet-configured
game, the same set the interactive "all" answer selects; --select takes
the same 1-based indices the prompt accepts (e.g. "1,2"), including
already-configured games' numbers for a repair.

--include-unknown also lists every OTHER installed Steam game, in its own
section (#206). Those are not numbered and cannot be selected here -
nothing tells lmm where they keep their mods - so each is listed with its
Steam app id for 'lmm game add --from-detected' with that app id, which
prefills the name, install path, game id and a default mod path and asks
only for the source. Under --json, --include-unknown with neither --all nor
--select emits the detect LISTING document (every candidate, known and
unknown) instead of prompting: search first, add second.

Examples:
  lmm game detect
  lmm game detect --all
  lmm game detect --select 1,3
  lmm game detect --include-unknown
  lmm game detect --include-unknown --json`,
	Args: cobra.NoArgs,
	RunE: runGameDetect,
}

var (
	gameDetectAll            bool
	gameDetectSelect         string
	gameDetectIncludeUnknown bool
)

func init() {
	gameCmd.AddCommand(gameSetDefaultCmd)
	gameCmd.AddCommand(gameShowDefaultCmd)
	gameCmd.AddCommand(gameClearDefaultCmd)
	gameCmd.AddCommand(gameDetectCmd)

	gameDetectCmd.Flags().BoolVar(&gameDetectAll, "all", false, "select every not-yet-configured detected game without prompting")
	gameDetectCmd.Flags().StringVar(&gameDetectSelect, "select", "", "comma-separated 1-based indices to add/repair without prompting (see the printed listing)")
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
	games, warnings, err := app.DetectGames(cmd.Context(), svcCfg.ConfigDir,
		app.DetectOptions{IncludeUnknown: gameDetectIncludeUnknown})
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

	// Only the known rows are selectable: ApplyGameDetect configures a game
	// from its known-games entry, and an unknown candidate has none (#206).
	// Splitting here - rather than filtering in the scan - is what lets the
	// unknown ones still be LISTED, with the one thing that makes them
	// actionable: their Steam app id.
	known, unknown := splitDetectedGames(games)

	existingGames, err := service.LoadGamesFromDisk()
	if err != nil {
		return fmt.Errorf("loading games: %w", err)
	}

	// The listing is the prompt's own context; under --json there is no
	// prompt (Ruling 2 decides the selection from --all/--select or fails)
	// and no console text may sit beside the document.
	if !jsonOutput {
		if len(known) > 0 {
			cmd.Printf("Found %d moddable game(s):\n", len(known))
			for i, g := range known {
				marker := ""
				if _, ok := existingGames[g.Slug]; ok {
					marker = " " + colorGreen("[configured]")
				}
				cmd.Printf("  %d. %s (%s)%s\n", i+1, g.Name, g.Slug, marker)
				cmd.Printf("      Path: %s\n", g.InstallPath)
			}
		}
		printUnknownDetectedGames(cmd, unknown)
	}

	if len(known) == 0 {
		// Nothing here is selectable, so there is no prompt to print and
		// no answer to read - the unknown section above already said what
		// to do next.
		if jsonOutput {
			return emitJSON(&core.GameDetectResult{Warnings: detectWarnings})
		}
		return nil
	}
	games = known
	line, err := gameDetectAnswer(cmd, reader)
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

	result, applyErr := service.ApplyGameDetect(ctx, selected)
	// ApplyGameDetect converts and persists one game at a time, stopping at
	// the first failing game (conversion or persistence); result.Profiles
	// holds exactly the games that fully completed (games.yaml write +
	// default profile), one-for-one with selected's leading entries in the
	// same order - so this prints "Added:" for precisely the games
	// doGameDetect's old interleaved loop would have printed before hitting
	// the same error.
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
		cmd.Printf("Added: %s (%s)\n", selected[i].Name, selected[i].Slug)
	}
	return applyErr
}

// gameDetectAnswer resolves the selection line gameDetectSelectionIndices
// parses: --all/--select decide it non-interactively (in that priority -
// mutually exclusive by the flag definition, so both set never reaches
// here) with no prompt printed or read at all; otherwise it prints the
// prompt and reads an answer via readPromptLineFrom, the CLI's one choke
// point for the non-interactive rule (v2 Phase 3 Ruling 2) - under --json
// with neither flag, that call returns core.ErrConfirmationRequired without
// ever touching reader.
func gameDetectAnswer(cmd *cobra.Command, reader *bufio.Reader) (string, error) {
	switch {
	case gameDetectAll:
		return "all", nil
	case gameDetectSelect != "":
		return gameDetectSelect, nil
	default:
		if !jsonOutput {
			cmd.Print("Add games to config? [1,2/all/none]: ")
		}
		return readPromptLineFrom(reader)
	}
}

// splitDetectedGames separates a scan into the rows a detect selection can
// name (the known-games matches) and the rest (#206). Order is preserved
// within each half, so the printed numbering is exactly the numbering
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

// printUnknownDetectedGames renders the "installed, but lmm has no curated
// entry for it" section. It is deliberately keyed by Steam app id rather
// than by a number: the app id is what `lmm game add --from-detected`
// takes, and it does not shift when the scan finds one more game.
func printUnknownDetectedGames(cmd *cobra.Command, unknown []domain.DetectedGame) {
	if len(unknown) == 0 {
		return
	}
	cmd.Printf("\nInstalled but not in the known-games list - add with `lmm game add --from-detected <app-id>`:\n")
	for _, g := range unknown {
		cmd.Printf("  %s  %s (%s)\n", g.SteamAppID, g.Name, g.Slug)
		cmd.Printf("      Path: %s\n", g.InstallPath)
	}
}

// gameDetectSelectionIndices parses the detect prompt's answer into the
// 1-based indices into games to add/repair.
//
// "all"/"a" defaults to every NOT-yet-configured game (#205 item 2): a game
// already in games.yaml doesn't need re-offering by default, since silently
// re-selecting it would replay doGameDetect's unconditional games.yaml +
// default-profile overwrite against a game the user already set up -
// possibly wiping its default profile's installed-mod list for no reason
// the user asked for. An explicit numeric selection (e.g. "2,5") is NOT
// filtered: naming an already-configured game's number is how a user
// deliberately repairs/re-adds it, mirroring the same overwrite 'lmm game
// add' has always performed unconditionally (it has no existing-ID guard
// either) - #205 asks only for visibility into what's already configured,
// not a merge-preserving repair.
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
			indices = append(indices, i+1)
		}
		return indices, nil
	}
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > len(games) {
			return nil, fmt.Errorf("invalid selection: %q (use numbers 1-%d, all, or none)", part, len(games))
		}
		indices = append(indices, n)
	}
	return indices, nil
}
