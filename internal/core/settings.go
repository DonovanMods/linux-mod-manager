package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// SettingsResult reports the state of the settings a mutation just wrote -
// today the whole of config.yaml a frontend can change from the CLI: the
// configured default game, empty after `lmm game clear-default`. It is the
// document `lmm game set-default`/`clear-default` emit under --json
// (v2 Phase 3 Ruling 15), which is why the effect is reported as the
// resulting VALUE rather than as the "set"/"cleared" verb that produced it:
// a caller re-reading the setting after either command sees the same field.
type SettingsResult struct {
	DefaultGame string `json:"default_game"`
}

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
	unlock, err := lockConfig(configDir)
	if err != nil {
		return err
	}
	defer unlock()
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

// configLockFile is the lock, in the config dir, every read-modify-write of
// config.yaml holds (#586 review). It is its own lock, not the mutation
// lock: a config write must never wait behind a deploy.
const configLockFile = ".config.yaml.lock"

// lockConfig takes configDir's config.yaml lock, waiting for whichever lmm
// process - a CLI command, `lmm serve` - holds it; a write holds it for
// only its read and its write. flock locks an open file, not a process, so
// two writers in one process exclude each other too.
func lockConfig(configDir string) (unlock func(), err error) {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating config dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(configDir, configLockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the config lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking config.yaml: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
