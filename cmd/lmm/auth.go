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
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// supportedSources lists all sources that support authentication
var supportedSources = []string{"nexusmods", "curseforge"}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication for mod sources",
	Long: `Manage authentication credentials for mod sources like NexusMods and CurseForge.

Use 'lmm auth login' to authenticate with a source.
Use 'lmm auth logout' to remove stored credentials.
Use 'lmm auth status' to check authentication status.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login [source]",
	Short: "Authenticate with a mod source",
	Long: `Authenticate with a mod source.

If no source is specified, you will be prompted to select one.

Supported sources:
  - nexusmods
  - curseforge

Examples:
  lmm auth login              # Interactive source selection
  lmm auth login nexusmods    # Authenticate with NexusMods
  lmm auth login curseforge   # Authenticate with CurseForge

For NexusMods:
  1. Visit https://www.nexusmods.com/users/myaccount?tab=api
  2. Click "Request an API Key" if you don't have one
  3. Copy your Personal API Key

For CurseForge:
  1. Visit https://console.curseforge.com/
  2. Create a project and generate an API key
  3. Copy your API key`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout [source]",
	Short: "Remove stored credentials for a mod source",
	Long: `Remove stored credentials for a mod source.

If no source is specified, you will be prompted to select one.

Supported sources:
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

// promptForSource displays an interactive menu to select a source
func promptForSource() (string, error) {
	fmt.Println("Select a source to authenticate with:")
	for i, source := range supportedSources {
		fmt.Printf("  [%d] %s\n", i+1, getSourceDisplayName(source))
	}
	fmt.Print("Enter choice (1-" + strconv.Itoa(len(supportedSources)) + "): ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}

	choice, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || choice < 1 || choice > len(supportedSources) {
		return "", fmt.Errorf("invalid choice: please enter a number between 1 and %d", len(supportedSources))
	}

	return supportedSources[choice-1], nil
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		sourceID, err := selectAuthSource(service, args)
		if err != nil {
			return err
		}

		printAuthInstructions(sourceID)

		apiKey, err := readAPIKey()
		if err != nil {
			return fmt.Errorf("reading API key: %w", err)
		}
		if apiKey == "" {
			return fmt.Errorf("API key cannot be empty")
		}

		if isSupportedSource(sourceID) {
			fmt.Print("Validating... ")
			if err := validateAPIKey(ctx, sourceID, apiKey); err != nil {
				fmt.Println("failed")
				return fmt.Errorf("invalid API key: %w", err)
			}
			fmt.Println("done")
		}

		if err := service.SaveSourceToken(sourceID, apiKey); err != nil {
			return fmt.Errorf("saving token: %w", err)
		}
		printLoginResult(os.Stdout, sourceID)
		printAuthLoginSuccess(os.Stdout, sourceID)
		return nil
	})
}

// printLoginResult reports the outcome of storing credentials for sourceID.
// Built-in sources (nexusmods, curseforge) were actively validated via a real
// API call just above ("Validating... done"), so nothing more is needed here.
// Custom sources have no generic validation endpoint (validateAPIKey is a
// no-op for them) — printing the same "Validating... done" sequence would be
// a fabricated result, so they get an honest message instead: the key is
// simply stored and will be exercised on first use.
func printLoginResult(w io.Writer, sourceID string) {
	if isSupportedSource(sourceID) {
		return
	}
	fmt.Fprintln(w, "Stored (validated on first use).")
}

// printAuthLoginSuccess prints the final confirmation line for a completed
// login. Built-in sources were actively validated via a real API call
// earlier in the flow ("Validating... done"), so "Successfully
// authenticated" is accurate. Custom sources have no generic validation
// endpoint — printing that same claim would fabricate a result that never
// happened, so they get an honest "stored" message instead.
func printAuthLoginSuccess(w io.Writer, sourceID string) {
	if isSupportedSource(sourceID) {
		fmt.Fprintf(w, "Successfully authenticated with %s!\n", getSourceDisplayName(sourceID))
		return
	}
	fmt.Fprintf(w, "API key stored for %s.\n", sourceID)
}

// selectAuthSource resolves the source from args or prompts the user. The
// service is used to recognize registered custom sources that declare auth
// support; the interactive prompt path only ever offers built-ins.
func selectAuthSource(service *core.Service, args []string) (string, error) {
	if len(args) > 0 {
		sourceID := args[0]
		if !isAuthCapableSource(service, sourceID) {
			return "", fmt.Errorf("unsupported source: %s (supported: %s, or a registered custom source with auth declared)", sourceID, strings.Join(supportedSources, ", "))
		}
		return sourceID, nil
	}
	sourceID, err := promptForSource()
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

// isAuthCapableSource reports whether sourceID can hold an API key: either a
// built-in from supportedSources, or a registered custom source whose
// definition declares auth.
func isAuthCapableSource(service *core.Service, sourceID string) bool {
	if isSupportedSource(sourceID) {
		return true
	}
	src, err := service.GetSource(sourceID)
	if err != nil {
		return false
	}
	return source.CapabilitiesOf(src).Auth
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
		fmt.Printf("Removed %s credentials.\n", getSourceDisplayName(sourceID))
		return nil
	})
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doAuthStatus(service)
	})
}

func doAuthStatus(service *core.Service) error {
	for _, sourceID := range supportedSources {
		// Check stored token first
		token, err := service.GetSourceToken(sourceID)
		if err != nil {
			return fmt.Errorf("checking %s: %w", sourceID, err)
		}

		if token != nil {
			masked := maskAPIKey(token.APIKey)
			fmt.Printf("%s: authenticated (key: %s)\n", getSourceDisplayName(sourceID), masked)
			continue
		}

		// Check environment variable
		envKey := getEnvKeyForSource(sourceID)
		if envKey != "" {
			if apiKey := os.Getenv(envKey); apiKey != "" {
				masked := maskAPIKey(apiKey)
				fmt.Printf("%s: authenticated via %s (key: %s)\n", getSourceDisplayName(sourceID), envKey, masked)
				continue
			}
		}

		fmt.Printf("%s: not authenticated\n", getSourceDisplayName(sourceID))
	}

	// Custom sources that declare auth get the same treatment as built-ins.
	// Sorted by ID: registry/ListSources order is map iteration, which Go
	// randomizes, and this output must be deterministic.
	customSources := service.ListSources()
	sort.Slice(customSources, func(i, j int) bool { return customSources[i].ID() < customSources[j].ID() })

	registered := make(map[string]bool, len(supportedSources)+len(customSources))
	for _, id := range supportedSources {
		registered[id] = true
	}

	for _, src := range customSources {
		id := src.ID()
		registered[id] = true
		if isSupportedSource(id) {
			continue // already reported above
		}
		if !source.CapabilitiesOf(src).Auth {
			continue // directory sources, auth-less manifests: nothing to report
		}

		token, err := service.GetSourceToken(id)
		if err != nil {
			return fmt.Errorf("checking %s: %w", id, err)
		}
		if token != nil {
			fmt.Printf("%s: authenticated (key: %s)\n", id, maskAPIKey(token.APIKey))
			continue
		}
		envKey := envKeyForSourceID(id)
		if apiKey := os.Getenv(envKey); apiKey != "" {
			fmt.Printf("%s: authenticated via %s (key: %s)\n", id, envKey, maskAPIKey(apiKey))
			continue
		}
		fmt.Printf("%s: not authenticated (run: lmm auth login %s)\n", id, id)
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

// isSupportedSource checks if a source ID is in the supported list
func isSupportedSource(sourceID string) bool {
	for _, s := range supportedSources {
		if s == sourceID {
			return true
		}
	}
	return false
}

// getSourceDisplayName returns the display name for a source
func getSourceDisplayName(sourceID string) string {
	switch sourceID {
	case "nexusmods":
		return "NexusMods"
	case "curseforge":
		return "CurseForge"
	default:
		return sourceID
	}
}

// printAuthInstructions prints source-specific auth instructions
func printAuthInstructions(sourceID string) {
	switch sourceID {
	case "nexusmods":
		fmt.Println("To authenticate with NexusMods:")
		fmt.Println("1. Visit https://www.nexusmods.com/users/myaccount?tab=api")
		fmt.Println("2. Click \"Request an API Key\" if you don't have one")
		fmt.Println("3. Copy your Personal API Key")
	case "curseforge":
		fmt.Println("To authenticate with CurseForge:")
		fmt.Println("1. Visit https://console.curseforge.com/")
		fmt.Println("2. Create a project and generate an API key")
		fmt.Println("3. Copy your API key")
	default:
		fmt.Printf("Enter the API key for %s.\n", sourceID)
		fmt.Printf("(Alternatively, set the %s environment variable.)\n", envKeyForSourceID(sourceID))
	}
	fmt.Println()
}

// validateAPIKey validates an API key for the given source
func validateAPIKey(ctx context.Context, sourceID, apiKey string) error {
	switch sourceID {
	case "nexusmods":
		client := nexusmods.NewClient(nil, "")
		return client.ValidateAPIKey(ctx, apiKey)
	case "curseforge":
		// Validate by making a test API call to GetGames
		client := curseforge.NewClient(nil, apiKey)
		_, err := client.GetGames(ctx)
		if err != nil {
			return fmt.Errorf("API validation failed: %w", err)
		}
		return nil
	default:
		// Custom sources have no generic validation endpoint. By the time we
		// get here the source has already passed isAuthCapableSource, so the
		// key is simply stored and exercised on first fetch.
		return nil
	}
}

// getEnvKeyForSource returns the environment variable name for a source's API key
func getEnvKeyForSource(sourceID string) string {
	switch sourceID {
	case "nexusmods":
		return "NEXUSMODS_API_KEY"
	case "curseforge":
		return "CURSEFORGE_API_KEY"
	default:
		return envKeyForSourceID(sourceID)
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
