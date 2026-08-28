package db

import (
	"context"
	"fmt"
)

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

	// Get current version
	var version int
	err := d.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		return fmt.Errorf("getting schema version: %w", err)
	}

	// Apply migrations
	migrations := []func(context.Context, *DB) error{
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
	}

	for i := version; i < len(migrations); i++ {
		if err := migrations[i](ctx, d); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := d.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", i+1); err != nil {
			return fmt.Errorf("recording migration %d: %w", i+1, err)
		}
	}

	return nil
}

func migrateV1(ctx context.Context, d *DB) error {
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

func migrateV2(ctx context.Context, d *DB) error {
	// Add previous_version column for rollback support
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN previous_version TEXT`)
	return err
}

func migrateV3(ctx context.Context, d *DB) error {
	// Add link_method column to track deployment method per mod
	// Default 0 = symlink (LinkSymlink)
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN link_method INTEGER DEFAULT 0`)
	return err
}

func migrateV4(ctx context.Context, d *DB) error {
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

func migrateV5(ctx context.Context, d *DB) error {
	// Add deployed column to track whether mod files are currently in game directory
	// enabled = user intent (wants mod active)
	// deployed = current state (files are in game directory)
	// Default 1 for existing mods (assume they're deployed)
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN deployed INTEGER DEFAULT 1`)
	return err
}

func migrateV6(ctx context.Context, d *DB) error {
	// Add checksum column for cache integrity verification
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mod_files ADD COLUMN checksum TEXT`)
	return err
}

func migrateV7(ctx context.Context, d *DB) error {
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

func migrateV8(ctx context.Context, d *DB) error {
	// Add manual_download column to track mods that require manual download
	// (e.g., CurseForge mods with API restrictions)
	// Default 0 = false (can be auto-downloaded)
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN manual_download INTEGER DEFAULT 0`)
	return err
}

func migrateV9(ctx context.Context, d *DB) error {
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

func migrateV10(ctx context.Context, d *DB) error {
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN previous_file_ids TEXT DEFAULT '[]'`)
	return err
}

// migrateV11 drops mod_cache, created in v1 and never read or written. The cache
// is keyed entirely by directory layout (internal/storage/cache), so a DB mirror
// would only add a way for the two to disagree; the metadata that turned out to
// matter was added to installed_mods in v9 instead.
func migrateV11(ctx context.Context, d *DB) error {
	_, err := d.ExecContext(ctx, `DROP TABLE IF EXISTS mod_cache`)
	return err
}

func migrateV12(ctx context.Context, d *DB) error {
	// #221: per-mod pak-to-exmod conversion opt-out. Default 1 = convert
	// (paks join the merged pak); 0 = deploy raw. Deliberately excluded
	// from SaveInstalledMod's upsert so reinstall preserves the user's choice.
	_, err := d.ExecContext(ctx, `ALTER TABLE installed_mods ADD COLUMN convert_paks INTEGER DEFAULT 1`)
	return err
}
