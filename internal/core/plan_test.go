package core

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	svc := newBareService(t)
	ctx := context.Background()
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)

	want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
	require.NoError(t, err)

	assert.NoError(t, svc.checkPlanFresh(ctx, "g1", "default", want))
}

// TestCheckPlanFresh_VersionBumpIsStale pins that a version change between
// plan and apply is caught.
func TestCheckPlanFresh_VersionBumpIsStale(t *testing.T) {
	svc := newBareService(t)
	ctx := context.Background()
	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)

	want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
	require.NoError(t, err)

	seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "2.0", true)

	err = svc.checkPlanFresh(ctx, "g1", "default", want)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStalePlan))
}

// TestCheckPlanFresh_ChangedInstallIsStale pins that a change to the
// installed set between plan and apply is caught: an enable/disable flip, a
// mod installed after the plan was computed, and a mod uninstalled after it.
// Every row starts from mod1 and mod2 installed and enabled.
func TestCheckPlanFresh_ChangedInstallIsStale(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, svc *Service)
	}{
		{"enable flip", func(t *testing.T, svc *Service) {
			seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", false)
		}},
		{"added mod", func(t *testing.T, svc *Service) {
			seedPlanTestMod(t, svc, "g1", "default", "src", "mod3", "1.0", true)
		}},
		{"removed mod", func(t *testing.T, svc *Service) {
			require.NoError(t, svc.DeleteInstalledMod(context.Background(), "src", "mod2", "g1", "default"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newBareService(t)
			ctx := context.Background()
			seedPlanTestMod(t, svc, "g1", "default", "src", "mod1", "1.0", true)
			seedPlanTestMod(t, svc, "g1", "default", "src", "mod2", "1.0", true)

			want, err := svc.currentInstalledSnapshot(ctx, "g1", "default")
			require.NoError(t, err)

			tt.mutate(t, svc)

			err = svc.checkPlanFresh(ctx, "g1", "default", want)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrStalePlan))
		})
	}
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
