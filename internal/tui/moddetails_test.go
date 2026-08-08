package tui

import (
	"fmt"
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

// detailsWithDescriptionLines builds a minimal ModDetails whose body is
// precisely controllable: the fixed header line plus exactly n description
// lines (when n > 0), with no other optional fields to blur the count.
func detailsWithDescriptionLines(n int) ModDetails {
	d := ModDetails{ID: "a", Name: "N", Version: "1.0", Author: "X"}
	if n > 0 {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i)
		}
		d.Description = strings.Join(lines, "\n")
	}
	return d
}

// TestModDetailsContent_LinesNeverExceedsHeight pins the invariant the #86
// review demanded: len(Lines(w, h)) <= h always, for every offset and every
// body length - including the boundaries where the body is shorter than,
// equal to, and one line longer than the requested height, which is exactly
// where off-by-ones hide. Before the fix, a scrolled (non-bottomed-out) body
// longer than height returned height+1 lines: the body filled height, and
// the "↓ N more" indicator was appended on top instead of budgeted within it.
func TestModDetailsContent_LinesNeverExceedsHeight(t *testing.T) {
	for _, height := range []int{1, 2, 3, 5, 10} {
		bodyLensToTry := map[int]bool{0: true, 1: true, height: true, height + 1: true, height * 3: true}
		if height > 1 {
			bodyLensToTry[height-1] = true
		}
		for bodyLines := range bodyLensToTry {
			t.Run(fmt.Sprintf("height=%d/descLines=%d", height, bodyLines), func(t *testing.T) {
				c := newModDetailsContent(detailsWithDescriptionLines(bodyLines), DefaultKeyMap())
				total := len(c.body())

				// Sweep every offset from 0 through past the end - HandleKey's
				// Down branch increments c.offset unboundedly and relies on
				// Lines itself to clamp, so Lines must hold the invariant for
				// out-of-range offsets too, not just in-range ones.
				for offset := 0; offset <= total+2; offset++ {
					c.offset = offset
					got := c.Lines(80, height)
					assert.LessOrEqualf(t, len(got), height,
						"offset=%d height=%d total=%d body-lines returned %d: %v",
						offset, height, total, len(got), got)
				}
			})
		}
	}
}

// TestModDetailsContent_IndicatorReflectsRealRemainingCount is the other
// half of the review finding: when the indicator IS shown, its count must be
// the real number of remaining body lines, not a number derived from a
// clamp that already discarded some of them.
func TestModDetailsContent_IndicatorReflectsRealRemainingCount(t *testing.T) {
	c := newModDetailsContent(detailsWithDescriptionLines(50), DefaultKeyMap())
	total := len(c.body())

	const height = 10
	got := c.Lines(80, height)
	require.Len(t, got, height, "indicator must be budgeted within height, not appended on top")

	last := got[len(got)-1]
	require.True(t, strings.HasPrefix(last, "↓ "), "expected an indicator line, got %q", last)

	// height-1 body lines are shown ahead of the indicator (the indicator
	// itself occupies the height-th slot), so this many remain.
	wantRemaining := total - (height - 1)
	assert.Equal(t, fmt.Sprintf("↓ %d more", wantRemaining), last)
}

// TestModDetailsContent_HostRenderNoDoubleClamp is the end-to-end proof the
// review asked for: rendering through the real host (contextView, via
// screenView) must show the view's own "↓ N more" indicator, and must NOT
// show clampLines' generic "+N more" tail. Before the fix, Lines()
// over-produced by one line whenever scrolled short of the bottom, so
// contextView's own clampLines fired a second time and replaced the
// content's honest, correct count with a misleading one derived from the
// clamp's own math. A unit test on Lines alone would not catch this - only
// the full render pipeline does.
func TestModDetailsContent_HostRenderNoDoubleClamp(t *testing.T) {
	model := sizedPrototypeModel(t, "wizardry", 100, 24)

	d := testDetails()
	descLines := make([]string, 200)
	for i := range descLines {
		descLines[i] = fmt.Sprintf("description line %d", i)
	}
	d.Description = strings.Join(descLines, "\n")

	model.pushContext(newModDetailsContent(d, model.keys))
	view := model.screenView()

	assert.Regexp(t, `↓ \d+ more`, view, "the view's own indicator must survive the host's render")
	assert.NotRegexp(t, `\+\d+ more`, view,
		"a generic '+N more' tail means clampLines double-clamped over this view's own indicator")
}
