package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModRow_ShowsPinFlag: ModItem.UpdatePolicy was populated but read only to
// mark the current choice inside the 'P' picker, so reviewing pins meant
// opening the picker on every mod one at a time.
func TestModRow_ShowsPinFlag(t *testing.T) {
	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	pinned := ModItem{Name: "Pinned Mod", Author: "Auth", Version: "1.0", Status: "deployed", UpdatePolicy: "pin"}
	notify := ModItem{Name: "Normal Mod", Author: "Auth", Version: "1.0", Status: "deployed", UpdatePolicy: "notify"}

	assert.Contains(t, m.modRow(0, 80, pinned), "pin", "a pinned mod's row must show it")
	assert.NotContains(t, m.modRow(0, 80, notify), "pin", "an unpinned mod must not be marked")
}

// TestModRow_PinFlagSurvivesNarrowTerminal guards the 80-column budget: the
// marker is useless if it is the first thing truncation eats.
func TestModRow_PinFlagSurvivesNarrowTerminal(t *testing.T) {
	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	pinned := ModItem{Name: "A Mod With A Fairly Long Name", Author: "Some Author", Version: "1.2.3", Status: "deployed", UpdatePolicy: "pin"}
	for _, width := range []int{80, 60, 40} {
		assert.Contains(t, m.modRow(0, width, pinned), "pin", "pin marker must survive at width %d", width)
	}
}

// TestModRow_NoTrailingColumnDrift: the flags column is blank for unpinned
// mods, so later columns must still line up across rows.
func TestModRow_NoTrailingColumnDrift(t *testing.T) {
	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	pinned := ModItem{Name: "Mod", Author: "Auth", Version: "1.0", Status: "deployed", UpdatePolicy: "pin"}
	notify := ModItem{Name: "Mod", Author: "Auth", Version: "1.0", Status: "deployed", UpdatePolicy: "notify"}

	a := ansi.Strip(m.modRow(0, 80, pinned))
	b := ansi.Strip(m.modRow(0, 80, notify))
	assert.Equal(t, len([]rune(a)), len([]rune(b)), "rows must be the same width whether or not the flag is set")
	assert.Equal(t, strings.LastIndex(a, "1.0"), strings.LastIndex(b, "1.0"), "version column must not shift")
}

// TestModFlags_LockOutranksPin pins modFlags' precedence contract (#97,
// task-6-brief.md; extended #221, task-12-brief.md; extended again for the
// game-level convert_paks gap, Copilot round 3 on PR #222): "lck" takes the
// 3-char slot over "pin" when a mod is both locked and pinned, and "pin"
// outranks "raw" the same way - the pin/raw state itself is untouched
// (still visible elsewhere), it just doesn't get its own glyph here. A
// plain pin-only mod still renders "pin", a plain raw-only mod (CompileGame
// true, GameConvertPaks true, ConvertPaks false) renders "raw", and a mod
// with none of the three renders blank - exercising modFlags directly (not
// through modRow) for an exact string match on the documented "%-3s %s"
// shape. "raw" requires BOTH ModItem.GameConvertPaks and ModItem.
// ConvertPaks to be true before it clears - a game-level convert_paks:
// false (GameConvertPaks false) shows "raw" even when the per-mod flag is
// itself on, mirroring the README's "either one is enough to keep a pak
// raw" rule. "raw" ALSO requires ModItem.HasPakSource (#221 round-4 fix,
// Copilot round 4 on PR #222): an exmodz-only mod never shows "raw", no
// matter what the conversion flags say, because it has no pak to leave
// unconverted. The "lck *" row (#143 polish) pins the shape's documented
// two-slots-at-once case: the flag and the updated-this-session marker are
// independent, so a locked mod brought current this session fills both
// (m.lastUpdates is seeded with a matching applied entry — Source+ID match
// AND Version == ToVersion, the two conditions wasUpdatedThisSession
// requires).
func TestModFlags_LockOutranksPin(t *testing.T) {
	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	m.lastUpdates = &UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "skyui", ToVersion: "5.3"},
	}}

	tests := []struct {
		name string
		mod  ModItem
		want string
	}{
		{"locked only", ModItem{Locked: true}, "lck  "},
		{"locked and pinned - lock wins the slot", ModItem{Locked: true, UpdatePolicy: "pin"}, "lck  "},
		{"locked and updated this session - both slots fill", ModItem{Locked: true, Source: "nexusmods", ID: "skyui", Version: "5.3"}, "lck *"},
		{"pinned only", ModItem{UpdatePolicy: "pin"}, "pin  "},
		{"raw only", ModItem{CompileGame: true, GameConvertPaks: true, ConvertPaks: false, HasPakSource: true}, "raw  "},
		{"compile game with conversion ON at both levels is not raw", ModItem{CompileGame: true, GameConvertPaks: true, ConvertPaks: true, HasPakSource: true}, "     "},
		{"non-compile game is never raw, even unconverted", ModItem{CompileGame: false, GameConvertPaks: false, ConvertPaks: false, HasPakSource: true}, "     "},
		{"game-level convert_paks off shows raw even with the per-mod flag on", ModItem{CompileGame: true, GameConvertPaks: false, ConvertPaks: true, HasPakSource: true}, "raw  "},
		{"pinned and raw - pin wins the slot", ModItem{UpdatePolicy: "pin", CompileGame: true, GameConvertPaks: true, ConvertPaks: false, HasPakSource: true}, "pin  "},
		{"locked and raw - lock wins the slot", ModItem{Locked: true, CompileGame: true, GameConvertPaks: true, ConvertPaks: false, HasPakSource: true}, "lck  "},
		// #221 round-4 fix: an exmodz-only mod (HasPakSource false) never
		// shows "raw" even when the game-level flag is off and the per-mod
		// flag is on - the conversion flags have no deploy-time effect on a
		// mod with no pak merge source, so there is nothing to name "raw".
		{"exmodz-only mod is never raw, even with game-level convert_paks off", ModItem{CompileGame: true, GameConvertPaks: false, ConvertPaks: true, HasPakSource: false}, "     "},
		{"neither", ModItem{}, "     "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, m.modFlags(tt.mod))
		})
	}
}

// TestModRow_ShowsLockFlag mirrors TestModRow_ShowsPinFlag above for the
// "lck" flag: a locked mod's row must show it, an unlocked one must not.
func TestModRow_ShowsLockFlag(t *testing.T) {
	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	locked := ModItem{Name: "Locked Mod", Author: "Auth", Version: "1.0", Status: "deployed", Locked: true}
	unlocked := ModItem{Name: "Normal Mod", Author: "Auth", Version: "1.0", Status: "deployed"}

	assert.Contains(t, m.modRow(0, 80, locked), "lck", "a locked mod's row must show it")
	assert.NotContains(t, m.modRow(0, 80, unlocked), "lck", "an unlocked mod must not be marked")
}

// TestModRow_LockedAndPinnedShowsOnlyLock proves the precedence at the
// modRow level (not just modFlags directly, above): a mod that is both
// locked and pinned must show "lck" in its row and must NOT also show
// "pin" - the flags column has room for exactly one 3-char glyph.
func TestModRow_LockedAndPinnedShowsOnlyLock(t *testing.T) {
	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	both := ModItem{Name: "Both Mod", Author: "Auth", Version: "1.0", Status: "deployed", Locked: true, UpdatePolicy: "pin"}
	row := m.modRow(0, 80, both)
	assert.Contains(t, row, "lck", "a locked+pinned mod's row must show the lock flag")
	assert.NotContains(t, row, "pin", "a locked+pinned mod's row must not also show the pin flag")
}
