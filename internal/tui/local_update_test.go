package tui

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
)

// TestUpdateSkipWarning_ReportsBothReasons: the TUI's update check shares
// core's filter, so skipped mods vanish there too. UpdatesView.Warnings is the
// existing channel for "here is what you should know about this result".
func TestUpdateSkipWarning_ReportsBothReasons(t *testing.T) {
	skips := core.UpdateSkips{Pinned: 2, Local: 1}
	msg := updateSkipWarning(skips)

	assert.Contains(t, msg, "2 pinned")
	assert.Contains(t, msg, "1 local")
}

// TestUpdateSkipWarning_SingularAndEmpty: no warning when nothing was skipped,
// and correct singular wording for one.
func TestUpdateSkipWarning_SingularAndEmpty(t *testing.T) {
	assert.Empty(t, updateSkipWarning(core.UpdateSkips{}), "nothing skipped means no warning")

	one := updateSkipWarning(core.UpdateSkips{Pinned: 1})
	assert.Contains(t, one, "1 pinned mod ")
	assert.NotContains(t, one, "mods")
	assert.NotContains(t, one, "local", "should not mention a reason that did not apply")
}

// TestApplyUpdate_LocalMod_MessageSaysLocal mirrors the CLI: a local mod is
// filtered before any source call, so "already up to date" describes a check
// that never ran.
func TestApplyUpdate_LocalMod_MessageSaysLocal(t *testing.T) {
	msg := notCheckedMessage("Some Mod", domain.InstalledMod{
		Mod:          domain.Mod{SourceID: domain.SourceLocal},
		UpdatePolicy: domain.UpdateNotify,
	})
	assert.Contains(t, msg, "local")
	assert.NotContains(t, msg, "up to date")

	pinned := notCheckedMessage("Some Mod", domain.InstalledMod{
		Mod:          domain.Mod{SourceID: "nexusmods"},
		UpdatePolicy: domain.UpdatePinned,
	})
	assert.Contains(t, pinned, "pinned")
	assert.NotContains(t, pinned, "up to date")

	// A genuinely current mod still reports currency.
	current := notCheckedMessage("Some Mod", domain.InstalledMod{
		Mod:          domain.Mod{SourceID: "nexusmods"},
		UpdatePolicy: domain.UpdateNotify,
	})
	assert.Contains(t, current, "up to date")
}
