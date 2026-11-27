package db

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryPlan is EXPLAIN QUERY PLAN's detail lines for query, in order.
func queryPlan(t testing.TB, d *DB, query string, args ...any) []string {
	t.Helper()
	rows, err := d.Query("EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return details
}

// TestDeployedFileStates_UsesThePathIndex pins #677's fix: the per-path
// lookup every deployed-file judgement makes (deploy, purge, verify's
// deployed-content pass) searches deployed_files by its path, through the
// path index, without ANALYZE having run - not the primary key's game_id
// prefix, which scanned every row of the game per lookup and made a pass
// over a game's paths quadratic. The ORDER BY comes from the same index, so
// no temporary sort either.
func TestDeployedFileStates_UsesThePathIndex(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })

	plan := queryPlan(t, d, deployedFileStatesQuery, "g", "mod_path", "a/b.txt")
	joined := strings.Join(plan, "\n")
	assert.Contains(t, joined, "SEARCH df USING INDEX idx_deployed_files_path (game_id=? AND root=? AND relative_path=?)", joined)
	assert.NotContains(t, joined, "TEMP B-TREE", joined)
}

// BenchmarkDeployedFileStates measures one path lookup in a game with a
// small and a large deployed_files table (#677): with the path index the
// per-lookup cost stays flat as the game's row count grows; without it,
// it grew with the row count (about 1 ms a lookup at 23k rows).
//
//	go test ./internal/storage/db -run '^$' -bench DeployedFileStates
func BenchmarkDeployedFileStates(b *testing.B) {
	for _, n := range []int{1_000, 20_000} {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			d, err := New(":memory:")
			require.NoError(b, err)
			b.Cleanup(func() { require.NoError(b, d.Close()) })
			seedDeployedFiles(b, d, "g", "default", n)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				path := domain.ModPathFile(fmt.Sprintf("Interface/AddOns/mod%03d/file%05d.lua", (i%n)/100, i%n))
				states, err := d.DeployedFileStates(ctx, "g", path)
				if err != nil || len(states) != 1 {
					b.Fatalf("lookup %s: %d states, %v", path.Rel, len(states), err)
				}
			}
		})
	}
}

// seedDeployedFiles records n deployed files of profile in gameID, in
// one transaction, spread over n/100 mods' directories.
func seedDeployedFiles(tb testing.TB, d *DB, gameID, profile string, n int) {
	tb.Helper()
	tx, err := d.Begin()
	require.NoError(tb, err)
	stmt, err := tx.Prepare(`INSERT INTO deployed_files (game_id, profile_name, root, relative_path, source_id, mod_id, mod_path) VALUES (?, ?, 'mod_path', ?, 'src', ?, '')`)
	require.NoError(tb, err)
	for i := range n {
		_, err := stmt.Exec(gameID, profile, fmt.Sprintf("Interface/AddOns/mod%03d/file%05d.lua", i/100, i), fmt.Sprintf("m%03d", i/100))
		require.NoError(tb, err)
	}
	require.NoError(tb, stmt.Close())
	require.NoError(tb, tx.Commit())
}

// TestMigrateV31_IndexesDeployedFilesByPath covers the migration itself: a
// V30 database, rows and all, gains the path index, and a re-run is a no-op.
func TestMigrateV31_IndexesDeployedFilesByPath(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()
	seedDeployedFiles(t, d, "g", "default", 3)

	// Back to V30: no path index.
	for _, stmt := range []string{
		`DROP INDEX idx_deployed_files_path`,
		`DELETE FROM schema_migrations WHERE version >= 31`,
	} {
		_, err := d.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	indexed := func() bool {
		var n int
		require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_deployed_files_path' AND tbl_name = 'deployed_files'`).Scan(&n))
		return n == 1
	}
	require.False(t, indexed())

	require.NoError(t, d.migrate(ctx))
	assert.True(t, indexed())
	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 31, version)
	states, err := d.DeployedFileStates(ctx, "g", domain.ModPathFile("Interface/AddOns/mod000/file00001.lua"))
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, "m000", states[0].ModID)

	require.NoError(t, migrateV31(ctx, d), "a re-run is a no-op")
	assert.True(t, indexed())
}
