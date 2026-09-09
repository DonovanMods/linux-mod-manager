package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// secureFileMode is the permission mask for the database and its WAL/SHM sidecars.
// The auth_tokens table holds API keys - encrypted since #79, but the file
// must still not be readable by other local users.
const secureFileMode = 0600

// DB wraps the SQLite database connection
type DB struct {
	*sql.DB
	log *slog.Logger

	// path is the database file this handle opened, absolute, or
	// ":memory:". Kept so an error can name the file the user has to act
	// on - the credential scrub's "close the other lmm process and try
	// again" is useless without it.
	path string

	// keyPath is the token-encryption key file (#79); "" means an
	// ephemeral process-lifetime key, which is what an in-memory database
	// gets. key is loaded at most once, on first need, under keyMu.
	keyPath string
	keyMu   sync.Mutex
	key     []byte
}

// Options configures Open beyond the database path.
//
// KeyPath is the token-encryption key file (#79). The composition root
// (internal/app) resolves it - <DataDir>/key - and passes it down, so
// neither core nor this package has to know the XDG layout. Left empty it
// defaults beside the database file (defaultKeyPath), which is what the
// path-only constructors below rely on; an in-memory database gets an
// ephemeral key instead. There is no configuration that stores a token in
// the clear.
type Options struct {
	Logger  *slog.Logger
	KeyPath string
}

// dsnFor builds the modernc.org/sqlite DSN. Pragmas passed as _pragma= query
// parameters run on EVERY new pooled connection (the driver applies them in
// newConn); a plain Exec after Open would only reach one connection (#271).
// The path is percent-encoded so '#', '?', '%' and spaces cannot be read as
// URI syntax. path must be absolute (or ":memory:") — url.URL renders a
// relative Path as "file://<path>", which parses back with the path's first
// segment as the host instead of as part of the file path; New resolves
// relative paths before calling this.
func dsnFor(path string) string {
	const pragmas = "_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		return "file::memory:?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: pragmas}
	return u.String()
}

// New creates a new database connection and runs migrations, with a
// discarding logger. See Open for the logger-aware constructor and
// OpenWithOptions for the one that takes a token-encryption key path.
func New(path string) (*DB, error) {
	return Open(path, nil)
}

// Open creates a new database connection and runs migrations, logging
// migration activity to log (nil means discard). A relative path is
// resolved against the current working directory before being placed in
// the DSN, since a relative path in a "file:" URI is ambiguous (see dsnFor).
func Open(path string, log *slog.Logger) (*DB, error) {
	return OpenWithOptions(path, Options{Logger: log})
}

// OpenWithOptions is Open with the full option set - today, the
// token-encryption key path (#79).
func OpenWithOptions(path string, opts Options) (*DB, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	dsnPath := path
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolving database path: %w", err)
		}
		dsnPath = abs
	}

	sqlDB, err := sql.Open("sqlite", dsnFor(dsnPath))
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if path == ":memory:" {
		// Each pooled connection to ":memory:" is a separate database;
		// tests rely on there being exactly one.
		sqlDB.SetMaxOpenConns(1)
	}
	// Force the first connection now so a bad path fails here, not on
	// the first query.
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Before any write, so the main database file is already 0600 when SQLite
	// creates the WAL/SHM sidecars — it derives their mode from this file. The
	// sidecars do not exist yet at this point; they are handled after migrations.
	if err := restrictPermissions(path); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("%w (closing database: %v)", err, closeErr)
		}
		return nil, err
	}

	keyPath := opts.KeyPath
	if keyPath == "" {
		keyPath = defaultKeyPath(dsnPath)
	}
	database := &DB{DB: sqlDB, log: log, path: dsnPath, keyPath: keyPath}

	// One root context for the whole open sequence: the schema migrations
	// and the credential re-encryption below share it, so this package keeps
	// exactly one context.Background() call site (CLAUDE.md's ctx census).
	openCtx := context.Background()

	if err := database.migrate(openCtx); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("running migrations: %w (closing database: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	// #79: any credential still sitting in the clear from a pre-encryption
	// lmm is re-encrypted here, before anything can read the table. Runs on
	// every open rather than as a numbered schema migration - see
	// migrateTokenEncryption for why.
	if err := database.migrateTokenEncryption(openCtx); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("%w (closing database: %v)", err, closeErr)
		}
		return nil, err
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
