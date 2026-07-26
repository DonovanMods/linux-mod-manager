package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
)

func mod(id, source string, policy domain.UpdatePolicy) domain.InstalledMod {
	return domain.InstalledMod{
		Mod:          domain.Mod{ID: id, SourceID: source},
		UpdatePolicy: policy,
	}
}

// TestCountUpdateSkips_MatchesFilter pins the invariant the counts exist for:
// Total must equal exactly how many mods CheckUpdates' own filter drops, or the
// reported "N skipped" would contradict what actually happened.
func TestCountUpdateSkips_MatchesFilter(t *testing.T) {
	installed := []domain.InstalledMod{
		mod("a", "nexusmods", domain.UpdateNotify),
		mod("b", "nexusmods", domain.UpdatePinned),
		mod("c", domain.SourceLocal, domain.UpdateNotify),
		mod("d", domain.SourceLocal, domain.UpdatePinned), // both reasons
		mod("e", "curseforge", domain.UpdateAuto),
	}

	skips := CountUpdateSkips(installed)
	assert.Equal(t, 2, skips.Pinned, "b and d")
	assert.Equal(t, 1, skips.Local, "c only — d is already counted as pinned")

	filtered := 0
	for _, m := range installed {
		if !UpdateCheckable(m) {
			filtered++
		}
	}
	assert.Equal(t, filtered, skips.Total(), "counts must match the filter exactly")
	assert.LessOrEqual(t, skips.Total(), len(installed), "a mod must never be counted twice")
}

func TestUpdateCheckable(t *testing.T) {
	assert.True(t, UpdateCheckable(mod("a", "nexusmods", domain.UpdateNotify)))
	assert.True(t, UpdateCheckable(mod("a", "nexusmods", domain.UpdateAuto)))
	assert.False(t, UpdateCheckable(mod("a", "nexusmods", domain.UpdatePinned)))
	assert.False(t, UpdateCheckable(mod("a", domain.SourceLocal, domain.UpdateNotify)))
}
