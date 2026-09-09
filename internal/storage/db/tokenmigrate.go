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
	"time"
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
//     VACUUM rebuilds the whole file.
//   - the WAL then holds every page VACUUM wrote (in WAL mode a VACUUM
//     lands there, not in the main file), and before that the original
//     plaintext page. TRUNCATE checkpoints it into the main file and
//     empties it. It must run AFTER the VACUUM, or it would checkpoint the
//     pre-VACUUM file and leave the rebuild sitting in a fresh WAL.
//
// BOTH are required, and failing either fails the open (review 1). A
// migration that leaves the key readable in the main file or in a sidecar
// has not done its job, and reporting success over one is worse than
// refusing to start: the user is told their credentials are encrypted while
// a backup, a file sync or an rsync can still copy them in the clear.
//
// The contention this guards against is ordinary: any second lmm process
// with the database open - `lmm serve` up while a CLI command runs, or two
// shells - holds a read lock that stops both steps. Neither reports that as
// an ERROR. VACUUM returns SQLITE_BUSY, and PRAGMA wal_checkpoint returns no
// error at all: busy=1 in its first result column, with the WAL untouched.
// So each step is retried against the 5 s busy_timeout, and the checkpoint's
// result columns are checked rather than discarded.
func (d *DB) scrubTokenPlaintext(ctx context.Context) error {
	if err := d.retryUntilUncontended(ctx, "rebuilding the database", d.vacuumOnce); err != nil {
		return err
	}
	return d.retryUntilUncontended(ctx, "truncating the write-ahead log", d.checkpointWALOnce)
}

// scrubAttempts is how many times each scrub step is tried before the open
// fails. Each attempt already waits out the 5 s busy_timeout, so this is a
// second chance for a short-lived reader, not a way to outlast a long one.
const scrubAttempts = 3

// scrubRetryPause is the gap between attempts, doubling each time.
const scrubRetryPause = 250 * time.Millisecond

// retryUntilUncontended runs step until it succeeds, giving a transient
// reader time to finish, and turns a persistent failure into the error that
// fails the open - naming the database, the step, and the remedy.
func (d *DB) retryUntilUncontended(ctx context.Context, step string, once func(context.Context) error) error {
	pause := scrubRetryPause
	var err error
	for attempt := 1; attempt <= scrubAttempts; attempt++ {
		if err = once(ctx); err == nil {
			return nil
		}
		if attempt == scrubAttempts {
			break
		}
		d.log.Debug("retrying the credential scrub", "step", step, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
		pause *= 2
	}
	return fmt.Errorf("could not remove the pre-encryption credentials from %s: %s did not complete after %d attempts, which usually means the database is in use by another process; close any other lmm process (`lmm serve` included) and run the command again: %w", d.path, step, scrubAttempts, err)
}

// vacuumOnce rebuilds the database file, dropping the free pages that can
// still hold the pre-migration plaintext.
func (d *DB) vacuumOnce(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuuming: %w", err)
	}
	return nil
}

// checkpointWALOnce folds the WAL into the main file and truncates it.
//
// The result columns are the whole point: wal_checkpoint reports a
// database it could not checkpoint as busy=1 with a nil error, and a
// TRUNCATE that completed leaves no frames behind - so anything other than
// busy=0 with an empty log means the plaintext may still be in the sidecar.
func (d *DB) checkpointWALOnce(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	err := d.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed)
	if errors.Is(err, sql.ErrNoRows) {
		// An in-memory database has no WAL at all; nothing to scrub.
		return nil
	}
	if err != nil {
		return fmt.Errorf("checkpointing the write-ahead log: %w", err)
	}
	if busy != 0 || logFrames != 0 {
		return fmt.Errorf("checkpointing the write-ahead log: busy=%d, %d frame(s) left in the log, %d checkpointed", busy, logFrames, checkpointed)
	}
	return nil
}
