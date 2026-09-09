package db

import (
	"bytes"
	"context"
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
	require.NoError(t, d.Close())
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
