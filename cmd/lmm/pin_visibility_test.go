package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPolicy overwrites a seeded mod's update policy.
func setPolicy(t *testing.T, svc *core.Service, game *domain.Game, modID string, policy domain.UpdatePolicy) {
	t.Helper()
	require.NoError(t, svc.SetModUpdatePolicy("src", modID, game.ID, "default", policy))
}

// listVerbose runs doList with --verbose and returns stdout.
func listVerbose(t *testing.T, svc *core.Service, game *domain.Game, asJSON bool) string {
	t.Helper()
	oldVerbose, oldJSON := verbose, jsonOutput
	verbose, jsonOutput = !asJSON, asJSON
	t.Cleanup(func() { verbose, jsonOutput = oldVerbose, oldJSON })

	return captureStdout(t, func() error {
		return doList(&cobra.Command{}, svc, game)
	})
}

// TestList_VerboseShowsUpdatePolicy: pinning works but was invisible — no
// listing surfaced it, so reviewing which mods are pinned meant probing each
// one individually.
func TestList_VerboseShowsUpdatePolicy(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")
	setPolicy(t, svc, game, "b", domain.UpdatePinned)

	out := listVerbose(t, svc, game, false)

	assert.Contains(t, out, "POLICY", "verbose listing needs a policy column")
	lines := strings.Split(out, "\n")
	var rowA, rowB string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "Mod A"):
			rowA = l
		case strings.Contains(l, "Mod B"):
			rowB = l
		}
	}
	require.NotEmpty(t, rowA)
	require.NotEmpty(t, rowB)
	assert.Contains(t, rowB, "pinned", "the pinned mod's row should say so")
	assert.Contains(t, rowA, "notify", "an unpinned mod should show its policy too")
}

// TestList_JSONIncludesUpdatePolicy: --json consumers could not see pin state
// at all. Present unconditionally, not gated on --verbose.
func TestList_JSONIncludesUpdatePolicy(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	setPolicy(t, svc, game, "a", domain.UpdatePinned)

	var out struct {
		Mods []struct {
			ID           string `json:"id"`
			UpdatePolicy string `json:"update_policy"`
		} `json:"mods"`
	}
	require.NoError(t, json.Unmarshal([]byte(listVerbose(t, svc, game, true)), &out))
	require.Len(t, out.Mods, 1)
	assert.Equal(t, "pinned", out.Mods[0].UpdatePolicy)
}

// TestApplySingleUpdate_PinnedSaysPinned: the old message claimed "already up
// to date", which is not what happened — the mod was filtered out as pinned
// before any version comparison, so a newer version may well exist.
func TestApplySingleUpdate_PinnedSaysPinned(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	setPolicy(t, svc, game, "a", domain.UpdatePinned)

	mod, err := svc.GetInstalledMod("src", "a", game.ID, "default")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return applySingleUpdate(context.Background(), svc, game, mod, "default")
	})

	assert.Contains(t, out, "pinned", "must say the mod is pinned")
	assert.NotContains(t, out, "up to date", "must not claim currency it never checked")
	assert.Contains(t, out, "set-update", "should name the command that unpins")
}

// TestDoUpdate_ReportsSkippedPinnedCount: pinned mods are filtered before the
// source is queried, so they vanish from `lmm update` with no trace.
func TestDoUpdate_ReportsSkippedPinnedCount(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	// doUpdate resolves a source before doing anything else.
	game.SourceIDs = map[string]string{"src": "src"}
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")
	setPolicy(t, svc, game, "a", domain.UpdatePinned)
	setPolicy(t, svc, game, "b", domain.UpdatePinned)

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "2 pinned", "should report how many mods were skipped")
}

// TestDoUpdate_AllPinned_NoLeadingBlankLine: when every mod is pinned the
// skipped-count line is the entire output, so a leading newline shows up as a
// stray blank first line with nothing to separate it from.
func TestDoUpdate_AllPinned_NoLeadingBlankLine(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	game.SourceIDs = map[string]string{"src": "src"}
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	setPolicy(t, svc, game, "a", domain.UpdatePinned)

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	assert.False(t, strings.HasPrefix(out, "\n"), "output must not open with a blank line, got %q", out)
	assert.Contains(t, out, "1 pinned mod skipped")
}

// TestDoUpdate_SomePinned_SeparatesFromPrecedingOutput: the counterpart — when
// something is printed first, the line still needs a blank line above it.
func TestDoUpdate_SomePinned_SeparatesFromPrecedingOutput(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	game.SourceIDs = map[string]string{"src": "src"}
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")
	setPolicy(t, svc, game, "a", domain.UpdatePinned)

	out := captureStdout(t, func() error {
		return doUpdate(context.Background(), svc, game, nil)
	})

	// Asserts the separator, not the preceding sentence: which summary line
	// precedes it depends on whether the check succeeded, and tying the two
	// together made this test encode a bug (it used to accept "All mods are up
	// to date" after a check that had actually failed).
	assert.Contains(t, out, "\n\n1 pinned mod skipped", "needs a blank line after preceding output")
	// HasPrefix, not out[:1]: a regression that suppressed output entirely
	// should fail this assertion, not panic on an index out of range.
	assert.False(t, strings.HasPrefix(out, "\n"), "but not a leading blank line")
}
