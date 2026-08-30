package core

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPlanTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func seedPlanTestMod(t *testing.T, svc *Service, gameID, profileName, sourceID, modID, version string, enabled bool) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       modID,
			SourceID: sourceID,
			Name:     modID,
			Version:  version,
			GameID:   gameID,
		},
		ProfileName:  profileName,
		Enabled:      enabled,
		UpdatePolicy: domain.UpdateNotify,
	}))
}

// TestCheckPlanFresh_EqualSnapshotsPass pins that a plan whose recorded
// snapshot still matches the current installed-mod set is not stale.
func TestCheckPlanFresh_EqualSnapshotsPass(t *testing.T) {
	svc := newPlanTestService(t)
	ctx := context.Background()
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)

	want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
	require.NoError(t, err)

	assert.NoError(t, svc.checkPlanFresh(ctx, "g1", "default", want))
}

// TestCheckPlanFresh_VersionBumpIsStale pins that a version change between
// plan and apply is caught.
func TestCheckPlanFresh_VersionBumpIsStale(t *testing.T) {
	svc := newPlanTestService(t)
	ctx := context.Background()
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)

	want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
	require.NoError(t, err)

	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "2.0", true)

	err = svc.checkPlanFresh(ctx, "g1", "default", want)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStalePlan))
}

// TestCheckPlanFresh_EnableFlipIsStale pins that an enable/disable flip
// between plan and apply is caught.
func TestCheckPlanFresh_EnableFlipIsStale(t *testing.T) {
	svc := newPlanTestService(t)
	ctx := context.Background()
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)

	want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
	require.NoError(t, err)

	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", false)

	err = svc.checkPlanFresh(ctx, "g1", "default", want)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStalePlan))
}

// TestCheckPlanFresh_AddedModIsStale pins that a mod installed after the
// plan was computed is caught.
func TestCheckPlanFresh_AddedModIsStale(t *testing.T) {
	svc := newPlanTestService(t)
	ctx := context.Background()
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)

	want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
	require.NoError(t, err)

	seedPlanTestMod(t, svc, "g1", "default", "src", "mod2", "1.0", true)

	err = svc.checkPlanFresh(ctx, "g1", "default", want)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStalePlan))
}

// TestCheckPlanFresh_RemovedModIsStale pins that a mod uninstalled after the
// plan was computed is caught.
func TestCheckPlanFresh_RemovedModIsStale(t *testing.T) {
	svc := newPlanTestService(t)
	ctx := context.Background()
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod2", "1.0", true)

	want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
	require.NoError(t, err)

	require.NoError(t, svc.DeleteInstalledMod(ctx, "src", "mod2", "g1", "default"))

	err = svc.checkPlanFresh(ctx, "g1", "default", want)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStalePlan))
}

// TestMergedArtifactAction_TextRoundTrip pins the typed enum's wire
// behaviour: MarshalText/UnmarshalText round-trip both known values, and an
// unknown value like a mistyped "resyncc" is rejected rather than silently
// accepted (the defect a bare string const would allow).
func TestMergedArtifactAction_TextRoundTrip(t *testing.T) {
	for _, a := range []MergedArtifactAction{MergedArtifactResync, MergedArtifactRemove} {
		b, err := a.MarshalText()
		require.NoError(t, err)
		assert.Equal(t, a.String(), string(b))

		var back MergedArtifactAction
		require.NoError(t, back.UnmarshalText(b))
		assert.Equal(t, a, back)
	}

	var bad MergedArtifactAction
	assert.Error(t, bad.UnmarshalText([]byte("resyncc")))
}
