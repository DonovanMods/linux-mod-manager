package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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

	// Point sourceNameResolver at this service's registry for the duration
	// of the call, then restore whatever was there before (nested
	// withService calls don't happen in practice, but tests invoke it
	// repeatedly in the same process). See sourceNameResolver's doc comment
	// for why this indirection exists instead of threading a *core.Service
	// through resolveSource itself.
	prevResolver := sourceNameResolver
	sourceNameResolver = resolverFromService(svc)
	defer func() { sourceNameResolver = prevResolver }()

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
			// Wrap rather than reformat so callers can errors.Is(err, domain.ErrGameNotFound).
			// The visible message stays "game not found: <id>".
			return fmt.Errorf("%w: %s", err, gameID)
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

// readPromptLine reads a line from stdin, trim-spaced and lower-cased, ready
// for y/n comparison. io.EOF is treated as empty input (Ctrl-D and piped
// input both legitimately end the line); any other read error is propagated
// with context so a stdin failure is not silently conflated with a "no".
func readPromptLine() (string, error) {
	return readPromptLineFrom(os.Stdin)
}

// readPromptLineFrom is the testable seam for readPromptLine. The split
// exists so unit tests can drive the helper with a strings.Reader instead
// of os.Stdin.
func readPromptLineFrom(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(strings.ToLower(line)), nil
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
	return promptForGameSource(game.Name, sources, sourceNameResolver)
}

// sourceNameResolver renders a source ID's display name for
// promptForGameSource's "Name (id)" menu lines - the same format
// auth.go's promptForSource/doAuthStatus use. withService points it at the
// active service's registry for the duration of each command (see
// withService), so resolveSource - which resolves purely from *domain.Game
// and carries no *core.Service of its own - can still render registry
// names without its own signature changing. Threading a resolver through
// resolveSource's own parameters would ripple into every one of its
// callers (search/install/update/mod/deploy/import), including deploy.go
// and import.go, both out of scope for this task. The default (and any ID
// the active resolver doesn't know) falls back to the bare source ID,
// matching the design's "unregistered source" rule.
var sourceNameResolver = func(sourceID string) string { return sourceID }

// resolverFromService builds a sourceNameResolver-shaped func backed by
// svc's registry: a source's Name() when it's still registered, the bare ID
// otherwise (covers a SourceIDs entry whose source was since removed).
func resolverFromService(svc *core.Service) func(string) string {
	return func(sourceID string) string {
		if src, err := svc.GetSource(sourceID); err == nil {
			return src.Name()
		}
		return sourceID
	}
}

// promptForGameSource prompts the user to select from multiple configured
// sources, rendering each as "Name (id)" via resolve. A nil resolve (or one
// that returns "" for a given ID) falls back to the bare ID.
func promptForGameSource(gameName string, sources []string, resolve func(string) string) (string, error) {
	if resolve == nil {
		resolve = func(id string) string { return "" }
	}
	fmt.Printf("%s has multiple mod sources configured. Select one:\n", gameName)
	for i, src := range sources {
		name := resolve(src)
		if name == "" {
			name = src
		}
		fmt.Printf("  [%d] %s (%s)\n", i+1, name, src)
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
