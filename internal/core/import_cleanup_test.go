package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #310 item 2: GetEffectiveLinkMethod sits between the cache write and the
// conflict gate, and its hard-error return skipped the same cleanup the
// refusal does - leaking the entry this call created.
func TestApplyImportArchive_LinkMethodFailure_LeavesNoCacheEntry(t *testing.T) {
	svc, game, archiveB := setupImportArchiveConflict(t)
	opts := core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1", Force: true}

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archiveB, opts)
	require.NoError(t, err)

	// An unrecognised link_method is GetEffectiveLinkMethod's one hard
	// error (domain.ErrInvalidLinkMethod); every other profile read failure
	// falls back to the game's method.
	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(profilePath), 0o755))
	require.NoError(t, os.WriteFile(profilePath,
		[]byte("name: default\ngame_id: g1\nlink_method: teleport\nmods: []\n"), 0o644))

	_, err = svc.ApplyImportArchive(context.Background(), game, "default", plan, opts, nil)
	require.Error(t, err)

	assert.False(t, svc.GetGameCache(game).Exists("g1", "acme-source", "B1", "1.0"),
		"the hard-error path must discard the entry this call created, like the refusal does")
}
