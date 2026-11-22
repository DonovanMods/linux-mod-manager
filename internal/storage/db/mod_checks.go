package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ModCheckKind names what a mod_checks row records (migrateV29, #672).
type ModCheckKind string

const (
	// ModCheckUpdate is the update check's answer for the mod.
	ModCheckUpdate ModCheckKind = "update"
	// ModCheckVersion is verify's confirmation that the recorded version
	// matches the source's file list.
	ModCheckVersion ModCheckKind = "version"
)

// ModCheck is one mod_checks row: what lmm last learned about an installed
// mod from its source, about the installed Version and FileIDs, as of
// CheckedAt. Result is the update check's answer (the update found as JSON,
// "" for up to date); a version confirmation carries none.
type ModCheck struct {
	SourceID  string
	ModID     string
	Version   string
	FileIDs   []string
	CheckedAt time.Time
	Result    string
}

// SaveModCheck records rec for kind, replacing what was recorded. It fails
// for a mod that is not installed in gameID/profileName.
func (d *DB) SaveModCheck(ctx context.Context, kind ModCheckKind, gameID, profileName string, rec ModCheck) error {
	files, err := json.Marshal(rec.FileIDs)
	if err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx, `
		INSERT OR REPLACE INTO mod_checks
			(kind, source_id, mod_id, game_id, profile_name, installed_version, installed_file_ids, checked_at, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(kind), rec.SourceID, rec.ModID, gameID, profileName, rec.Version, string(files), formatTime(rec.CheckedAt), rec.Result,
	); err != nil {
		return fmt.Errorf("recording %s's %s check: %w", domain.ModKey(rec.SourceID, rec.ModID), kind, err)
	}
	return nil
}

// ModChecks returns gameID/profileName's records of kind, keyed by
// domain.ModKey. A row whose time does not read is left out: a cache entry
// lmm cannot use is one it does not have.
func (d *DB) ModChecks(ctx context.Context, kind ModCheckKind, gameID, profileName string) (map[string]ModCheck, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT source_id, mod_id, installed_version, installed_file_ids, checked_at, result
		FROM mod_checks WHERE kind = ? AND game_id = ? AND profile_name = ?`,
		string(kind), gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying %s checks: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]ModCheck)
	for rows.Next() {
		var rec ModCheck
		var files, at string
		if err := rows.Scan(&rec.SourceID, &rec.ModID, &rec.Version, &files, &at, &rec.Result); err != nil {
			return nil, fmt.Errorf("scanning %s checks: %w", kind, err)
		}
		var ok bool
		if rec.CheckedAt, ok = parseStoredTime(at); !ok || json.Unmarshal([]byte(files), &rec.FileIDs) != nil {
			continue
		}
		out[domain.ModKey(rec.SourceID, rec.ModID)] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating %s checks: %w", kind, err)
	}
	return out, nil
}

// TouchModChecks moves each of sourceID's records of kind (by mod ID) to
// the time its source vouched for it up to, never back: a mod the source
// says has not changed since its record still has the recorded answer, as
// of then.
func (d *DB) TouchModChecks(ctx context.Context, kind ModCheckKind, gameID, profileName, sourceID string, at map[string]time.Time) error {
	for modID, t := range at {
		if _, err := d.ExecContext(ctx, `
			UPDATE mod_checks SET checked_at = ?
			WHERE kind = ? AND source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ? AND checked_at < ?`,
			formatTime(t), string(kind), sourceID, modID, gameID, profileName, formatTime(t)); err != nil {
			return fmt.Errorf("touching %s's %s check: %w", domain.ModKey(sourceID, modID), kind, err)
		}
	}
	return nil
}

// DeleteModCheck forgets one record. Forgetting what is not recorded is not
// an error.
func (d *DB) DeleteModCheck(ctx context.Context, kind ModCheckKind, gameID, profileName, sourceID, modID string) error {
	if _, err := d.ExecContext(ctx,
		`DELETE FROM mod_checks WHERE kind = ? AND source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?`,
		string(kind), sourceID, modID, gameID, profileName); err != nil {
		return fmt.Errorf("forgetting %s's %s check: %w", domain.ModKey(sourceID, modID), kind, err)
	}
	return nil
}
