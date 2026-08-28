package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

func encodeFileIDs(fileIDs []string) (string, error) {
	if len(fileIDs) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(fileIDs)
	if err != nil {
		return "", fmt.Errorf("encoding file IDs: %w", err)
	}
	return string(data), nil
}

func decodeFileIDs(raw *string) ([]string, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil, fmt.Errorf("decoding file IDs: %w", err)
	}
	return out, nil
}

// SaveInstalledMod inserts or updates an installed mod record.
// The mod upsert and file ID replacement are performed atomically within a transaction.
// On update of an existing record, the existing update_policy is preserved:
// callers always pass the default policy, so honoring it here would silently
// reset a user-set --pin/--auto policy on reinstall. Policy changes go through
// UpdateModPolicy. A first-time insert still uses the policy passed in.
// Similarly, convert_paks is never written here - the schema default covers first
// insert, and SetModConvertPaks is the only writer, so reinstall can't reset it.
func (d *DB) SaveInstalledMod(ctx context.Context, mod *domain.InstalledMod) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	var prevVersion *string
	if mod.PreviousVersion != "" {
		prevVersion = &mod.PreviousVersion
	}
	prevFileIDs, err := encodeFileIDs(mod.PreviousFileIDs)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, deployed, installed_at, previous_version, previous_file_ids, link_method, manual_download, summary, source_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, mod_id, game_id, profile_name) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			author = excluded.author,
			enabled = excluded.enabled,
			deployed = excluded.deployed,
			previous_version = excluded.previous_version,
			previous_file_ids = excluded.previous_file_ids,
			link_method = excluded.link_method,
			manual_download = excluded.manual_download,
			summary = excluded.summary,
			source_url = excluded.source_url
	`, mod.SourceID, mod.ID, mod.GameID, mod.ProfileName, mod.Name, mod.Version, mod.Author, mod.UpdatePolicy, mod.Enabled, mod.Deployed, time.Now(), prevVersion, prevFileIDs, mod.LinkMethod, mod.ManualDownload, mod.Summary, mod.SourceURL)
	if err != nil {
		return fmt.Errorf("saving installed mod: %w", err)
	}

	// Replace file IDs within the same transaction
	if err := replaceModFileIDsTx(ctx, tx, mod.SourceID, mod.ID, mod.GameID, mod.ProfileName, mod.FileIDs); err != nil {
		return err
	}

	return tx.Commit()
}

// GetInstalledMods returns all installed mods for a game/profile combination
func (d *DB) GetInstalledMods(ctx context.Context, gameID, profileName string) (mods []domain.InstalledMod, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, deployed, installed_at, previous_version, previous_file_ids, link_method, manual_download, summary, source_url, convert_paks
		FROM installed_mods
		WHERE game_id = ? AND profile_name = ?
		ORDER BY installed_at ASC
	`, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying installed mods: %w", err)
	}

	for rows.Next() {
		var mod domain.InstalledMod
		var prevVersion *string
		var prevFileIDs *string
		err := rows.Scan(
			&mod.SourceID, &mod.ID, &mod.GameID, &mod.ProfileName,
			&mod.Name, &mod.Version, &mod.Author, &mod.UpdatePolicy,
			&mod.Enabled, &mod.Deployed, &mod.InstalledAt, &prevVersion, &prevFileIDs, &mod.LinkMethod, &mod.ManualDownload,
			&mod.Summary, &mod.SourceURL, &mod.ConvertPaks,
		)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scanning installed mod: %w", err)
		}
		if prevVersion != nil {
			mod.PreviousVersion = *prevVersion
		}
		mod.PreviousFileIDs, err = decodeFileIDs(prevFileIDs)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		mods = append(mods, mod)
	}

	rowsErr := rows.Err()
	if cerr := rows.Close(); cerr != nil {
		return nil, fmt.Errorf("closing rows: %w", cerr)
	}
	if rowsErr != nil {
		return nil, rowsErr
	}

	// Batch fetch file IDs for all mods (avoids N+1). The first query's Rows
	// is already closed above: with MaxOpenConns(1) (":memory:"), issuing this
	// second query while the first is still open would deadlock (#271).
	fileIDsByMod, err := d.getModFileIDsBatch(ctx, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting file IDs: %w", err)
	}
	for i := range mods {
		key := domain.ModKey(mods[i].SourceID, mods[i].ID)
		mods[i].FileIDs = fileIDsByMod[key]
	}

	return mods, nil
}

// getModFileIDsBatch returns file IDs for all mods in game/profile, keyed by "sourceID:modID"
func (d *DB) getModFileIDsBatch(ctx context.Context, gameID, profileName string) (out map[string][]string, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT source_id, mod_id, file_id FROM installed_mod_files
		WHERE game_id = ? AND profile_name = ?
		ORDER BY source_id, mod_id
	`, gameID, profileName)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()

	out = make(map[string][]string)
	for rows.Next() {
		var sourceID, modID, fileID string
		if err := rows.Scan(&sourceID, &modID, &fileID); err != nil {
			return nil, err
		}
		key := domain.ModKey(sourceID, modID)
		out[key] = append(out[key], fileID)
	}
	return out, rows.Err()
}

// DeleteInstalledMod removes an installed mod record
func (d *DB) DeleteInstalledMod(ctx context.Context, sourceID, modID, gameID, profileName string) error {
	result, err := d.ExecContext(ctx, `
		DELETE FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("deleting installed mod: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("deleting installed mod: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// UpdateModPolicy updates the update policy for an installed mod
func (d *DB) UpdateModPolicy(ctx context.Context, sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) error {
	result, err := d.ExecContext(ctx, `
		UPDATE installed_mods SET update_policy = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, policy, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod policy: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("updating mod policy: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModConvertPaks sets the per-mod pak-conversion flag (#221).
func (d *DB) SetModConvertPaks(ctx context.Context, sourceID, modID, gameID, profileName string, convert bool) error {
	result, err := d.ExecContext(ctx, `
		UPDATE installed_mods SET convert_paks = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, convert, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod convert_paks: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("updating mod convert_paks: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModEnabled enables or disables a mod
func (d *DB) SetModEnabled(ctx context.Context, sourceID, modID, gameID, profileName string, enabled bool) error {
	result, err := d.ExecContext(ctx, `
		UPDATE installed_mods SET enabled = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, enabled, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod enabled: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("setting mod enabled: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModDeployed sets whether a mod is currently deployed to the game directory
func (d *DB) SetModDeployed(ctx context.Context, sourceID, modID, gameID, profileName string, deployed bool) error {
	result, err := d.ExecContext(ctx, `
		UPDATE installed_mods SET deployed = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, deployed, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod deployed: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("setting mod deployed: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// GetInstalledMod retrieves a single installed mod
func (d *DB) GetInstalledMod(ctx context.Context, sourceID, modID, gameID, profileName string) (*domain.InstalledMod, error) {
	var mod domain.InstalledMod
	var prevVersion *string
	var prevFileIDs *string
	err := d.QueryRowContext(ctx, `
		SELECT source_id, mod_id, game_id, profile_name, name, version, author,
		       update_policy, enabled, deployed, installed_at, previous_version, previous_file_ids, link_method, manual_download,
		       summary, source_url, convert_paks
		FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName).Scan(
		&mod.SourceID, &mod.ID, &mod.GameID, &mod.ProfileName,
		&mod.Name, &mod.Version, &mod.Author, &mod.UpdatePolicy,
		&mod.Enabled, &mod.Deployed, &mod.InstalledAt, &prevVersion, &prevFileIDs, &mod.LinkMethod, &mod.ManualDownload,
		&mod.Summary, &mod.SourceURL, &mod.ConvertPaks,
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
	mod.PreviousFileIDs, err = decodeFileIDs(prevFileIDs)
	if err != nil {
		return nil, err
	}

	// Fetch file IDs
	fileIDs, err := d.GetModFileIDs(ctx, sourceID, modID, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting file IDs: %w", err)
	}
	mod.FileIDs = fileIDs

	return &mod, nil
}

// GetModFileIDs retrieves the file IDs for an installed mod
func (d *DB) GetModFileIDs(ctx context.Context, sourceID, modID, gameID, profileName string) (fileIDs []string, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT file_id FROM installed_mod_files
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying mod file IDs: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	for rows.Next() {
		var fileID string
		if err := rows.Scan(&fileID); err != nil {
			return nil, fmt.Errorf("scanning file ID: %w", err)
		}
		fileIDs = append(fileIDs, fileID)
	}

	return fileIDs, rows.Err()
}

func getModFileIDsTx(ctx context.Context, tx *sql.Tx, sourceID, modID, gameID, profileName string) (fileIDs []string, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT file_id FROM installed_mod_files
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying mod file IDs: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	for rows.Next() {
		var fileID string
		if err := rows.Scan(&fileID); err != nil {
			return nil, fmt.Errorf("scanning file ID: %w", err)
		}
		fileIDs = append(fileIDs, fileID)
	}

	return fileIDs, rows.Err()
}

// SetModVersion corrects an installed mod's recorded version in place: a
// plain UPDATE of the version column only. Unlike UpdateModVersion, it does
// NOT shift the current version into previous_version/previous_file_ids -
// this is for repairing a WRONG recorded value (issue #94's verify --fix),
// not a real version change that should be rollback-able. And unlike
// SaveInstalledMod's full-row upsert, it does NOT touch installed_mod_files
// at all: SaveInstalledMod always runs replaceModFileIDsTx (DELETE + a
// checksum-less re-INSERT), so calling it just to fix the version string
// silently wipes every stored checksum for the mod's files even when the
// file IDs themselves never changed. SetModVersion exists specifically to
// avoid that: the version is wrong, the file IDs and their checksums are
// not, so only the version column should move.
func (d *DB) SetModVersion(ctx context.Context, sourceID, modID, gameID, profileName, version string) error {
	result, err := d.ExecContext(ctx, `
		UPDATE installed_mods SET version = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, version, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod version: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("setting mod version: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// UpdateModVersion updates a mod's version, preserving the previous version and file IDs for rollback.
func (d *DB) UpdateModVersion(ctx context.Context, sourceID, modID, gameID, profileName, newVersion string) error {
	currentFileIDs, err := d.GetModFileIDs(ctx, sourceID, modID, gameID, profileName)
	if err != nil {
		return err
	}
	prevFileIDs, err := encodeFileIDs(currentFileIDs)
	if err != nil {
		return err
	}
	result, err := d.ExecContext(ctx, `
		UPDATE installed_mods
		SET previous_version = version, previous_file_ids = ?, version = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, prevFileIDs, newVersion, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod version: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("updating mod version: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModLinkMethod updates the link method for an installed mod
func (d *DB) SetModLinkMethod(ctx context.Context, sourceID, modID, gameID, profileName string, linkMethod domain.LinkMethod) error {
	result, err := d.ExecContext(ctx, `
		UPDATE installed_mods SET link_method = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, linkMethod, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod link method: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("setting mod link method: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModFileIDs updates the file IDs for an installed mod
func (d *DB) SetModFileIDs(ctx context.Context, sourceID, modID, gameID, profileName string, fileIDs []string) error {
	return d.replaceModFileIDs(ctx, sourceID, modID, gameID, profileName, fileIDs)
}

// ApplyModUpdate updates version and file IDs atomically while preserving rollback state.
func (d *DB) ApplyModUpdate(ctx context.Context, sourceID, modID, gameID, profileName, newVersion string, newFileIDs []string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var currentVersion string
	err = tx.QueryRowContext(ctx, `
		SELECT version FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName).Scan(&currentVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrModNotFound
		}
		return fmt.Errorf("checking current version: %w", err)
	}

	currentFileIDs, err := getModFileIDsTx(ctx, tx, sourceID, modID, gameID, profileName)
	if err != nil {
		return err
	}
	prevFileIDs, err := encodeFileIDs(currentFileIDs)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE installed_mods
		SET previous_version = ?, previous_file_ids = ?, version = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, currentVersion, prevFileIDs, newVersion, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod version: %w", err)
	}

	if err := replaceModFileIDsTx(ctx, tx, sourceID, modID, gameID, profileName, newFileIDs); err != nil {
		return err
	}

	return tx.Commit()
}

// SwapModVersions swaps version and previous_version (for rollback).
// The read/write and file ID restoration are performed atomically within a transaction.
func (d *DB) SwapModVersions(ctx context.Context, sourceID, modID, gameID, profileName string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var version string
	var prevVersion *string
	var prevFileIDsRaw *string
	err = tx.QueryRowContext(ctx, `
		SELECT version, previous_version, previous_file_ids FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName).Scan(&version, &prevVersion, &prevFileIDsRaw)
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
	prevFileIDs, err := decodeFileIDs(prevFileIDsRaw)
	if err != nil {
		return err
	}
	currentFileIDs, err := getModFileIDsTx(ctx, tx, sourceID, modID, gameID, profileName)
	if err != nil {
		return err
	}
	currentFileIDsRaw, err := encodeFileIDs(currentFileIDs)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE installed_mods
		SET version = ?, previous_version = ?, previous_file_ids = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, prevVal, version, currentFileIDsRaw, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("swapping mod versions: %w", err)
	}
	if err := replaceModFileIDsTx(ctx, tx, sourceID, modID, gameID, profileName, prevFileIDs); err != nil {
		return err
	}

	return tx.Commit()
}

// FileWithChecksum represents a file record with its checksum
type FileWithChecksum struct {
	SourceID string
	ModID    string
	FileID   string
	Checksum string
}

// SaveFileChecksum stores the MD5 checksum for a downloaded file. The target
// installed_mod_files row must already exist: an UPDATE matching no row would
// otherwise succeed as a silent no-op while the caller believes the checksum
// was persisted (#164), so 0 affected rows is an error.
func (d *DB) SaveFileChecksum(ctx context.Context, sourceID, modID, gameID, profileName, fileID, checksum string) error {
	res, err := d.ExecContext(ctx, `
		UPDATE installed_mod_files SET checksum = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ? AND file_id = ?
	`, checksum, sourceID, modID, gameID, profileName, fileID)
	if err != nil {
		return fmt.Errorf("saving file checksum: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("saving file checksum: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("saving file checksum: no installed file row for %s/%s file %s (game %s, profile %s)",
			sourceID, modID, fileID, gameID, profileName)
	}
	return nil
}

// GetFileChecksum retrieves the checksum for a specific file
// Returns empty string if file not found or has no checksum
func (d *DB) GetFileChecksum(ctx context.Context, sourceID, modID, gameID, profileName, fileID string) (string, error) {
	var checksum *string
	err := d.QueryRowContext(ctx, `
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
func (d *DB) GetFilesWithChecksums(ctx context.Context, gameID, profileName string) (files []FileWithChecksum, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT source_id, mod_id, file_id, checksum
		FROM installed_mod_files
		WHERE game_id = ? AND profile_name = ?
	`, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying files with checksums: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

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

// execer abstracts *sql.DB and *sql.Tx for running SQL statements.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// replaceModFileIDs replaces all file IDs for a mod within a new transaction.
func (d *DB) replaceModFileIDs(ctx context.Context, sourceID, modID, gameID, profileName string, fileIDs []string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := replaceModFileIDsTx(ctx, tx, sourceID, modID, gameID, profileName, fileIDs); err != nil {
		return err
	}

	return tx.Commit()
}

// replaceModFileIDsTx performs the DELETE + INSERT within an existing transaction/execer.
func replaceModFileIDsTx(ctx context.Context, e execer, sourceID, modID, gameID, profileName string, fileIDs []string) error {
	_, err := e.ExecContext(ctx, `
		DELETE FROM installed_mod_files
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("clearing mod file IDs: %w", err)
	}

	for _, fileID := range fileIDs {
		if fileID == "" {
			continue
		}
		_, err = e.ExecContext(ctx, `
			INSERT INTO installed_mod_files (source_id, mod_id, game_id, profile_name, file_id)
			VALUES (?, ?, ?, ?, ?)
		`, sourceID, modID, gameID, profileName, fileID)
		if err != nil {
			return fmt.Errorf("saving mod file ID: %w", err)
		}
	}

	return nil
}
