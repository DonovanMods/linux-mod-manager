package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	var sourceID string
	var err error

	if len(args) > 0 {
		sourceID = args[0]
		if !isSupportedSource(sourceID) {
			return fmt.Errorf("unsupported source: %s (supported: %s)", sourceID, strings.Join(supportedSources, ", "))
		}
	} else {
		sourceID, err = promptForSource()
		if err != nil {
			return err
		}
		fmt.Println()
	}

	printAuthInstructions(sourceID)

	apiKey, err := readAPIKey()
	if err != nil {
		return fmt.Errorf("reading API key: %w", err)
	}

	if apiKey == "" {
		return fmt.Errorf("API key cannot be empty")
	}

	fmt.Print("Validating... ")

	// Validate the API key based on source
	ctx := context.Background()
	if err := validateAPIKey(ctx, sourceID, apiKey); err != nil {
		fmt.Println("failed")
		return fmt.Errorf("invalid API key: %w", err)
	}

	fmt.Println("done")

	// Save the token
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	if err := service.SaveSourceToken(sourceID, apiKey); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}

	fmt.Printf("Successfully authenticated with %s!\n", getSourceDisplayName(sourceID))
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	var sourceID string
	var err error

	if len(args) > 0 {
		sourceID = args[0]
		if !isSupportedSource(sourceID) {
			return fmt.Errorf("unsupported source: %s (supported: %s)", sourceID, strings.Join(supportedSources, ", "))
		}
	} else {
		sourceID, err = promptForSource()
		if err != nil {
			return err
		}
		fmt.Println()
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	if err := service.DeleteSourceToken(sourceID); err != nil {
		return fmt.Errorf("removing token: %w", err)
	}

	fmt.Printf("Removed %s credentials.\n", getSourceDisplayName(sourceID))
	return nil
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

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
		// CurseForge doesn't have a dedicated validate endpoint,
		// so we just accept the key and let it fail on first use.
		// TODO: Implement validation by making a test API call
		if len(apiKey) < 10 {
			return fmt.Errorf("API key too short")
		}
		return nil
	default:
		return fmt.Errorf("unknown source: %s", sourceID)
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
		return ""
	}
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

// maskAPIKey returns a masked version of the API key (shows first 3 and last 3 chars)
func maskAPIKey(key string) string {
	if len(key) <= 6 {
		return "***"
	}
	return key[:3] + "..." + key[len(key)-3:]
}
