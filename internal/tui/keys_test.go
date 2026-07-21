package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestDefaultKeyMapDocumentsPrototypeBindings(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, keyMap.Help))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyCtrlC}, keyMap.Quit))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyTab}, keyMap.NextScreen))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}, keyMap.Search))
}

// TestDefaultKeyMapDocumentsMutationBindings guards the Task 7 keybindings:
// e (toggle enable/disable), x (uninstall), and D (deploy). Profile switch
// deliberately reuses the existing Select ("enter") binding rather than
// adding a new one - see mutations.go's switchSelectedProfile.
func TestDefaultKeyMapDocumentsMutationBindings(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}, keyMap.ToggleEnable))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}, keyMap.Uninstall))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}}, keyMap.Deploy))
}

// TestMutationBindingsDoNotCollideWithExistingBindings traces every binding
// already registered in KeyMap against the three new single-key mutation
// bindings, guarding task-7-brief.md's "verify no collision" requirement as
// a standing regression check rather than a one-time manual trace.
func TestMutationBindingsDoNotCollideWithExistingBindings(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	existing := map[string]key.Binding{
		"Quit": keyMap.Quit, "Help": keyMap.Help, "NextScreen": keyMap.NextScreen,
		"PrevScreen": keyMap.PrevScreen, "Up": keyMap.Up, "Down": keyMap.Down,
		"Search": keyMap.Search, "SearchScreen": keyMap.SearchScreen, "Dashboard": keyMap.Dashboard,
		"InstalledMods": keyMap.InstalledMods, "Profiles": keyMap.Profiles, "Sources": keyMap.Sources,
		"Select": keyMap.Select, "Submit": keyMap.Submit, "Blur": keyMap.Blur,
		"NextPage": keyMap.NextPage, "PrevPage": keyMap.PrevPage, "CycleSource": keyMap.CycleSource,
		"ConfirmAction": keyMap.ConfirmAction, "CancelAction": keyMap.CancelAction,
	}
	mutation := map[string]key.Binding{"ToggleEnable": keyMap.ToggleEnable, "Uninstall": keyMap.Uninstall, "Deploy": keyMap.Deploy}

	for mName, m := range mutation {
		for eName, e := range existing {
			for _, k := range m.Keys() {
				msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
				require.False(t, key.Matches(msg, e), "%s (%q) collides with existing binding %s", mName, k, eName)
			}
		}
	}
}

// TestDefaultKeyMapDocumentsInstallBinding guards Phase 5b's install-from-
// search binding: 'i' on ScreenSearch (see mutations.go's
// installSelectedSearchResult).
func TestDefaultKeyMapDocumentsInstallBinding(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}}, keyMap.Install))
}

// TestInstallBindingDoesNotCollideWithExistingBindings extends
// TestMutationBindingsDoNotCollideWithExistingBindings's standing-regression
// pattern (task-5-brief.md: "Key collision checks... are your
// responsibility - verify and document") to Phase 5b's Install binding:
// 'i' must not match any binding already registered in KeyMap, including
// the three Phase 5a mutation bindings.
func TestInstallBindingDoesNotCollideWithExistingBindings(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	existing := map[string]key.Binding{
		"Quit": keyMap.Quit, "Help": keyMap.Help, "NextScreen": keyMap.NextScreen,
		"PrevScreen": keyMap.PrevScreen, "Up": keyMap.Up, "Down": keyMap.Down,
		"Search": keyMap.Search, "SearchScreen": keyMap.SearchScreen, "Dashboard": keyMap.Dashboard,
		"InstalledMods": keyMap.InstalledMods, "Profiles": keyMap.Profiles, "Sources": keyMap.Sources,
		"Select": keyMap.Select, "Submit": keyMap.Submit, "Blur": keyMap.Blur,
		"NextPage": keyMap.NextPage, "PrevPage": keyMap.PrevPage, "CycleSource": keyMap.CycleSource,
		"ConfirmAction": keyMap.ConfirmAction, "CancelAction": keyMap.CancelAction,
		"ToggleEnable": keyMap.ToggleEnable, "Uninstall": keyMap.Uninstall, "Deploy": keyMap.Deploy,
	}

	for _, k := range keyMap.Install.Keys() {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		for eName, e := range existing {
			require.False(t, key.Matches(msg, e), "Install (%q) collides with existing binding %s", k, eName)
		}
	}
}

// TestDefaultKeyMapDocumentsCheckUpdatesBinding guards Phase 5b's
// check-updates binding: 'u' on ScreenDashboard/ScreenInstalledMods (see
// mutations.go's checkForUpdates).
func TestDefaultKeyMapDocumentsCheckUpdatesBinding(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}}, keyMap.CheckUpdates))
}

// TestCheckUpdatesBindingDoesNotCollideWithExistingBindings extends the
// same standing-regression pattern to Phase 5b's CheckUpdates binding: 'u'
// must not match any binding already registered in KeyMap, including the
// Phase 5a mutation bindings and Phase 5b's own Install binding.
func TestCheckUpdatesBindingDoesNotCollideWithExistingBindings(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	existing := map[string]key.Binding{
		"Quit": keyMap.Quit, "Help": keyMap.Help, "NextScreen": keyMap.NextScreen,
		"PrevScreen": keyMap.PrevScreen, "Up": keyMap.Up, "Down": keyMap.Down,
		"Search": keyMap.Search, "SearchScreen": keyMap.SearchScreen, "Dashboard": keyMap.Dashboard,
		"InstalledMods": keyMap.InstalledMods, "Profiles": keyMap.Profiles, "Sources": keyMap.Sources,
		"Select": keyMap.Select, "Submit": keyMap.Submit, "Blur": keyMap.Blur,
		"NextPage": keyMap.NextPage, "PrevPage": keyMap.PrevPage, "CycleSource": keyMap.CycleSource,
		"ConfirmAction": keyMap.ConfirmAction, "CancelAction": keyMap.CancelAction,
		"ToggleEnable": keyMap.ToggleEnable, "Uninstall": keyMap.Uninstall, "Deploy": keyMap.Deploy,
		"Install": keyMap.Install,
	}

	for _, k := range keyMap.CheckUpdates.Keys() {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		for eName, e := range existing {
			require.False(t, key.Matches(msg, e), "CheckUpdates (%q) collides with existing binding %s", k, eName)
		}
	}
}
