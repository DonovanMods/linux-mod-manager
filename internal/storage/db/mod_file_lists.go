package db

import (
	"context"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ModFileList is one mod_file_lists row (migrateV30, #676): the file list a
// source last gave for a mod, as JSON, valid as of CheckedAt - the fetch,
// or a later time the source vouched the mod unchanged up to. Format is
// the JSON shape's version.
type ModFileList struct {
	SourceID  string
	GameID    string
	ModID     string
	Files     string
	FetchedAt time.Time
	CheckedAt time.Time
	Format    int
}

// SaveModFileList records list, replacing what was recorded for its mod. A
// zero FetchedAt is CheckedAt.
func (d *DB) SaveModFileList(ctx context.Context, list ModFileList) error {
	if list.FetchedAt.IsZero() {
		list.FetchedAt = list.CheckedAt
	}
	if _, err := d.ExecContext(ctx, `
		INSERT OR REPLACE INTO mod_file_lists (source_id, game_id, mod_id, files, fetched_at, checked_at, format)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		list.SourceID, list.GameID, list.ModID, list.Files, formatTime(list.FetchedAt), formatTime(list.CheckedAt), list.Format,
	); err != nil {
		return fmt.Errorf("storing %s's file list: %w", domain.ModKey(list.SourceID, list.ModID), err)
	}
	return nil
}

// ModFileList returns the list recorded for one mod; ok is false when none
// is, or its times do not read.
func (d *DB) ModFileList(ctx context.Context, sourceID, gameID, modID string) (ModFileList, bool, error) {
	rows, err := d.queryModFileLists(ctx, `WHERE source_id = ? AND game_id = ? AND mod_id = ?`, sourceID, gameID, modID)
	if err != nil {
		return ModFileList{}, false, err
	}
	list, ok := rows[modID]
	return list, ok, nil
}

// ModFileLists returns every list recorded for sourceID's mods of gameID,
// keyed by mod ID.
func (d *DB) ModFileLists(ctx context.Context, sourceID, gameID string) (map[string]ModFileList, error) {
	return d.queryModFileLists(ctx, `WHERE source_id = ? AND game_id = ?`, sourceID, gameID)
}

func (d *DB) queryModFileLists(ctx context.Context, where string, args ...any) (map[string]ModFileList, error) {
	rows, err := d.QueryContext(ctx, `SELECT source_id, game_id, mod_id, files, fetched_at, checked_at, format FROM mod_file_lists `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("querying stored file lists: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]ModFileList)
	for rows.Next() {
		var list ModFileList
		var fetched, checked string
		if err := rows.Scan(&list.SourceID, &list.GameID, &list.ModID, &list.Files, &fetched, &checked, &list.Format); err != nil {
			return nil, fmt.Errorf("scanning stored file lists: %w", err)
		}
		var okF, okC bool
		list.FetchedAt, okF = parseStoredTime(fetched)
		list.CheckedAt, okC = parseStoredTime(checked)
		if !okF || !okC {
			continue
		}
		out[list.ModID] = list
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stored file lists: %w", err)
	}
	return out, nil
}

// TouchModFileLists moves each of sourceID's gameID lists (by mod ID) to
// the time its source vouched it unchanged up to, never back - in one
// transaction, so a game's hundreds of lists are one write.
func (d *DB) TouchModFileLists(ctx context.Context, sourceID, gameID string, at map[string]time.Time) error {
	if len(at) == 0 {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("touching stored file lists: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for modID, t := range at {
		if _, err := tx.ExecContext(ctx, `
			UPDATE mod_file_lists SET checked_at = ?
			WHERE source_id = ? AND game_id = ? AND mod_id = ? AND checked_at < ?`,
			formatTime(t), sourceID, gameID, modID, formatTime(t)); err != nil {
			return fmt.Errorf("touching %s's file list: %w", domain.ModKey(sourceID, modID), err)
		}
	}
	return tx.Commit()
}

// ForgetModFileList drops one mod's list. Forgetting what is not recorded
// is not an error.
func (d *DB) ForgetModFileList(ctx context.Context, sourceID, gameID, modID string) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM mod_file_lists WHERE source_id = ? AND game_id = ? AND mod_id = ?`,
		sourceID, gameID, modID); err != nil {
		return fmt.Errorf("forgetting %s's file list: %w", domain.ModKey(sourceID, modID), err)
	}
	return nil
}

// PruneModFileLists bounds the store and reports how many lists it
// removed: a list for a mod no profile of its game has, checked before
// orphanedBefore (an install plan's, whose install never came); any list
// checked before staleBefore, which no source vouches for any more; and,
// past limit rows, the least recently checked.
func (d *DB) PruneModFileLists(ctx context.Context, orphanedBefore, staleBefore time.Time, limit int) (int, error) {
	removed := 0
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM mod_file_lists WHERE checked_at < ? AND NOT EXISTS (
			SELECT 1 FROM installed_mods i
			WHERE i.source_id = mod_file_lists.source_id AND i.mod_id = mod_file_lists.mod_id AND i.game_id = mod_file_lists.game_id)`,
			[]any{formatTime(orphanedBefore)}},
		{`DELETE FROM mod_file_lists WHERE checked_at < ?`, []any{formatTime(staleBefore)}},
		{`DELETE FROM mod_file_lists WHERE rowid IN (
			SELECT rowid FROM mod_file_lists ORDER BY checked_at DESC LIMIT -1 OFFSET ?)`, []any{limit}},
	} {
		res, err := d.ExecContext(ctx, stmt.sql, stmt.args...)
		if err != nil {
			return removed, fmt.Errorf("pruning stored file lists: %w", err)
		}
		n, _ := res.RowsAffected()
		removed += int(n)
	}
	return removed, nil
}
