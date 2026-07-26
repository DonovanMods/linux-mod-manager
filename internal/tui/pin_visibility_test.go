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
