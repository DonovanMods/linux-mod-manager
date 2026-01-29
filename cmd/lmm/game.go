package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/steam"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

var gameCmd = &cobra.Command{
	Use:   "game",
	Short: "Game management commands",
	Long:  `Commands for managing game configurations.`,
}

var gameSetDefaultCmd = &cobra.Command{
	Use:   "set-default <game-id>",
	Short: "Set the default game",
	Long: `Set the default game so you don't have to specify --game for every command.

Example:
  lmm game set-default skyrim-se
  lmm game set-default starrupture`,
	Args: cobra.ExactArgs(1),
	RunE: runGameSetDefault,
}

var gameShowDefaultCmd = &cobra.Command{
	Use:   "show-default",
	Short: "Show the current default game",
	Long:  `Display the currently configured default game.`,
	Args:  cobra.NoArgs,
	RunE:  runGameShowDefault,
}

var gameClearDefaultCmd = &cobra.Command{
	Use:   "clear-default",
	Short: "Clear the default game setting",
	Long:  `Remove the default game setting, requiring --game flag for all commands.`,
	Args:  cobra.NoArgs,
	RunE:  runGameClearDefault,
}

var gameDetectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Detect Steam games and add them to config",
	Long: `Scan Steam libraries for known moddable games and optionally add them to games.yaml.

Prompts for which games to add (e.g. 1,2 or all or none).`,
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
	newDefault := args[0]

	// Verify the game exists
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	game, err := service.GetGame(newDefault)
	if err != nil {
		return fmt.Errorf("game not found: %s", newDefault)
	}

	// Load config
	cfg, err := config.Load(getServiceConfig().ConfigDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Update and save
	cfg.DefaultGame = newDefault
	if err := cfg.Save(getServiceConfig().ConfigDir); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	cmd.Printf("Default game set to: %s (%s)\n", game.Name, newDefault)
	return nil
}

func runGameShowDefault(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(getServiceConfig().ConfigDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.DefaultGame == "" {
		cmd.Println("No default game set")
		cmd.Println("Use 'lmm game set-default <game-id>' to set one")
		return nil
	}

	// Try to get game name for display
	service, err := initService()
	if err == nil {
		defer service.Close()
		if game, err := service.GetGame(cfg.DefaultGame); err == nil {
			cmd.Printf("Default game: %s (%s)\n", game.Name, cfg.DefaultGame)
			return nil
		}
	}

	cmd.Printf("Default game: %s\n", cfg.DefaultGame)
	return nil
}

func runGameClearDefault(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(getServiceConfig().ConfigDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.DefaultGame == "" {
		cmd.Println("No default game was set")
		return nil
	}

	oldDefault := cfg.DefaultGame
	cfg.DefaultGame = ""
	if err := cfg.Save(getServiceConfig().ConfigDir); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	cmd.Printf("Cleared default game (was: %s)\n", oldDefault)
	return nil
}

func runGameDetect(cmd *cobra.Command, args []string) error {
	cmd.Println("Scanning Steam libraries...")
	configDir := getServiceConfig().ConfigDir
	games, err := steam.DetectGames(configDir)
	if err != nil {
		return fmt.Errorf("detecting games: %w", err)
	}
	if len(games) == 0 {
		cmd.Println("No moddable Steam games found.")
		return nil
	}
	cmd.Printf("Found %d moddable game(s):\n", len(games))
	for i, g := range games {
		cmd.Printf("  %d. %s (%s)\n", i+1, g.Name, g.Slug)
		cmd.Printf("      Path: %s\n", g.InstallPath)
	}
	cmd.Print("Add games to config? [1,2/all/none]: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" || line == "n" || line == "none" {
		cmd.Println("No games added.")
		return nil
	}
	var indices []int
	if line == "all" || line == "a" {
		for i := 1; i <= len(games); i++ {
			indices = append(indices, i)
		}
	} else {
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > len(games) {
				return fmt.Errorf("invalid selection: %q (use numbers 1-%d, all, or none)", part, len(games))
			}
			indices = append(indices, n)
		}
	}
	for _, n := range indices {
		g := games[n-1]
		game := &domain.Game{
			ID:          g.Slug,
			Name:        g.Name,
			InstallPath: g.InstallPath,
			ModPath:     g.ModPath,
			SourceIDs:   map[string]string{"nexusmods": g.NexusID},
			LinkMethod:  domain.LinkSymlink,
		}
		if err := config.SaveGame(configDir, game); err != nil {
			return fmt.Errorf("saving game %s: %w", g.Slug, err)
		}
		defaultProfile := &domain.Profile{
			Name:       "default",
			GameID:     g.Slug,
			Mods:       nil,
			LinkMethod: domain.LinkSymlink,
			IsDefault:  true,
		}
		if err := config.SaveProfile(configDir, defaultProfile); err != nil {
			return fmt.Errorf("creating default profile for %s: %w", g.Slug, err)
		}
		cmd.Printf("Added: %s (%s)\n", g.Name, g.Slug)
	}
	return nil
}
