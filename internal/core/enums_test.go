package core_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
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
		var bad core.UpdateStatus
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
}
