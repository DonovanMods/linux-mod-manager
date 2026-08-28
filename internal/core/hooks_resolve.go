package core

import (
	"context"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

const defaultHookTimeout = 60 * time.Second

// hookRunner returns a HookRunner configured from the global config's
// HookTimeout - mirrors the pre-lift CLI's getHookRunner (cmd/lmm/hooks.go)
// exactly, including its swallow-and-warn behavior on an unreadable
// config.yaml (defaults to 60s rather than failing the caller's flow).
// Returns error for forward-compatibility with callers that resolve hooks
// before a mutation and must propagate a real failure; today this always
// returns nil.
func (s *Service) hookRunner(_ context.Context) (*HookRunner, error) {
	cfg, err := config.Load(s.ConfigDir())
	timeout := defaultHookTimeout
	if err == nil && cfg.HookTimeout > 0 {
		timeout = time.Duration(cfg.HookTimeout) * time.Second
	} else if err != nil {
		s.logger().Warn("config.yaml unreadable; using default hook timeout", "err", err)
	}
	return NewHookRunner(timeout), nil
}

// resolvedHooks resolves the merged game/profile hooks for profileName -
// mirrors the pre-lift CLI's getResolvedHooks (cmd/lmm/hooks.go) exactly,
// including its swallow-on-failure behavior: an empty profileName, or ANY
// profile-load error (not just domain.ErrProfileNotFound), resolves
// game-level hooks only rather than failing the caller's flow. Returns
// error for forward-compatibility with callers that resolve hooks before a
// mutation and must propagate a real failure; today this always returns
// nil.
func (s *Service) resolvedHooks(_ context.Context, game *domain.Game, profileName string) (*ResolvedHooks, error) {
	var profile *domain.Profile
	if profileName != "" {
		if p, err := s.NewProfileManager().Get(game.ID, profileName); err == nil {
			profile = p
		}
	}
	return ResolveHooks(game, profile), nil
}

// hookContextFor builds the base HookContext for game - mirrors the
// pre-lift CLI's makeHookContext (cmd/lmm/hooks.go) exactly. Callers set
// HookName (and, for per-mod hooks, ModID/ModName/ModVersion) themselves.
func hookContextFor(game *domain.Game) HookContext {
	return HookContext{
		GameID:   game.ID,
		GamePath: game.InstallPath,
		ModPath:  game.ModPath,
	}
}
