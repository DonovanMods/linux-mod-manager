package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNew_PragmasApplyToEveryPooledConnection pins #271: foreign_keys is a
// per-connection pragma, so it must be set through the DSN (which runs on
// every new connection), not by one Exec on whichever connection the pool
// happened to hand out. Holding a Rows open forces a second connection.
func TestNew_PragmasApplyToEveryPooledConnection(t *testing.T) {
	d, err := New(filepath.Join(t.TempDir(), "lmm.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()

	rows, err := d.QueryContext(ctx, "SELECT name FROM sqlite_master")
	require.NoError(t, err)
	defer rows.Close() // keep connection #1 busy for the duration

	var fk int
	require.NoError(t, d.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk)) // connection #2
	require.Equal(t, 1, fk, "foreign_keys must be ON for a second pooled connection")

	var timeout int
	require.NoError(t, d.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout))
	require.Equal(t, 5000, timeout)

	var mode string
	require.NoError(t, d.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)
}

// TestNew_MemoryDatabaseIsSingleConnection pins that ":memory:" does not
// silently give each pooled connection its own empty database.
func TestNew_MemoryDatabaseIsSingleConnection(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	require.Equal(t, 1, d.Stats().MaxOpenConnections)

	var n int
	require.NoError(t, d.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&n))
	require.Greater(t, n, 0, "migrations must be visible on the (only) connection")
}

// TestQueryContext_HonoursCancellation pins that a cancelled ctx reaches SQLite.
func TestQueryContext_HonoursCancellation(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = d.GetInstalledMods(ctx, "g", "p")
	require.ErrorIs(t, err, context.Canceled)
}
