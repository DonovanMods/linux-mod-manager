// Package app is lmm's composition root. It locates an installation on disk,
// prepares its directories, opens the core service, and registers the mod
// sources. Every frontend (the CLI today, `lmm serve` later) starts here so
// they resolve paths and sources identically. It is the only package that
// imports concrete source implementations.
package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// appDirName is the per-application subdirectory under each base directory.
const appDirName = "lmm"

// Options controls how an installation is located and opened. Zero values
// mean "resolve from the environment".
type Options struct {
	// ConfigDir overrides configuration-directory resolution (the CLI's --config).
	ConfigDir string
	// DataDir overrides data-directory resolution (the CLI's --data).
	DataDir string
	// WarnWriter receives non-fatal warnings raised while registering sources
	// (a custom source definition that fails to load, an ID collision).
	// nil means os.Stderr.
	WarnWriter io.Writer
	// Logger receives diagnostics from the opened service. nil means discard.
	Logger *slog.Logger
}

// Paths is a resolved on-disk layout.
type Paths struct {
	ConfigDir string // config.yaml, games.yaml, sources/, games/<id>/profiles/
	DataDir   string // lmm.db, downloads/
	CacheDir  string // downloaded and extracted mod files
}

// ResolvePaths applies, in order: explicit overrides from opts, the XDG Base
// Directory variables, and the legacy ~/.config and ~/.local/share defaults.
// The cache lives under the data directory unless config.yaml sets cache_path;
// it is deliberately not placed under XDG_CACHE_HOME, because it holds
// downloads that are expensive to fetch again and must not be treated as
// disposable.
func ResolvePaths(opts Options) (Paths, error) {
	p := Paths{ConfigDir: opts.ConfigDir, DataDir: opts.DataDir}
	if p.ConfigDir == "" {
		dir, err := resolveBaseDir("XDG_CONFIG_HOME", ".config")
		if err != nil {
			return Paths{}, err
		}
		p.ConfigDir = dir
	}
	if p.DataDir == "" {
		dir, err := resolveBaseDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
		if err != nil {
			return Paths{}, err
		}
		p.DataDir = dir
	}

	// A config.yaml that fails to parse is reported with context by
	// core.NewService; here it only means "no cache_path override".
	if cfg, err := config.Load(p.ConfigDir); err == nil && cfg.CachePath != "" {
		p.CacheDir = cfg.CachePath
	} else {
		p.CacheDir = filepath.Join(p.DataDir, "cache")
	}
	return p, nil
}

// resolveBaseDir returns <base>/lmm, where base is the XDG variable when it is
// set to an absolute path (the spec requires relative values to be ignored)
// and $HOME/<legacyRel> otherwise. When the XDG location does not exist yet
// but the legacy one does, the legacy directory wins so installs that predate
// XDG support keep finding their data. $HOME is consulted only on this path,
// so a caller that supplies both directories explicitly never needs it (#277).
func resolveBaseDir(envVar, legacyRel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	legacy := filepath.Join(home, legacyRel, appDirName)
	base := os.Getenv(envVar)
	if base == "" || !filepath.IsAbs(base) {
		return legacy, nil
	}
	xdg := filepath.Join(base, appDirName)
	if xdg == legacy {
		return xdg, nil
	}
	if _, err := os.Stat(xdg); err == nil {
		return xdg, nil
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	return xdg, nil
}
