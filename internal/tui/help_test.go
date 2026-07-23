package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHelpViewListsPerScreenGroups proves Task 9's restructure: the help
// panel documents every binding added across Tasks 4-8 (files, policy,
// purge, game switch, profile create/delete) grouped by the screen each
// applies to, with "global" first. Zero-size model (no WindowSizeMsg) keeps
// helpBodyBudget() at its generous unsized default so the full group list
// renders uncapped - this test is about content, not the height invariant
// (see TestViewFitsTerminalBoundsWithHelpVisible for that).
func TestHelpViewListsPerScreenGroups(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.showHelp = true
	view := model.helpView()

	for _, want := range []string{
		"global", "dashboard", "installed mods", "search", "profiles",
		"files", "policy", "purge", "game", "new profile", "delete profile",
	} {
		require.Contains(t, view, want, "missing %q", want)
	}
}

// TestHelpViewCurrentScreenGroupFirst proves the current screen's group is
// promoted to immediately after "global", while the rest keep their fixed
// relative order - so a Profiles-screen user sees their own bindings first,
// not buried after Installed Mods' longer list.
func TestHelpViewCurrentScreenGroupFirst(t *testing.T) {
	t.Parallel()

	onProfiles, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	onProfiles.screen = ScreenProfiles
	onProfiles.showHelp = true
	profilesView := onProfiles.helpView()
	profilesIdx := strings.Index(profilesView, "profiles")
	installedIdx := strings.Index(profilesView, "installed mods")
	require.NotEqual(t, -1, profilesIdx, "profiles header missing")
	require.NotEqual(t, -1, installedIdx, "installed mods header missing")
	require.Less(t, profilesIdx, installedIdx, "profiles group should render before installed mods when on Profiles")

	onInstalled, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	onInstalled.screen = ScreenInstalledMods
	onInstalled.showHelp = true
	installedView := onInstalled.helpView()
	installedIdx = strings.Index(installedView, "installed mods")
	profilesIdx = strings.Index(installedView, "profiles")
	require.NotEqual(t, -1, installedIdx, "installed mods header missing")
	require.NotEqual(t, -1, profilesIdx, "profiles header missing")
	require.Less(t, installedIdx, profilesIdx, "installed mods group should render before profiles when on Installed Mods")
}

// TestFooterMentionsHelpKey pins "?" as the discovery point for help: the
// footer must always name it, even as the panel it opens grows with new
// bindings.
func TestFooterMentionsHelpKey(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	require.Contains(t, model.footerLine(), "?: help")
}
