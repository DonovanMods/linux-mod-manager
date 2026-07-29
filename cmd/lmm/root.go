package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

// ErrCancelled is returned when the user cancels an operation (e.g. prompt declined).
// When returned from a command, Execute exits with code 2.
var ErrCancelled = errors.New("cancelled")

// ErrReported marks a failure the command has already communicated in its own
// output format. Execute still exits 1, but prints nothing further.
//
// Needed because the two would otherwise collide: under --json, Execute prints
// {"error":"..."} on any returned error, so a command that has already written
// a complete JSON document would emit a second one and break any caller piping
// stdout to a parser.
var ErrReported = errors.New("already reported")

var (
	version = "1.22.1"

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
updating, and managing game mods from sources like NexusMods and CurseForge,
plus user-defined custom sources (directory scans, static manifests, or REST
APIs — see 'lmm source --help').

Both a CLI (this command tree) and an interactive TUI ('lmm tui') are
available; run 'lmm COMMAND --help' for details on any subcommand.

EXIT CODES

    0  success
    1  error
    2  cancelled by the user (e.g. declined a confirmation prompt)

FILES

    ~/.config/lmm/        Configuration: games.yaml, config.yaml, per-game
                          profiles, and sources/*.yaml (custom source
                          definitions). Override with --config.
    ~/.local/share/lmm/   Data: lmm.db (mod metadata and auth tokens),
                          cache/ (downloaded and extracted mod files), and
                          downloads/ (staging area for in-flight downloads
                          and archive extraction). Override with --data.`,
	Version:       version,
	SilenceUsage:  true, // Runtime errors should not print usage
	SilenceErrors: true, // We handle error output in Execute()
}

func init() {
	// Persistent flags available to all commands
	rootCmd.PersistentFlags().StringVar(&configDir, "config", "", "config directory (default: ~/.config/lmm)")
	rootCmd.PersistentFlags().StringVar(&dataDir, "data", "", "data directory (default: ~/.local/share/lmm)")
	rootCmd.PersistentFlags().StringVarP(&gameID, "game", "g", "", "game ID to operate on")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVar(&noHooks, "no-hooks", false, "disable all hooks")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output in JSON format (list, status, search, update, conflicts, verify, mod show, source list)")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable colored output (NO_COLOR env is also honored)")
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

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
)

// colorGreen returns s with green ANSI when color is enabled, otherwise s.
func colorGreen(s string) string {
	if !colorEnabled() {
		return s
	}
	return ansiGreen + s + ansiReset
}

// colorRed returns s with red ANSI when color is enabled, otherwise s.
func colorRed(s string) string {
	if !colorEnabled() {
		return s
	}
	return ansiRed + s + ansiReset
}

// colorYellow returns s with yellow ANSI when color is enabled, otherwise s.
func colorYellow(s string) string {
	if !colorEnabled() {
		return s
	}
	return ansiYellow + s + ansiReset
}

// Execute runs the root command. Exit codes: 0 = success, 1 = error, 2 = user cancelled.
// When --json is set and an error occurs, prints {"error":"..."} to stdout before exiting.
// Cancellation (ErrCancelled or context.Canceled) exits with code 2 without printing JSON,
// since it is a user action, not an error. SIGINT/SIGTERM cancel the per-command context
// so RunE handlers using cmd.Context() can stop in-flight I/O cleanly.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runRoot(ctx); err != nil {
		if errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) {
			os.Exit(2)
		}
		reportError(err)
		os.Exit(1)
	}
}

// reportError prints err in the active output format, unless the command
// already reported it (ErrReported).
func reportError(err error) {
	if errors.Is(err, ErrReported) {
		return
	}
	if jsonOutput {
		fmt.Printf(`{"error":%q}`+"\n", err.Error())
	} else {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
}

// runRoot dispatches to rootCmd with the given context. Split out so tests can
// drive the command tree without going through signal.NotifyContext.
func runRoot(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
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
	// Owner-only: this holds lmm.db, whose auth_tokens table stores API keys in
	// plaintext, plus the downloads staging root. Also closes the window between
	// SQLite creating the DB at 0644 and the db package tightening it.
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, so installs predating
	// the line above keep a 0755 data dir without this.
	if err := os.Chmod(cfg.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("restricting data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.CacheDir, 0755); err != nil {
		return nil, fmt.Errorf("creating cache dir: %w", err)
	}

	svc, err := core.NewService(cfg)
	if err != nil {
		return nil, err
	}

	// Register mod sources
	registerSources(svc, cfg.ConfigDir)

	return svc, nil
}

// builtinSourceFactories constructs each built-in source keyless — the
// unified pipeline resolves and applies API keys post-construction via
// registerSource's SetAPIKey seam, the same path custom sources use.
var builtinSourceFactories = []func() source.ModSource{
	func() source.ModSource { return nexusmods.New(nil, "") },
	func() source.ModSource { return curseforge.New(nil, "") },
}

// registerSources registers all available mod sources with the service
// through one ordered pipeline: built-ins first (so the collision rule's
// "first wins" preserves their identity against a same-id custom
// definition), then user-defined sources from <configDir>/sources/.
func registerSources(svc *core.Service, cfgDir string) {
	for _, factory := range builtinSourceFactories {
		registerSource(svc, factory())
	}

	registerCustomSources(svc, cfgDir)
}

// registerSource runs src through the shared registration steps used for
// both built-in and custom sources: collision check (first registration
// wins, warning on customSourceWarnWriter) → API-key resolution (env var via
// envKeyFor, falling back to the stored DB token) → SetAPIKey when the
// source accepts one → RegisterSource.
func registerSource(svc *core.Service, src source.ModSource) {
	id := src.ID()
	// Custom sources are constructed (custom.New) by the caller before this
	// runs; a definition that both collides with an existing ID AND fails to
	// construct reports its construction error instead, since it never
	// reaches this check — construct-then-check means construction wins.
	if _, err := svc.GetSource(id); err == nil {
		fmt.Fprintf(customSourceWarnWriter(), "warning: skipping source %q: id already in use\n", id)
		return
	}
	// Gate the token lookup on the source actually being able to use a key:
	// skips a pointless per-source SQLite read for auth-incapable sources.
	// Both halves matter: custom API/manifest sources implement SetAPIKey
	// even when their definition declares no auth (the key would be unused).
	if setter, ok := src.(interface{ SetAPIKey(string) }); ok && source.CapabilitiesOf(src).Auth {
		if key := getSourceAPIKey(svc, id, envKeyFor(src)); key != "" {
			setter.SetAPIKey(key)
		}
	}
	svc.RegisterSource(src)
}

// envKeyFor returns the environment variable name consulted for src's API
// key: src's own EnvKeyProvider when implemented (preserves legacy names
// like NEXUSMODS_API_KEY), otherwise the derived LMM_<ID>_API_KEY
// convention.
func envKeyFor(src source.ModSource) string {
	if p, ok := src.(source.EnvKeyProvider); ok {
		return p.EnvKey()
	}
	return envKeyForSourceID(src.ID())
}

// customSourceWarnOut overrides where registerCustomSources sends its
// per-definition warnings. Nil (the default) means "use the live os.Stderr",
// resolved fresh on every write rather than captured once — so a test that
// redirects the real os.Stderr still observes normal warnings. `source list`
// points this at io.Discard for the duration of its own service init, since
// it re-derives and renders the very same broken definitions as table rows;
// without this seam every broken definition would be reported twice per
// invocation (#52 item 14).
var customSourceWarnOut io.Writer

// customSourceWarnWriter resolves the writer registerCustomSources should
// warn to: the override if one is set, otherwise the live os.Stderr.
func customSourceWarnWriter() io.Writer {
	if customSourceWarnOut != nil {
		return customSourceWarnOut
	}
	return os.Stderr
}

// registerCustomSources loads user-defined source definitions and registers
// the valid ones. Broken definitions warn (via customSourceWarnWriter, normally
// os.Stderr) and are skipped — a bad file must never prevent lmm from starting.
func registerCustomSources(svc *core.Service, cfgDir string) {
	defs, loadErrs, err := config.LoadSourceDefinitions(cfgDir)
	if err != nil {
		fmt.Fprintf(customSourceWarnWriter(), "warning: loading custom sources: %v\n", err)
		return
	}
	for _, le := range loadErrs {
		fmt.Fprintf(customSourceWarnWriter(), "warning: skipping source definition %v\n", le)
	}
	for _, def := range defs {
		src, err := custom.New(def)
		if err != nil {
			fmt.Fprintf(customSourceWarnWriter(), "warning: skipping source %q: %v\n", def.ID, err)
			continue
		}
		registerSource(svc, src)
	}
}

// getSourceAPIKey retrieves an API key from environment or database
func getSourceAPIKey(svc *core.Service, sourceID, envVar string) string {
	// Check environment variable first
	if key := os.Getenv(envVar); key != "" {
		return key
	}

	// Fall back to stored token
	token, err := svc.GetSourceToken(sourceID)
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

// resolveProfile returns the profile a command should operate on: the explicit
// -p/--profile value when given, otherwise the game's active profile as
// resolved by ProfileManager.GetDefault (the IsDefault profile set by
// `lmm profile switch`, else the first profile). Falls back to "default" when
// no profiles exist yet so a fresh setup still works.
func resolveProfile(svc *core.Service, gameID, flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	profile, err := svc.NewProfileManager().GetDefault(gameID)
	if err != nil {
		if errors.Is(err, domain.ErrProfileNotFound) {
			return "default", nil
		}
		return "", fmt.Errorf("resolving active profile for %s: %w", gameID, err)
	}
	return profile.Name, nil
}
