package core_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnumTextRoundTrip(t *testing.T) {
	t.Run("VerifyTier", func(t *testing.T) {
		for _, tier := range []core.VerifyTier{core.VerifyLocal, core.VerifyFull} {
			b, err := tier.MarshalText()
			require.NoError(t, err)
			var back core.VerifyTier
			require.NoError(t, back.UnmarshalText(b))
			assert.Equal(t, tier, back)
		}
		assert.Equal(t, "local", core.VerifyLocal.String())
		assert.Equal(t, "full", core.VerifyFull.String())
		var bad core.VerifyTier
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
	t.Run("VerifyEventKind", func(t *testing.T) {
		for _, k := range []core.VerifyEventKind{
			core.VerifyEvBegin, core.VerifyEvFinding, core.VerifyEvRepairDetail,
			core.VerifyEvSyncWarning, core.VerifyEvVerbose, core.VerifyEvProgress,
		} {
			b, err := k.MarshalText()
			require.NoError(t, err)
			var back core.VerifyEventKind
			require.NoError(t, back.UnmarshalText(b))
			assert.Equal(t, k, back)
		}
		assert.Equal(t, "begin", core.VerifyEvBegin.String())
		assert.Equal(t, "finding", core.VerifyEvFinding.String())
		assert.Equal(t, "repair_detail", core.VerifyEvRepairDetail.String())
		assert.Equal(t, "sync_warning", core.VerifyEvSyncWarning.String())
		assert.Equal(t, "verbose", core.VerifyEvVerbose.String())
		assert.Equal(t, "progress", core.VerifyEvProgress.String())
		var bad core.VerifyEventKind
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
	t.Run("DeployModClass", func(t *testing.T) {
		for _, c := range []core.DeployModClass{core.DeployModIndividual, core.DeployModMerged, core.DeployModRaw} {
			b, err := c.MarshalText()
			require.NoError(t, err)
			var back core.DeployModClass
			require.NoError(t, back.UnmarshalText(b))
			assert.Equal(t, c, back)
		}
		assert.Equal(t, "individual", core.DeployModIndividual.String())
		assert.Equal(t, "merged", core.DeployModMerged.String())
		assert.Equal(t, "raw", core.DeployModRaw.String())
		var bad core.DeployModClass
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
	t.Run("UpdateStatus", func(t *testing.T) {
		for _, s := range []core.UpdateStatus{
			core.UpdateUpdated, core.UpdateUpToDate, core.UpdateSkipped,
			core.UpdateRecompiled, core.UpdateRecompileAvailable, core.UpdateAvailable,
			core.UpdateRolledBack,
		} {
			b, err := s.MarshalText()
			require.NoError(t, err)
			var back core.UpdateStatus
			require.NoError(t, back.UnmarshalText(b))
			assert.Equal(t, s, back)
		}
		assert.Equal(t, "updated", core.UpdateUpdated.String())
		assert.Equal(t, "up_to_date", core.UpdateUpToDate.String())
		assert.Equal(t, "skipped", core.UpdateSkipped.String())
		assert.Equal(t, "recompiled", core.UpdateRecompiled.String())
		assert.Equal(t, "recompile_available", core.UpdateRecompileAvailable.String())
		assert.Equal(t, "available", core.UpdateAvailable.String())
		assert.Equal(t, "rolled_back", core.UpdateRolledBack.String())
		var bad core.UpdateStatus
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
}
