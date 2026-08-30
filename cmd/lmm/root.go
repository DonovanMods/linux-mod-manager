package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

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
	version = "2.0.0"

	// buildDescribe is injected at build time via -ldflags -X (see the
	// Makefile's `build` target) with `git describe --tags --dirty`.
	// Empty for builds made without that ldflags (plain `go build`/`go
	// test`), which display identically to a clean release build.
	buildDescribe = ""

	// Global flags
	configDir  string
	dataDir    string
	gameID     string
	verbose    bool
	noHooks    bool
	jsonOutput bool
	noColor    bool
	logLevel   string

	// rawArgs is the argument list Execute is about to hand to runRoot,
	// captured before ParseFlags can consume it. logLevelFlagErrorFunc
	// consults it to detect a --json flag that pflag's FlagSet.Parse never
	// reached because it aborted on an earlier --log-level Set error - see
	// that function's comment in logging.go. Tests that call runRoot
	// directly (bypassing Execute) must set this alongside rootCmd.SetArgs
	// so the two argument lists agree.
	rawArgs []string
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "lmm",
	Short: "Linux Mod Manager - Terminal-based mod manager for Linux",
	Long: `lmm is a terminal-based mod manager for Linux for searching, installing,
updating, and managing game mods from sources like NexusMods and CurseForge,
plus user-defined custom sources (directory scans, static manifests, or REST
APIs — see 'lmm source --help').

Run 'lmm COMMAND --help' for details on any subcommand.

EXIT CODES

    0  success
    1  error
    2  cancelled by the user (e.g. declined a confirmation prompt)

FILES

    $XDG_CONFIG_HOME/lmm/   Configuration: games.yaml, config.yaml, per-game
                            profiles, and sources/*.yaml (custom source
                            definitions). Defaults to ~/.config/lmm.
                            Override with --config.
    $XDG_DATA_HOME/lmm/     Data: lmm.db (mod metadata and auth tokens),
                            cache/ (downloaded and extracted mod files), and
                            downloads/ (staging area for in-flight downloads
                            and archive extraction). Defaults to
                            ~/.local/share/lmm. Override with --data.

    When an XDG variable is set but its lmm directory does not exist yet and
    the legacy default does, the legacy directory is used.`,
	Version:       computeDisplayVersion(version, buildDescribe),
	SilenceUsage:  true, // Runtime errors should not print usage
	SilenceErrors: true, // We handle error output in Execute()

	// PersistentPreRunE validates --log-level before any subcommand runs
	// (including --version and --help), so an invalid level is rejected up
	// front instead of only surfacing if and when the subcommand happens to
	// open a Service. No subcommand in this tree defines its own
	// PersistentPreRun(E), so cobra never skips this one in favor of a
	// nearer override. newCLILogger's returned logger is discarded here;
	// initServiceWith re-validates (idempotently) when it builds the real one.
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		_, err := newCLILogger(logLevel, io.Discard)
		return err
	},
}

// computeDisplayVersion returns the version string shown to the user for
// `--version`: ver unadorned when buildDescribe is empty (no ldflags, e.g.
// `go build`/`go test`) or matches "v"+ver exactly (a clean build of the
// release tag itself), otherwise ver with buildDescribe's git-describe
// provenance appended - a "-dirty" suffix from `git describe --dirty`
// passes through inside that unchanged. JSON surfaces must keep emitting
// the static `version` var, not this - only human-facing display uses it.
func computeDisplayVersion(ver, describe string) string {
	if describe == "" || describe == "v"+ver {
		return ver
	}
	return fmt.Sprintf("%s (dev: %s)", ver, describe)
}

func init() {
	// Persistent flags available to all commands
	rootCmd.PersistentFlags().StringVar(&configDir, "config", "", "config directory (default: $XDG_CONFIG_HOME/lmm or ~/.config/lmm)")
	rootCmd.PersistentFlags().StringVar(&dataDir, "data", "", "data directory (default: $XDG_DATA_HOME/lmm or ~/.local/share/lmm)")
	rootCmd.PersistentFlags().StringVarP(&gameID, "game", "g", "", "game ID to operate on")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVar(&noHooks, "no-hooks", false, "disable all hooks")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output JSON instead of text; mutating commands print their result, --dry-run prints the plan; never prompts")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable colored output (NO_COLOR env is also honored)")
	logLevel = "off"
	rootCmd.PersistentFlags().Var(logLevelFlag{&logLevel}, logLevelFlagName, "diagnostic log level written to stderr (off, error, warn, info, debug)")
	rootCmd.SetFlagErrorFunc(logLevelFlagErrorFunc)
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

// modRowColor returns the row-tint color function for a mod's
// enabled/deployed state: dim for disabled (a routine, expected state - not
// an error), yellow for enabled-but-not-yet-deployed (drift worth noticing),
// green for enabled+deployed (the common, healthy case - #193: originally
// left untinted, which read as nearly plain in smoke feedback).
//
// The single shared decision for any command that lists mod rows keyed on
// this state - do not reimplement this switch inline in a second call site;
// a mod's health must render identically everywhere it's shown, independent
// of which columns that particular view happens to display (#193 round 2:
// list -v and plain list colored inconsistently because the decision only
// existed inline in the verbose branch).
func modRowColor(enabled, deployed bool) func(string) string {
	switch {
	case !enabled:
		return colorDim
	case !deployed:
		return colorYellow
	default:
		return colorGreen
	}
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

	rawArgs = os.Args[1:]
	if err := runRoot(ctx); err != nil {
		if errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) {
			printCancelledNotice(os.Stderr, jsonOutput)
			os.Exit(2)
		}
		reportError(err)
		os.Exit(1)
	}
}

// printCancelledNotice names the cancellation on Execute's exit-2 path
// (Ruling 16 addendum): plain mode alone gets "Cancelled." on stderr, since
// exit 2 alone was otherwise silent even though `lmm --help` documents it as
// "cancelled by the user" - Unit R final review Minor 3. --json emits
// nothing extra here (Ruling 15): the JSON contract carries no envelope for
// a cancellation exit.
func printCancelledNotice(out io.Writer, jsonOutput bool) {
	if jsonOutput {
		return
	}
	_, _ = fmt.Fprintln(out, "Cancelled.") //nolint:errcheck // best-effort notice write
}

// reportError prints err in the active output format, unless the command
// already reported it (ErrReported). Under --json this is a
// {"error": "...", "details": {...}} envelope via emitJSON, with "details"
// present only when errorDetails finds data to attach; otherwise it is
// "Error: ..." on stderr.
func reportError(err error) {
	if errors.Is(err, ErrReported) {
		return
	}
	if jsonOutput {
		_ = emitJSON(jsonErrorEnvelope{Error: err.Error(), Details: errorDetails(err)})
	} else {
		fmt.Fprintf(os.Stderr, "%s %v\n", colorRed("Error:"), err)
	}
}

// runRoot dispatches to rootCmd with the given context. Split out so tests can
// drive the command tree without going through signal.NotifyContext.
func runRoot(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}

// initService builds the CLI's *core.Service from the global --config/--data
// flags. See app.Open for what bootstrap involves.
func initService(ctx context.Context) (*core.Service, error) {
	return initServiceWith(ctx, app.Options{})
}

// initServiceWith is initService with additional bootstrap options; ConfigDir
// and DataDir always come from the flags.
func initServiceWith(ctx context.Context, opts app.Options) (*core.Service, error) {
	opts.ConfigDir = configDir
	opts.DataDir = dataDir
	logger, err := newCLILogger(logLevel, os.Stderr)
	if err != nil {
		return nil, err
	}
	opts.Logger = logger
	return app.Open(ctx, opts)
}

// getServiceConfig resolves the on-disk layout the CLI flags select, without
// opening a service.
func getServiceConfig() (core.ServiceConfig, error) {
	p, err := app.ResolvePaths(app.Options{ConfigDir: configDir, DataDir: dataDir})
	if err != nil {
		return core.ServiceConfig{}, err
	}
	return core.ServiceConfig{ConfigDir: p.ConfigDir, DataDir: p.DataDir, CacheDir: p.CacheDir}, nil
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
	defaultGame, err := svcCfg.DefaultGame(cmd.Context())
	if err == nil && defaultGame != "" {
		gameID = defaultGame
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
func resolveProfile(ctx context.Context, svc *core.Service, gameID, flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	profile, err := svc.NewProfileManager().GetDefault(ctx, gameID)
	if err != nil {
		if errors.Is(err, domain.ErrProfileNotFound) {
			return "default", nil
		}
		return "", fmt.Errorf("resolving active profile for %s: %w", gameID, err)
	}
	return profile.Name, nil
}
