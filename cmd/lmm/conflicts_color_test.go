package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoConflicts_Stale_ColorPath: the "(stale — redeploy to apply)" suffix
// is a pending/attention-worthy state (yellow, per the repo's palette), and
// is a plain non-tabular line so no tabwriter alignment concern applies.
func TestDoConflicts_Stale_ColorPath(t *testing.T) {
	svc, game := setupConflictsTest(t)
	seedTwinConflictFixture(t, svc, game)
	require.NoError(t, svc.NewProfileManager().ReorderMods(context.Background(), game.ID, "default", []domain.ModReference{
		{SourceID: "src", ModID: "b", Version: "1.0"},
		{SourceID: "src", ModID: "a", Version: "1.0"},
	}))

	resetColorFlags(t)
	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return doConflicts(context.Background(), svc, game)
	})

	assert.Contains(t, out, "Winner: Mod A "+colorYellow("(stale — redeploy to apply)"))
}
