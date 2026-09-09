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
// Directory variables when they name an absolute path, and the legacy
// ~/.config and ~/.local/share defaults otherwise (#297).
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

// xdgValueIsAuthoritative is #297's policy, in one predicate: an XDG base
// directory variable that names an ABSOLUTE path is an explicit instruction
// and always decides where lmm reads and writes. Anything else - unset, or
// relative (which the XDG spec requires be ignored) - leaves lmm free to
// prefer a legacy directory that already exists.
//
// The ruling (#297, 2026-09-09) is option (a): silently writing into
// ~/.local/share/lmm while XDG_DATA_HOME pointed elsewhere surprised the
// user in the one case where they had said exactly what they wanted, and it
// made a test harness that set XDG_* but left HOME alone corrupt the real
// install. Kept as a named predicate so a different ruling is one function
// away.
func xdgValueIsAuthoritative(value string) bool {
	return value != "" && filepath.IsAbs(value)
}

// resolveBaseDir returns <base>/lmm, where base is the XDG variable when it
// is set to an absolute path and $HOME/<legacyRel> otherwise.
//
// An absolute XDG value wins outright, whether or not that directory exists
// yet (xdgValueIsAuthoritative, #297). The legacy fallback - use
// $HOME/<legacyRel>/lmm when it exists - therefore applies only when the
// variable is unset or relative, which is the case an install that predates
// XDG support is actually in: it never set the variable. $HOME is consulted
// only on this path, so a caller that supplies both directories explicitly
// never needs it (#277).
func resolveBaseDir(envVar, legacyRel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	legacy := filepath.Join(home, legacyRel, appDirName)
	if base := os.Getenv(envVar); xdgValueIsAuthoritative(base) {
		return filepath.Join(base, appDirName), nil
	}
	return legacy, nil
}

// opLockFileName is the advisory lock file every lmm mutation takes,
// inside the data directory (#317). Dot-prefixed so it does not clutter a
// directory a user browses, and inside DataDir rather than a temp or
// runtime directory so it identifies exactly what it protects: one
// installation's database, cache and deploy bookkeeping.
const opLockFileName = ".oplock"

// OpLockPath returns the advisory mutation-lock file for a resolved
// layout - core.ServiceConfig.OpLockPath's value (#317). Exported because
// a frontend that documents or diagnoses the lock needs to name the same
// file core takes, without re-deriving the convention.
func OpLockPath(p Paths) string {
	return filepath.Join(p.DataDir, opLockFileName)
}
