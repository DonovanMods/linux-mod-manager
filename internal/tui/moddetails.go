package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// modDetailsContent is the mod details view (#86): a contextContent pushed
// over Installed Mods or Search, rendering the same fields as `lmm mod show`
// in the same order. Scrolls its whole body rather than truncating, since a
// description can run long - the CLI's 2000-char cap exists only because a
// one-shot terminal dump cannot scroll.
type modDetailsContent struct {
	details ModDetails
	// offset is the first visible body line, mirroring infoOverlay.offset.
	// Clamped on every key and again at render time.
	offset int
	// keys is the session's live KeyMap, not DefaultKeyMap(), so a custom
	// remapping of Up/Down can never desync this view's scrolling from the
	// rest of the TUI - the same reasoning behind overlay.go matching
	// m.keys.Files instead of a literal "f" (Copilot PR #69 finding).
	keys KeyMap
}

func newModDetailsContent(d ModDetails, keys KeyMap) *modDetailsContent {
	return &modDetailsContent{details: d, keys: keys}
}

func (c *modDetailsContent) Title() string { return c.details.Name }

// body builds every line before windowing, so scrolling and clamping operate
// on one flat list. Field order and omit-when-empty rules mirror doModShow
// (cmd/lmm/mod.go) exactly - that parity is the point of the feature.
func (c *modDetailsContent) body() []string {
	d := c.details
	lines := []string{
		fmt.Sprintf("ID: %s   Version: %s   Author: %s", d.ID, d.Version, d.Author),
	}
	if d.Category != "" {
		lines = append(lines, "Category: "+d.Category)
	}
	if d.HasEndorsements {
		lines = append(lines, fmt.Sprintf("Endorsements: %d", d.Endorsements))
	}
	if d.SourceURL != "" {
		lines = append(lines, "URL: "+d.SourceURL)
	}
	if d.PictureURL != "" {
		lines = append(lines, "Image: "+d.PictureURL)
	}

	if d.Summary != "" {
		lines = append(lines, "", "Summary:")
		lines = append(lines, strings.Split(strings.TrimSpace(d.Summary), "\n")...)
	}

	// Description has three states. An empty description with no fetch in
	// flight and no error is simply absent upstream - omit the block, same as
	// mod show, rather than showing an empty heading.
	switch {
	case d.Description != "":
		lines = append(lines, "", "Description:")
		lines = append(lines, strings.Split(strings.TrimSpace(d.Description), "\n")...)
	case d.FetchErr != "":
		// singleLine applied HERE, at the render site, not where FetchErr is
		// assigned (resolveModDetailsFailed, mutations.go) - so any future
		// FetchErr writer is covered too, not just this one. body() renders
		// the error as ONE slice element; clampLines (contextview.go) counts
		// slice elements, and truncateLines bounds display WIDTH, not line
		// count - neither guard catches an embedded newline, so an
		// unwrapped multi-line error renders that many extra physical rows
		// past the height budget (#86 review finding; Task 7 already
		// applied singleLine to m.action.status but missed this).
		lines = append(lines, "", "Description:", "(unavailable — "+singleLine(d.FetchErr)+")")
	case d.Fetching:
		lines = append(lines, "", "Description:", "(loading…)")
	}

	if d.Installed != nil {
		// The seeded row (modDetailsFromItem) always knows the profile now
		// (#86 review - ModItem.Profile), but this omit-when-empty guard
		// stays as the backstop against any future caller that seeds an
		// Installed block without one: printing "(profile: )" is worse than
		// omitting the parenthetical entirely.
		header := fmt.Sprintf("Installed: v%s", d.Installed.Version)
		if d.Installed.Profile != "" {
			header += fmt.Sprintf(" (profile: %s)", d.Installed.Profile)
		}
		lines = append(lines, "", header)
		lines = append(lines, "  Update policy: "+d.Installed.UpdatePolicy)
		if d.Installed.Locked {
			lock := "locked at v" + d.Installed.LockedVersion
			// Only name the converge command when the lock's target actually
			// differs from what's installed - identical condition to
			// doModShow's (cmd/lmm/mod.go:734-736).
			if d.Installed.LockedVersion != d.Installed.Version {
				lock += " — run 'lmm profile apply' to converge"
			}
			lines = append(lines, "  Lock: "+lock)
		} else {
			lines = append(lines, "  Lock: none")
		}
		if d.Installed.ConvertPaks != nil {
			state := "on"
			if !*d.Installed.ConvertPaks {
				state = "off"
			}
			lines = append(lines, "  Pak conversion: "+state)
		}
	}
	return lines
}

// maxOffset is the largest first-visible-line index that still fills height:
// the offset at which exactly `height` body lines remain, so the final
// screenful needs no "↓ N more" indicator and no trailing blank space.
func (c *modDetailsContent) maxOffset(height int) int {
	over := len(c.body()) - height
	if over < 0 {
		return 0
	}
	return over
}

// Lines returns AT MOST height lines, always - windowedRows' own indicator
// convention (clamp.go), which this previously violated: the "↓ N more" row
// used to be appended ON TOP of a full height of body lines, so a scrolled,
// non-bottomed-out view returned height+1 lines. That over-production made
// the host's own clampLines (contextview.go) fire a second time, discarding
// this view's honest count in favor of a generic, clamp-derived "+N more"
// tail. The indicator is now budgeted INSIDE height: when the remaining body
// fits, it's returned as-is (no indicator, matching maxOffset's own
// definition of "fits" above); otherwise only height-1 body lines are shown,
// making room for the indicator as the height-th line.
func (c *modDetailsContent) Lines(width, height int) []string {
	body := c.body()
	if height < 1 {
		height = 1
	}
	offset := min(max(c.offset, 0), c.maxOffset(height))
	remaining := len(body) - offset

	if remaining <= height {
		return append([]string(nil), body[offset:]...)
	}

	end := offset + height - 1
	visible := append([]string(nil), body[offset:end]...)
	visible = append(visible, fmt.Sprintf("↓ %d more", len(body)-end))
	return visible
}

// HandleKey consumes scrolling. Consuming up/down is MANDATORY, not a
// nicety: a declined arrow would move the list selection on the screen
// underneath, which the user cannot see (#86). Every other key is declined
// so the host's swallow rule and esc-pop can act on it.
func (c *modDetailsContent) HandleKey(msg tea.KeyMsg) (contextContent, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, c.keys.Down):
		c.offset++
		return c, nil, true
	case key.Matches(msg, c.keys.Up):
		if c.offset > 0 {
			c.offset--
		}
		return c, nil, true
	}
	return c, nil, false
}

func (c *modDetailsContent) HelpGroup() helpGroup {
	return helpGroup{
		name: "mod details",
		entries: []string{
			helpRow("↑/↓", "scroll"),
			helpRow("esc", "back"),
		},
	}
}
