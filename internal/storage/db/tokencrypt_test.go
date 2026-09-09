package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandboxHome points HOME and every XDG variable at t.TempDir() so nothing
// in this file can read or write the developer's real lmm installation -
// these tests create key files and databases on disk.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// openFileDB opens a real on-disk database under a sandboxed HOME, with the
// token key file next to it, and returns the database plus both paths.
func openFileDB(t *testing.T) (*DB, string, string) {
	t.Helper()
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	return d, dbPath, keyPath
}

// rawTokenBlob reads token_data for sourceID exactly as it sits in the
// database - the only way to prove a key is not stored in the clear.
func rawTokenBlob(t *testing.T, d *DB, sourceID string) []byte {
	t.Helper()
	var blob []byte
	require.NoError(t, d.QueryRowContext(context.Background(),
		"SELECT token_data FROM auth_tokens WHERE source_id = ?", sourceID).Scan(&blob))
	return blob
}

func TestSaveToken_StoresAnEncryptedEnvelopeNotPlaintext(t *testing.T) {
	d, _, _ := openFileDB(t)
	ctx := context.Background()

	const key = "plaintext-nexus-key-1234567890"
	require.NoError(t, d.SaveToken(ctx, "nexusmods", key))

	blob := rawTokenBlob(t, d, "nexusmods")
	assert.NotContains(t, string(blob), key, "the API key must not be readable in token_data")
	assert.True(t, bytes.HasPrefix(blob, []byte(tokenEnvelopeMagic)), "envelope must start with the %q magic", tokenEnvelopeMagic)
	assert.Greater(t, len(blob), len(tokenEnvelopeMagic)+tokenNonceSize, "envelope must carry a nonce and a ciphertext")

	// It still round-trips through the one plaintext path.
	got, err := d.GetToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, key, got.APIKey)
}

func TestSaveToken_UsesAFreshNonceForEveryWrite(t *testing.T) {
	d, _, _ := openFileDB(t)
	ctx := context.Background()

	seen := map[string]bool{}
	for range 25 {
		// The same source and the same key every time: only the nonce can differ.
		require.NoError(t, d.SaveToken(ctx, "nexusmods", "identical-key"))
		blob := rawTokenBlob(t, d, "nexusmods")
		nonce := blob[len(tokenEnvelopeMagic) : len(tokenEnvelopeMagic)+tokenNonceSize]
		assert.False(t, seen[string(nonce)], "nonce reused across writes")
		seen[string(nonce)] = true
	}
	assert.Len(t, seen, 25)
}

func TestOpenToken_WrongKeyFails(t *testing.T) {
	right := mustKey(t)
	wrong := mustKey(t)

	blob, err := sealToken(right, "nexusmods", "secret")
	require.NoError(t, err)

	_, err = openToken(wrong, "nexusmods", blob)
	require.Error(t, err, "a ciphertext must not open under a different key")
}

func TestOpenToken_AADMismatchFails(t *testing.T) {
	key := mustKey(t)

	blob, err := sealToken(key, "nexusmods", "secret")
	require.NoError(t, err)

	// The source id is the AAD, so a row copied to another source must fail.
	_, err = openToken(key, "curseforge", blob)
	require.Error(t, err, "a ciphertext copied to another source must not open")
}

func TestOpenToken_CorruptCiphertextFails(t *testing.T) {
	key := mustKey(t)
	blob, err := sealToken(key, "nexusmods", "secret")
	require.NoError(t, err)

	blob[len(blob)-1] ^= 0xff
	_, err = openToken(key, "nexusmods", blob)
	require.Error(t, err)
}

func TestSealToken_RoundTripsEveryShape(t *testing.T) {
	key := mustKey(t)
	for _, plain := range []string{"", "a", "ünïcödé-key-🔑", string(bytes.Repeat([]byte("x"), 4096))} {
		blob, err := sealToken(key, "src", plain)
		require.NoError(t, err)
		got, err := openToken(key, "src", blob)
		require.NoError(t, err)
		assert.Equal(t, plain, got)
	}
}

func TestTokenFingerprint_IsTheFirst8HexOfSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("some-api-key"))
	assert.Equal(t, hex.EncodeToString(sum[:])[:8], TokenFingerprint("some-api-key"))
	assert.Empty(t, TokenFingerprint(""), "an empty key has no fingerprint to report")
}

func TestListTokens_ReportsPresenceAndFingerprintButNeverThePlaintext(t *testing.T) {
	d, _, _ := openFileDB(t)
	ctx := context.Background()

	require.NoError(t, d.SaveToken(ctx, "nexusmods", "key-a"))
	require.NoError(t, d.SaveToken(ctx, "ghost-repo", "key-b"))

	infos, err := d.ListTokens(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 2)

	assert.Equal(t, "ghost-repo", infos[0].SourceID)
	assert.Equal(t, TokenFingerprint("key-b"), infos[0].Fingerprint)
	assert.True(t, infos[0].Readable)
	assert.False(t, infos[0].CreatedAt.IsZero())
	assert.False(t, infos[0].UpdatedAt.IsZero())

	assert.Equal(t, "nexusmods", infos[1].SourceID)
	assert.Equal(t, TokenFingerprint("key-a"), infos[1].Fingerprint)
}

func TestListTokens_AnUndecryptableRowDoesNotTakeDownTheOthers(t *testing.T) {
	d, _, _ := openFileDB(t)
	ctx := context.Background()

	require.NoError(t, d.SaveToken(ctx, "good-repo", "key-good"))
	require.NoError(t, d.SaveToken(ctx, "bad-repo", "key-bad"))

	// Corrupt exactly one row's ciphertext.
	blob := rawTokenBlob(t, d, "bad-repo")
	blob[len(blob)-1] ^= 0xff
	_, err := d.ExecContext(ctx, "UPDATE auth_tokens SET token_data = ? WHERE source_id = ?", blob, "bad-repo")
	require.NoError(t, err)

	infos, err := d.ListTokens(ctx)
	require.NoError(t, err, "one bad row must not fail the whole listing")
	require.Len(t, infos, 2)

	byID := map[string]TokenInfo{}
	for _, i := range infos {
		byID[i.SourceID] = i
	}
	assert.False(t, byID["bad-repo"].Readable)
	assert.Empty(t, byID["bad-repo"].Fingerprint)
	assert.True(t, byID["good-repo"].Readable)
	assert.Equal(t, TokenFingerprint("key-good"), byID["good-repo"].Fingerprint)

	// GetToken on that same row IS an error - it is the path that needs the key.
	_, err = d.GetToken(ctx, "bad-repo")
	var keyErr *KeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, KeyUndecryptable, keyErr.Reason)
	assert.Equal(t, "bad-repo", keyErr.SourceID)
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	key, err := newTokenKey()
	require.NoError(t, err)
	require.Len(t, key, tokenKeySize)
	return key
}

func TestMemoryDatabaseUsesAnEphemeralKeyAndNeverTouchesDisk(t *testing.T) {
	home := sandboxHome(t)
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	ctx := context.Background()
	require.NoError(t, d.SaveToken(ctx, "nexusmods", "in-memory-key"))
	got, err := d.GetToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "in-memory-key", got.APIKey)

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotEqual(t, TokenKeyFileName, e.Name(), "an in-memory database must not write a key file")
	}
}
