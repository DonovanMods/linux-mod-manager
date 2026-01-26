package main

import (
	"fmt"

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

func init() {
	gameCmd.AddCommand(gameSetDefaultCmd)
	gameCmd.AddCommand(gameShowDefaultCmd)
	gameCmd.AddCommand(gameClearDefaultCmd)
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
