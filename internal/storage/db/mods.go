package db

import (
	"fmt"
	"time"

	"lmm/internal/domain"
)

// SaveInstalledMod inserts or updates an installed mod record
func (d *DB) SaveInstalledMod(mod *domain.InstalledMod) error {
	_, err := d.Exec(`
		INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, installed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, mod_id, game_id, profile_name) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			author = excluded.author,
			update_policy = excluded.update_policy,
			enabled = excluded.enabled
	`, mod.SourceID, mod.ID, mod.GameID, mod.ProfileName, mod.Name, mod.Version, mod.Author, mod.UpdatePolicy, mod.Enabled, time.Now())
	if err != nil {
		return fmt.Errorf("saving installed mod: %w", err)
	}
	return nil
}

// GetInstalledMods returns all installed mods for a game/profile combination
func (d *DB) GetInstalledMods(gameID, profileName string) ([]domain.InstalledMod, error) {
	rows, err := d.Query(`
		SELECT source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, installed_at
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
		err := rows.Scan(
			&mod.SourceID, &mod.ID, &mod.GameID, &mod.ProfileName,
			&mod.Name, &mod.Version, &mod.Author, &mod.UpdatePolicy,
			&mod.Enabled, &mod.InstalledAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning installed mod: %w", err)
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
