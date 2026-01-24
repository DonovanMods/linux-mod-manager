package main

import (
	"fmt"
	"os"
	"path/filepath"

	"lmm/internal/core"
	"lmm/internal/source/nexusmods"
	"lmm/internal/tui"

	"github.com/spf13/cobra"
)

var (
	version = "0.1.0-dev"

	// Global flags
	configDir string
	dataDir   string
	gameID    string
	verbose   bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "lmm",
	Short: "Linux Mod Manager - Terminal-based mod manager for Linux",
	Long: `lmm is a terminal-based mod manager for Linux that provides both TUI and CLI
interfaces for searching, installing, updating, and managing game mods from
various sources like NexusMods.

Running lmm without arguments launches the interactive TUI.
Use subcommands for CLI operations.`,
	Version: version,
	RunE: func(cmd *cobra.Command, args []string) error {
		// No subcommand - launch TUI
		service, err := initService()
		if err != nil {
			return fmt.Errorf("initializing service: %w", err)
		}
		defer service.Close()

		return tui.Run(service)
	},
}

func init() {
	// Persistent flags available to all commands
	rootCmd.PersistentFlags().StringVar(&configDir, "config", "", "config directory (default: ~/.config/lmm)")
	rootCmd.PersistentFlags().StringVar(&dataDir, "data", "", "data directory (default: ~/.local/share/lmm)")
	rootCmd.PersistentFlags().StringVarP(&gameID, "game", "g", "", "game ID to operate on")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// initService creates and initializes the core service
func initService() (*core.Service, error) {
	cfg := getServiceConfig()

	// Ensure directories exist
	if err := os.MkdirAll(cfg.ConfigDir, 0755); err != nil {
		return nil, fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.CacheDir, 0755); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	svc, err := core.NewService(cfg)
	if err != nil {
		return nil, err
	}

	// Get NexusMods API key from environment or database
	apiKey := getNexusModsAPIKey(svc)

	// Register default mod sources
	svc.RegisterSource(nexusmods.New(nil, apiKey))

	return svc, nil
}

// getNexusModsAPIKey retrieves the API key from environment or database
func getNexusModsAPIKey(svc *core.Service) string {
	// Check environment variable first
	if key := os.Getenv("NEXUSMODS_API_KEY"); key != "" {
		return key
	}

	// Fall back to stored token
	token, err := svc.GetSourceToken("nexusmods")
	if err != nil || token == nil {
		return ""
	}

	return token.APIKey
}

// getServiceConfig returns the service configuration with defaults
func getServiceConfig() core.ServiceConfig {
	homeDir, _ := os.UserHomeDir()

	cfg := core.ServiceConfig{
		ConfigDir: configDir,
		DataDir:   dataDir,
		CacheDir:  "",
	}

	// Apply defaults
	if cfg.ConfigDir == "" {
		cfg.ConfigDir = filepath.Join(homeDir, ".config", "lmm")
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(homeDir, ".local", "share", "lmm")
	}
	cfg.CacheDir = filepath.Join(cfg.DataDir, "cache")

	return cfg
}

// requireGame ensures a game is specified
func requireGame(cmd *cobra.Command) error {
	if gameID == "" {
		return fmt.Errorf("no game specified; use --game or -g flag")
	}
	return nil
}
