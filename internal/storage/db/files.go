package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// FileOwner represents the mod that owns a deployed file
type FileOwner struct {
	SourceID string
	ModID    string
}

// FileConflict represents a file that would be overwritten
type FileConflict struct {
	Path     domain.DeployPath
	SourceID string
	ModID    string
}

// A deployed_files row names its file by two columns, root and
// relative_path (#629, migrateV26): root is 'mod_path', 'install_path' or
// (#636, migrateV28) 'prefix'.
// Callers name it by one value, a domain.DeployPath, which every function
// here takes and returns.

// scanRoot is the root a row's root column names. A value this build does
// not know is an error, never read as the mod_path.
func scanRoot(name string) (domain.DeployRoot, error) {
	root, err := domain.ParseDeployRoot(name)
	if err != nil {
		return root, fmt.Errorf("reading deployed_files.root: %w", err)
	}
	return root, nil
}

// orderByPath is the order every path listing returns rows in: the
// mod_path rows first, in relative_path order exactly as before #629, then
// the install_path ones, then (#636) the prefix ones -
// domain.CompareDeployPaths' order.
const orderByPath = "(CASE root WHEN 'mod_path' THEN 0 WHEN 'install_path' THEN 1 ELSE 2 END), relative_path"

// SaveDeployedFile records that a file under the game's mod_path is deployed
// by a specific mod, with no fingerprint and no recorded mod_path
// (RecordDeployedFile).
// Uses upsert to handle overwrites (new mod takes ownership).
func (d *DB) SaveDeployedFile(ctx context.Context, gameID, profileName, relativePath, sourceID, modID string) error {
	return d.RecordDeployedFile(ctx, DeployedFileRecord{
		GameID: gameID, Profile: profileName, Path: domain.ModPathFile(relativePath),
		SourceID: sourceID, ModID: modID,
	})
}

// FileFingerprint identifies the content a copy or hardlink deployment
// wrote at a path (#466): its hex SHA-256, its length, and its modification
// and inode-change times in Unix nanoseconds. Size, MTime and CTime are the
// cheap pre-check; Checksum is the proof.
type FileFingerprint struct {
	Checksum string
	Size     int64
	MTime    int64
	CTime    int64
}

// DeployedFileRecord is one deployed_files row as a deploy writes it.
type DeployedFileRecord struct {
	GameID  string
	Profile string
	// Path is the file: its root and its path relative to it (#629).
	Path     domain.DeployPath
	SourceID string
	ModID    string
	// ModPath is the absolute mod_path the record was deployed under
	// (#451) - the game's mod_path generation, for a game-root file too;
	// empty records none.
	ModPath string
	// Fingerprint is the content a copy or hardlink deployment wrote; nil
	// for a symlink deployment, or a row written without deploying.
	Fingerprint *FileFingerprint
}

// RecordDeployedFile upserts rec: a path already recorded for the game and
// profile changes hands to rec's mod, and its fingerprint and mod_path are
// replaced by rec's - cleared when rec carries none, since whatever the
// old row described is no longer what the new one does.
func (d *DB) RecordDeployedFile(ctx context.Context, rec DeployedFileRecord) error {
	var checksum, modPath sql.NullString
	var size, mtime, ctime sql.NullInt64
	if fp := rec.Fingerprint; fp != nil && fp.Checksum != "" {
		checksum = sql.NullString{String: fp.Checksum, Valid: true}
		size = sql.NullInt64{Int64: fp.Size, Valid: true}
		mtime = sql.NullInt64{Int64: fp.MTime, Valid: true}
		ctime = sql.NullInt64{Int64: fp.CTime, Valid: true}
	}
	if rec.ModPath != "" {
		modPath = sql.NullString{String: rec.ModPath, Valid: true}
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO deployed_files (game_id, profile_name, root, relative_path, source_id, mod_id, checksum, size, mtime, ctime, mod_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(game_id, profile_name, root, relative_path) DO UPDATE SET
			source_id = excluded.source_id,
			mod_id = excluded.mod_id,
			checksum = excluded.checksum,
			size = excluded.size,
			mtime = excluded.mtime,
			ctime = excluded.ctime,
			mod_path = excluded.mod_path,
			deployed_at = CURRENT_TIMESTAMP
	`, rec.GameID, rec.Profile, rec.Path.Root.String(), rec.Path.Rel, rec.SourceID, rec.ModID, checksum, size, mtime, ctime, modPath)
	if err != nil {
		return fmt.Errorf("saving deployed file: %w", err)
	}
	return nil
}

// DeployedFileState is one profile's record of a path: the fingerprint it
// was deployed with (nil when none was recorded), when the row was last
// written, the mod_path it was deployed under ("" when none was recorded),
// and the link method on the installed-mod row that owns it. LinkMethod is
// nil when that row no longer exists; a deployed-file row alone cannot prove
// whether fingerprintless content was a symlink or a legacy copy/hardlink.
type DeployedFileState struct {
	Profile     string
	SourceID    string
	ModID       string
	ModPath     string
	Fingerprint *FileFingerprint
	LinkMethod  *domain.LinkMethod
	DeployedAt  time.Time
}

// DeployedFileStates returns every profile's record of relativePath in
// gameID, sorted by profile - what a removal compares the file on disk
// against (#466), including the method provenance that distinguishes a
// replaced symlink (#483).
func (d *DB) DeployedFileStates(ctx context.Context, gameID string, path domain.DeployPath) (states []DeployedFileState, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT df.profile_name, df.source_id, df.mod_id, df.mod_path,
		       df.checksum, df.size, df.mtime, df.ctime, im.link_method,
		       df.deployed_at
		FROM deployed_files AS df
		LEFT JOIN installed_mods AS im
		  ON im.game_id = df.game_id
		 AND im.profile_name = df.profile_name
		 AND im.source_id = df.source_id
		 AND im.mod_id = df.mod_id
		WHERE df.game_id = ? AND df.root = ? AND df.relative_path = ?
		ORDER BY df.profile_name
	`, gameID, path.Root.String(), path.Rel)
	if err != nil {
		return nil, fmt.Errorf("querying deployed file states: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()
	for rows.Next() {
		var st DeployedFileState
		var modPath, checksum sql.NullString
		var size, mtime, ctime, linkMethod sql.NullInt64
		if err := rows.Scan(&st.Profile, &st.SourceID, &st.ModID, &modPath, &checksum, &size, &mtime, &ctime, &linkMethod, &st.DeployedAt); err != nil {
			return nil, fmt.Errorf("scanning deployed file state: %w", err)
		}
		st.ModPath = modPath.String
		if checksum.Valid && checksum.String != "" {
			st.Fingerprint = &FileFingerprint{Checksum: checksum.String, Size: size.Int64, MTime: mtime.Int64, CTime: ctime.Int64}
		}
		if linkMethod.Valid {
			method := domain.LinkMethod(linkMethod.Int64)
			st.LinkMethod = &method
		}
		states = append(states, st)
	}
	return states, rows.Err()
}

// HasFingerprintedLinkedFiles reports whether profileName of gameID records
// a deployed file with a fingerprint (a checksum) whose mod deploys by
// symlink or hardlink - what an overridden copy left by a profile override
// looks like (#630), where a plain link records none (#641). A record whose
// installed row is gone is judged by defaultMethod. One query, which stops
// at the first match.
func (d *DB) HasFingerprintedLinkedFiles(ctx context.Context, gameID, profileName string, defaultMethod domain.LinkMethod) (bool, error) {
	var found bool
	err := d.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM deployed_files AS df
			LEFT JOIN installed_mods AS im
			  ON im.game_id = df.game_id
			 AND im.profile_name = df.profile_name
			 AND im.source_id = df.source_id
			 AND im.mod_id = df.mod_id
			WHERE df.game_id = ? AND df.profile_name = ?
			  AND df.checksum IS NOT NULL AND df.checksum != ''
			  AND COALESCE(im.link_method, ?) != ?
		)
	`, gameID, profileName, int(defaultMethod), int(domain.LinkCopy)).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("querying fingerprinted deployed files: %w", err)
	}
	return found, nil
}

// DeployedRoot is how many deployed_files rows one profile of a game has
// under one recorded mod_path ("" for rows that recorded none).
type DeployedRoot struct {
	Profile string
	ModPath string
	Files   int
}

// DeployedFileRoots returns gameID's deployed_files rows counted per
// profile and recorded mod_path, sorted by profile then mod_path (#451).
func (d *DB) DeployedFileRoots(ctx context.Context, gameID string) (roots []DeployedRoot, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT profile_name, COALESCE(mod_path, ''), COUNT(*) FROM deployed_files
		WHERE game_id = ?
		GROUP BY profile_name, COALESCE(mod_path, '')
		ORDER BY profile_name, COALESCE(mod_path, '')
	`, gameID)
	if err != nil {
		return nil, fmt.Errorf("counting deployed file roots: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()
	for rows.Next() {
		var r DeployedRoot
		if err := rows.Scan(&r.Profile, &r.ModPath, &r.Files); err != nil {
			return nil, fmt.Errorf("scanning deployed file root: %w", err)
		}
		roots = append(roots, r)
	}
	return roots, rows.Err()
}

// RerootDeployedFiles records every gameID row deployed under one of from as
// deployed under to instead, in one transaction, and returns how many rows
// it changed. "" in from names the rows that recorded no mod_path. It is
// #528's ledger move: a game folder that moved took its deployed files
// with it, still at the same paths relative to the mod_path.
func (d *DB) RerootDeployedFiles(ctx context.Context, gameID string, from []string, to string) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	var total int64
	for _, root := range from {
		query := `UPDATE deployed_files SET mod_path = ? WHERE game_id = ? AND mod_path = ?`
		args := []any{to, gameID, root}
		if root == "" {
			query = `UPDATE deployed_files SET mod_path = ? WHERE game_id = ? AND (mod_path IS NULL OR mod_path = '')`
			args = args[:2]
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, fmt.Errorf("re-rooting deployed files: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("re-rooting deployed files: %w", err)
		}
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing re-rooted deployed files: %w", err)
	}
	return total, nil
}

// GetLastDeployTime returns the most recent deployed_at recorded for
// gameID/profileName across every tracked file (#106a's dashboard "Last
// deploy" row), or nil if the profile has never had a file deployed. A
// never-deployed profile is a normal state, not an error.
//
// Deliberately ORDER BY ... DESC LIMIT 1 rather than SELECT MAX(deployed_at):
// the modernc.org/sqlite driver converts a TEXT column to time.Time on Scan
// by consulting the column's DECLARED type (deployed_at is DATETIME, per
// migrateV7) - but MAX(deployed_at) is a computed expression with no
// declared type of its own, so the driver hands back the raw SQLite storage
// string instead and Scan(&time.Time) fails ("unsupported Scan ... string
// into type *time.Time"), verified empirically against this driver version.
// Selecting the actual column keeps the declared-type conversion intact and
// is equivalent to MAX for this query shape (both scoped to one game/profile,
// ORDER BY DESC LIMIT 1 is exactly "the row with the largest deployed_at").
func (d *DB) GetLastDeployTime(ctx context.Context, gameID, profileName string) (*time.Time, error) {
	var deployedAt time.Time
	err := d.QueryRowContext(ctx, `
		SELECT deployed_at FROM deployed_files
		WHERE game_id = ? AND profile_name = ?
		ORDER BY deployed_at DESC
		LIMIT 1
	`, gameID, profileName).Scan(&deployedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting last deploy time: %w", err)
	}
	return &deployedAt, nil
}

// GetFileOwner returns the mod that owns a specific file path.
// Returns nil if no mod owns the file.
func (d *DB) GetFileOwner(ctx context.Context, gameID, profileName string, path domain.DeployPath) (*FileOwner, error) {
	var owner FileOwner
	err := d.QueryRowContext(ctx, `
		SELECT source_id, mod_id FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND root = ? AND relative_path = ?
	`, gameID, profileName, path.Root.String(), path.Rel).Scan(&owner.SourceID, &owner.ModID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting file owner: %w", err)
	}
	return &owner, nil
}

// AnyProfileOwnsFile reports whether ANY profile of gameID has a
// deployed-file record for relativePath.
//
// GetFileOwner is scoped to a game AND a profile, which is right for
// "whose file is this in the deployment I am changing" but wrong for
// "did lmm put this here at all" (#350 review minor 7): with `lmm deploy
// -p B` over a copy/hardlink deployment made under profile A, A's own file
// looked foreign to B - stored as an "original", so a later restore would
// have written a mod's bytes back as if they were stock content.
func (d *DB) AnyProfileOwnsFile(ctx context.Context, gameID string, path domain.DeployPath) (bool, error) {
	var one int
	err := d.QueryRowContext(ctx, `
		SELECT 1 FROM deployed_files
		WHERE game_id = ? AND root = ? AND relative_path = ?
		LIMIT 1
	`, gameID, path.Root.String(), path.Rel).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking file ownership: %w", err)
	}
	return true, nil
}

// PathRecord is one profile's deployed-file record of a path: the profile
// and the mod the record names.
type PathRecord struct {
	Profile  string
	SourceID string
	ModID    string
	// ModPath is the mod_path the record was deployed under (#451), or ""
	// when it recorded none.
	ModPath string
}

// DeployedPathRecords returns, for every path any profile of gameID has a
// deployed-file record for, those records, sorted by profile. A purge of a
// profile that is not active (#445) removes only the paths no other record
// claims, and hands a path the active profile lists on only to a record
// that keeps it for the same reason - which depends on the mod each record
// names.
func (d *DB) DeployedPathRecords(ctx context.Context, gameID string) (records map[domain.DeployPath][]PathRecord, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT root, relative_path, profile_name, source_id, mod_id, COALESCE(mod_path, '') FROM deployed_files
		WHERE game_id = ?
		ORDER BY `+orderByPath+`, profile_name
	`, gameID)
	if err != nil {
		return nil, fmt.Errorf("querying deployed files: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	records = make(map[domain.DeployPath][]PathRecord)
	for rows.Next() {
		var root, rel string
		var r PathRecord
		if err := rows.Scan(&root, &rel, &r.Profile, &r.SourceID, &r.ModID, &r.ModPath); err != nil {
			return nil, fmt.Errorf("scanning deployed file: %w", err)
		}
		dr, err := scanRoot(root)
		if err != nil {
			return nil, err
		}
		path := domain.DeployPath{Root: dr, Rel: rel}
		records[path] = append(records[path], r)
	}
	return records, rows.Err()
}

// DeployedFileCounts returns how many deployed_files rows each profile of
// gameID has, keyed by profile name; a profile with none has no entry. It
// is the population a mod_path move would strand, since each row is
// relative to the mod_path it was deployed under - per profile, because
// each profile is purged on its own.
func (d *DB) DeployedFileCounts(ctx context.Context, gameID string) (counts map[string]int, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT profile_name, COUNT(*) FROM deployed_files
		WHERE game_id = ?
		GROUP BY profile_name
	`, gameID)
	if err != nil {
		return nil, fmt.Errorf("counting deployed files: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	counts = map[string]int{}
	for rows.Next() {
		var profile string
		var n int
		if err := rows.Scan(&profile, &n); err != nil {
			return nil, fmt.Errorf("scanning deployed file count: %w", err)
		}
		counts[profile] = n
	}
	return counts, rows.Err()
}

// DeleteDeployedFiles removes all deployed file records for a specific mod.
func (d *DB) DeleteDeployedFiles(ctx context.Context, gameID, profileName, sourceID, modID string) error {
	_, err := d.ExecContext(ctx, `
		DELETE FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return fmt.Errorf("deleting deployed files: %w", err)
	}
	return nil
}

// DeleteDeployedFilesExcept is DeleteDeployedFiles keeping every row
// recorded under one of the mod_paths in keepRoots (#451) - an uninstall
// under a game's current mod_path does not know what is under another one,
// so it leaves those rows for the purge that does - and every row for one
// of the relative paths in keepPaths: files the uninstall could not judge
// (#466), whose records must not change.
func (d *DB) DeleteDeployedFilesExcept(ctx context.Context, gameID, profileName, sourceID, modID string, keepRoots []string, keepPaths []domain.DeployPath) error {
	if len(keepRoots) == 0 && len(keepPaths) == 0 {
		return d.DeleteDeployedFiles(ctx, gameID, profileName, sourceID, modID)
	}
	query := `
		DELETE FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?`
	args := []any{gameID, profileName, sourceID, modID}
	notIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		query += fmt.Sprintf(" AND %s NOT IN (%s)", column, strings.TrimSuffix(strings.Repeat("?,", len(values)), ","))
		for _, v := range values {
			args = append(args, v)
		}
	}
	notIn("COALESCE(mod_path, '')", keepRoots)
	// The kept paths, one NOT IN per root: a clause per path nests one
	// expression per path, and SQLite refuses a tree over 1000 deep.
	byRoot := map[domain.DeployRoot][]string{}
	for _, p := range keepPaths {
		byRoot[p.Root] = append(byRoot[p.Root], p.Rel)
	}
	for _, root := range domain.DeployRoots() {
		rels := byRoot[root]
		if len(rels) == 0 {
			continue
		}
		query += fmt.Sprintf(" AND NOT (root = ? AND relative_path IN (%s))", strings.TrimSuffix(strings.Repeat("?,", len(rels)), ","))
		args = append(args, root.String())
		for _, rel := range rels {
			args = append(args, rel)
		}
	}
	if _, err := d.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("deleting deployed files: %w", err)
	}
	return nil
}

// HasDeployedFilesUnder reports whether any profile of the game records a
// deployed file under root (#636: whether a game has files in its prefix
// to account for).
func (d *DB) HasDeployedFilesUnder(ctx context.Context, gameID string, root domain.DeployRoot) (bool, error) {
	var one int
	err := d.QueryRowContext(ctx, `SELECT 1 FROM deployed_files WHERE game_id = ? AND root = ? LIMIT 1`, gameID, root.String()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking deployed files: %w", err)
	}
	return true, nil
}

// DeployedFileRecordsForMod returns every deployed_files row of one mod in
// a game's profile exactly as it is stored - fingerprint and mod_path
// included - sorted by path: what a replace puts back if it fails (#466).
func (d *DB) DeployedFileRecordsForMod(ctx context.Context, gameID, profileName, sourceID, modID string) (records []DeployedFileRecord, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT root, relative_path, mod_path, checksum, size, mtime, ctime FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
		ORDER BY `+orderByPath+`
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("querying deployed file records: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()
	for rows.Next() {
		rec := DeployedFileRecord{GameID: gameID, Profile: profileName, SourceID: sourceID, ModID: modID}
		var root string
		var modPath, checksum sql.NullString
		var size, mtime, ctime sql.NullInt64
		if err := rows.Scan(&root, &rec.Path.Rel, &modPath, &checksum, &size, &mtime, &ctime); err != nil {
			return nil, fmt.Errorf("scanning deployed file record: %w", err)
		}
		if rec.Path.Root, err = scanRoot(root); err != nil {
			return nil, err
		}
		rec.ModPath = modPath.String
		if checksum.Valid && checksum.String != "" {
			rec.Fingerprint = &FileFingerprint{Checksum: checksum.String, Size: size.Int64, MTime: mtime.Int64, CTime: ctime.Int64}
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// DeleteDeployedFile removes one deployed-file ownership row. Deleting a
// row that does not exist is a silent no-op - convergence (#168/#212) calls
// this for paths it just undeployed, and a row may already be gone when the
// path was attributed only by a dangling link.
func (d *DB) DeleteDeployedFile(ctx context.Context, gameID, profileName string, path domain.DeployPath) error {
	_, err := d.ExecContext(ctx, `
		DELETE FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND root = ? AND relative_path = ?`,
		gameID, profileName, path.Root.String(), path.Rel)
	if err != nil {
		return fmt.Errorf("deleting deployed file record: %w", err)
	}
	return nil
}

// GetDeployedFilesForMod returns all file paths deployed by a specific mod.
func (d *DB) GetDeployedFilesForMod(ctx context.Context, gameID, profileName, sourceID, modID string) (paths []domain.DeployPath, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT root, relative_path FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
		ORDER BY `+orderByPath+`
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("querying deployed files: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	for rows.Next() {
		var root, rel string
		if err := rows.Scan(&root, &rel); err != nil {
			return nil, fmt.Errorf("scanning path: %w", err)
		}
		dr, err := scanRoot(root)
		if err != nil {
			return nil, err
		}
		paths = append(paths, domain.DeployPath{Root: dr, Rel: rel})
	}
	return paths, rows.Err()
}

// CheckFileConflicts checks which of the given paths are already owned by other mods.
// Returns a slice of conflicts (empty if no conflicts).
func (d *DB) CheckFileConflicts(ctx context.Context, gameID, profileName string, paths []domain.DeployPath) (conflicts []FileConflict, err error) {
	if len(paths) == 0 {
		return nil, nil
	}

	// One IN clause per root the paths name.
	byRoot := map[domain.DeployRoot][]any{}
	for _, p := range paths {
		byRoot[p.Root] = append(byRoot[p.Root], p.Rel)
	}
	args := []any{gameID, profileName}
	var clauses []string
	for _, root := range domain.DeployRoots() {
		rels := byRoot[root]
		if len(rels) == 0 {
			continue
		}
		clauses = append(clauses, fmt.Sprintf("(root = ? AND relative_path IN (%s))", strings.TrimSuffix(strings.Repeat("?,", len(rels)), ",")))
		args = append(args, root.String())
		args = append(args, rels...)
	}

	query := fmt.Sprintf(`
		SELECT root, relative_path, source_id, mod_id FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND (%s)
		ORDER BY %s
	`, strings.Join(clauses, " OR "), orderByPath)

	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("checking conflicts: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	for rows.Next() {
		var c FileConflict
		var root string
		if err := rows.Scan(&root, &c.Path.Rel, &c.SourceID, &c.ModID); err != nil {
			return nil, fmt.Errorf("scanning conflict: %w", err)
		}
		if c.Path.Root, err = scanRoot(root); err != nil {
			return nil, err
		}
		conflicts = append(conflicts, c)
	}
	return conflicts, rows.Err()
}

// DeployedPath is one deployed_files row: a game-dir-relative path and the
// mod that owns it.
type DeployedPath struct {
	// Path is the file: its root and its path relative to it (#629).
	Path     domain.DeployPath
	SourceID string
	ModID    string
	// ModPath is the mod_path the row was deployed under (#451), or ""
	// when it recorded none.
	ModPath string
}

// ListDeployedFiles returns every tracked path for gameID/profileName,
// path-sorted so a caller's output is stable. The per-mod
// GetDeployedFilesForMod answers "what did THIS mod deploy"; this answers
// "what is deployed at all", which is what `lmm snapshot create` records
// as the profile's deployed-files manifest (#350).
func (d *DB) ListDeployedFiles(ctx context.Context, gameID, profileName string) (files []DeployedPath, err error) {
	rows, err := d.QueryContext(ctx, `
		SELECT root, relative_path, source_id, mod_id, COALESCE(mod_path, '') FROM deployed_files
		WHERE game_id = ? AND profile_name = ?
		ORDER BY `+orderByPath+`
	`, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying deployed files: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing rows: %w", cerr)
		}
	}()

	for rows.Next() {
		var f DeployedPath
		var root string
		if err := rows.Scan(&root, &f.Path.Rel, &f.SourceID, &f.ModID, &f.ModPath); err != nil {
			return nil, fmt.Errorf("scanning deployed file: %w", err)
		}
		if f.Path.Root, err = scanRoot(root); err != nil {
			return nil, err
		}

		files = append(files, f)
	}
	return files, rows.Err()
}
