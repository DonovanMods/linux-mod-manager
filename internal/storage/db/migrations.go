package db

import "fmt"

const currentVersion = 4

func (d *DB) migrate() error {
	// Create migrations table if it doesn't exist
	if _, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	// Get current version
	var version int
	err := d.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		return fmt.Errorf("getting schema version: %w", err)
	}

	// Apply migrations
	migrations := []func(*DB) error{
		migrateV1,
		migrateV2,
		migrateV3,
		migrateV4,
	}

	for i := version; i < len(migrations); i++ {
		if err := migrations[i](d); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := d.Exec("INSERT INTO schema_migrations (version) VALUES (?)", i+1); err != nil {
			return fmt.Errorf("recording migration %d: %w", i+1, err)
		}
	}

	return nil
}

func migrateV1(d *DB) error {
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
		if _, err := d.Exec(stmt); err != nil {
			return fmt.Errorf("executing %q: %w", stmt[:50], err)
		}
	}

	return nil
}

func migrateV2(d *DB) error {
	// Add previous_version column for rollback support
	_, err := d.Exec(`ALTER TABLE installed_mods ADD COLUMN previous_version TEXT`)
	return err
}

func migrateV3(d *DB) error {
	// Add link_method column to track deployment method per mod
	// Default 0 = symlink (LinkSymlink)
	_, err := d.Exec(`ALTER TABLE installed_mods ADD COLUMN link_method INTEGER DEFAULT 0`)
	return err
}

func migrateV4(d *DB) error {
	// Create table to track which source files were downloaded for each installed mod
	// Supports multiple files per mod (e.g., MAIN + OPTIONAL files)
	_, err := d.Exec(`
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
