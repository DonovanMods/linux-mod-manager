package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveToken(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	err := db.SaveToken("nexusmods", "test-api-key-123")
	require.NoError(t, err)

	// Verify it was saved
	token, err := db.GetToken("nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "nexusmods", token.SourceID)
	assert.Equal(t, "test-api-key-123", token.APIKey)
	assert.False(t, token.UpdatedAt.IsZero())
}

func TestSaveToken_Update(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// Save initial token
	err := db.SaveToken("nexusmods", "old-key")
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond) // Ensure time difference

	// Update with new token
	err = db.SaveToken("nexusmods", "new-key")
	require.NoError(t, err)

	// Verify update
	token, err := db.GetToken("nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "new-key", token.APIKey)
}

func TestGetToken_NotFound(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	token, err := db.GetToken("nonexistent")
	assert.NoError(t, err)
	assert.Nil(t, token)
}

func TestDeleteToken(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// Save a token
	err := db.SaveToken("nexusmods", "test-key")
	require.NoError(t, err)

	// Delete it
	err = db.DeleteToken("nexusmods")
	require.NoError(t, err)

	// Verify deletion
	token, err := db.GetToken("nexusmods")
	assert.NoError(t, err)
	assert.Nil(t, token)
}

func TestDeleteToken_NotFound(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// Deleting non-existent token should not error
	err := db.DeleteToken("nonexistent")
	assert.NoError(t, err)
}

func TestHasToken(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// No token initially
	has, err := db.HasToken("nexusmods")
	require.NoError(t, err)
	assert.False(t, has)

	// Save a token
	err = db.SaveToken("nexusmods", "test-key")
	require.NoError(t, err)

	// Now has token
	has, err = db.HasToken("nexusmods")
	require.NoError(t, err)
	assert.True(t, has)

	// Delete token
	err = db.DeleteToken("nexusmods")
	require.NoError(t, err)

	// No longer has token
	has, err = db.HasToken("nexusmods")
	require.NoError(t, err)
	assert.False(t, has)
}

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(":memory:")
	require.NoError(t, err)
	return db
}
