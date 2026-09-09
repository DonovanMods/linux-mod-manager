package db

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
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

	// Release the contention; the very next open completes the job.
	require.NoError(t, reader.Rollback())
	require.NoError(t, seeder.Close())

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	got, err := d.GetToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, legacyKey, got.APIKey)
	assert.False(t, fileContains(t, dbPath, legacyKey), "plaintext still present in the database")
	assert.False(t, fileContains(t, dbPath+"-wal", legacyKey), "plaintext still present in the WAL")
}
