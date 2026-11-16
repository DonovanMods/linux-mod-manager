package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSchemaService opens a Service over its own data directory and returns
// it with that directory's database path.
func newSchemaService(t *testing.T, lockPath string) (*Service, string) {
	t.Helper()
	dataDir := t.TempDir()
	svc, err := NewService(ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(), OpLockPath: lockPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, filepath.Join(dataDir, "lmm.db")
}

// migrateAsNewerLMM is #648's other process: a newer lmm opening the same
// database file through a handle of its own and recording a migration this
// binary does not know. It returns the version it recorded.
func migrateAsNewerLMM(t *testing.T, path string) int {
	t.Helper()
	other, err := db.New(path)
	require.NoError(t, err)
	version := other.SupportedSchemaVersion() + 1
	_, err = other.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version)
	require.NoError(t, err)
	require.NoError(t, other.Close())
	return version
}

// TestBeginOp_RefusesOnceANewerLMMMigratedTheDatabase is #648: a running
// lmm (serve, or a CLI command that opened before the migration) used to
// carry on writing to a schema it does not know - an update-from-file then
// failed deep in a deployed_files upsert. Every mutation now re-checks the
// schema as it takes the slot and refuses with the open's own refusal,
// naming the remedy, before it writes anything.
func TestBeginOp_RefusesOnceANewerLMMMigratedTheDatabase(t *testing.T) {
	svc, path := newSchemaService(t, "")
	ctx := context.Background()

	release, err := svc.beginOp(ctx)
	require.NoError(t, err, "the schema this lmm opened is current")
	release()

	newer := migrateAsNewerLMM(t, path)

	_, err = svc.beginOp(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSchemaTooNew, "the same error identity an open refuses with")
	assert.ErrorIs(t, err, db.ErrSchemaTooNew)
	var upgraded *SchemaUpgradedError
	require.ErrorAs(t, err, &upgraded)
	assert.Equal(t, newer, upgraded.Version)
	assert.Equal(t, newer-1, upgraded.Supported)
	assert.Contains(t, err.Error(), "another, newer lmm upgraded the database")
	assert.Contains(t, err.Error(), "restart this lmm")
	assert.Contains(t, err.Error(), "lmm service install")

	// The refusal is the gate's, not a mutation's half-run: the slot is
	// free again, so a later refusal is refused the same way, not hung.
	_, err = svc.tryBeginOp(ctx)
	assert.ErrorIs(t, err, ErrSchemaTooNew)

	// And an exported mutation surfaces it unchanged, writing nothing.
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	err = svc.SaveGame(ctx, game)
	assert.ErrorIs(t, err, ErrSchemaTooNew)
	_, ok := svc.game("g1")
	assert.False(t, ok, "a refused mutation writes nothing")
}

// TestBeginOp_SchemaIsCheckedAfterTheLockWait: a CLI command that waited
// on the cross-process lock while the newer lmm ran is the case the lock
// hides - the check belongs after the wait, not before it.
func TestBeginOp_SchemaIsCheckedAfterTheLockWait(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	holder, _ := newSchemaService(t, lockPath)
	waiter, path := newSchemaService(t, lockPath)
	ctx := context.Background()

	release, err := holder.beginOp(ctx)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		r, err := waiter.beginOp(ctx)
		if r != nil {
			r()
		}
		done <- err
	}()

	// The newer lmm migrates while the waiter waits, then the holder ends.
	time.Sleep(100 * time.Millisecond)
	migrateAsNewerLMM(t, path)
	release()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrSchemaTooNew)
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting mutation never returned")
	}
}

// TestCheckSchema_QueriesAskTheSameQuestion: the exported check is what a
// long-running frontend asks before answering a read (serve's /api/v1
// gate), so it refuses with the same error - and stays refused, since a
// schema never goes back.
func TestCheckSchema_QueriesAskTheSameQuestion(t *testing.T) {
	svc, path := newSchemaService(t, "")
	ctx := context.Background()
	require.NoError(t, svc.CheckSchema(ctx))

	migrateAsNewerLMM(t, path)

	first := svc.CheckSchema(ctx)
	require.ErrorIs(t, first, ErrSchemaTooNew)
	second := svc.CheckSchema(ctx)
	require.ErrorIs(t, second, ErrSchemaTooNew)
	var upgraded *SchemaUpgradedError
	require.ErrorAs(t, second, &upgraded)
}

// TestSchemaUpgradedError_Details pins the refusal's data: a frontend
// tells it from every other failure by its code, and shows both versions.
func TestSchemaUpgradedError_Details(t *testing.T) {
	err := newSchemaUpgradedError(&db.SchemaTooNewError{Path: "/x/lmm.db", Version: 29, Supported: 28})
	assert.Same(t, err, err.Details())
	assert.Equal(t, "schema_too_new", err.Code)
	assert.Equal(t, SchemaTooNewCode, err.Code)
	assert.Equal(t, 29, err.Version)
	assert.Equal(t, 28, err.Supported)
	assert.ErrorIs(t, err, ErrSchemaTooNew)
}
