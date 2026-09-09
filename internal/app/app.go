package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// Open resolves the installation's paths, prepares its directories, opens the
// core service, and registers every mod source. The caller owns the returned
// service and must Close it. ctx governs the bootstrap token reads that
// source registration performs; an already-cancelled ctx aborts before any
// directory or service work.
func Open(ctx context.Context, opts Options) (*core.Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := ResolvePaths(opts)
	if err != nil {
		return nil, err
	}
	if err := ensureDirs(p); err != nil {
		return nil, err
	}
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: p.ConfigDir,
		DataDir:   p.DataDir,
		CacheDir:  p.CacheDir,
		// This layer owns every path lmm uses, the token-encryption key
		// included (#79): core and the storage layer are handed it, so
		// neither has to know the XDG layout to encrypt a credential.
		KeyPath: filepath.Join(p.DataDir, db.TokenKeyFileName),
		Logger:  opts.Logger,
		// The same channel the source warnings below use: opening the
		// database can stall for tens of seconds re-encrypting credentials
		// while another lmm process holds it (#79), and that has to be
		// visible at the CLI's default --log-level off.
		WarnWriter: warnWriter(opts),
	})
	if err != nil {
		return nil, err
	}
	registerSources(ctx, svc, p.ConfigDir, warnWriter(opts))
	return svc, nil
}

func warnWriter(opts Options) io.Writer {
	if opts.WarnWriter != nil {
		return opts.WarnWriter
	}
	return os.Stderr
}

// ensureDirs creates the layout. The data directory is owner-only: it holds
// lmm.db, whose auth_tokens table stores API keys (encrypted since #79),
// the key those are encrypted under, and the downloads staging root;
// creating it 0700 also closes the window between SQLite creating the DB at
// 0644 and the db package tightening it. MkdirAll leaves an existing
// directory's mode alone, so installs predating the 0700 rule are
// re-tightened explicitly.
func ensureDirs(p Paths) error {
	if err := os.MkdirAll(p.ConfigDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.MkdirAll(p.DataDir, 0700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	if err := os.Chmod(p.DataDir, 0700); err != nil {
		return fmt.Errorf("restricting data dir: %w", err)
	}
	if err := os.MkdirAll(p.CacheDir, 0755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}
	return nil
}
