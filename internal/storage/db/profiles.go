package db

import (
	"context"
	"fmt"
)

// RenameProfile moves every row this database keys by profile name from
// oldName to newName, for one game, in a single transaction (#332).
//
// Three tables carry a profile_name column and all three move together:
// installed_mods (the install records themselves), installed_mod_files
// (their per-file rows, which FOREIGN KEY the installed_mods composite),
// and deployed_files (the game-directory ownership map, the merged pak's
// own synthetic rows included). Nothing else in the schema names a profile.
//
// PRAGMA defer_foreign_keys is set for the life of the transaction because
// installed_mod_files' foreign key declares ON DELETE CASCADE but no ON
// UPDATE clause, so SQLite's default NO ACTION would reject the parent
// UPDATE the instant it ran, whichever of the two tables went first - there
// is no ordering that keeps the constraint satisfied at every statement.
// Deferring moves the single check to COMMIT, where both halves have
// landed. The pragma is transaction-scoped: SQLite clears it automatically
// when the transaction ends, so nothing leaks to the next caller.
//
// It reports no error when oldName has no rows at all - a profile with no
// installs is a perfectly ordinary profile, and its rename is entirely a
// config-file operation. Refusing to rename ONTO an existing profile is the
// caller's business (core.ProfileManager.Rename), which knows about the
// profile files this table knows nothing about.
func (d *DB) RenameProfile(ctx context.Context, gameID, oldName, newName string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning profile rename: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return fmt.Errorf("deferring foreign keys: %w", err)
	}

	for _, table := range []string{"installed_mods", "installed_mod_files", "deployed_files"} {
		stmt := fmt.Sprintf(`UPDATE %s SET profile_name = ? WHERE game_id = ? AND profile_name = ?`, table)
		if _, err := tx.ExecContext(ctx, stmt, newName, gameID, oldName); err != nil {
			return fmt.Errorf("renaming %s rows: %w", table, err)
		}
	}

	return tx.Commit()
}
