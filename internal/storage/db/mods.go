package db

import (
	"fmt"
	"time"

	"lmm/internal/domain"
)

// SaveInstalledMod inserts or updates an installed mod record
func (d *DB) SaveInstalledMod(mod *domain.InstalledMod) error {
	var prevVersion *string
	if mod.PreviousVersion != "" {
		prevVersion = &mod.PreviousVersion
	}

	_, err := d.Exec(`
		INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, installed_at, previous_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, mod_id, game_id, profile_name) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			author = excluded.author,
			update_policy = excluded.update_policy,
			enabled = excluded.enabled,
			previous_version = excluded.previous_version
	`, mod.SourceID, mod.ID, mod.GameID, mod.ProfileName, mod.Name, mod.Version, mod.Author, mod.UpdatePolicy, mod.Enabled, time.Now(), prevVersion)
	if err != nil {
		return fmt.Errorf("saving installed mod: %w", err)
	}
	return nil
}

// GetInstalledMods returns all installed mods for a game/profile combination
func (d *DB) GetInstalledMods(gameID, profileName string) ([]domain.InstalledMod, error) {
	rows, err := d.Query(`
		SELECT source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, installed_at, previous_version
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
			&mod.Enabled, &mod.InstalledAt, &prevVersion,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning installed mod: %w", err)
		}
		if prevVersion != nil {
			mod.PreviousVersion = *prevVersion
		}
		mods = append(mods, mod)
	}

	return mods, rows.Err()
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

// GetInstalledMod retrieves a single installed mod
func (d *DB) GetInstalledMod(sourceID, modID, gameID, profileName string) (*domain.InstalledMod, error) {
	var mod domain.InstalledMod
	var prevVersion *string
	err := d.QueryRow(`
		SELECT source_id, mod_id, game_id, profile_name, name, version, author,
		       update_policy, enabled, installed_at, previous_version
		FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName).Scan(
		&mod.SourceID, &mod.ID, &mod.GameID, &mod.ProfileName,
		&mod.Name, &mod.Version, &mod.Author, &mod.UpdatePolicy,
		&mod.Enabled, &mod.InstalledAt, &prevVersion,
	)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, domain.ErrModNotFound
		}
		return nil, fmt.Errorf("querying installed mod: %w", err)
	}

	if prevVersion != nil {
		mod.PreviousVersion = *prevVersion
	}

	return &mod, nil
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

// SwapModVersions swaps version and previous_version (for rollback)
func (d *DB) SwapModVersions(sourceID, modID, gameID, profileName string) error {
	// First check if previous_version exists
	var prevVersion *string
	err := d.QueryRow(`
		SELECT previous_version FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName).Scan(&prevVersion)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return domain.ErrModNotFound
		}
		return fmt.Errorf("checking previous version: %w", err)
	}

	if prevVersion == nil || *prevVersion == "" {
		return fmt.Errorf("no previous version available for rollback")
	}

	// Swap the versions
	result, err := d.Exec(`
		UPDATE installed_mods
		SET version = previous_version, previous_version = version
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("swapping mod versions: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}
