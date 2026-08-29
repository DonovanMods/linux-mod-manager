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
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

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

Examples:
  lmm game detect`,
	Args: cobra.NoArgs,
	RunE: runGameDetect,
}

func init() {
	gameCmd.AddCommand(gameSetDefaultCmd)
	gameCmd.AddCommand(gameShowDefaultCmd)
	gameCmd.AddCommand(gameClearDefaultCmd)
	gameCmd.AddCommand(gameDetectCmd)
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

	// Load config
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	cfg, err := config.Load(svcCfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Update and save
	cfg.DefaultGame = newDefault
	if err := cfg.Save(svcCfg.ConfigDir); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	cmd.Printf("Default game set to: %s (%s)\n", game.Name, newDefault)
	return nil
}

func runGameShowDefault(cmd *cobra.Command, args []string) error {
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	cfg, err := config.Load(svcCfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.DefaultGame == "" {
		cmd.Println("No default game set")
		cmd.Println("Use 'lmm game set-default <game-id>' to set one")
		return nil
	}

	// Try to get game name for display
	if service, err := initService(cmd.Context()); err == nil {
		defer closeService(service)
		if game, err := service.GetGame(cfg.DefaultGame); err == nil {
			cmd.Printf("Default game: %s (%s)\n", game.Name, cfg.DefaultGame)
			return nil
		}
	}

	cmd.Printf("Default game: %s\n", cfg.DefaultGame)
	return nil
}

func runGameClearDefault(cmd *cobra.Command, args []string) error {
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	cfg, err := config.Load(svcCfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.DefaultGame == "" {
		cmd.Println("No default game was set")
		return nil
	}

	oldDefault := cfg.DefaultGame
	cfg.DefaultGame = ""
	if err := cfg.Save(svcCfg.ConfigDir); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	cmd.Printf("Cleared default game (was: %s)\n", oldDefault)
	return nil
}

func runGameDetect(cmd *cobra.Command, args []string) error {
	cmd.Println("Scanning Steam libraries...")
	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	games, warnings, err := app.DetectGames(cmd.Context(), svcCfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("detecting games: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	svc, err := initService(cmd.Context())
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer closeService(svc)

	reader := bufio.NewReader(os.Stdin)
	return doGameDetect(cmd.Context(), cmd, reader, svc, games)
}

// doGameDetect drives the interactive detect-and-select flow against an
// already-detected games list, so it can be tested without a real Steam
// library scan. service's ConfigDir is used both for the existing-games
// lookup (to mark/exclude already-configured games, #205 item 2) and for
// saving newly selected ones.
func doGameDetect(ctx context.Context, cmd *cobra.Command, reader *bufio.Reader, service *core.Service, games []domain.DetectedGame) error {
	if len(games) == 0 {
		cmd.Println("No moddable Steam games found.")
		return nil
	}

	existingGames, err := config.LoadGames(service.ConfigDir())
	if err != nil {
		return fmt.Errorf("loading games: %w", err)
	}

	cmd.Printf("Found %d moddable game(s):\n", len(games))
	for i, g := range games {
		marker := ""
		if _, ok := existingGames[g.Slug]; ok {
			marker = " " + colorGreen("[configured]")
		}
		cmd.Printf("  %d. %s (%s)%s\n", i+1, g.Name, g.Slug, marker)
		cmd.Printf("      Path: %s\n", g.InstallPath)
	}
	cmd.Print("Add games to config? [1,2/all/none]: ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	line = strings.TrimSpace(strings.ToLower(line))

	indices, err := gameDetectSelectionIndices(line, games, existingGames)
	if err != nil {
		return err
	}
	if len(indices) == 0 {
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
	for i := range result.Profiles {
		cmd.Printf("Added: %s (%s)\n", selected[i].Name, selected[i].Slug)
	}
	return applyErr
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
