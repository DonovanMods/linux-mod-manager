package db

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedPlaintextTokens writes rows the pre-#79 code wrote: the raw key bound
// straight into token_data. Raw SQL, deliberately - the point is to produce
// exactly what a database from an older lmm contains.
func seedPlaintextTokens(t *testing.T, dbPath string, keys map[string]string) {
	t.Helper()
	require.NoError(t, openPlaintextSeeder(t, dbPath, keys).Close())
}

// openPlaintextSeeder is seedPlaintextTokens with the connection left OPEN,
// so the plaintext frames stay in lmm.db-wal instead of being checkpointed
// into the main file at close. That is the state a second lmm process finds
// when it runs the migration, and it is what the contention test needs.
func openPlaintextSeeder(t *testing.T, dbPath string, keys map[string]string) *DB {
	t.Helper()
	d, err := OpenWithOptions(dbPath, Options{KeyPath: filepath.Join(t.TempDir(), "unused-key")})
	require.NoError(t, err)
	ctx := context.Background()
	for source, key := range keys {
		_, err := d.ExecContext(ctx, `
			INSERT INTO auth_tokens (source_id, token_data, updated_at)
			VALUES (?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(source_id) DO UPDATE SET token_data = excluded.token_data
		`, source, key)
		require.NoError(t, err)
	}
	return d
}

// fileContains reports whether the file at path holds needle verbatim. A
// missing file holds nothing - the WAL does not exist until WAL mode has
// something to write.
func fileContains(t *testing.T, path, needle string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	return bytes.Contains(raw, []byte(needle))
}

func TestOpen_ReencryptsLegacyPlaintextRows(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()

	seedPlaintextTokens(t, dbPath, map[string]string{
		"nexusmods":  "legacy-nexus-key-123456",
		"curseforge": "legacy-curse-key-abcdef",
	})

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	// They come back decrypted...
	for source, want := range map[string]string{
		"nexusmods":  "legacy-nexus-key-123456",
		"curseforge": "legacy-curse-key-abcdef",
	} {
		got, err := d.GetToken(ctx, source)
		require.NoError(t, err)
		assert.Equal(t, want, got.APIKey, "source %s", source)

		// ...and are no longer plaintext in the row itself.
		blob := rawTokenBlob(t, d, source)
		assert.True(t, bytes.HasPrefix(blob, []byte(tokenEnvelopeMagic)))
		assert.NotContains(t, string(blob), want)
	}

	// Nor anywhere on disk: neither the database nor its WAL may still
	// carry the old bytes.
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(dbPath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "legacy-nexus-key-123456", "plaintext still present in %s", dbPath+suffix)
		assert.NotContains(t, string(raw), "legacy-curse-key-abcdef", "plaintext still present in %s", dbPath+suffix)
	}
}

func TestOpen_TokenMigrationIsIdempotent(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()

	seedPlaintextTokens(t, dbPath, map[string]string{"nexusmods": "legacy-key-0987654321"})

	var first []byte
	for range 3 {
		d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
		require.NoError(t, err)
		got, err := d.GetToken(ctx, "nexusmods")
		require.NoError(t, err)
		assert.Equal(t, "legacy-key-0987654321", got.APIKey)

		blob := rawTokenBlob(t, d, "nexusmods")
		if first == nil {
			first = bytes.Clone(blob)
		} else {
			assert.Equal(t, first, blob, "an already-encrypted row must not be re-sealed on every open")
		}
		require.NoError(t, d.Close())
	}
}

// TestOpen_NoLegacyRowsCreatesNoKey pins that the migration is free for the
// overwhelming majority of opens: no plaintext rows, no key file, no work.
func TestOpen_NoLegacyRowsCreatesNoKey(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	require.NoError(t, d.Close())

	_, err = os.Stat(keyPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestOpen_LegacyRowsWithAnUnusableKeyFileFailTheOpen: the migration cannot
// silently leave plaintext behind, so a key file it cannot use is fatal to
// the open - with the typed error naming the file.
func TestOpen_LegacyRowsWithAnUnusableKeyFileFailTheOpen(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)

	seedPlaintextTokens(t, dbPath, map[string]string{"nexusmods": "legacy-key"})
	require.NoError(t, os.WriteFile(keyPath, []byte("not-a-32-byte-key"), 0600))

	_, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	var keyErr *KeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, KeyMalformed, keyErr.Reason)
	assert.Equal(t, keyPath, keyErr.Path)
}

func TestSaveToken_RecordsCreatedAtAndPreservesItAcrossUpdates(t *testing.T) {
	d, _, _ := openFileDB(t)
	ctx := context.Background()

	require.NoError(t, d.SaveToken(ctx, "nexusmods", "first"))
	first, err := d.ListTokens(ctx)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.False(t, first[0].CreatedAt.IsZero())

	require.NoError(t, d.SaveToken(ctx, "nexusmods", "second"))
	second, err := d.ListTokens(ctx)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, first[0].CreatedAt, second[0].CreatedAt, "created_at must survive a re-login")
	assert.Equal(t, TokenFingerprint("second"), second[0].Fingerprint)
}

// TestOpen_ContendedMigrationFailsRatherThanLeavingPlaintextInTheWAL is the
// regression for the review's Critical 1. PRAGMA wal_checkpoint does not
// report a busy database as an ERROR - it returns busy=1 in its first result
// column and leaves the WAL exactly where it was. Scanning that column and
// discarding it meant a migration run while any other lmm process held the
// database open returned SUCCESS over a credential still readable in
// lmm.db-wal, which is the one thing #79 exists to prevent.
//
// The contention here is the real one: a second connection holding a read
// transaction open across the whole migration, with the pre-#79 plaintext
// sitting in an unchecked­pointed WAL.
//
// This test asserts the REFUSAL and nothing more. It used to go on to open
// again after closing the seeder and assert a clean WAL, which passed for
// the wrong reason: closing the last connection makes SQLite checkpoint the
// WAL itself, so the assertion was satisfied without lmm scrubbing anything.
// What happens on the next open is
// TestOpen_AFailedScrubIsRetriedOnTheNextOpen, which keeps a connection up.
func TestOpen_ContendedMigrationFailsRatherThanLeavingPlaintextInTheWAL(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()
	const legacyKey = "legacy-nexus-key-contended-1234567890"

	// Left open, so the plaintext frames stay in the WAL.
	seeder := openPlaintextSeeder(t, dbPath, map[string]string{"nexusmods": legacyKey})
	require.True(t, fileContains(t, dbPath+"-wal", legacyKey),
		"precondition: the pre-#79 plaintext must be sitting in the WAL")

	// A reader holding its snapshot across the migration. BEGIN is deferred
	// in SQLite, so the read lock is only taken at the first query.
	reader, err := seeder.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	var n int
	require.NoError(t, reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_tokens").Scan(&n))
	require.Equal(t, 1, n)

	// The open must FAIL. Succeeding here would be a lie: the row would be
	// re-encrypted but the plaintext would still be in the sidecar.
	contended, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	if err == nil {
		require.NoError(t, contended.Close())
	}
	require.Error(t, err, "a migration that cannot scrub the WAL must not report success")
	assert.ErrorContains(t, err, "in use by another process")
	assert.ErrorContains(t, err, dbPath)

	// The plaintext really is still on disk - refusing is the whole point.
	assert.True(t, fileContains(t, dbPath+"-wal", legacyKey))

	require.NoError(t, reader.Rollback())
	require.NoError(t, seeder.Close())
}

// TestOpen_ReencryptsALegacyKeyThatStartsWithTheMagic is the regression for
// the review's Important 3. The legacy discriminator used to be a four-byte
// prefix sniff, so a pre-#79 plaintext key that happened to begin with
// "lmm1" was mistaken for an envelope: the migration skipped it, no key file
// was ever created, and the credential stayed in the clear in lmm.db
// INDEFINITELY while every read of the table failed with "the
// token-encryption key is missing" - a wrong diagnosis that took the whole
// auth listing down with it. A user-defined `api` source's key is whatever
// the user pastes, so the shape is not impossible.
func TestOpen_ReencryptsALegacyKeyThatStartsWithTheMagic(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()

	// 40 bytes - longer than the smallest possible envelope - and printable
	// throughout, which is what makes it a key and not a ciphertext.
	const legacyKey = "lmm1abcdefghijklmnopqrstuvwxyz0123456789"
	seedPlaintextTokens(t, dbPath, map[string]string{"my-api-source": legacyKey})

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	got, err := d.GetToken(ctx, "my-api-source")
	require.NoError(t, err, "a legacy key beginning with the magic must still be readable")
	assert.Equal(t, legacyKey, got.APIKey)

	// It really was re-encrypted, not merely handed back verbatim.
	assert.NotEqual(t, []byte(legacyKey), rawTokenBlob(t, d, "my-api-source"))
	assert.False(t, fileContains(t, dbPath, legacyKey), "plaintext still present in the database")
	assert.False(t, fileContains(t, dbPath+"-wal", legacyKey), "plaintext still present in the WAL")

	// And the listing works, rather than failing with a spurious "the key is missing".
	infos, err := d.ListTokens(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.True(t, infos[0].Readable)
	assert.Equal(t, TokenFingerprint(legacyKey), infos[0].Fingerprint)
}

// scrubMarkerKey is the db_meta key the durable "credential scrub pending"
// obligation is recorded under. Spelled out here rather than referenced from
// the implementation: it is an on-disk contract, and a database written by
// one build has to be understood by the next.
const scrubMarkerKey = "token_scrub_pending"

// scrubMarkerSet reports whether the marker is recorded in db_meta. A
// database written before the marker existed has no db_meta table at all,
// and owes nothing.
func scrubMarkerSet(t *testing.T, d *DB) bool {
	t.Helper()
	var n int
	err := d.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM db_meta WHERE key = ?", scrubMarkerKey).Scan(&n)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return false
	}
	require.NoError(t, err)
	return n > 0
}

// setScrubMarker records the marker by hand, so a test can build the state a
// crashed or contended scrub leaves behind without having to contend.
func setScrubMarker(t *testing.T, d *DB) {
	t.Helper()
	_, err := d.ExecContext(context.Background(),
		"INSERT OR REPLACE INTO db_meta (key, value) VALUES (?, ?)", scrubMarkerKey, "set-by-test")
	require.NoError(t, err)
}

// holdContention seeds a legacy plaintext row through a connection it leaves
// open - so the plaintext frames stay in lmm.db-wal - and holds a read
// transaction across it, which is exactly what a second lmm process (`lmm
// serve`) does to the scrub. The returned release() drops the read lock but
// KEEPS the connection open, which is the state Proof A of the re-review
// showed lmm never recovered from.
func holdContention(t *testing.T, dbPath, legacyKey string) (seeder *DB, release func()) {
	t.Helper()
	ctx := context.Background()

	seeder = openPlaintextSeeder(t, dbPath, map[string]string{"nexusmods": legacyKey})
	t.Cleanup(func() { _ = seeder.Close() }) //nolint:errcheck // may already be closed
	require.True(t, fileContains(t, dbPath+"-wal", legacyKey),
		"precondition: the pre-#79 plaintext must be sitting in the WAL")

	// BEGIN is deferred in SQLite, so the read lock is only taken at the
	// first query.
	reader, err := seeder.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	var n int
	require.NoError(t, reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM auth_tokens").Scan(&n))
	require.Equal(t, 1, n)

	return seeder, func() { require.NoError(t, reader.Rollback()) }
}

// TestOpen_AFailedScrubIsRetriedOnTheNextOpen is the regression for the
// re-review's remaining Critical. The scrub obligation used to be DERIVED
// from the rows: the re-encryption transaction committed first, so once the
// scrub failed the rows carried the envelope, legacyTokenRows came back
// empty on every later open, and the scrub never ran again. lmm then
// reported success over a credential still readable in lmm.db-wal - the
// exact outcome the error message's own remedy ("close the other process and
// run the command again") tells the user they have fixed.
//
// The seeder connection is deliberately left OPEN across the second open:
// closing it would let SQLite's own last-connection checkpoint clean the WAL
// and the assertion would pass without lmm scrubbing anything.
func TestOpen_AFailedScrubIsRetriedOnTheNextOpen(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()
	const legacyKey = "legacy-nexus-key-retried-1234567890"

	seeder, release := holdContention(t, dbPath, legacyKey)

	contended, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	if err == nil {
		require.NoError(t, contended.Close())
	}
	require.Error(t, err, "a migration that cannot scrub the WAL must not report success")
	assert.ErrorContains(t, err, "in use by another process")
	assert.True(t, scrubMarkerSet(t, seeder),
		"the failed scrub must leave a durable obligation behind")

	// Contention gone, but the other connection is still up - so nothing but
	// lmm itself can clean this WAL.
	release()

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err, "the next open must finish the scrub the first one could not")
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	got, err := d.GetToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, legacyKey, got.APIKey)
	assert.False(t, fileContains(t, dbPath, legacyKey), "plaintext still present in the database")
	assert.False(t, fileContains(t, dbPath+"-wal", legacyKey), "plaintext still present in the WAL")
	assert.False(t, scrubMarkerSet(t, d), "a completed scrub must clear its marker")
}

// TestOpen_ASecondContendedOpenFailsAgainRatherThanReportingSuccess is the
// other face of the same Critical: the run that still cannot scrub must
// still refuse. Reporting success on the second attempt would be worse than
// on the first, because the user has by then followed the remedy and
// believes the plaintext is gone.
func TestOpen_ASecondContendedOpenFailsAgainRatherThanReportingSuccess(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	const legacyKey = "legacy-nexus-key-still-contended-1234567890"

	seeder, release := holdContention(t, dbPath, legacyKey)
	defer release()

	for attempt := 1; attempt <= 2; attempt++ {
		d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
		if err == nil {
			require.NoError(t, d.Close())
		}
		require.Error(t, err, "open %d must not report success while the plaintext is still in the WAL", attempt)
		assert.ErrorContains(t, err, "in use by another process")
	}

	assert.True(t, fileContains(t, dbPath+"-wal", legacyKey),
		"precondition of the assertion above: the plaintext really is still there")
	assert.True(t, scrubMarkerSet(t, seeder), "the obligation must survive a failed retry")
}

// TestOpen_APendingScrubMarkerWithNoLegacyRowsClearsItself pins that the
// obligation is honoured on its own terms. Every row can already carry an
// envelope - that is precisely the state a failed scrub leaves - so the
// marker, not the row contents, is what decides whether the scrub runs.
func TestOpen_APendingScrubMarkerWithNoLegacyRowsClearsItself(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()

	first, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	require.NoError(t, first.SaveToken(ctx, "nexusmods", "already-encrypted-key-1234567890"))
	setScrubMarker(t, first)
	require.NoError(t, first.Close())

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	assert.False(t, scrubMarkerSet(t, d), "an uncontended open must discharge a pending scrub")
	got, err := d.GetToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "already-encrypted-key-1234567890", got.APIKey)
}

// TestOpen_TheScrubMarkerIsIdempotentAcrossReopens pins the other side of
// the marker: once discharged it stays discharged, so the ordinary open -
// which is every open after the one migration - still does no scrub work and
// leaves no obligation behind.
func TestOpen_TheScrubMarkerIsIdempotentAcrossReopens(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()
	const legacyKey = "legacy-nexus-key-idempotent-1234567890"

	seedPlaintextTokens(t, dbPath, map[string]string{"nexusmods": legacyKey})

	for range 3 {
		d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
		require.NoError(t, err)
		assert.False(t, scrubMarkerSet(t, d), "a settled database owes no scrub")
		got, err := d.GetToken(ctx, "nexusmods")
		require.NoError(t, err)
		assert.Equal(t, legacyKey, got.APIKey)
		require.NoError(t, d.Close())

		assert.False(t, fileContains(t, dbPath, legacyKey), "plaintext still present in the database")
		assert.False(t, fileContains(t, dbPath+"-wal", legacyKey), "plaintext still present in the WAL")
	}
}

// TestCheckpointWALOnce_ADatabaseWithNoWALHasNothingToCheckpoint pins the
// real shape of the non-WAL answer (re-review N2). The guard here used to
// test for sql.ErrNoRows, which this driver never returns from PRAGMA
// wal_checkpoint - so an in-memory database would have been REJECTED with
// "0 frame(s) left in the log" rather than skipped, had one ever reached
// the scrub. The actual answer is a row with both counts at -1.
func TestCheckpointWALOnce_ADatabaseWithNoWALHasNothingToCheckpoint(t *testing.T) {
	ctx := context.Background()
	d, err := OpenWithOptions(":memory:", Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	var busy, logFrames, checkpointed int
	require.NoError(t, d.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logFrames, &checkpointed),
		"the pragma answers with a row, not sql.ErrNoRows")
	assert.Negative(t, logFrames, "a database with no WAL reports a negative frame count")

	assert.NoError(t, d.checkpointWALOnce(ctx))
}
