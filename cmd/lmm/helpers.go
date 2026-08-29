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

	"github.com/DonovanMods/linux-mod-manager/internal/app"
	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
)

// withService wires up the standard CLI service lifecycle: build a *core.Service,
// guarantee Close on return (with a stderr warning on close failure), and forward
// cmd.Context() to fn so SIGINT and explicit cancellation propagate downstream.
func withService(cmd *cobra.Command, fn func(ctx context.Context, svc *core.Service) error) error {
	return withServiceOpts(cmd, app.Options{}, fn)
}

// withServiceOpts is withService with bootstrap options (e.g. a custom warning
// writer); ConfigDir and DataDir still come from the global flags.
func withServiceOpts(cmd *cobra.Command, opts app.Options, fn func(ctx context.Context, svc *core.Service) error) error {
	svc, err := initServiceWith(cmd.Context(), opts)
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
//
// It is also the CLI's one choke point for the non-interactive rule (v2
// Phase 3 Ruling 2): under --json, r is never touched - every caller either
// checks its own deciding flag before reaching here (so this path is never
// hit at all) or has none, in which case core.ErrConfirmationRequired is the
// answer. Returning before the read means a poison reader fed to this
// function under --json is provably never read, not merely returned an
// error quickly.
func readPromptLineFrom(r io.Reader) (string, error) {
	if jsonOutput {
		return "", core.ErrConfirmationRequired
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(strings.ToLower(line)), nil
}

// confirmationRequiredVia returns core.ErrConfirmationRequired augmented
// with how, the specific flag or argument that would have answered this
// particular prompt without one. Most prompts share the sentinel's own
// generic --yes/--force text (their deciding flag already gates the call to
// readPromptLine, so the plain sentinel is accurate); the few prompts with
// no such flag - a source or game selection - use this instead so the
// --json envelope names the actual way out.
func confirmationRequiredVia(how string) error {
	return fmt.Errorf("%w: %s", core.ErrConfirmationRequired, how)
}

// resolveSource determines which source to use for a game.
// If sourceFlag is provided, validates it's configured for the game.
// If sourceFlag is empty and only one source is configured, uses that.
// If multiple sources are configured and autoSelect is false, prompts for selection.
// If autoSelect is true (e.g., -y flag), uses the first configured source (alphabetically).
// svc is used only to render display names in the interactive-prompt path
// (see resolverFromService); every real call site already has one in scope
// (all route through withService/withGameService).
func resolveSource(svc *core.Service, game *domain.Game, sourceFlag string, autoSelect bool) (string, error) {
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
	return promptForGameSource(game.Name, sources, resolverFromService(svc))
}

// resolverFromService builds a promptForGameSource-shaped resolver backed by
// svc's registry: a source's Name() when it's still registered, the bare ID
// otherwise (covers a SourceIDs entry whose source was since removed,
// matching the design's "unregistered source" rule). A nil svc (defensive -
// every real resolveSource caller has one in scope) yields a nil resolver,
// so promptForGameSource's own bare-ID fallback applies.
func resolverFromService(svc *core.Service) func(string) string {
	if svc == nil {
		return nil
	}
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
//
// Non-interactive rule (Ruling 2): under --json this never prints or reads
// anything - every resolveSource caller already exposes -s/--source, so the
// envelope names that flag as the way to decide the prompt non-interactively.
func promptForGameSource(gameName string, sources []string, resolve func(string) string) (string, error) {
	if jsonOutput {
		return "", confirmationRequiredVia("pass -s/--source to select a mod source")
	}
	if resolve == nil {
		resolve = func(id string) string { return "" }
	}
	fmt.Printf("%s has multiple mod sources configured. Select one:\n", gameName)
	for i, src := range sources {
		// Bare ID when there's no distinct display name - "id (id)" is
		// noise, and the doc contract promises the bare-ID fallback.
		if name := resolve(src); name != "" && name != src {
			fmt.Printf("  [%d] %s (%s)\n", i+1, name, src)
		} else {
			fmt.Printf("  [%d] %s\n", i+1, src)
		}
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
