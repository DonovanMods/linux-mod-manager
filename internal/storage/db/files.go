package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
func (d *DB) GetDeployedFilesForMod(gameID, profileName, sourceID, modID string) ([]string, error) {
	rows, err := d.Query(`
		SELECT relative_path FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
		ORDER BY relative_path
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("querying deployed files: %w", err)
	}
	defer rows.Close()

	var paths []string
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
func (d *DB) CheckFileConflicts(gameID, profileName string, paths []string) ([]FileConflict, error) {
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
	defer rows.Close()

	var conflicts []FileConflict
	for rows.Next() {
		var c FileConflict
		if err := rows.Scan(&c.RelativePath, &c.SourceID, &c.ModID); err != nil {
			return nil, fmt.Errorf("scanning conflict: %w", err)
		}
		conflicts = append(conflicts, c)
	}
	return conflicts, rows.Err()
}
