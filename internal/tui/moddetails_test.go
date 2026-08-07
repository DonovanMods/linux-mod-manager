package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDetails() ModDetails {
	return ModDetails{
		ID: "12345", Name: "Mod A", Version: "2.2.6", Author: "Author A",
		Category: "Utilities", Summary: "A short summary.",
		Description:     "Line one.\n\nLine two.",
		SourceURL:       "https://example.invalid/mods/1",
		PictureURL:      "https://example.invalid/img/1.png",
		Endorsements:    84213,
		HasEndorsements: true,
		Installed: &InstalledDetails{
			Version: "2.2.3", Profile: "default", UpdatePolicy: "notify",
			Locked: true, LockedVersion: "2.2.3",
		},
	}
}

// TestModDetailsContent_FieldOrderMatchesModShow pins CLI parity: the TUI
// renders the same fields, in the same order, as `lmm mod show`.
func TestModDetailsContent_FieldOrderMatchesModShow(t *testing.T) {
	body := strings.Join(newModDetailsContent(testDetails(), DefaultKeyMap()).Lines(80, 40), "\n")

	order := []string{"ID: 12345", "Category: Utilities", "Endorsements: 84213",
		"URL: https://", "Image: https://", "Summary:", "Description:", "Installed: v2.2.3"}
	last := -1
	for _, want := range order {
		at := strings.Index(body, want)
		require.GreaterOrEqual(t, at, 0, "missing field %q in:\n%s", want, body)
		assert.Greater(t, at, last, "field %q is out of mod show order", want)
		last = at
	}
}

// TestModDetailsContent_OmitsEmptyFields mirrors mod show's omit rules -
// blank labels with nothing after them are noise.
func TestModDetailsContent_OmitsEmptyFields(t *testing.T) {
	d := ModDetails{ID: "a", Name: "Mod A", Version: "1.0", Author: "X"}
	body := strings.Join(newModDetailsContent(d, DefaultKeyMap()).Lines(80, 40), "\n")

	for _, absent := range []string{"Category:", "Endorsements:", "URL:", "Image:", "Summary:", "Description:", "Installed:"} {
		assert.NotContains(t, body, absent)
	}
}

// TestModDetailsContent_InstalledBlockRendersLockAndConverge: parity with
// mod show's Installed section, including the converge hint's condition.
func TestModDetailsContent_InstalledBlockRendersLockAndConverge(t *testing.T) {
	d := testDetails()
	d.Installed.LockedVersion = "1.0.0" // differs from installed 2.2.3
	body := strings.Join(newModDetailsContent(d, DefaultKeyMap()).Lines(80, 40), "\n")

	assert.Contains(t, body, "Installed: v2.2.3 (profile: default)")
	assert.Contains(t, body, "Update policy: notify")
	assert.Contains(t, body, "Lock: locked at v1.0.0 — run 'lmm profile apply' to converge")
}

func TestModDetailsContent_NoConvergeHintWhenLockMatchesInstalled(t *testing.T) {
	body := strings.Join(newModDetailsContent(testDetails(), DefaultKeyMap()).Lines(80, 40), "\n")
	assert.Contains(t, body, "Lock: locked at v2.2.3")
	assert.NotContains(t, body, "converge")
}

// TestModDetailsContent_RenderStates covers the three fetch states.
func TestModDetailsContent_RenderStates(t *testing.T) {
	fetching := testDetails()
	fetching.Fetching = true
	fetching.Description = ""
	assert.Contains(t, strings.Join(newModDetailsContent(fetching, DefaultKeyMap()).Lines(80, 40), "\n"), "(loading…)")

	failed := testDetails()
	failed.Description = ""
	failed.FetchErr = "source unreachable"
	assert.Contains(t, strings.Join(newModDetailsContent(failed, DefaultKeyMap()).Lines(80, 40), "\n"),
		"(unavailable — source unreachable)")
}

// TestModDetailsContent_ScrollsAndConsumesArrows is the safety-critical half:
// the view MUST consume up/down, or the list selection behind it drifts.
func TestModDetailsContent_ScrollsAndConsumesArrows(t *testing.T) {
	d := testDetails()
	d.Description = strings.Repeat("a line of description text\n", 200)
	c := newModDetailsContent(d, DefaultKeyMap())

	first := c.Lines(80, 10)
	next, _, handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	require.True(t, handled, "the details view must consume Down, not let it move the list behind")
	scrolled := next.(*modDetailsContent).Lines(80, 10)
	assert.NotEqual(t, first, scrolled, "Down must scroll the body")

	_, _, handledUp := next.HandleKey(tea.KeyMsg{Type: tea.KeyUp})
	assert.True(t, handledUp)
	_, _, handledJ := next.HandleKey(keyRunes("j"))
	assert.True(t, handledJ, "j/k aliases must scroll too")
}

// TestModDetailsContent_ShortBodyDoesNotScroll: no phantom scrolling.
func TestModDetailsContent_ShortBodyDoesNotScroll(t *testing.T) {
	c := newModDetailsContent(testDetails(), DefaultKeyMap())
	before := c.Lines(80, 40)
	next, _, handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	assert.True(t, handled, "arrows stay consumed even when there is nothing to scroll")
	assert.Equal(t, before, next.(*modDetailsContent).Lines(80, 40))
}

func TestModDetailsContent_TitleIsModName(t *testing.T) {
	assert.Equal(t, "Mod A", newModDetailsContent(testDetails(), DefaultKeyMap()).Title())
}
