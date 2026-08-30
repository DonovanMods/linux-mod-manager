package core

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelledContext returns a context that is already cancelled.
func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(cancel)
	return ctx
}

// TestEnsureProfileExists_CancelledRead_IsReportedNotTreatedAsExisting is the
// class-(B) shape test for v2 Phase 3 Ruling 16: a pre-write profile read
// that could not answer "does this profile exist?" must be returned, not
// mapped onto "the profile is fine". One test covers the shape; the sibling
// site (import_archive.go) now calls this same helper.
//
// Against 85a3ed0 this fails at the require.Error: ensureProfileExists
// returned nil for every error that was not ErrProfileNotFound, so its
// callers went straight on to a profile write with no answer in hand.
func TestEnsureProfileExists_CancelledRead_IsReportedNotTreatedAsExisting(t *testing.T) {
	configDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	pm := NewProfileManager(configDir, database)

	err = ensureProfileExists(cancelledContext(t), pm, "skyrim-se", "brand-new")
	require.Error(t, err, "a read that could not answer must not report success")
	assert.ErrorIs(t, err, context.Canceled)

	// Nothing was created: the profile still does not exist, so an
	// uncancelled Get still reports ErrProfileNotFound.
	_, err = pm.Get(context.Background(), "skyrim-se", "brand-new")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}
