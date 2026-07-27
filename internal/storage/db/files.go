package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FileOwner represents the mod that owns a deployed file
type FileOwner struct {
	SourceID string
	ModID    string
}

// FileConflict represents a file that would be overwritten
type FileConflict struct {
	RelativePath string
	SourceID     string
	ModID        string
}

// SaveDeployedFile records that a file is deployed by a specific mod.
// Uses upsert to handle overwrites (new mod takes ownership).
func (d *DB) SaveDeployedFile(gameID, profileName, relativePath, sourceID, modID string) error {
	_, err := d.Exec(`
		INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(game_id, profile_name, relative_path) DO UPDATE SET
			source_id = excluded.source_id,
			mod_id = excluded.mod_id,
			deployed_at = CURRENT_TIMESTAMP
	`, gameID, profileName, relativePath, sourceID, modID)
	if err != nil {
		return fmt.Errorf("saving deployed file: %w", err)
	}
	return nil
}

// GetLastDeployTime returns the most recent deployed_at recorded for
// gameID/profileName across every tracked file (#106a's dashboard "Last
// deploy" row), or nil if the profile has never had a file deployed. A
// never-deployed profile is a normal state, not an error.
//
// Deliberately ORDER BY ... DESC LIMIT 1 rather than SELECT MAX(deployed_at):
// the modernc.org/sqlite driver converts a TEXT column to time.Time on Scan
// by consulting the column's DECLARED type (deployed_at is DATETIME, per
// migrateV7) - but MAX(deployed_at) is a computed expression with no
// declared type of its own, so the driver hands back the raw SQLite storage
// string instead and Scan(&time.Time) fails ("unsupported Scan ... string
// into type *time.Time"), verified empirically against this driver version.
// Selecting the actual column keeps the declared-type conversion intact and
// is equivalent to MAX for this query shape (both scoped to one game/profile,
// ORDER BY DESC LIMIT 1 is exactly "the row with the largest deployed_at").
func (d *DB) GetLastDeployTime(gameID, profileName string) (*time.Time, error) {
	var deployedAt time.Time
	err := d.QueryRow(`
		SELECT deployed_at FROM deployed_files
		WHERE game_id = ? AND profile_name = ?
		ORDER BY deployed_at DESC
		LIMIT 1
	`, gameID, profileName).Scan(&deployedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting last deploy time: %w", err)
	}
	return &deployedAt, nil
}

// GetFileOwner returns the mod that owns a specific file path.
// Returns nil if no mod owns the file.
func (d *DB) GetFileOwner(gameID, profileName, relativePath string) (*FileOwner, error) {
	var owner FileOwner
	err := d.QueryRow(`
		SELECT source_id, mod_id FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND relative_path = ?
	`, gameID, profileName, relativePath).Scan(&owner.SourceID, &owner.ModID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting file owner: %w", err)
	}
	return &owner, nil
}

// DeleteDeployedFiles removes all deployed file records for a specific mod.
func (d *DB) DeleteDeployedFiles(gameID, profileName, sourceID, modID string) error {
	_, err := d.Exec(`
		DELETE FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return fmt.Errorf("deleting deployed files: %w", err)
	}
	return nil
}

// GetDeployedFilesForMod returns all file paths deployed by a specific mod.
func (d *DB) GetDeployedFilesForMod(gameID, profileName, sourceID, modID string) (paths []string, err error) {
	rows, err := d.Query(`
		SELECT relative_path FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
		ORDER BY relative_path
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("querying deployed files: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scanning path: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

// CheckFileConflicts checks which of the given paths are already owned by other mods.
// Returns a slice of conflicts (empty if no conflicts).
func (d *DB) CheckFileConflicts(gameID, profileName string, paths []string) (conflicts []FileConflict, err error) {
	if len(paths) == 0 {
		return nil, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(paths))
	args := make([]interface{}, 0, len(paths)+2)
	args = append(args, gameID, profileName)
	for i, p := range paths {
		placeholders[i] = "?"
		args = append(args, p)
	}

	query := fmt.Sprintf(`
		SELECT relative_path, source_id, mod_id FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND relative_path IN (%s)
		ORDER BY relative_path
	`, strings.Join(placeholders, ","))

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("checking conflicts: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	for rows.Next() {
		var c FileConflict
		if err := rows.Scan(&c.RelativePath, &c.SourceID, &c.ModID); err != nil {
			return nil, fmt.Errorf("scanning conflict: %w", err)
		}
		conflicts = append(conflicts, c)
	}
	return conflicts, rows.Err()
}
