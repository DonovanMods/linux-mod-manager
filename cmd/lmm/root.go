package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

// ErrCancelled is returned when the user cancels an operation (e.g. prompt declined).
// When returned from a command, Execute exits with code 2.
var ErrCancelled = errors.New("cancelled")

var (
	version = "1.0.0"

	// Global flags
	configDir  string
	dataDir    string
	gameID     string
	verbose    bool
	noHooks    bool
	jsonOutput bool
	noColor    bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "lmm",
	Short: "Linux Mod Manager - Terminal-based mod manager for Linux",
	Long: `lmm is a terminal-based mod manager for Linux for searching, installing,
updating, and managing game mods from various sources like NexusMods.

Use subcommands for operations. Run 'lmm --help' for available commands.`,
	Version: version,
}

func init() {
	// Persistent flags available to all commands
	rootCmd.PersistentFlags().StringVar(&configDir, "config", "", "config directory (default: ~/.config/lmm)")
	rootCmd.PersistentFlags().StringVar(&dataDir, "data", "", "data directory (default: ~/.local/share/lmm)")
	rootCmd.PersistentFlags().StringVarP(&gameID, "game", "g", "", "game ID to operate on")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVar(&noHooks, "no-hooks", false, "disable all hooks")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output in JSON format (list, status, search)")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable colored output")
}

// colorEnabled returns true if colored output should be used (respects --no-color and NO_COLOR env).
// NO_COLOR: if set (any value), color is disabled per https://no-color.org
func colorEnabled() bool {
	if noColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return true
}

// Execute runs the root command. Exit codes: 0 = success, 1 = error, 2 = user cancelled.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		if errors.Is(err, ErrCancelled) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// initService creates and initializes the core service
func initService() (*core.Service, error) {
	cfg, err := getServiceConfig()
	if err != nil {
		return nil, err
	}

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

// getServiceConfig returns the service configuration with defaults.
// Returns an error if UserHomeDir fails and defaults are needed.
func getServiceConfig() (core.ServiceConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return core.ServiceConfig{}, fmt.Errorf("home directory: %w", err)
	}

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

	// Check config file for custom cache path
	if appConfig, err := config.Load(cfg.ConfigDir); err == nil && appConfig.CachePath != "" {
		cfg.CacheDir = appConfig.CachePath
	} else {
		cfg.CacheDir = filepath.Join(cfg.DataDir, "cache")
	}

	return cfg, nil
}

// requireGame ensures a game is specified, checking config for default if not provided
func requireGame(cmd *cobra.Command) error {
	if gameID != "" {
		return nil
	}

	svcCfg, err := getServiceConfig()
	if err != nil {
		return err
	}
	cfg, err := config.Load(svcCfg.ConfigDir)
	if err == nil && cfg.DefaultGame != "" {
		gameID = cfg.DefaultGame
		if verbose {
			fmt.Printf("Using default game: %s\n", gameID)
		}
		return nil
	}

	return fmt.Errorf("no game specified; use --game or -g flag, or set a default with 'lmm game set-default <game-id>'")
}

// profileOrDefault returns the given profile name, or "default" if empty
func profileOrDefault(profile string) string {
	if profile == "" {
		return "default"
	}
	return profile
}
