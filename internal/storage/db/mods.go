package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// SaveInstalledMod inserts or updates an installed mod record
func (d *DB) SaveInstalledMod(mod *domain.InstalledMod) error {
	var prevVersion *string
	if mod.PreviousVersion != "" {
		prevVersion = &mod.PreviousVersion
	}

	_, err := d.Exec(`
		INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, deployed, installed_at, previous_version, link_method)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, mod_id, game_id, profile_name) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			author = excluded.author,
			update_policy = excluded.update_policy,
			enabled = excluded.enabled,
			deployed = excluded.deployed,
			previous_version = excluded.previous_version,
			link_method = excluded.link_method
	`, mod.SourceID, mod.ID, mod.GameID, mod.ProfileName, mod.Name, mod.Version, mod.Author, mod.UpdatePolicy, mod.Enabled, mod.Deployed, time.Now(), prevVersion, mod.LinkMethod)
	if err != nil {
		return fmt.Errorf("saving installed mod: %w", err)
	}

	// Save file IDs to separate table
	return d.replaceModFileIDs(mod.SourceID, mod.ID, mod.GameID, mod.ProfileName, mod.FileIDs)
}

// GetInstalledMods returns all installed mods for a game/profile combination
func (d *DB) GetInstalledMods(gameID, profileName string) ([]domain.InstalledMod, error) {
	rows, err := d.Query(`
		SELECT source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, deployed, installed_at, previous_version, link_method
		FROM installed_mods
		WHERE game_id = ? AND profile_name = ?
		ORDER BY installed_at ASC
	`, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying installed mods: %w", err)
	}
	defer rows.Close()

	var mods []domain.InstalledMod
	for rows.Next() {
		var mod domain.InstalledMod
		var prevVersion *string
		err := rows.Scan(
			&mod.SourceID, &mod.ID, &mod.GameID, &mod.ProfileName,
			&mod.Name, &mod.Version, &mod.Author, &mod.UpdatePolicy,
			&mod.Enabled, &mod.Deployed, &mod.InstalledAt, &prevVersion, &mod.LinkMethod,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning installed mod: %w", err)
		}
		if prevVersion != nil {
			mod.PreviousVersion = *prevVersion
		}
		mods = append(mods, mod)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch file IDs for each mod
	for i := range mods {
		fileIDs, err := d.GetModFileIDs(mods[i].SourceID, mods[i].ID, gameID, profileName)
		if err != nil {
			return nil, fmt.Errorf("getting file IDs for %s: %w", mods[i].ID, err)
		}
		mods[i].FileIDs = fileIDs
	}

	return mods, nil
}

// DeleteInstalledMod removes an installed mod record
func (d *DB) DeleteInstalledMod(sourceID, modID, gameID, profileName string) error {
	result, err := d.Exec(`
		DELETE FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("deleting installed mod: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// UpdateModPolicy updates the update policy for an installed mod
func (d *DB) UpdateModPolicy(sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) error {
	result, err := d.Exec(`
		UPDATE installed_mods SET update_policy = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, policy, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod policy: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModEnabled enables or disables a mod
func (d *DB) SetModEnabled(sourceID, modID, gameID, profileName string, enabled bool) error {
	result, err := d.Exec(`
		UPDATE installed_mods SET enabled = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, enabled, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod enabled: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModDeployed sets whether a mod is currently deployed to the game directory
func (d *DB) SetModDeployed(sourceID, modID, gameID, profileName string, deployed bool) error {
	result, err := d.Exec(`
		UPDATE installed_mods SET deployed = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, deployed, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod deployed: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// GetInstalledMod retrieves a single installed mod
func (d *DB) GetInstalledMod(sourceID, modID, gameID, profileName string) (*domain.InstalledMod, error) {
	var mod domain.InstalledMod
	var prevVersion *string
	err := d.QueryRow(`
		SELECT source_id, mod_id, game_id, profile_name, name, version, author,
		       update_policy, enabled, deployed, installed_at, previous_version, link_method
		FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName).Scan(
		&mod.SourceID, &mod.ID, &mod.GameID, &mod.ProfileName,
		&mod.Name, &mod.Version, &mod.Author, &mod.UpdatePolicy,
		&mod.Enabled, &mod.Deployed, &mod.InstalledAt, &prevVersion, &mod.LinkMethod,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrModNotFound
		}
		return nil, fmt.Errorf("querying installed mod: %w", err)
	}

	if prevVersion != nil {
		mod.PreviousVersion = *prevVersion
	}

	// Fetch file IDs
	fileIDs, err := d.GetModFileIDs(sourceID, modID, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting file IDs: %w", err)
	}
	mod.FileIDs = fileIDs

	return &mod, nil
}

// GetModFileIDs retrieves the file IDs for an installed mod
func (d *DB) GetModFileIDs(sourceID, modID, gameID, profileName string) ([]string, error) {
	rows, err := d.Query(`
		SELECT file_id FROM installed_mod_files
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying mod file IDs: %w", err)
	}
	defer rows.Close()

	var fileIDs []string
	for rows.Next() {
		var fileID string
		if err := rows.Scan(&fileID); err != nil {
			return nil, fmt.Errorf("scanning file ID: %w", err)
		}
		fileIDs = append(fileIDs, fileID)
	}

	return fileIDs, rows.Err()
}

// UpdateModVersion updates a mod's version, preserving the previous version for rollback
func (d *DB) UpdateModVersion(sourceID, modID, gameID, profileName, newVersion string) error {
	result, err := d.Exec(`
		UPDATE installed_mods
		SET previous_version = version, version = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, newVersion, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod version: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModLinkMethod updates the link method for an installed mod
func (d *DB) SetModLinkMethod(sourceID, modID, gameID, profileName string, linkMethod domain.LinkMethod) error {
	result, err := d.Exec(`
		UPDATE installed_mods SET link_method = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, linkMethod, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod link method: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModFileIDs updates the file IDs for an installed mod
func (d *DB) SetModFileIDs(sourceID, modID, gameID, profileName string, fileIDs []string) error {
	return d.replaceModFileIDs(sourceID, modID, gameID, profileName, fileIDs)
}

// SwapModVersions swaps version and previous_version (for rollback).
// Uses explicit read-then-write so the swap is correct regardless of SQL evaluation order.
func (d *DB) SwapModVersions(sourceID, modID, gameID, profileName string) error {
	var version string
	var prevVersion *string
	err := d.QueryRow(`
		SELECT version, previous_version FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName).Scan(&version, &prevVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrModNotFound
		}
		return fmt.Errorf("checking versions: %w", err)
	}

	if prevVersion == nil || *prevVersion == "" {
		return fmt.Errorf("no previous version available for rollback")
	}
	prevVal := *prevVersion

	// Swap: write previous into version and version into previous_version
	_, err = d.Exec(`
		UPDATE installed_mods
		SET version = ?, previous_version = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, prevVal, version, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("swapping mod versions: %w", err)
	}

	return nil
}

// FileWithChecksum represents a file record with its checksum
type FileWithChecksum struct {
	SourceID string
	ModID    string
	FileID   string
	Checksum string
}

// SaveFileChecksum stores the MD5 checksum for a downloaded file
func (d *DB) SaveFileChecksum(sourceID, modID, gameID, profileName, fileID, checksum string) error {
	_, err := d.Exec(`
		UPDATE installed_mod_files SET checksum = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ? AND file_id = ?
	`, checksum, sourceID, modID, gameID, profileName, fileID)
	if err != nil {
		return fmt.Errorf("saving file checksum: %w", err)
	}
	return nil
}

// GetFileChecksum retrieves the checksum for a specific file
// Returns empty string if file not found or has no checksum
func (d *DB) GetFileChecksum(sourceID, modID, gameID, profileName, fileID string) (string, error) {
	var checksum *string
	err := d.QueryRow(`
		SELECT checksum FROM installed_mod_files
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ? AND file_id = ?
	`, sourceID, modID, gameID, profileName, fileID).Scan(&checksum)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("getting file checksum: %w", err)
	}
	if checksum == nil {
		return "", nil
	}
	return *checksum, nil
}

// GetFilesWithChecksums returns all files for a game/profile with their checksums
func (d *DB) GetFilesWithChecksums(gameID, profileName string) ([]FileWithChecksum, error) {
	rows, err := d.Query(`
		SELECT source_id, mod_id, file_id, checksum
		FROM installed_mod_files
		WHERE game_id = ? AND profile_name = ?
	`, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying files with checksums: %w", err)
	}
	defer rows.Close()

	var files []FileWithChecksum
	for rows.Next() {
		var f FileWithChecksum
		var checksum *string
		if err := rows.Scan(&f.SourceID, &f.ModID, &f.FileID, &checksum); err != nil {
			return nil, fmt.Errorf("scanning file with checksum: %w", err)
		}
		if checksum != nil {
			f.Checksum = *checksum
		}
		files = append(files, f)
	}

	return files, rows.Err()
}

// replaceModFileIDs replaces all file IDs for a mod with new ones
func (d *DB) replaceModFileIDs(sourceID, modID, gameID, profileName string, fileIDs []string) error {
	// Delete existing file IDs
	_, err := d.Exec(`
		DELETE FROM installed_mod_files
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("clearing mod file IDs: %w", err)
	}

	// Insert new file IDs
	for _, fileID := range fileIDs {
		if fileID == "" {
			continue
		}
		_, err = d.Exec(`
			INSERT INTO installed_mod_files (source_id, mod_id, game_id, profile_name, file_id)
			VALUES (?, ?, ?, ?, ?)
		`, sourceID, modID, gameID, profileName, fileID)
		if err != nil {
			return fmt.Errorf("saving mod file ID: %w", err)
		}
	}

	return nil
}
