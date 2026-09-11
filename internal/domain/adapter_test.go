package domain_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// TestValidAdapterName is internal/adapter's own ValidName table, moved
// here with it (#411, M7): the syntax rule belongs beside
// ErrInvalidAdapter, so internal/storage/config can enforce it without
// importing the seam.
func TestValidAdapterName(t *testing.T) {
	for _, ok := range []string{"generic-files", "icarus", "bepinex", "unity-loader-2"} {
		assert.True(t, domain.ValidAdapterName(ok), "%q should be a valid adapter name", ok)
	}
	for _, bad := range []string{"", "Icarus", "generic files", "-icarus", "icarus-", "generic--files", "icarus/1"} {
		assert.False(t, domain.ValidAdapterName(bad), "%q should not be a valid adapter name", bad)
	}
}
