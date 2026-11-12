package source

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #601: a free account's refusal is its own class, and never a manual
// download - core records every ErrManualDownload per mod, for everyone.
func TestAccountRestrictedError_IsItsOwnClassNotAManualDownload(t *testing.T) {
	err := fmt.Errorf("getting download links: %w", &AccountRestrictedError{Reason: "premium only"})

	assert.True(t, errors.Is(err, ErrAccountRestricted))
	assert.False(t, errors.Is(err, ErrManualDownload), "an account refusal must never read as a per-mod manual download")
	var manual *ManualDownloadError
	assert.False(t, errors.As(err, &manual))
	assert.Equal(t, "getting download links: premium only", err.Error())
}

func TestAccountRestrictedError_EmptyReasonFallsBackToTheClass(t *testing.T) {
	assert.Equal(t, ErrAccountRestricted.Error(), (&AccountRestrictedError{}).Error())
}

func TestTier_Premium(t *testing.T) {
	assert.True(t, TierPremium.Premium())
	assert.False(t, TierFree.Premium())
	assert.False(t, Tier("").Premium())
}
