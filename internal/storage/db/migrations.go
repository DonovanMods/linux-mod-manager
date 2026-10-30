package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// migrationExec is what a migration needs: a statement executor. The one a
// pending run passes is the single connection holding its write
// transaction (see migrate).
type migrationExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (d *DB) migrate(ctx context.Context) error {
	// Create migrations table if it doesn't exist
	if _, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	// Apply migrations
	migrations := []func(context.Context, migrationExec) error{
		migrateV1,
		migrateV2,
		migrateV3,
		migrateV4,
		migrateV5,
		migrateV6,
		migrateV7,
		migrateV8,
		migrateV9,
		migrateV10,
		migrateV11,
		migrateV12,
		migrateV13,
		migrateV14,
		migrateV15,
		migrateV16,
		migrateV17,
		migrateV18,
		d.migrateV19,
		migrateV20,
		migrateV21,
	}

	// The ordinary open: the schema is current, and finding that out takes
	// one read and no lock.
	version, err := schemaVersion(ctx, d)
	if err != nil {
		return err
	}
	if version >= len(migrations) {
		return nil
	}

	// Something is pending. Several lmm processes can get here at once - a
	// `lmm serve` started beside a CLI command right after an upgrade - and
	// each used to run the pending migrations and then fail to record them
	// (UNIQUE constraint on schema_migrations), or, for a migration that
	// records an obligation (migrateV17), record it again after another
	// process had discharged it. So the pending run is one IMMEDIATE
	// transaction on one connection: it takes the write lock up front (a
	// second opener waits out busy_timeout), re-reads the version under it,
	// and applies and records exactly what is still missing - atomically,
	// since SQLite's DDL is transactional.
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserving a connection for migrations: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("locking the database for migrations: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if version, err = schemaVersion(ctx, conn); err != nil {
		return err
	}
	if version < len(migrations) {
		d.log.Debug("running migrations", "from", version, "to", len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if err := migrations[i](ctx, conn); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", i+1); err != nil {
			return fmt.Errorf("recording migration %d: %w", i+1, err)
		}
		d.log.Debug("migration applied", "version", i+1)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("committing migrations: %w", err)
	}
	committed = true
	return nil
}

// schemaVersion is the highest migration recorded, read through q.
func schemaVersion(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (int, error) {
	var version int
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("getting schema version: %w", err)
	}
	return version, nil
}

func migrateV1(ctx context.Context, d migrationExec) error {
	statements := []string{
		`CREATE TABLE installed_mods (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			game_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			name TEXT NOT NULL,
			version TEXT NOT NULL,
			author TEXT,
			update_policy INTEGER DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			installed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(source_id, mod_id, game_id, profile_name)
		)`,
		`CREATE INDEX idx_installed_mods_game_profile ON installed_mods(game_id, profile_name)`,
		`CREATE TABLE mod_cache (
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			game_id TEXT NOT NULL,
			metadata TEXT,
			cached_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(source_id, mod_id, game_id)
		)`,
		`CREATE TABLE auth_tokens (
			source_id TEXT PRIMARY KEY,
			token_data BLOB,
			expires_at DATETIME,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, stmt := range statements {
		if _, err := d.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("executing %q: %w", stmt[:50], err)
		}
	}

	return nil
}

func migrateV2(ctx context.Context, d migrationExec) error {
	// Add previous_version column for rollback support
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN previous_version TEXT`)
	return err
}

func migrateV3(ctx context.Context, d migrationExec) error {
	// Add link_method column to track deployment method per mod
	// Default 0 = symlink (LinkSymlink)
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN link_method INTEGER DEFAULT 0`)
	return err
}

func migrateV4(ctx context.Context, d migrationExec) error {
	// Create table to track which source files were downloaded for each installed mod
	// Supports multiple files per mod (e.g., MAIN + OPTIONAL files)
	_, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS installed_mod_files (
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			game_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			file_id TEXT NOT NULL,
			PRIMARY KEY(source_id, mod_id, game_id, profile_name, file_id),
			FOREIGN KEY(source_id, mod_id, game_id, profile_name)
				REFERENCES installed_mods(source_id, mod_id, game_id, profile_name)
				ON DELETE CASCADE
		)
	`)
	return err
}

func migrateV5(ctx context.Context, d migrationExec) error {
	// Add deployed column to track whether mod files are currently in game directory
	// enabled = user intent (wants mod active)
	// deployed = current state (files are in game directory)
	// Default 1 for existing mods (assume they're deployed)
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN deployed INTEGER DEFAULT 1`)
	return err
}

func migrateV6(ctx context.Context, d migrationExec) error {
	// Add checksum column for cache integrity verification
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mod_files ADD COLUMN checksum TEXT`)
	return err
}

func migrateV7(ctx context.Context, d migrationExec) error {
	// Create table to track which mod owns each deployed file
	// Used for conflict detection when installing mods
	_, err := d.ExecContext(ctx, `
		CREATE TABLE deployed_files (
			game_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (game_id, profile_name, relative_path)
		)
	`)
	return err
}

func migrateV8(ctx context.Context, d migrationExec) error {
	// Add manual_download column to track mods that require manual download
	// (e.g., CurseForge mods with API restrictions)
	// Default 0 = false (can be auto-downloaded)
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN manual_download INTEGER DEFAULT 0`)
	return err
}

func migrateV9(ctx context.Context, d migrationExec) error {
	// Add metadata columns for expanded mod information
	statements := []string{
		`ALTER TABLE installed_mods ADD COLUMN summary TEXT DEFAULT ''`,
		`ALTER TABLE installed_mods ADD COLUMN source_url TEXT DEFAULT ''`,
	}
	for _, stmt := range statements {
		if _, err := d.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("executing %q: %w", stmt, err)
		}
	}
	return nil
}

func migrateV10(ctx context.Context, d migrationExec) error {
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN previous_file_ids TEXT DEFAULT '[]'`)
	return err
}

// migrateV11 drops mod_cache, created in v1 and never read or written. The cache
// is keyed entirely by directory layout (internal/storage/cache), so a DB mirror
// would only add a way for the two to disagree; the metadata that turned out to
// matter was added to installed_mods in v9 instead.
func migrateV11(ctx context.Context, d migrationExec) error {
	_, err := d.ExecContext(ctx, `DROP TABLE IF EXISTS mod_cache`)
	return err
}

func migrateV12(ctx context.Context, d migrationExec) error {
	// #221: per-mod pak-to-exmod conversion opt-out. Default 1 = convert
	// (paks join the merged pak); 0 = deploy raw. Deliberately excluded
	// from SaveInstalledMod's upsert so reinstall preserves the user's choice.
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN convert_paks INTEGER DEFAULT 1`)
	return err
}

// migrateV13 gives auth_tokens a created_at, so a status surface can say how
// long a credential has been held rather than only when it was last written
// (#79 - the encrypted table shows presence and age instead of the key).
// SQLite cannot default an added column to CURRENT_TIMESTAMP, so it is added
// nullable and backfilled from updated_at, which for a row that was never
// re-logged-in is the same instant.
func migrateV13(ctx context.Context, d migrationExec) error {
	if _, err := d.ExecContext(ctx, `ALTER TABLE auth_tokens ADD COLUMN created_at DATETIME`); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx, `UPDATE auth_tokens SET created_at = updated_at WHERE created_at IS NULL`)
	return err
}

// migrateV14 adds db_meta, a one-row-per-key store for facts about the
// database itself rather than about mods. Its first and so far only key is
// #79's "token_scrub_pending": a durable record that the pre-encryption
// plaintext has NOT yet been removed from the files on disk.
//
// It has to be durable, and it has to live outside auth_tokens. The scrub
// (VACUUM + WAL checkpoint) cannot run inside the transaction that writes
// the envelopes — VACUUM cannot run in a transaction at all — so the rows
// are already sealed by the time it starts. Deriving "there is scrubbing to
// do" from the rows therefore loses the obligation the moment the first
// attempt fails: every later open sees a fully encrypted table and skips
// work that never happened. The marker is written in the SAME transaction
// as the envelopes and deleted only once both scrub steps have proved they
// completed, so the obligation survives a crash, a contended run, and a
// process that is killed mid-scrub.
func migrateV14(ctx context.Context, d migrationExec) error {
	_, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS db_meta (
			key TEXT PRIMARY KEY,
			value TEXT
		)
	`)
	return err
}

// migrateV15 adds #269's two external-mod columns: external marks a mod lmm
// TRACKS but never deploys (a Steam Workshop item, whose files the Steam
// client owns where they sit), and external_path records the directory that
// agent owns.
//
// Both are written by SaveInstalledMod's upsert, unlike convert_paks: they
// are facts about WHAT the mod is rather than a user preference, so a
// re-adopt must be able to move a mod's recorded path when Steam moved the
// library. Existing rows default to 0/” - every mod installed before this
// migration is an ordinary managed one.
func migrateV15(ctx context.Context, d migrationExec) error {
	if _, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN external INTEGER DEFAULT 0`); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN external_path TEXT DEFAULT ''`)
	return err
}

// migrateV16 persists domain.Mod.UpdatedAt - when the SOURCE last published
// a revision of the mod, as distinct from installed_at (when lmm recorded
// it).
//
// It was previously fetched live and dropped on save, which was harmless
// while every version was a readable string. #269 made it load-bearing: a
// Steam Workshop item's version IS a 19-digit content id, and the approved
// design says no human-facing surface prints one - `lmm list`, the update
// summary and the web UI's rows show the revision DATE instead. That date
// has to survive a round trip through the database to be shown by a
// listing, which reads nothing else.
//
// NULL for every existing row, which decodes to the zero time - exactly
// what those rows carried in memory before this column existed.
func migrateV16(ctx context.Context, d migrationExec) error {
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN updated_at DATETIME`)
	return err
}

// migrateV17 records #431's one-time profile-document backfill as OWED -
// under MetaProfileDisabledBackfill, which core discharges - when this
// database, written by an lmm that predates the profile document's
// `disabled:` marker, holds a row the backfill might act on: a managed row
// that says both enabled = 0 and deployed = 0. A fresh database (no rows)
// never owes it, and neither does one whose only disabled rows are ones a
// profile switch left at (0, 1) or external Workshop items (#269).
//
// Why a migration and not a check at open. The evidence the backfill reads
// is the rows themselves, and the new binary's own flows write the same
// flag values for other reasons - its profile switch clears deployed on
// every mod it switches away from. So the obligation has to be fixed before
// any of those flows can run, and the one step that runs before all of them,
// exactly once per database, is this. Deciding it later from whatever the
// rows look like by then would misread them.
//
// This predicate is deliberately a SUPERSET of the one core applies (which
// adds "under the game's single explicitly-default profile"): it only has
// to be true whenever there could be work.
//
// It also drops MetaProfileDisabledBackfillLegacy, which only a database a
// fix-round-1 development build touched can hold.
func migrateV17(ctx context.Context, d migrationExec) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM db_meta WHERE key = ?`, MetaProfileDisabledBackfillLegacy); err != nil {
		return err
	}
	_, err := d.ExecContext(ctx, `
		INSERT OR IGNORE INTO db_meta (key, value)
		SELECT ?, ?
		WHERE EXISTS (
			SELECT 1 FROM installed_mods
			WHERE enabled = 0 AND deployed = 0 AND COALESCE(external, 0) = 0
		)
	`, MetaProfileDisabledBackfill, time.Now().UTC().Format(time.RFC3339))
	return err
}

// migrateV18 gives deployed_files what a removal needs to tell lmm's file
// from the user's, and where the file is.
//
// checksum (hex SHA-256), size, mtime and ctime (both Unix nanoseconds)
// fingerprint the content a copy or hardlink deployment wrote (#466): under
// those methods the deployed path is a regular file, so without them a file
// the user replaced looks exactly like lmm's own and a purge deleted it. A
// symlink deployment leaves them NULL - its link target is its identity.
// Size, mtime and ctime together are the cheap pre-check before hashing;
// ctime is there because a user can copy a size and an mtime across, but
// cannot set a ctime.
//
// mod_path is the absolute mod_path the row's relative_path was deployed
// under (#451), so a mod_path changed behind lmm's back (a games.yaml hand
// edit) is detected rather than silently stranding the files.
//
// Every existing row keeps NULL in all four: nothing is backfilled. A
// NULL fingerprint is reported as unverified and removed as before; a NULL
// mod_path is taken to be the game's current one, as it always was. The
// next deploy of the path fills them in.
//
// Each column is added only when it is missing, so a schema_migrations
// table that lost its record of v18 re-runs it harmlessly.
func migrateV18(ctx context.Context, d migrationExec) error {
	for _, col := range []struct{ name, decl string }{
		{"checksum", "TEXT"},
		{"size", "INTEGER"},
		{"mtime", "INTEGER"},
		{"ctime", "INTEGER"},
		{"mod_path", "TEXT"},
	} {
		var n int
		if err := d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('deployed_files') WHERE name = ?`, col.name).Scan(&n); err != nil {
			return fmt.Errorf("inspecting deployed_files: %w", err)
		}
		if n > 0 {
			continue
		}
		if _, err := d.ExecContext(ctx, "ALTER TABLE deployed_files ADD COLUMN "+col.name+" "+col.decl); err != nil {
			return fmt.Errorf("adding deployed_files.%s: %w", col.name, err)
		}
	}
	return nil
}

// migrateV19 rewrites installed_mods.installed_at and updated_at into
// storedTimeLayout (#515). Until now SaveInstalledMod handed the driver a bare
// time.Time, which it stored as time.Time.String(): local-zone text with the
// monotonic reading attached ("2026-10-01 13:30:57.086694056 -0400 EDT
// m=+1.598230846"). That text sorts by wall clock in whatever zone wrote it,
// so ORDER BY installed_at misordered rows across zones and DST, and the
// "m=" half is process uptime that has no meaning once stored.
//
// Each value is parsed (parseStoredTime takes the String() form with or
// without its suffix, the driver's formats and CURRENT_TIMESTAMP's) and
// stored back as UTC. A value that is already in the new form is rewritten
// to itself, so a re-run is harmless. A value that cannot be parsed is left
// exactly as it was and logged - never dropped, never zeroed. Reading it
// then fails, naming the column and the text, as an unreadable row always
// has.
//
// The other DATETIME columns (deployed_files.deployed_at, auth_tokens.
// created_at/updated_at, schema_migrations.applied_at) are filled by SQLite's
// CURRENT_TIMESTAMP, which is already UTC and fixed-width, so they stay.
func (d *DB) migrateV19(ctx context.Context, q migrationExec) error {
	// CAST drops the declared DATETIME type, so the driver returns the stored
	// text instead of a time.Time it has already parsed (and, for the junk
	// this migration must survive, failed to).
	rows, err := q.QueryContext(ctx, `
		SELECT id, CAST(installed_at AS TEXT), CAST(updated_at AS TEXT)
		FROM installed_mods
	`)
	if err != nil {
		return fmt.Errorf("reading installed_mods timestamps: %w", err)
	}
	type rewrite struct {
		id                 int64
		installed, updated *string
	}
	var todo []rewrite
	for rows.Next() {
		var id int64
		var installed, updated *string
		if err := rows.Scan(&id, &installed, &updated); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scanning installed_mods timestamps: %w", err)
		}
		next := rewrite{id: id}
		var changed bool
		for _, c := range []struct {
			col  string
			from *string
			to   **string
		}{{"installed_at", installed, &next.installed}, {"updated_at", updated, &next.updated}} {
			if c.from == nil {
				continue
			}
			t, ok := parseStoredTime(*c.from)
			if !ok {
				d.log.Warn("leaving an unparseable timestamp as it was", "table", "installed_mods", "id", id, "column", c.col, "value", *c.from)
				continue
			}
			if text := formatTime(t); text != *c.from {
				*c.to = &text
				changed = true
			}
		}
		if changed {
			todo = append(todo, next)
		}
	}
	rowsErr := rows.Err()
	if err := rows.Close(); err != nil {
		return fmt.Errorf("closing installed_mods timestamps: %w", err)
	}
	if rowsErr != nil {
		return rowsErr
	}

	// The reader is closed first: the migration holds one connection, and a
	// second statement cannot run on it beside an open cursor.
	for _, r := range todo {
		if _, err := q.ExecContext(ctx, `
			UPDATE installed_mods
			SET installed_at = COALESCE(?, installed_at), updated_at = COALESCE(?, updated_at)
			WHERE id = ?
		`, r.installed, r.updated, r.id); err != nil {
			return fmt.Errorf("rewriting installed_mods timestamps: %w", err)
		}
	}
	return nil
}

// migrateV20 adds installed_mods.skipped_version (#542): the pending update
// the user chose to skip without pinning the mod. NULL skips nothing. Like
// update_policy and convert_paks it is a user setting, so SaveInstalledMod's
// upsert never writes it; SetModSkippedVersion does, and ApplyModUpdate
// clears it.
//
// The column is added only when it is missing, so a schema whose recorded
// version was rolled back (the timestamp migration's own tests re-run every
// migration from 19 on) re-applies this cleanly.
func migrateV20(ctx context.Context, q migrationExec) error {
	var have int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('installed_mods') WHERE name = 'skipped_version'`).Scan(&have); err != nil {
		return fmt.Errorf("inspecting installed_mods: %w", err)
	}
	if have > 0 {
		return nil
	}
	_, err := q.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN skipped_version TEXT`)
	return err
}

// migrateV21 adds manual_only_mods (#543): the mods a source will not serve
// through its API (source.ErrManualDownload, or the source's own
// classification - CurseForge's allowModDistribution), so they can only be
// updated from a hand-downloaded file.
//
// A table of its own, not an installed_mods column: the fact is about the
// source's mod, not one profile's row of it, and it has to exist before any
// row does - an install the source refused saves nothing, and the from-file
// install that follows must still know. Keyed by game as well, because a
// NexusMods mod id is only unique within its game.
func migrateV21(ctx context.Context, d migrationExec) error {
	_, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS manual_only_mods (
			game_id TEXT NOT NULL,
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (game_id, source_id, mod_id)
		)
	`)
	return err
}
