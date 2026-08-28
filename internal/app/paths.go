// Package app is lmm's composition root. It locates an installation on disk,
// prepares its directories, opens the core service, and registers the mod
// sources. Every frontend (the CLI today, `lmm serve` later) starts here so
// they resolve paths and sources identically. It is the only package that
// imports concrete source implementations.
package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
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
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("home directory: %w", err)
	}

	p := Paths{ConfigDir: opts.ConfigDir, DataDir: opts.DataDir}
	if p.ConfigDir == "" {
		p.ConfigDir = resolveBaseDir("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	}
	if p.DataDir == "" {
		p.DataDir = resolveBaseDir("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
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
// and legacyBase otherwise. When the XDG location does not exist yet but the
// legacy one does, the legacy directory wins so installs that predate XDG
// support keep finding their data.
func resolveBaseDir(envVar, legacyBase string) string {
	legacy := filepath.Join(legacyBase, appDirName)
	base := os.Getenv(envVar)
	if base == "" || !filepath.IsAbs(base) {
		return legacy
	}
	xdg := filepath.Join(base, appDirName)
	if xdg == legacy {
		return xdg
	}
	if _, err := os.Stat(xdg); err == nil {
		return xdg
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return xdg
}
