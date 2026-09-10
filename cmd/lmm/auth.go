package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication for mod sources",
	Long: `Manage authentication credentials for mod sources.

NexusMods, CurseForge and Steam Workshop are validated live against the
source's API when you log in. Any other registered source that declares auth support (a
custom source with auth enabled in its definition - see 'lmm source
--help') also accepts a stored API key; it is simply stored and exercised
on first use, since custom sources have no generic validation endpoint.
The interactive picker ('lmm auth login'/'lmm auth logout' with no source
argument) lists every registered source that declares auth support,
built-in or custom, sorted by ID.

Use 'lmm auth login [source]' to authenticate with a source.
Use 'lmm auth logout [source]' to remove stored credentials.
Use 'lmm auth status' to check authentication status.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login [source]",
	Short: "Authenticate with a mod source",
	Long: `Authenticate with a mod source.

If no source is specified, you are prompted to choose from every
registered source that declares auth support - the built-ins (NexusMods,
CurseForge) plus any auth-capable custom source (see 'lmm source
--help'), sorted by ID. A custom source's key is stored and exercised on
first use, since there is no generic way to validate it live.

Built-in sources:
  - nexusmods
  - curseforge
  - steamworkshop

Examples:
  lmm auth login                # Interactive selection (all auth-capable sources)
  lmm auth login nexusmods      # Authenticate with NexusMods
  lmm auth login curseforge     # Authenticate with CurseForge
  lmm auth login steamworkshop  # Store your own Steam Web API key
  lmm auth login my-custom-src  # Store a key for a registered custom source

For NexusMods:
  1. Visit https://www.nexusmods.com/users/myaccount?tab=api
  2. Click "Request an API Key" if you don't have one
  3. Copy your Personal API Key

For CurseForge:
  1. Visit https://console.curseforge.com/
  2. Create a project and generate an API key
  3. Copy your API key

For Steam Workshop:
  1. Visit https://steamcommunity.com/dev/apikey
  2. Request a key (it is free)
  3. Copy it, and keep it to yourself - a Steam Web API key is personal
     and confidential. Never share it, and never paste someone else's.
  The key is only needed for SEARCH. Tracking the items you are already
  subscribed to ('lmm import --workshop') and importing a collection need
  no key at all. lmm also reads STEAM_WEB_API_KEY from the environment.

For a custom source, either enter the key at the prompt, or skip login
entirely and set an environment variable instead: LMM_MYSOURCE_API_KEY
for a source with id "mysource" (id uppercased, dashes become
underscores) - lmm reads that on every run.

Non-interactive (#307): --key-from-env reads the source's own environment
variable (NEXUSMODS_API_KEY, CURSEFORGE_API_KEY, or the derived
LMM_<ID>_API_KEY), and --key-stdin reads exactly one line from stdin with
no prompt. Either works under --json, which then emits the same
authentication report 'lmm auth status --json' does. The two are mutually
exclusive, and both require the source to be named positionally.

  lmm auth login nexusmods --key-from-env
  printf '%s\n' "$KEY" | lmm auth login curseforge --key-stdin --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout [source]",
	Short: "Remove stored credentials for a mod source",
	Long: `Remove stored credentials for a mod source.

If no source is specified, you are prompted to choose from every
registered source that declares auth support - the built-ins (NexusMods,
CurseForge) plus any auth-capable custom source, sorted by ID. Any
source with a stored token can also be named positionally to remove it -
including a custom source whose definition file was later deleted, which
would otherwise leave its stored token unremovable through the
interactive picker.

Built-in sources:
  - nexusmods
  - curseforge`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAuthLogout,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status for all sources",
	RunE:  runAuthStatus,
}

var (
	authKeyFromEnv bool
	authKeyStdin   bool
)

func init() {
	authLoginCmd.Flags().BoolVar(&authKeyFromEnv, "key-from-env", false, "read the API key from the source's environment variable instead of prompting")
	authLoginCmd.Flags().BoolVar(&authKeyStdin, "key-stdin", false, "read the API key as one line from stdin, with no prompt")
	authLoginCmd.MarkFlagsMutuallyExclusive("key-from-env", "key-stdin")

	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)
	rootCmd.AddCommand(authCmd)
}

// authCapableSourceIDs returns the comma-joined, sorted IDs of every
// registered auth-capable source, for the "unsupported source" error's hint
// text. Built-ins are always registered (app.Open registers them
// unconditionally), so they always appear here alongside any auth-capable
// custom source - this and promptForSource both draw from the same
// registry query (app.AuthCapableSources, shared with `auth status`'s
// app.AuthStatus so the two can't disagree), eliminating the old
// built-in-vs-custom special casing.
func authCapableSourceIDs(service *core.Service) string {
	sources := app.AuthCapableSources(service)
	ids := make([]string, len(sources))
	for i, src := range sources {
		ids[i] = src.ID()
	}
	return strings.Join(ids, ", ")
}

// promptForSource displays an interactive menu listing every registered
// auth-capable source (built-in and custom alike, sorted by ID - see
// app.AuthCapableSources) and reads the user's numbered choice.
func promptForSource(service *core.Service) (string, error) {
	sources := app.AuthCapableSources(service)
	if len(sources) == 0 {
		return "", fmt.Errorf("no auth-capable sources are registered")
	}

	// Non-interactive rule (Ruling 2): whichever of 'auth login'/'auth
	// logout' reaches here under --json did so with no positional source
	// argument, and the source is the one value neither command has a flag
	// for (#307 gave 'login' its --key-from-env/--key-stdin, not a
	// --source). Naming the source directly is the way out for both.
	if jsonOutput {
		return "", confirmationRequiredVia("pass the source ID as a positional argument (e.g. lmm auth logout <source>)")
	}

	fmt.Println("Select a source to authenticate with:")
	// Name (id) like auth status: names aren't uniqueness-validated across
	// definitions, and the id is what `lmm auth login <id>` takes.
	for i, src := range sources {
		fmt.Printf("  [%d] %s (%s)\n", i+1, src.Name(), src.ID())
	}
	fmt.Print("Enter choice (1-" + strconv.Itoa(len(sources)) + "): ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(input) == "" {
		return "", promptReadError(err, "pass the source ID as a positional argument (e.g. lmm auth logout <source>)")
	}

	choice, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || choice < 1 || choice > len(sources) {
		return "", fmt.Errorf("invalid choice: please enter a number between 1 and %d", len(sources))
	}

	return sources[choice-1].ID(), nil
}

// runAuthLogin resolves the source, then hands off to doAuthLogin. Since
// #307 it no longer rejects --json up front: --key-from-env and
// --key-stdin both supply the key without a terminal, so a flagged run is
// fully non-interactive. Only the SOURCE still has no flag - it is the
// positional argument - so a --json run that omits it refuses at
// selectAuthSource rather than opening the picker.
func runAuthLogin(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		sourceID, err := selectAuthSource(service, args)
		if err != nil {
			return err
		}
		return doAuthLogin(ctx, service, sourceID)
	})
}

// doAuthLogin performs the login flow for an already-resolved sourceID:
// prints its auth instructions, reads an API key from stdin, validates it
// live when the source implements source.KeyValidator (then reports
// "Successfully authenticated"), otherwise stores it unvalidated (then
// reports it was stored and will be validated on first use). Split out from
// runAuthLogin so the validator-vs-stored message split is testable against
// a mock source without driving the full interactive prompt.
func doAuthLogin(ctx context.Context, service *core.Service, sourceID string) error {
	return doAuthLoginIndented(ctx, service, sourceID, "")
}

// errAPIKeyEmpty is the interactive key prompt answered with nothing.
//
// A sentinel rather than a bare fmt.Errorf because `lmm init` has to tell
// it apart from a real failure: the wizard's banner promises "every step
// can be skipped - press Enter to take the default", so an empty key there
// is a SKIP and gets the same wording every other step's skip does. Only
// `lmm auth login`, where the user asked for the prompt, reports it as an
// error (#384).
var errAPIKeyEmpty = errors.New("API key cannot be empty")

// doAuthLoginIndented is doAuthLogin with every line it prints prefixed by
// indent, so `lmm init` can nest the source's own instruction block and key
// prompt inside its two-space step layout instead of being the one thing on
// screen that breaks out of it (#384). indent "" is `lmm auth login`'s own
// rendering, byte for byte.
func doAuthLoginIndented(ctx context.Context, service *core.Service, sourceID, indent string) error {
	out := newIndentWriter(os.Stdout, indent)

	src, err := service.GetSource(sourceID)
	if err != nil {
		return fmt.Errorf("looking up source %s: %w", sourceID, err)
	}

	// Only the interactive path prints instructions: they tell a human
	// where to get a key, which is noise for a --key-from-env run and
	// forbidden output beside a --json document (Ruling 15).
	if !authKeyFromEnv && !authKeyStdin && !jsonOutput {
		printAuthInstructions(out, src)
	}

	apiKey, err := acquireAPIKey(out, src)
	if err != nil {
		return err
	}
	if apiKey == "" {
		return errAPIKeyEmpty
	}

	// app owns the live check (app.ValidateSourceKey), shared with `lmm
	// serve`'s POST /api/v1/auth/{source}, so the two frontends cannot
	// disagree about when a key was really validated. HasKeyValidator is
	// asked first because the "Validating... " line has to be printed
	// BEFORE the call it describes.
	hasValidator := app.HasKeyValidator(service, sourceID)
	if hasValidator && !jsonOutput {
		fmt.Fprint(out, "Validating... ")
	}
	if _, err := app.ValidateSourceKey(ctx, service, sourceID, apiKey); err != nil {
		if hasValidator && !jsonOutput {
			fmt.Fprintln(out, "failed")
		}
		return fmt.Errorf("invalid API key: %w", err)
	}
	if hasValidator && !jsonOutput {
		fmt.Fprintln(out, "done")
	}

	if err := service.SaveSourceToken(ctx, sourceID, apiKey); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}

	if jsonOutput {
		// The whole app.AuthStatusReport, not just this source's row: it is
		// the document `lmm auth status --json` already emits (so a client
		// parses one shape for "what is authenticated"), and a login can
		// change more than one row - storing a key for a source that had an
		// orphaned token moves it out of "orphaned" in the same read.
		// `lmm serve`'s POST /api/v1/auth/{source} answers with the same
		// document, for the same reason.
		return doAuthStatus(ctx, service)
	}
	printLoginResult(out, hasValidator)
	printAuthLoginSuccess(out, src, hasValidator)
	return nil
}

// acquireAPIKey obtains the key for src: from its own environment variable
// (--key-from-env, resolved through app.EnvKeyFor so a built-in's legacy
// name and a custom source's derived LMM_<ID>_API_KEY are found the same
// way), from exactly one line of stdin (--key-stdin, no prompt), or from
// the interactive terminal prompt.
//
// The prompt is the only path that reads stdin without being asked to, so
// it is the only one --json refuses (Ruling 2), naming the two flags that
// answer it.
func acquireAPIKey(w io.Writer, src source.ModSource) (string, error) {
	switch {
	case authKeyFromEnv:
		envKey := app.EnvKeyFor(src)
		key := strings.TrimSpace(os.Getenv(envKey))
		if key == "" {
			return "", fmt.Errorf("%s is not set (or is empty); export it, or pass --key-stdin instead", envKey)
		}
		return key, nil
	case authKeyStdin:
		key, err := readOneLine(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading API key from stdin: %w", err)
		}
		return key, nil
	case jsonOutput:
		return "", fmt.Errorf("%w: pass --key-from-env or --key-stdin", core.ErrInteractiveOnly)
	}
	key, err := readAPIKey(w)
	if err != nil {
		return "", fmt.Errorf("reading API key: %w", err)
	}
	return key, nil
}

// readOneLine reads exactly one line from r, trim-spaced, treating EOF as
// the end of that line (a `printf '%s' "$KEY" |` pipe with no trailing
// newline is a legitimate way to supply it). It does NOT lower-case, which
// is why it is not readPromptLineFrom: an API key is case-sensitive.
func readOneLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// printLoginResult reports the outcome of storing credentials, for the case
// where no live validator ran. hasValidator sources were actively checked
// via a real API call just above ("Validating... done"), so nothing more is
// needed here. Sources without a validator get an honest message instead -
// printing that same "Validating... done" sequence would fabricate a result
// that never happened.
func printLoginResult(w io.Writer, hasValidator bool) {
	if hasValidator {
		return
	}
	fmt.Fprintln(w, "Stored (validated on first use).")
}

// printAuthLoginSuccess prints the final confirmation line for a completed
// login. Sources with a live validator were actively checked via a real API
// call earlier in the flow, so "Successfully authenticated" is accurate.
// Sources without one have no generic validation endpoint - printing that
// same claim would fabricate a result that never happened, so they get an
// honest "stored" message instead, keyed by ID rather than a display name.
func printAuthLoginSuccess(w io.Writer, src source.ModSource, hasValidator bool) {
	if hasValidator {
		fmt.Fprintf(w, "Successfully authenticated with %s!\n", src.Name())
		return
	}
	fmt.Fprintf(w, "API key stored for %s.\n", src.ID())
}

// selectAuthSource resolves the source from args or prompts interactively.
// Both paths test the same capability - CapabilitiesOf(src).Auth - though
// not via one shared call: a named arg is checked directly through
// isAuthCapableSource, while the interactive path lists every candidate via
// promptForSource (app.AuthCapableSources).
func selectAuthSource(service *core.Service, args []string) (string, error) {
	if len(args) > 0 {
		sourceID := args[0]
		if !isAuthCapableSource(service, sourceID) {
			return "", fmt.Errorf("unsupported source: %s (auth-capable sources: %s; a custom source appears here once its definition declares auth)", sourceID, authCapableSourceIDs(service))
		}
		return sourceID, nil
	}
	sourceID, err := promptForSource(service)
	if err != nil {
		return "", err
	}
	fmt.Println()
	return sourceID, nil
}

// resolveLogoutSource picks the source to log out. Unlike login, logout must
// also work for sources that are no longer registered (definition file
// deleted after a key was stored) — otherwise the stored token becomes
// unremovable via the CLI.
func resolveLogoutSource(ctx context.Context, service *core.Service, args []string) (string, error) {
	if len(args) == 0 {
		return selectAuthSource(service, args) // interactive prompt path unchanged
	}
	sourceID := args[0]
	if isAuthCapableSource(service, sourceID) {
		return sourceID, nil
	}
	token, err := service.GetSourceToken(ctx, sourceID)
	if err != nil {
		return "", fmt.Errorf("checking stored credentials for %s: %w", sourceID, err)
	}
	if token != nil {
		return sourceID, nil
	}
	return "", fmt.Errorf("no stored credentials for %q and it is not a registered auth-capable source", sourceID)
}

// isAuthCapableSource reports whether sourceID is a registered source whose
// definition declares auth (built-in or custom - both are found the same
// way, since built-ins are registered unconditionally with explicit
// Capabilities()).
func isAuthCapableSource(service *core.Service, sourceID string) bool {
	src, err := service.GetSource(sourceID)
	if err != nil {
		return false
	}
	return source.CapabilitiesOf(src).Auth
}

// authDisplayName returns sourceID's registered source's Name() when it is
// still registered, otherwise the raw ID. Needed because resolveLogoutSource
// allows removing a token for a source that is no longer registered (its
// definition file was deleted), in which case there is no Name() to consult.
func authDisplayName(service *core.Service, sourceID string) string {
	if src, err := service.GetSource(sourceID); err == nil {
		return src.Name()
	}
	return sourceID
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doAuthLogout(ctx, service, args)
	})
}

// doAuthLogout removes a source's stored credential and reports what is
// left. Split out from runAuthLogout so the --json document is testable
// without driving cobra (the same split doAuthLogin already has).
//
// #335: --json emits the re-read app.AuthStatusReport rather than the prose
// line, which is what every other --json command does (one document on
// stdout) and what `lmm serve`'s DELETE /api/v1/auth/{source} already
// answered - so the CLI and the web route stopped disagreeing about what a
// logout even answers. The report is re-read AFTER the delete, so it
// describes the state the command left behind: the row this logout emptied,
// and - since a logout can move a row between the two lists - anything else
// that changed with it. The plain-text line is unchanged.
func doAuthLogout(ctx context.Context, service *core.Service, args []string) error {
	sourceID, err := resolveLogoutSource(ctx, service, args)
	if err != nil {
		return err
	}
	if err := service.DeleteSourceToken(ctx, sourceID); err != nil {
		return fmt.Errorf("removing token: %w", err)
	}
	if jsonOutput {
		return doAuthStatus(ctx, service)
	}
	fmt.Printf("Removed %s credentials.\n", authDisplayName(service, sourceID))
	return nil
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doAuthStatus(ctx, service)
	})
}

// doAuthStatus renders the app.AuthStatusReport query (#309): the plain
// per-source and orphaned-token lines are rebuilt from the same document
// --json emits, byte-identically to the pre-#309 text that formatted
// straight from the registry/token scan.
func doAuthStatus(ctx context.Context, service *core.Service) error {
	report, err := app.AuthStatus(ctx, service)
	if err != nil {
		return err
	}

	if jsonOutput {
		return emitJSON(report)
	}

	for _, s := range report.Sources {
		switch s.Via {
		case "stored":
			// A stored key is encrypted at rest and never leaves the
			// storage layer (#79), so this identifies it by fingerprint
			// where it used to show a masked prefix and suffix.
			fmt.Printf("%s (%s): authenticated (key %s)\n", s.Name, s.ID, s.KeyFingerprint)
		case "env":
			// #356: a stored token that the environment outranks is named
			// here, so "which key is lmm sending?" has one honest answer.
			shadowed := ""
			if s.StoredKeyShadowed {
				shadowed = fmt.Sprintf(" (stored key present, shadowed by $%s)", s.EnvVar)
			}
			fmt.Printf("%s (%s): authenticated via %s (key: %s)%s\n", s.Name, s.ID, s.EnvVar, s.KeyMasked, shadowed)
		default:
			fmt.Printf("%s (%s): not authenticated (run: lmm auth login %s)\n", s.Name, s.ID, s.ID)
		}
		if s.Unreadable {
			fmt.Printf("%s (%s): a stored credential exists but could not be decrypted — run: lmm auth login %s\n", s.Name, s.ID, s.ID)
		}
	}

	// Wording keyed by Reason, matching the two remedies: re-declare auth vs.
	// the token is simply stale.
	for _, o := range report.Orphaned {
		if o.Reason == "auth_not_declared" {
			fmt.Printf("%s: stored token for source without auth declared (key %s) — stale token? remove with: lmm auth logout %s\n",
				o.ID, orphanKeyLabel(o), o.ID)
			continue
		}
		fmt.Printf("%s: stored token with no matching source (key %s) — remove with: lmm auth logout %s\n",
			o.ID, orphanKeyLabel(o), o.ID)
	}

	return nil
}

// orphanKeyLabel names an orphaned credential the only way a status surface
// may: by fingerprint, or as unreadable when it did not decrypt at all.
func orphanKeyLabel(o app.OrphanedToken) string {
	if o.Unreadable {
		return "unreadable"
	}
	return o.KeyFingerprint
}

// printAuthInstructions prints setup steps for obtaining src's API key: its
// own AuthInstructionsProvider text when implemented (built-ins preserve
// their exact wording), otherwise generic instructions naming the
// environment variable app.EnvKeyFor resolves for src.
func printAuthInstructions(w io.Writer, src source.ModSource) {
	if p, ok := src.(source.AuthInstructionsProvider); ok {
		fmt.Fprint(w, p.AuthInstructions())
	} else {
		fmt.Fprintf(w, "Enter the API key for %s.\n", src.ID())
		fmt.Fprintf(w, "(Alternatively, set the %s environment variable.)\n", app.EnvKeyFor(src))
	}
	fmt.Fprintln(w)
}

// readAPIKey prompts for and reads an API key from the terminal
func readAPIKey(w io.Writer) (string, error) {
	fmt.Fprint(w, "Enter API key: ")

	// Try to read securely (hidden input)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		keyBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(w) // Add newline after hidden input
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		return strings.TrimSpace(string(keyBytes)), nil
	}

	// Fallback for non-terminal input (e.g., piped input)
	reader := bufio.NewReader(os.Stdin)
	key, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(key), nil
}
