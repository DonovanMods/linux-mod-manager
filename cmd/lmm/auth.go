package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication for mod sources",
	Long: `Manage authentication credentials for mod sources.

NexusMods and CurseForge are validated live against the source's API when
you log in. Any other registered source that declares auth support (a
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

Examples:
  lmm auth login                # Interactive selection (all auth-capable sources)
  lmm auth login nexusmods      # Authenticate with NexusMods
  lmm auth login curseforge     # Authenticate with CurseForge
  lmm auth login my-custom-src  # Store a key for a registered custom source

For NexusMods:
  1. Visit https://www.nexusmods.com/users/myaccount?tab=api
  2. Click "Request an API Key" if you don't have one
  3. Copy your Personal API Key

For CurseForge:
  1. Visit https://console.curseforge.com/
  2. Create a project and generate an API key
  3. Copy your API key

For a custom source, either enter the key at the prompt, or skip login
entirely and set an environment variable instead: LMM_MYSOURCE_API_KEY
for a source with id "mysource" (id uppercased, dashes become
underscores) - lmm reads that on every run.`,
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

func init() {
	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authLogoutCmd)
	authCmd.AddCommand(authStatusCmd)
	rootCmd.AddCommand(authCmd)
}

// authCapableSources returns every registered source whose
// CapabilitiesOf(src).Auth is true, sorted by ID. Built-ins are always
// registered (registerSources in root.go runs unconditionally), so they
// always appear here alongside any auth-capable custom source - the
// interactive picker, `auth status`, and the "unsupported source" error
// hint all derive their source list from this single registry query,
// eliminating the old built-in-vs-custom special casing.
func authCapableSources(service *core.Service) []source.ModSource {
	all := service.ListSources()
	capable := make([]source.ModSource, 0, len(all))
	for _, src := range all {
		if source.CapabilitiesOf(src).Auth {
			capable = append(capable, src)
		}
	}
	sort.Slice(capable, func(i, j int) bool { return capable[i].ID() < capable[j].ID() })
	return capable
}

// authCapableSourceIDs returns the comma-joined, sorted IDs of every
// registered auth-capable source, for the "unsupported source" error's hint
// text.
func authCapableSourceIDs(service *core.Service) string {
	sources := authCapableSources(service)
	ids := make([]string, len(sources))
	for i, src := range sources {
		ids[i] = src.ID()
	}
	return strings.Join(ids, ", ")
}

// promptForSource displays an interactive menu listing every registered
// auth-capable source (built-in and custom alike, sorted by ID - see
// authCapableSources) and reads the user's numbered choice.
func promptForSource(service *core.Service) (string, error) {
	sources := authCapableSources(service)
	if len(sources) == 0 {
		return "", fmt.Errorf("no auth-capable sources are registered")
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
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}

	choice, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || choice < 1 || choice > len(sources) {
		return "", fmt.Errorf("invalid choice: please enter a number between 1 and %d", len(sources))
	}

	return sources[choice-1].ID(), nil
}

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
	src, err := service.GetSource(sourceID)
	if err != nil {
		return fmt.Errorf("looking up source %s: %w", sourceID, err)
	}

	printAuthInstructions(src)

	apiKey, err := readAPIKey()
	if err != nil {
		return fmt.Errorf("reading API key: %w", err)
	}
	if apiKey == "" {
		return fmt.Errorf("API key cannot be empty")
	}

	validator, hasValidator := src.(source.KeyValidator)
	if hasValidator {
		fmt.Print("Validating... ")
		if err := validator.ValidateKey(ctx, apiKey); err != nil {
			fmt.Println("failed")
			return fmt.Errorf("invalid API key: %w", err)
		}
		fmt.Println("done")
	}

	if err := service.SaveSourceToken(sourceID, apiKey); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	printLoginResult(os.Stdout, hasValidator)
	printAuthLoginSuccess(os.Stdout, src, hasValidator)
	return nil
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
// Both paths draw from the same registry query (authCapableSources): every
// registered source with Capabilities().Auth.
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
func resolveLogoutSource(service *core.Service, args []string) (string, error) {
	if len(args) == 0 {
		return selectAuthSource(service, args) // interactive prompt path unchanged
	}
	sourceID := args[0]
	if isAuthCapableSource(service, sourceID) {
		return sourceID, nil
	}
	token, err := service.GetSourceToken(sourceID)
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
		sourceID, err := resolveLogoutSource(service, args)
		if err != nil {
			return err
		}
		if err := service.DeleteSourceToken(sourceID); err != nil {
			return fmt.Errorf("removing token: %w", err)
		}
		fmt.Printf("Removed %s credentials.\n", authDisplayName(service, sourceID))
		return nil
	})
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doAuthStatus(service)
	})
}

// doAuthStatus reports authentication status for every registered
// auth-capable source (built-in and custom, uniformly - sorted by ID via
// authCapableSources), then a final pass surfacing stored tokens that match
// no registered source (e.g. a custom source's definition file was deleted
// after `lmm auth login`).
func doAuthStatus(service *core.Service) error {
	sources := authCapableSources(service)
	registered := make(map[string]bool, len(sources))

	for _, src := range sources {
		id := src.ID()
		registered[id] = true

		token, err := service.GetSourceToken(id)
		if err != nil {
			return fmt.Errorf("checking %s: %w", id, err)
		}
		if token != nil {
			fmt.Printf("%s (%s): authenticated (key: %s)\n", src.Name(), id, maskAPIKey(token.APIKey))
			continue
		}

		envKey := envKeyFor(src)
		if apiKey := os.Getenv(envKey); apiKey != "" {
			fmt.Printf("%s (%s): authenticated via %s (key: %s)\n", src.Name(), id, envKey, maskAPIKey(apiKey))
			continue
		}

		fmt.Printf("%s (%s): not authenticated (run: lmm auth login %s)\n", src.Name(), id, id)
	}

	// Stored tokens whose source matches nothing registered (built-in or
	// custom) are otherwise invisible — e.g. a custom source's definition
	// file was deleted after `lmm auth login`. Surface them so the user
	// knows the credential still exists and how to remove it.
	tokens, err := service.ListSourceTokens()
	if err != nil {
		return fmt.Errorf("listing stored tokens: %w", err)
	}
	for _, tok := range tokens {
		if registered[tok.SourceID] {
			continue
		}
		fmt.Printf("%s: stored token with no matching source (key: %s) — remove with: lmm auth logout %s\n",
			tok.SourceID, maskAPIKey(tok.APIKey), tok.SourceID)
	}

	return nil
}

// printAuthInstructions prints setup steps for obtaining src's API key: its
// own AuthInstructionsProvider text when implemented (built-ins preserve
// their exact wording), otherwise generic instructions naming the
// environment variable envKeyFor resolves for src.
func printAuthInstructions(src source.ModSource) {
	if p, ok := src.(source.AuthInstructionsProvider); ok {
		fmt.Print(p.AuthInstructions())
	} else {
		fmt.Printf("Enter the API key for %s.\n", src.ID())
		fmt.Printf("(Alternatively, set the %s environment variable.)\n", envKeyFor(src))
	}
	fmt.Println()
}

// getSourceDisplayName returns the display name for a source ID: the two
// built-ins' Name() values (kept in lockstep — "Nexus Mods"/"CurseForge"),
// else the ID unchanged.
//
// Retained here (unused by auth.go's own flows, which now derive display
// names from the registered source's Name() via authCapableSources) because
// helpers.go's promptForGameSource — the "this game has multiple configured
// sources, pick one" prompt shared by search/install/update/mod, entirely
// unrelated to auth — still calls it. That prompt's own normalization is
// out of scope for this task (PR 2 territory per the source-registry design
// doc); deleting this function would break the build for code this task
// does not touch.
func getSourceDisplayName(sourceID string) string {
	switch sourceID {
	case "nexusmods":
		return "Nexus Mods"
	case "curseforge":
		return "CurseForge"
	default:
		return sourceID
	}
}

// envKeyForSourceID derives the env var that can supply a custom source's API
// key: LMM_<ID>_API_KEY with the ID uppercased and dashes as underscores.
func envKeyForSourceID(sourceID string) string {
	return "LMM_" + strings.ReplaceAll(strings.ToUpper(sourceID), "-", "_") + "_API_KEY"
}

// readAPIKey prompts for and reads an API key from the terminal
func readAPIKey() (string, error) {
	fmt.Print("Enter API key: ")

	// Try to read securely (hidden input)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		keyBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println() // Add newline after hidden input
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

// maskAPIKey returns a masked version of the API key (shows first 3 and last
// 3 chars). Keys of 8 characters or fewer are fully masked instead: showing
// 6 of 7-8 characters exposes most of the key, defeating the point of
// masking.
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:3] + "..." + key[len(key)-3:]
}
