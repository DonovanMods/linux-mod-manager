package db

// tokenmigrate.go re-encrypts credentials written by a pre-#79 lmm, which
// bound the raw API key straight into token_data.
//
// WHY ON OPEN, not on the first token read or write. Every frontend reaches
// a credential through db.Open - the CLI, `lmm serve`, and every source
// registered at startup - so one trigger covers all of them, and it is the
// only place where the fix can happen BEFORE anything reads the table. The
// alternatives were worse: a read-triggered migration would have to write
// (and take a transaction) inside a query path, and a write-triggered one
// would leave a user who only ever READS their credentials sitting on
// plaintext forever. The cost is one indexed SELECT over a table with a
// handful of rows on every open, and nothing else when there is nothing to
// do - no key file is even created (TestOpen_NoLegacyRowsCreatesNoKey).
//
// It is idempotent by construction: a row that already carries the lmm1
// envelope is skipped, so repeated opens do not re-seal (and therefore do
// not churn the ciphertext) - only rows still in the clear are touched.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// migrateTokenEncryption finds every plaintext row, re-encrypts all of them
// inside ONE transaction, then makes sure the plaintext is gone from disk.
//
// The transaction is what makes a partial migration impossible: an
// interrupted run leaves every row as it was, and the next open tries
// again. A key file that cannot be used is fatal to the open, because the
// alternative - carrying on - is silently leaving credentials in the clear
// after lmm has said it encrypts them.
func (d *DB) migrateTokenEncryption(ctx context.Context) error {
	legacy, err := d.legacyTokenRows(ctx)
	if err != nil {
		return err
	}
	if len(legacy) == 0 {
		return nil
	}

	key, err := d.tokenKey(true)
	if err != nil {
		return err
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("re-encrypting stored credentials: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op once committed

	for sourceID, plaintext := range legacy {
		envelope, sealErr := sealToken(key, sourceID, plaintext)
		if sealErr != nil {
			return sealErr
		}
		if _, err := tx.ExecContext(ctx, "UPDATE auth_tokens SET token_data = ? WHERE source_id = ?", envelope, sourceID); err != nil {
			return fmt.Errorf("re-encrypting the stored credential for %q: %w", sourceID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("re-encrypting stored credentials: %w", err)
	}
	d.log.Info("re-encrypted stored credentials at rest", "sources", len(legacy))

	return d.scrubTokenPlaintext(ctx)
}

// legacyTokenRows returns every auth_tokens row still holding a plaintext
// key, keyed by source id. A row carrying the lmm1 magic is already done.
func (d *DB) legacyTokenRows(ctx context.Context) (map[string]string, error) {
	rows, err := d.QueryContext(ctx, "SELECT source_id, token_data FROM auth_tokens")
	if err != nil {
		return nil, fmt.Errorf("scanning stored credentials: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	legacy := map[string]string{}
	for rows.Next() {
		var sourceID string
		var blob []byte
		if err := rows.Scan(&sourceID, &blob); err != nil {
			return nil, fmt.Errorf("scanning stored credentials: %w", err)
		}
		if isTokenEnvelope(blob) {
			continue
		}
		legacy[sourceID] = string(blob)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning stored credentials: %w", err)
	}
	return legacy, nil
}

// scrubTokenPlaintext removes the pre-migration bytes from the files on
// disk. Rewriting the row is not enough on its own:
//
//   - the main file can retain the superseded page content - in the
//     freelist, or simply in a page the UPDATE did not have to rewrite.
//     VACUUM rebuilds the whole file. This is best effort: it needs the
//     database uncontended, and failing it is not a reason to refuse to
//     start, since the row itself is already encrypted.
//   - the WAL then holds every page VACUUM wrote (in WAL mode a VACUUM
//     lands there, not in the main file), and before that the original
//     plaintext page. TRUNCATE checkpoints it into the main file and
//     empties it. This is required - a migration that leaves the key
//     readable in a sidecar has not done its job - and it must run AFTER
//     the VACUUM, or it would checkpoint the pre-VACUUM file and leave the
//     rebuild sitting in a fresh WAL.
func (d *DB) scrubTokenPlaintext(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, "VACUUM"); err != nil {
		d.log.Warn("could not vacuum after re-encrypting credentials; the old bytes may linger in free pages until the next vacuum", "err", err)
	}
	var busy, logFrames, checkpointed int
	if err := d.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		// An in-memory database has no WAL at all; anything else is real.
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("checkpointing the write-ahead log after re-encrypting credentials: %w", err)
		}
	}
	return nil
}
