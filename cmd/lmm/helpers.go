package main

import (
	"fmt"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// resolveSource determines which source to use for a game.
// If sourceFlag is provided, validates it's configured for the game.
// If sourceFlag is empty, returns the first configured source (sorted for consistency).
// Returns the source name and any error.
func resolveSource(game *domain.Game, sourceFlag string) (string, error) {
	if sourceFlag != "" {
		// Validate the specified source is configured for this game
		if _, ok := game.SourceIDs[sourceFlag]; !ok {
			configuredSources := getConfiguredSources(game)
			return "", fmt.Errorf("source %q is not configured for %s; available: %v", sourceFlag, game.Name, configuredSources)
		}
		return sourceFlag, nil
	}

	// No source specified - use first configured source
	if len(game.SourceIDs) == 0 {
		return "", fmt.Errorf("no mod sources configured for %s; add sources with 'lmm game add' or edit games.yaml", game.Name)
	}

	// Pick first configured source (sorted for deterministic behavior)
	sources := getConfiguredSources(game)
	return sources[0], nil
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
