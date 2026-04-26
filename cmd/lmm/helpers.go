package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
)

// withService wires up the standard CLI service lifecycle: build a *core.Service,
// guarantee Close on return (with a stderr warning on close failure), and forward
// cmd.Context() to fn so SIGINT and explicit cancellation propagate downstream.
func withService(cmd *cobra.Command, fn func(ctx context.Context, svc *core.Service) error) error {
	svc, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer closeService(svc)

	return fn(cmd.Context(), svc)
}

// withGameService extends withService with the requireGame check and resolves
// the *domain.Game for the global -g flag, so callers receive a fully-populated
// game and never need to repeat the GetGame boilerplate.
func withGameService(cmd *cobra.Command, fn func(ctx context.Context, svc *core.Service, game *domain.Game) error) error {
	if err := requireGame(cmd); err != nil {
		return err
	}
	return withService(cmd, func(ctx context.Context, svc *core.Service) error {
		game, err := svc.GetGame(gameID)
		if err != nil {
			return fmt.Errorf("game not found: %s", gameID)
		}
		return fn(ctx, svc, game)
	})
}

func closeService(svc *core.Service) {
	if err := svc.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
	}
}

// authPromptError returns the canonical error shown when a source returns
// domain.ErrAuthRequired, instructing the user how to authenticate.
func authPromptError(sourceID string) error {
	return fmt.Errorf("authentication required; run 'lmm auth login %s' to authenticate", sourceID)
}

// resolveSource determines which source to use for a game.
// If sourceFlag is provided, validates it's configured for the game.
// If sourceFlag is empty and only one source is configured, uses that.
// If multiple sources are configured and autoSelect is false, prompts for selection.
// If autoSelect is true (e.g., -y flag), uses the first configured source (alphabetically).
func resolveSource(game *domain.Game, sourceFlag string, autoSelect bool) (string, error) {
	if sourceFlag != "" {
		// Validate the specified source is configured for this game
		if _, ok := game.SourceIDs[sourceFlag]; !ok {
			configuredSources := getConfiguredSources(game)
			return "", fmt.Errorf("source %q is not configured for %s; available: %v", sourceFlag, game.Name, configuredSources)
		}
		return sourceFlag, nil
	}

	// No source specified - check configured sources
	if len(game.SourceIDs) == 0 {
		return "", fmt.Errorf("no mod sources configured for %s; add sources with 'lmm game add' or edit games.yaml", game.Name)
	}

	sources := getConfiguredSources(game)

	// Only one source - use it automatically
	if len(sources) == 1 {
		return sources[0], nil
	}

	// Multiple sources
	if autoSelect {
		// Auto-select mode: use first source
		return sources[0], nil
	}

	// Interactive mode: prompt for selection
	return promptForGameSource(game.Name, sources)
}

// promptForGameSource prompts the user to select from multiple configured sources.
func promptForGameSource(gameName string, sources []string) (string, error) {
	fmt.Printf("%s has multiple mod sources configured. Select one:\n", gameName)
	for i, source := range sources {
		fmt.Printf("  [%d] %s\n", i+1, getSourceDisplayName(source))
	}
	fmt.Printf("Enter choice (1-%d): ", len(sources))

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}

	choice, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil || choice < 1 || choice > len(sources) {
		return "", fmt.Errorf("invalid choice: please enter a number between 1 and %d", len(sources))
	}

	return sources[choice-1], nil
}

// getConfiguredSources returns the configured source names for a game, sorted alphabetically.
func getConfiguredSources(game *domain.Game) []string {
	sources := make([]string, 0, len(game.SourceIDs))
	for src := range game.SourceIDs {
		sources = append(sources, src)
	}
	sort.Strings(sources)
	return sources
}
