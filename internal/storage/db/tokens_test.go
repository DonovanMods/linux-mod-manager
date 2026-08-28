package db

import (
	"context"
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

	err := db.SaveToken(context.Background(), "nexusmods", "test-api-key-123")
	require.NoError(t, err)

	// Verify it was saved
	token, err := db.GetToken(context.Background(), "nexusmods")
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
	err := db.SaveToken(context.Background(), "nexusmods", "old-key")
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond) // Ensure time difference

	// Update with new token
	err = db.SaveToken(context.Background(), "nexusmods", "new-key")
	require.NoError(t, err)

	// Verify update
	token, err := db.GetToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "new-key", token.APIKey)
}

func TestGetToken_NotFound(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	token, err := db.GetToken(context.Background(), "nonexistent")
	assert.NoError(t, err)
	assert.Nil(t, token)
}

func TestDeleteToken(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// Save a token
	err := db.SaveToken(context.Background(), "nexusmods", "test-key")
	require.NoError(t, err)

	// Delete it
	err = db.DeleteToken(context.Background(), "nexusmods")
	require.NoError(t, err)

	// Verify deletion
	token, err := db.GetToken(context.Background(), "nexusmods")
	assert.NoError(t, err)
	assert.Nil(t, token)
}

func TestDeleteToken_NotFound(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// Deleting non-existent token should not error
	err := db.DeleteToken(context.Background(), "nonexistent")
	assert.NoError(t, err)
}

func TestHasToken(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// No token initially
	has, err := db.HasToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	assert.False(t, has)

	// Save a token
	err = db.SaveToken(context.Background(), "nexusmods", "test-key")
	require.NoError(t, err)

	// Now has token
	has, err = db.HasToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	assert.True(t, has)

	// Delete token
	err = db.DeleteToken(context.Background(), "nexusmods")
	require.NoError(t, err)

	// No longer has token
	has, err = db.HasToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	assert.False(t, has)
}

func TestListTokens(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// No tokens initially.
	tokens, err := db.ListTokens(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tokens)

	require.NoError(t, db.SaveToken(context.Background(), "nexusmods", "key-a"))
	require.NoError(t, db.SaveToken(context.Background(), "ghost-repo", "key-b"))

	tokens, err = db.ListTokens(context.Background())
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	// Ordered by source_id so callers get deterministic output.
	assert.Equal(t, "ghost-repo", tokens[0].SourceID)
	assert.Equal(t, "key-b", tokens[0].APIKey)
	assert.Equal(t, "nexusmods", tokens[1].SourceID)
	assert.Equal(t, "key-a", tokens[1].APIKey)
}

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(":memory:")
	require.NoError(t, err)
	return db
}
