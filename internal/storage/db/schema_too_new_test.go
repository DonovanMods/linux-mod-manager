package db_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// seedNewerSchema leaves a database at path whose recorded schema version
// is one past what this lmm knows - what a newer lmm's migration leaves for
// an older binary (a package downgrade) to open. It returns this lmm's own
// version.
func seedNewerSchema(t *testing.T, path string) int {
	t.Helper()
	seed, err := db.New(path)
	require.NoError(t, err)
	var current int
	require.NoError(t, seed.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&current))
	_, err = seed.Exec("INSERT INTO schema_migrations (version) VALUES (?)", current+1)
	require.NoError(t, err)
	require.NoError(t, seed.Close())
	return current
}

// A database a newer lmm migrated may hold a schema this binary cannot read
// (#516's review: a renamed column fails every query). Opening it is
// refused up front, naming both versions and what to do, rather than
// carrying on into "no such column" on the first installed-mod read.
func TestOpen_RefusesASchemaNewerThanThisLMM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")
	current := seedNewerSchema(t, path)

	d, err := db.New(path)
	require.Error(t, err)
	assert.Nil(t, d)

	var tooNew *db.SchemaTooNewError
	require.ErrorAs(t, err, &tooNew)
	assert.Equal(t, current+1, tooNew.Version)
	assert.Equal(t, current, tooNew.Supported)
	assert.Equal(t, path, tooNew.Path)
	assert.True(t, errors.Is(err, db.ErrSchemaTooNew))
	assert.Contains(t, err.Error(), "written by a newer lmm")
	assert.Contains(t, err.Error(), "upgrade lmm")
	assert.Contains(t, err.Error(), path)
}

// The current schema still opens: the refusal is for a newer one only.
func TestOpen_OpensTheCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")
	seed, err := db.New(path)
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	d, err := db.New(path)
	require.NoError(t, err)
	require.NoError(t, d.Close())
}
