package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"

	_ "modernc.org/sqlite"
)

// secureFileMode is the permission mask for the database and its WAL/SHM sidecars.
// The auth_tokens table holds API keys in plaintext, so the file must not be
// readable by other local users.
const secureFileMode = 0600

// DB wraps the SQLite database connection
type DB struct {
	*sql.DB
}

// New creates a new database connection and runs migrations
func New(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable foreign keys and WAL mode for better performance
	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;"); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("setting pragmas: %w (closing database: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("setting pragmas: %w", err)
	}

	// After the pragmas, so the WAL/SHM sidecars exist and get tightened too.
	if err := restrictPermissions(path); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("%w (closing database: %v)", err, closeErr)
		}
		return nil, err
	}

	database := &DB{DB: sqlDB}

	if err := database.migrate(); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("running migrations: %w (closing database: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	// Again after migrations: the WAL/SHM sidecars do not exist yet at the call
	// above (verified — the pragma alone does not create them), so they are only
	// tightened here. They currently land at 0600 regardless, because SQLite
	// derives their mode from the main database file we just restricted, but that
	// is driver behavior rather than a contract — this makes it explicit.
	if err := restrictPermissions(path); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("%w (closing database: %v)", err, closeErr)
		}
		return nil, err
	}

	return database, nil
}

// restrictPermissions tightens the database file and its WAL/SHM sidecars to
// owner-only. SQLite creates these itself — 0644 under a typical umask — so they
// can only be fixed after the fact; the data directory is created 0700 by the
// caller, which keeps the brief window between creation and chmod from being
// useful. Applied on every open, so databases predating this get tightened too.
//
// Paths that do not exist are skipped: an in-memory database (":memory:") has no
// file, and the sidecars are absent until WAL mode has something to write.
func restrictPermissions(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, secureFileMode); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("restricting permissions on %s: %w", p, err)
		}
	}
	return nil
}
