package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication for mod sources",
	Long: `Manage authentication credentials for mod sources like NexusMods.

Use 'lmm auth login' to authenticate with a source.
Use 'lmm auth logout' to remove stored credentials.
Use 'lmm auth status' to check authentication status.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login [source]",
	Short: "Authenticate with a mod source",
	Long: `Authenticate with a mod source.

Currently supported sources:
  - nexusmods (default)

For NexusMods:
  1. Visit https://www.nexusmods.com/users/myaccount?tab=api
  2. Click "Request an API Key" if you don't have one
  3. Copy your Personal API Key`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAuthLogin,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout [source]",
	Short: "Remove stored credentials for a mod source",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAuthLogout,
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

func runAuthLogin(cmd *cobra.Command, args []string) error {
	sourceID := "nexusmods"
	if len(args) > 0 {
		sourceID = args[0]
	}

	if sourceID != "nexusmods" {
		return fmt.Errorf("unsupported source: %s (only 'nexusmods' is currently supported)", sourceID)
	}

	fmt.Println("To authenticate with NexusMods:")
	fmt.Println("1. Visit https://www.nexusmods.com/users/myaccount?tab=api")
	fmt.Println("2. Click \"Request an API Key\" if you don't have one")
	fmt.Println("3. Copy your Personal API Key")
	fmt.Println()

	apiKey, err := readAPIKey()
	if err != nil {
		return fmt.Errorf("reading API key: %w", err)
	}

	if apiKey == "" {
		return fmt.Errorf("API key cannot be empty")
	}

	fmt.Print("Validating... ")

	// Validate the API key
	ctx := context.Background()
	client := nexusmods.NewClient(nil, "")
	if err := client.ValidateAPIKey(ctx, apiKey); err != nil {
		fmt.Println("failed")
		return fmt.Errorf("invalid API key: %w", err)
	}

	fmt.Println("done")

	// Save the token
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	if err := service.SaveSourceToken(sourceID, apiKey); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}

	fmt.Println("Successfully authenticated with NexusMods!")
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	sourceID := "nexusmods"
	if len(args) > 0 {
		sourceID = args[0]
	}

	if sourceID != "nexusmods" {
		return fmt.Errorf("unsupported source: %s (only 'nexusmods' is currently supported)", sourceID)
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	if err := service.DeleteSourceToken(sourceID); err != nil {
		return fmt.Errorf("removing token: %w", err)
	}

	fmt.Printf("Removed %s credentials.\n", sourceID)
	return nil
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	sources := []string{"nexusmods"}

	for _, sourceID := range sources {
		// Check stored token first
		token, err := service.GetSourceToken(sourceID)
		if err != nil {
			return fmt.Errorf("checking %s: %w", sourceID, err)
		}

		if token != nil {
			masked := maskAPIKey(token.APIKey)
			fmt.Printf("%s: authenticated (key: %s)\n", sourceID, masked)
			continue
		}

		// Check environment variable
		envKey := getEnvKeyForSource(sourceID)
		if envKey != "" {
			if apiKey := os.Getenv(envKey); apiKey != "" {
				masked := maskAPIKey(apiKey)
				fmt.Printf("%s: authenticated via %s (key: %s)\n", sourceID, envKey, masked)
				continue
			}
		}

		fmt.Printf("%s: not authenticated\n", sourceID)
	}

	return nil
}

// getEnvKeyForSource returns the environment variable name for a source's API key
func getEnvKeyForSource(sourceID string) string {
	switch sourceID {
	case "nexusmods":
		return "NEXUSMODS_API_KEY"
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
