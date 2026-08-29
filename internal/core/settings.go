package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// DefaultGame returns the game ID configured with 'lmm game set-default',
// or "" when none is set. It re-reads config.yaml on every call rather than
// trusting the copy NewService loaded at Open, since a frontend may write
// config.yaml directly (config.Config.Save) against an already-open Service.
func (s *Service) DefaultGame(ctx context.Context) (string, error) {
	cfg, err := config.Load(s.configDir)
	if err != nil {
		return "", err
	}
	return cfg.DefaultGame, nil
}

// SetDefaultGame persists id as the default game.
func (s *Service) SetDefaultGame(ctx context.Context, id string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

	cfg, err := config.Load(s.configDir)
	if err != nil {
		return err
	}
	cfg.DefaultGame = id
	return cfg.Save(s.configDir)
}

// ClearDefaultGame removes the configured default game.
func (s *Service) ClearDefaultGame(ctx context.Context) error {
	return s.SetDefaultGame(ctx, "")
}

// DefaultGame reads ConfigDir's default game directly, for a caller that
// needs it before (or without ever) opening a full Service - e.g. resolving
// --game before deciding whether a service is even needed. Unlike
// (*Service).DefaultGame, this never opens a database or loads games.yaml.
func (c ServiceConfig) DefaultGame(ctx context.Context) (string, error) {
	cfg, err := config.Load(c.ConfigDir)
	if err != nil {
		return "", err
	}
	return cfg.DefaultGame, nil
}

// SetDefaultGame persists id as ConfigDir's default game without an open
// Service.
func (c ServiceConfig) SetDefaultGame(ctx context.Context, id string) error {
	cfg, err := config.Load(c.ConfigDir)
	if err != nil {
		return err
	}
	cfg.DefaultGame = id
	return cfg.Save(c.ConfigDir)
}

// ClearDefaultGame removes ConfigDir's default game without an open
// Service.
func (c ServiceConfig) ClearDefaultGame(ctx context.Context) error {
	return c.SetDefaultGame(ctx, "")
}
