package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// db_meta is the one-row-per-key store migrateV14 added for facts about the
// DATABASE rather than about a mod - see that migration's doc comment for
// why an obligation has to be recorded rather than derived from what the
// rows currently look like. The two accessors below are its general form,
// for obligations whose discharge lives outside this package (the
// credential scrub's own marker stays private to tokenmigrate.go, which
// both sets and clears it inside one transaction with the work).

// GetMeta returns the value recorded under key in db_meta, or "" when the
// key is absent. An absent key and an empty value are deliberately the same
// answer: every marker this holds is meaningful by its PRESENCE, and the
// value is a human-readable note (a timestamp) for a bug report.
func (d *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var value sql.NullString
	err := d.QueryRowContext(ctx, "SELECT value FROM db_meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading db_meta key %q: %w", key, err)
	}
	return value.String, nil
}

// SetMeta records value under key, replacing whatever was there.
func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	if _, err := d.ExecContext(ctx,
		"INSERT OR REPLACE INTO db_meta (key, value) VALUES (?, ?)", key, value); err != nil {
		return fmt.Errorf("recording db_meta key %q: %w", key, err)
	}
	return nil
}

// DeleteMeta removes key from db_meta. Removing a key that is not there is
// not an error: the caller is discharging an obligation, and one already
// discharged is the state it wants.
func (d *DB) DeleteMeta(ctx context.Context, key string) error {
	if _, err := d.ExecContext(ctx, "DELETE FROM db_meta WHERE key = ?", key); err != nil {
		return fmt.Errorf("removing db_meta key %q: %w", key, err)
	}
	return nil
}

// MetaWithPrefix returns every db_meta key that starts with prefix, and its
// value. The match is exact - substr, not LIKE, whose `_` wildcard every key
// this table holds would otherwise turn into a pattern.
func (d *DB) MetaWithPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := d.QueryContext(ctx,
		"SELECT key, value FROM db_meta WHERE substr(key, 1, ?) = ?", len(prefix), prefix)
	if err != nil {
		return nil, fmt.Errorf("querying db_meta keys under %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]string)
	for rows.Next() {
		var key string
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scanning db_meta key under %q: %w", prefix, err)
		}
		out[key] = value.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating db_meta keys under %q: %w", prefix, err)
	}
	return out, nil
}

// MetaProfileDisabledBackfill is the db_meta key under which migrateV17
// records that #431's one-time profile-document backfill is OWED. Its
// presence is the obligation; core discharges it by deleting it, and keeps
// any per-profile remainder under the same prefix.
const MetaProfileDisabledBackfill = "profile_disabled_backfill"

// DisabledModRow identifies one installed row that says its mod is switched
// off, with the profile it belongs to - the (game, profile, mod) triple a
// caller needs to find the same mod's reference in a profile document - and
// the two flags that say how it got there.
type DisabledModRow struct {
	GameID      string
	ProfileName string
	SourceID    string
	ModID       string
	Name        string
	Deployed    bool
	External    bool
}

// DisabledModRows returns every installed row across every game and profile
// whose enabled flag is false, in a deterministic order (game, profile,
// source, mod).
//
// It exists for #431's one-time profile-document backfill, which has to
// answer "which mods does the database say are off?" without being told
// which games or profiles to look in: a pre-marker disable left its only
// record here, and games.yaml is not a reliable index of it (a game the
// user has since removed from the file can still own rows). Deployed is
// returned alongside because enabled = 0 alone is not evidence of a
// disable - every profile switch writes it too.
func (d *DB) DisabledModRows(ctx context.Context) ([]DisabledModRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT game_id, profile_name, source_id, mod_id, name, deployed, external
		FROM installed_mods
		WHERE enabled = 0
		ORDER BY game_id, profile_name, source_id, mod_id
	`)
	if err != nil {
		return nil, fmt.Errorf("querying disabled installed mods: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DisabledModRow
	for rows.Next() {
		var r DisabledModRow
		if err := rows.Scan(&r.GameID, &r.ProfileName, &r.SourceID, &r.ModID, &r.Name, &r.Deployed, &r.External); err != nil {
			return nil, fmt.Errorf("scanning disabled installed mod: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating disabled installed mods: %w", err)
	}
	return out, nil
}
