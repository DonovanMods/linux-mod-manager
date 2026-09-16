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
		// #317: the cross-process mutation lock, one per installation.
		// core takes it inside beginOp but never decides WHERE it lives -
		// path resolution is this package's job, and the data directory is
		// what an installation is keyed by (two frontends pointed at one
		// --data must contend; two pointed at different ones must not).
		OpLockPath: OpLockPath(p),
	})
	if err != nil {
		return nil, err
	}
	registerAdapters(svc)
	registerSources(ctx, svc, p, warnWriter(opts))
	// #431: the one-time backfill of the profile documents' `disabled:`
	// markers, owed only by a database an older lmm wrote (migrateV17). It
	// runs here so the first command after the upgrade - a read-only one
	// included - records it and prints what it recorded; beginOp runs it too,
	// so a mutation can never get to the evidence first. A no-op on every
	// later open, and on any installation that does not owe it. Core prints
	// its own notice, on the same WarnWriter this passes it.
	//
	// Nothing here may stop lmm from starting. Core only tries the
	// mutation lock, never waits for it (another lmm mid-mutation leaves
	// the job to the next mutation or open), and turns every per-file or
	// per-row problem - an editor panic included - into a skipped profile
	// in its own notice. What is left (the database itself failing) is a
	// warning, never a refusal: the intent it records is already recorded
	// in the database. Same channel and same reasoning as the source
	// warnings above; the next open, or this process's first mutation,
	// finishes the job.
	if _, err := svc.BackfillProfileDisabledMarkers(ctx); err != nil {
		_, _ = fmt.Fprintf(warnWriter(opts), "warning: could not record mods disabled before this upgrade in their profiles: %v\n", err) //nolint:errcheck // best-effort warning write
	}
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
