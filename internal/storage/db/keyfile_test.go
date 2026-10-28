package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenKey_CreatedOnFirstNeedAt0600(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	// Opening a database with no tokens must NOT create a key: a user who
	// never logs in has no secret to protect.
	_, err = os.Stat(keyPath)
	require.ErrorIs(t, err, os.ErrNotExist, "the key file must only be created on first need")

	require.NoError(t, d.SaveToken(context.Background(), "nexusmods", "first-key"))

	info, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	assert.Equal(t, int64(tokenKeySize), info.Size())
}

func TestTokenKey_RefusesALooselyPermissionedKeyFile(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)

	key, err := newTokenKey()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath, key, 0644))

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err, "a loose key file must not stop the database opening")
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	err = d.SaveToken(context.Background(), "nexusmods", "some-key")
	var keyErr *KeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, KeyBadPermissions, keyErr.Reason)
	assert.Equal(t, keyPath, keyErr.Path)
	assert.Contains(t, err.Error(), keyPath, "the error must name the file")
	assert.Contains(t, err.Error(), "chmod 600", "the error must name the fix")
}

func TestTokenKey_RefusesAMalformedKeyFile(t *testing.T) {
	dir := sandboxHome(t)
	keyPath := filepath.Join(dir, TokenKeyFileName)
	require.NoError(t, os.WriteFile(keyPath, []byte("too short"), 0600))

	d, err := OpenWithOptions(filepath.Join(dir, "lmm.db"), Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	err = d.SaveToken(context.Background(), "nexusmods", "some-key")
	var keyErr *KeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, KeyMalformed, keyErr.Reason)
}

func TestTokenKey_MissingKeyWithEncryptedRowsIsATypedError(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	require.NoError(t, d.SaveToken(ctx, "nexusmods", "stored-key"))
	require.NoError(t, d.Close())

	// The user deleted the key file but kept the database.
	require.NoError(t, os.Remove(keyPath))

	reopened, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err, "the database must still open without its token key")
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	_, err = reopened.GetToken(ctx, "nexusmods")
	var keyErr *KeyError
	require.ErrorAs(t, err, &keyErr)
	assert.Equal(t, KeyMissing, keyErr.Reason)
	assert.Equal(t, keyPath, keyErr.Path)

	_, err = reopened.ListTokens(ctx)
	require.ErrorAs(t, err, &keyErr, "listing must report the missing key rather than pretend the rows are absent")
	assert.Equal(t, KeyMissing, keyErr.Reason)

	// Nothing else about the database is affected.
	has, err := reopened.HasToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.True(t, has)
}

func TestTokenKey_ReusedAcrossReopens(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")
	keyPath := filepath.Join(dir, TokenKeyFileName)
	ctx := context.Background()

	d, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	require.NoError(t, d.SaveToken(ctx, "nexusmods", "durable-key"))
	require.NoError(t, d.Close())

	reopened, err := OpenWithOptions(dbPath, Options{KeyPath: keyPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	got, err := reopened.GetToken(ctx, "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "durable-key", got.APIKey)
}

// TestTokenKey_DefaultsNextToTheDatabase pins the fallback db.New relies on:
// with no KeyPath configured, the key lives beside the database file, so a
// caller that only knows the database path still gets encryption.
func TestTokenKey_DefaultsNextToTheDatabase(t *testing.T) {
	dir := sandboxHome(t)
	dbPath := filepath.Join(dir, "lmm.db")

	d, err := New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	require.NoError(t, d.SaveToken(context.Background(), "nexusmods", "beside-key"))
	info, err := os.Stat(filepath.Join(dir, TokenKeyFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
