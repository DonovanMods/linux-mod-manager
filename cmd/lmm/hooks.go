// Package main: this file's helpers are what's left after Task 2 (#286)
// moved hook RESOLUTION into core (Service.hookRunner/resolvedHooks/
// hookContextFor, internal/core/hooks_resolve.go) - every core.XOptions
// literal now resolves its own hooks. Unit H (#288) removed the second of
// the two remaining callers by deleting batchInstallMods, so all four
// helpers below now have exactly ONE caller between them: doImport's
// archive tail (import.go), which sequences install.before_all/before_each/
// after_each/after_all itself. Nothing here shrank with that deletion -
// doImport uses every one of them - so the whole file dies with that tail
// in Unit K, per ruling 1.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// getHookRunner returns a HookRunner if hooks are enabled (respects
// --no-hooks flag). Every core.XOptions{...} literal ALSO sets SkipHooks
// from noHooks directly, so a nil runner here and SkipHooks there both say
// the same thing: --no-hooks disables hook execution.
func getHookRunner(svc *core.Service) *core.HookRunner {
	if noHooks {
		return nil
	}

	// Load config to get timeout
	cfg, err := config.Load(svc.ConfigDir())
	timeout := 60 * time.Second // default
	if err == nil && cfg.HookTimeout > 0 {
		timeout = time.Duration(cfg.HookTimeout) * time.Second
	} else if err != nil {
		svc.Logger().Warn("config.yaml unreadable; using default hook timeout", "err", err)
	}

	return core.NewHookRunner(timeout)
}

// getResolvedHooks resolves hooks from game/profile (returns nil if --no-hooks)
func getResolvedHooks(svc *core.Service, game *domain.Game, profileName string) *core.ResolvedHooks {
	if noHooks {
		return nil
	}

	var profile *domain.Profile
	if profileName != "" {
		var err error
		profile, err = config.LoadProfile(svc.ConfigDir(), game.ID, profileName)
		if err != nil {
			// If profile can't be loaded, proceed with game-level hooks only
			profile = nil
		}
	}

	return core.ResolveHooks(game, profile)
}

// makeHookContext creates a HookContext for batch operations
func makeHookContext(game *domain.Game) core.HookContext {
	return core.HookContext{
		GameID:   game.ID,
		GamePath: game.InstallPath,
		ModPath:  game.ModPath,
	}
}

// printHookWarnings prints non-fatal hook errors to stderr
func printHookWarnings(errors []error) {
	for _, err := range errors {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}
}
