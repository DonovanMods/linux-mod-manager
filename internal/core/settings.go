package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// loadDefaultGame reads configDir's default game. Both (*Service).DefaultGame
// and ServiceConfig.DefaultGame share this so a load failure is reported
// identically regardless of receiver.
func loadDefaultGame(configDir string) (string, error) {
	cfg, err := config.Load(configDir)
	if err != nil {
		return "", err
	}
	return cfg.DefaultGame, nil
}

// saveDefaultGame persists id as configDir's default game, wrapping a load
// or save failure with the text 'lmm game set-default'/'clear-default' have
// always surfaced ("loading config: %w" / "saving config: %w") so callers
// don't need to (and can't collapse the two into one label by re-wrapping).
func saveDefaultGame(configDir, id string) error {
	cfg, err := config.Load(configDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	cfg.DefaultGame = id
	if err := cfg.Save(configDir); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

// DefaultGame returns the game ID configured with 'lmm game set-default',
// or "" when none is set. It re-reads config.yaml on every call rather than
// trusting the copy NewService loaded at Open, since a frontend may write
// config.yaml directly (config.Config.Save) against an already-open Service.
func (s *Service) DefaultGame(ctx context.Context) (string, error) {
	return loadDefaultGame(s.configDir)
}

// SetDefaultGame persists id as the default game.
func (s *Service) SetDefaultGame(ctx context.Context, id string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()

	return saveDefaultGame(s.configDir, id)
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
	return loadDefaultGame(c.ConfigDir)
}

// SetDefaultGame persists id as ConfigDir's default game without an open
// Service.
func (c ServiceConfig) SetDefaultGame(ctx context.Context, id string) error {
	return saveDefaultGame(c.ConfigDir, id)
}

// ClearDefaultGame removes ConfigDir's default game without an open
// Service.
func (c ServiceConfig) ClearDefaultGame(ctx context.Context) error {
	return c.SetDefaultGame(ctx, "")
}
