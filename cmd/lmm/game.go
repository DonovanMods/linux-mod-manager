package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/app"
	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

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

Examples:
  lmm game detect
  lmm game detect --all
  lmm game detect --select 1,3`,
	Args: cobra.NoArgs,
	RunE: runGameDetect,
}

var (
	gameDetectAll    bool
	gameDetectSelect string
)

func init() {
	gameCmd.AddCommand(gameSetDefaultCmd)
	gameCmd.AddCommand(gameShowDefaultCmd)
	gameCmd.AddCommand(gameClearDefaultCmd)
	gameCmd.AddCommand(gameDetectCmd)

	gameDetectCmd.Flags().BoolVar(&gameDetectAll, "all", false, "select every not-yet-configured detected game without prompting")
	gameDetectCmd.Flags().StringVar(&gameDetectSelect, "select", "", "comma-separated 1-based indices to add/repair without prompting (see the printed listing)")
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

func runGameShowDefault(cmd *cobra.Command, args []string) error {
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	defaultGame, err := svcCfg.DefaultGame(cmd.Context())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if defaultGame == "" {
		cmd.Println("No default game set")
		cmd.Println("Use 'lmm game set-default <game-id>' to set one")
		return nil
	}

	// Try to get game name for display
	if service, err := initService(cmd.Context()); err == nil {
		defer closeService(service)
		if game, err := service.GetGame(defaultGame); err == nil {
			cmd.Printf("Default game: %s (%s)\n", game.Name, defaultGame)
			return nil
		}
	}

	cmd.Printf("Default game: %s\n", defaultGame)
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
	games, warnings, err := app.DetectGames(cmd.Context(), svcCfg.ConfigDir)
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
	if len(games) == 0 {
		if jsonOutput {
			return emitJSON(&core.GameDetectResult{Warnings: detectWarnings})
		}
		cmd.Println("No moddable Steam games found.")
		return nil
	}

	existingGames, err := service.LoadGamesFromDisk()
	if err != nil {
		return fmt.Errorf("loading games: %w", err)
	}

	// The listing is the prompt's own context; under --json there is no
	// prompt (Ruling 2 decides the selection from --all/--select or fails)
	// and no console text may sit beside the document.
	if !jsonOutput {
		cmd.Printf("Found %d moddable game(s):\n", len(games))
		for i, g := range games {
			marker := ""
			if _, ok := existingGames[g.Slug]; ok {
				marker = " " + colorGreen("[configured]")
			}
			cmd.Printf("  %d. %s (%s)%s\n", i+1, g.Name, g.Slug, marker)
			cmd.Printf("      Path: %s\n", g.InstallPath)
		}
	}
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
			return &gameDetectPartialError{err: applyErr, result: result}
		}
		return emitJSON(result)
	}
	for i := range result.Profiles {
		cmd.Printf("Added: %s (%s)\n", selected[i].Name, selected[i].Slug)
	}
	return applyErr
}

// gameDetectPartialError reports a `game detect --json` run that failed
// partway through ApplyGameDetect: result still names exactly the games
// that were fully persisted (games.yaml write + default profile) before err
// stopped it, so the --json error envelope's "details" can say what was
// saved instead of only that the run failed - mirroring the plain-text
// path's own partial-success contract (the "Added:" loop above prints every
// game result.Profiles names, even on failure). Follows the
// core.ConflictError / errorDetails convention (jsonout.go): Unwrap exposes
// err for errors.Is/As, Details() any is the unnamed interface errorDetails
// picks up automatically.
type gameDetectPartialError struct {
	err    error
	result *core.GameDetectResult
}

// Error returns the wrapped ApplyGameDetect failure's own message.
func (e *gameDetectPartialError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped ApplyGameDetect error for errors.Is/As.
func (e *gameDetectPartialError) Unwrap() error { return e.err }

// Details returns the partial GameDetectResult - what was saved before the
// failure - for the --json error envelope's "details" field.
func (e *gameDetectPartialError) Details() any { return e.result }

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
