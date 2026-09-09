package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #311: ProfileManager.UpsertMod's lock refusal was a fifth hand-worded
// sentence, and #294 promoted the apply/sync/switch warning that carries it
// from a -v note to an unconditional line - so its drift from the canonical
// wording (`profile %q` where every other refusal writes `profile %s`)
// became user-visible. It now goes through core.LockedRefRefusalError, the
// constructor for gates that refuse only at a DIFFERENT version, keeping
// its own "refusing to record vX" datum.
func TestProfileManager_UpsertMod_LockRefusalUsesTheCanonicalWording(t *testing.T) {
	svc := newFlowsTestService(t)
	pm := svc.NewProfileManager()
	ctx := context.Background()

	_, err := pm.Create(ctx, "g1", "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, "g1", "default", domain.ModReference{SourceID: "src", ModID: "lock1", Version: "1.0"}))
	require.NoError(t, pm.SetModLock(ctx, "g1", "default", "src", "lock1", ""))

	err = pm.UpsertMod(ctx, "g1", "default", domain.ModReference{SourceID: "src", ModID: "lock1", Version: "2.0"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)

	// The canonical sentence the shared constructor builds, plus this
	// gate's own datum - and no `profile "default"` quoting.
	canonical := core.LockedRefRefusalError(
		domain.Mod{ID: "lock1", SourceID: "src", Name: domain.ModKey("src", "lock1")},
		"default",
		&domain.ModReference{Version: "1.0"},
	)
	assert.Contains(t, err.Error(), strings.TrimPrefix(canonical.Error(), "mod is locked: "))
	assert.Contains(t, err.Error(), "refusing to record v2.0")
	assert.NotContains(t, err.Error(), `profile "default"`)
}
