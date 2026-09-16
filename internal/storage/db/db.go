package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// secureFileMode is the permission mask for the database and its WAL/SHM sidecars.
// The auth_tokens table holds API keys - encrypted since #79, but the file
// must still not be readable by other local users.
const secureFileMode = 0600

// DB wraps the SQLite database connection
type DB struct {
	*sql.DB
	log *slog.Logger

	// warn is the always-on channel for the ONE class of message a user
	// has to see whatever their --log-level: an operational wait that
	// holds the open for tens of seconds (the contended credential scrub,
	// #79). nil means silent. It is deliberately not a second logger -
	// anything diagnostic belongs in log.
	warn io.Writer

	// profileBackfillOwed is whether, when this handle opened, db_meta held
	// any record of #431's profile-document backfill (see
	// OwesProfileBackfill).
	profileBackfillOwed bool

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

	// WarnWriter receives the handful of lines a user must see even at the
	// CLI's default --log-level off, because they explain a wait that is
	// happening right now: today, only the contended credential scrub
	// (#79, re-review N1). The composition root supplies stderr. nil means
	// silent, which is what every test and every in-process caller that has
	// no console gets. Scope is deliberate - this is not a logging
	// channel, and diagnostics go to Logger.
	WarnWriter io.Writer
}

// dsnFor builds the modernc.org/sqlite DSN. Pragmas passed as _pragma= query
// parameters run on EVERY new pooled connection (the driver applies them in
// newConn); a plain Exec after Open would only reach one connection (#271).
// The path is percent-encoded so '#', '?', '%' and spaces cannot be read as
// URI syntax. path must be absolute (or ":memory:") — url.URL renders a
// relative Path as "file://<path>", which parses back with the path's first
// segment as the host instead of as part of the file path; New resolves
// relative paths before calling this.
//
// journal_mode is deliberately not among them: WAL is a property of the
// database FILE, not of a connection, so it is set once per open by
// enableWAL - which has to retry it, and a DSN pragma cannot.
func dsnFor(path string) string {
	const pragmas = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		return "file::memory:?" + pragmas
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: pragmas}
	return u.String()
}

// walBudget bounds enableWAL's retries: as long as busy_timeout would have
// waited, had SQLite consulted it.
const walBudget = 5 * time.Second

// enableWAL switches the database file into write-ahead-log mode. Once the
// file is in WAL mode this is a no-op that takes no lock; the first switch
// of a brand-new file needs the write lock, and SQLite answers SQLITE_BUSY
// AT ONCE when another connection holds it, without consulting
// busy_timeout. Several lmm processes starting together on a fresh
// installation (`lmm serve` beside a CLI command) all make that first
// switch, and all but one used to fail to open with "database is locked".
// So a busy answer is retried, briefly, until walBudget runs out.
func (d *DB) enableWAL(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, walBudget)
	defer cancel()
	pause := 5 * time.Millisecond
	for {
		var mode string
		err := d.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode)
		if err == nil {
			return nil
		}
		if !isBusy(err) {
			return fmt.Errorf("enabling the write-ahead log: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("enabling the write-ahead log: %w", err)
		case <-time.After(pause):
		}
		pause = min(2*pause, 100*time.Millisecond)
	}
}

// isBusy reports whether err is SQLite's SQLITE_BUSY, in any of its
// extended forms.
func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
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
	database := &DB{DB: sqlDB, log: log, warn: opts.WarnWriter, path: dsnPath, keyPath: keyPath}

	// One root context for the whole open sequence: the schema migrations
	// and the credential re-encryption below share it, so this package keeps
	// exactly one context.Background() call site (CLAUDE.md's ctx census).
	openCtx := context.Background()

	if path != ":memory:" {
		if err := database.enableWAL(openCtx); err != nil {
			if closeErr := sqlDB.Close(); closeErr != nil {
				return nil, fmt.Errorf("opening database: %w (closing database: %v)", err, closeErr)
			}
			return nil, fmt.Errorf("opening database: %w", err)
		}
	}

	if err := database.migrate(openCtx); err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("running migrations: %w (closing database: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	// #431: whether the profile-document backfill is owed, read once here
	// on the open's own context so core can answer every later mutation
	// from memory (OwesProfileBackfill).
	owed, err := database.MetaWithPrefix(openCtx, MetaProfileDisabledBackfill)
	if err != nil {
		if closeErr := sqlDB.Close(); closeErr != nil {
			return nil, fmt.Errorf("%w (closing database: %v)", err, closeErr)
		}
		return nil, err
	}
	database.profileBackfillOwed = len(owed) > 0

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
