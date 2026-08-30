package domain_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnumTextRoundTrip(t *testing.T) {
	t.Run("LinkMethod", func(t *testing.T) {
		for _, m := range []domain.LinkMethod{domain.LinkSymlink, domain.LinkHardlink, domain.LinkCopy} {
			b, err := m.MarshalText()
			require.NoError(t, err)
			var back domain.LinkMethod
			require.NoError(t, back.UnmarshalText(b))
			assert.Equal(t, m, back)
		}
		assert.Equal(t, "symlink", domain.LinkSymlink.String())
		var bad domain.LinkMethod
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
	t.Run("DeployMode", func(t *testing.T) {
		for _, m := range []domain.DeployMode{domain.DeployExtract, domain.DeployCopy, domain.DeployCompile} {
			b, err := m.MarshalText()
			require.NoError(t, err)
			var back domain.DeployMode
			require.NoError(t, back.UnmarshalText(b))
			assert.Equal(t, m, back)
		}
		assert.Equal(t, "extract", domain.DeployExtract.String())
		assert.Equal(t, "compile", domain.DeployCompile.String())
		var bad domain.DeployMode
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
	t.Run("UpdatePolicy", func(t *testing.T) {
		assert.Equal(t, "notify", domain.UpdateNotify.String())
		assert.Equal(t, "auto", domain.UpdateAuto.String())
		assert.Equal(t, "pinned", domain.UpdatePinned.String())
		for _, p := range []domain.UpdatePolicy{domain.UpdateNotify, domain.UpdateAuto, domain.UpdatePinned} {
			b, err := p.MarshalText()
			require.NoError(t, err)
			var back domain.UpdatePolicy
			require.NoError(t, back.UnmarshalText(b))
			assert.Equal(t, p, back)
		}
		var bad domain.UpdatePolicy
		assert.Error(t, bad.UnmarshalText([]byte("junk")))
	})
}
