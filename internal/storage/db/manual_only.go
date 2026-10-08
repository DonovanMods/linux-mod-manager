package db

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// manual_only_mods is migrateV20's record of the mods a source will not
// serve through its API (#543) - see that migration's doc comment for why
// it is a table of its own rather than an installed_mods column.

// RecordManualOnly records that sourceID will not serve modID's files for
// gameID through its API. Recording a fact already recorded is not an
// error.
func (d *DB) RecordManualOnly(ctx context.Context, gameID, sourceID, modID string) error {
	if _, err := d.ExecContext(ctx,
		"INSERT OR IGNORE INTO manual_only_mods (game_id, source_id, mod_id) VALUES (?, ?, ?)",
		gameID, sourceID, modID); err != nil {
		return fmt.Errorf("recording %s as manual-only: %w", domain.ModKey(sourceID, modID), err)
	}
	return nil
}

// ClearManualOnly removes the record RecordManualOnly made. Clearing a mod
// that was never recorded is not an error.
func (d *DB) ClearManualOnly(ctx context.Context, gameID, sourceID, modID string) error {
	if _, err := d.ExecContext(ctx,
		"DELETE FROM manual_only_mods WHERE game_id = ? AND source_id = ? AND mod_id = ?",
		gameID, sourceID, modID); err != nil {
		return fmt.Errorf("clearing %s's manual-only record: %w", domain.ModKey(sourceID, modID), err)
	}
	return nil
}

// ManualOnlyMods returns the manual-only mods recorded for gameID, keyed by
// domain.ModKey.
func (d *DB) ManualOnlyMods(ctx context.Context, gameID string) (map[string]bool, error) {
	rows, err := d.QueryContext(ctx, "SELECT source_id, mod_id FROM manual_only_mods WHERE game_id = ?", gameID)
	if err != nil {
		return nil, fmt.Errorf("querying manual-only mods: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]bool)
	for rows.Next() {
		var sourceID, modID string
		if err := rows.Scan(&sourceID, &modID); err != nil {
			return nil, fmt.Errorf("scanning manual-only mods: %w", err)
		}
		out[domain.ModKey(sourceID, modID)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating manual-only mods: %w", err)
	}
	return out, nil
}
