package main

import (
	"fmt"
	"os"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// getHookRunner returns a HookRunner if hooks are enabled (respects --no-hooks flag)
func getHookRunner(svc *core.Service) *core.HookRunner {
	if noHooks {
		return nil
	}

	// Load config to get timeout
	cfg, err := config.Load(svc.ConfigDir())
	timeout := 60 * time.Second // default
	if err == nil && cfg.HookTimeout > 0 {
		timeout = time.Duration(cfg.HookTimeout) * time.Second
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
