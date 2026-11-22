package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// TestUnchangedSummary (#672): the one sentence both frontends say about
// mods answered from their record, for the update check and verify alike.
func TestUnchangedSummary(t *testing.T) {
	assert.Equal(t, "340 mods unchanged since the last check, not asked again", core.UnchangedSummary(340))
	assert.Equal(t, "1 mod unchanged since the last check, not asked again", core.UnchangedSummary(1))
	assert.Empty(t, core.UnchangedSummary(0))
}
