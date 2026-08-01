package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/muesli/termenv"
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
	version = "1.27.1"

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

// stdoutColorCapable reports whether the live os.Stdout is a color-capable
// terminal (not a pipe, redirect, or non-interactive runner). A function
// var, not a direct termenv.ColorProfile() call, so it re-resolves the
// CURRENT os.Stdout on every call - tests that swap os.Stdout for an
// os.Pipe (see captureStdout) get a truthful "not a terminal" answer for
// free, and tests that need to simulate an interactive TTY can override
// this var directly instead of faking a pty.
var stdoutColorCapable = func() bool {
	return termenv.NewOutput(os.Stdout).ColorProfile() != termenv.Ascii
}

// colorEnabled returns true if colored output should be used: respects
// --no-color and NO_COLOR env (https://no-color.org) first, then falls back
// to TTY detection so piped/redirected output stays plain without an
// explicit opt-out.
func colorEnabled() bool {
	if noColor {
		return false
	}
	// Presence-only per https://no-color.org: NO_COLOR disables color when
	// set to ANY value, including the empty string - os.Getenv can't tell
	// "unset" from "set to empty", so this must use os.LookupEnv.
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return stdoutColorCapable()
}

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
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

// colorBold returns s with bold ANSI when color is enabled, otherwise s.
func colorBold(s string) string {
	if !colorEnabled() {
		return s
	}
	return ansiBold + s + ansiReset
}

// colorDim returns s with faint/dim ANSI when color is enabled, otherwise s.
// Used for negative-but-routine states (e.g. a disabled mod row) where a
// loud red would overstate the severity - accent, not alarm.
func colorDim(s string) string {
	if !colorEnabled() {
		return s
	}
	return ansiDim + s + ansiReset
}

// colorCyan returns s with cyan ANSI when color is enabled, otherwise s.
// The palette's fourth accent, used for "key field" values (a version
// number, a link method, an active profile name) that deserve a visual
// anchor without implying good/bad the way green/yellow/red do.
func colorCyan(s string) string {
	if !colorEnabled() {
		return s
	}
	return ansiCyan + s + ansiReset
}

// colorHeader returns s bold+cyan when color is enabled, otherwise s - the
// accent for a table header or section title. #193: bold alone read as
// nearly plain in smoke feedback, so headers now carry both bold and a
// color, not bold-only.
func colorHeader(s string) string {
	if !colorEnabled() {
		return s
	}
	return ansiBold + ansiCyan + s + ansiReset
}

// printTable writes a fully-flushed text/tabwriter table (buf) to os.Stdout,
// accenting the header line (bold+cyan, via colorHeader) and applying
// rowColor's per-row wrapper (nil for no tint) when color is enabled.
// headerLines is the number of leading lines to skip when indexing data
// rows (2: header + dashed separator).
//
// Color is applied ONLY to buf's already-rendered, already-padded text -
// never to a cell before it reaches the tabwriter. text/tabwriter computes
// column padding from raw byte length, so an ANSI-wrapped cell fed into it
// would inflate that cell's measured width and misalign every column after
// it (verified empirically). Wrapping an already-flushed line's start/end
// is safe: those bytes are invisible to the terminal and never shift where
// the real characters land. Do not colorize interior cell values before
// Fprintf-ing them into a tabwriter.Writer - use whole-row tinting (via
// rowColor) or, for a table's genuinely last column (nothing pads after it
// per tabwriter's own behavior), inline coloring of that one column instead.
func printTable(buf *bytes.Buffer, headerLines int, rowColor func(dataRowIndex int) func(string) string) error {
	return printTableTo(os.Stdout, buf, headerLines, rowColor)
}

// printTableTo is printTable's testable seam: same contract, explicit writer.
func printTableTo(out io.Writer, buf *bytes.Buffer, headerLines int, rowColor func(dataRowIndex int) func(string) string) error {
	text := strings.TrimSuffix(buf.String(), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if colorEnabled() {
		lines[0] = colorHeader(lines[0])
		if rowColor != nil {
			for i := headerLines; i < len(lines); i++ {
				if fn := rowColor(i - headerLines); fn != nil {
					lines[i] = fn(lines[i])
				}
			}
		}
	}
	_, err := fmt.Fprintln(out, strings.Join(lines, "\n"))
	return err
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
		fmt.Fprintf(os.Stderr, "%s %v\n", colorRed("Error:"), err)
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

// icarusFirestoreProjectID is Project Daedalus's Firebase project ID, from
// the Firebase console. It is public information (Firestore reads are
// unauthenticated by design, per the research spike) — this constant is the
// one place it needs to be substituted with the real value.
const icarusFirestoreProjectID = "projectdaedalus-fb09f"

// builtinSourceFactories constructs each built-in source keyless — the
// unified pipeline resolves and applies API keys post-construction via
// registerSource's SetAPIKey seam, the same path custom sources use.
var builtinSourceFactories = []func() source.ModSource{
	func() source.ModSource { return nexusmods.New(nil, "") },
	func() source.ModSource { return curseforge.New(nil, "") },
	func() source.ModSource { return icarus.New(nil, icarusFirestoreProjectID) },
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
