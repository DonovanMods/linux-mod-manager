package core_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
)

// refusalFixture is the mod/profile/ref triple both constructors are
// exercised against - a mod whose source, ID and name are all distinct, so a
// remedy that interpolated the wrong field would be visible.
func refusalFixture() (domain.Mod, string, *domain.ModReference) {
	return domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One"},
		"default",
		&domain.ModReference{Version: "1.0"}
}

// TestLockedRefRefusalError_MoveOrUnlockWording pins the two-remedy variant
// byte-for-byte: the wording used wherever MOVING the lock actually unblocks
// the refused operation (a version-mismatch gate).
func TestLockedRefRefusalError_MoveOrUnlockWording(t *testing.T) {
	mod, profile, ref := refusalFixture()

	err := core.LockedRefRefusalError(mod, profile, ref)

	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Equal(t, "mod is locked: Mod One is locked at v1.0 in profile default - move the lock with 'lmm mod lock -s test-src -p default mod1 <version>' or unlock with 'lmm mod unlock -s test-src -p default mod1'", err.Error())
}

// TestLockedRefUnlockOnlyRefusalError_UnlockOnlyWording pins the unlock-only
// variant (unit Q review, I1): the gates that refuse on Locked ALONE -
// regardless of version - cannot be unblocked by moving the lock, so their
// refusal must not offer that remedy at all.
func TestLockedRefUnlockOnlyRefusalError_UnlockOnlyWording(t *testing.T) {
	mod, profile, ref := refusalFixture()

	err := core.LockedRefUnlockOnlyRefusalError(mod, profile, ref)

	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Equal(t, "mod is locked: Mod One is locked at v1.0 in profile default - unlock with 'lmm mod unlock -s test-src -p default mod1' first", err.Error())
	assert.NotContains(t, err.Error(), "move the lock",
		"unit Q review I1: a gate that refuses on Locked alone must never suggest moving the lock")
}

// TestLockedRefRefusals_ShareOneSentenceBuilder guards the Ruling 5
// refinement: one wording per refusal KIND, built by ONE builder - the two
// variants differ ONLY in the remedy clause after the em-dash-less " - "
// separator, never in the "<name> is locked at v<version> in profile
// <profile>" head or in the unlock command they both name.
func TestLockedRefRefusals_ShareOneSentenceBuilder(t *testing.T) {
	mod, profile, ref := refusalFixture()

	const head = "Mod One is locked at v1.0 in profile default - "
	const unlock = "unlock with 'lmm mod unlock -s test-src -p default mod1'"

	both := []error{
		core.LockedRefRefusalError(mod, profile, ref),
		core.LockedRefUnlockOnlyRefusalError(mod, profile, ref),
	}
	for _, err := range both {
		sentence := strings.TrimPrefix(err.Error(), core.ErrModLocked.Error()+": ")
		assert.True(t, strings.HasPrefix(sentence, head), "shared head missing from %q", sentence)
		assert.Contains(t, sentence, unlock, "shared unlock remedy missing from %q", sentence)
		assert.True(t, errors.Is(err, core.ErrModLocked))
	}
}
