package tui

import (
	"context"
	"errors"
	"strconv"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// --- Task 8: in-TUI game switcher ('g' from any screen) ---

// TestGameKeyOpensPickerActiveMarked covers 'g' opening the switcher: the
// picker's title, every configured game's name as an option, and the
// active game's option carrying the "active" Note (mirroring
// editSelectedModPolicy's own "current" Note convention for the
// update-policy picker).
func TestGameKeyOpensPickerActiveMarked(t *testing.T) {
	t.Parallel()

	provider := &recordingProvider{
		delegate: NewPrototypeProvider(),
		ListGamesResult: []GameInfo{
			{ID: "skyrim", Name: "Skyrim", Active: false},
			{ID: "fallout4", Name: "Fallout 4", Active: true},
		},
	}
	model := modelWithProvider(t, provider)

	updated, cmd := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.picker)
	require.Equal(t, "switch game", model.picker.title)
	require.Len(t, model.picker.options, 2)
	require.Equal(t, "Skyrim", model.picker.options[0].Label)
	require.Empty(t, model.picker.options[0].Note)
	require.Equal(t, "Fallout 4", model.picker.options[1].Label)
	require.Equal(t, "active", model.picker.options[1].Note)
	require.Equal(t, 1, model.picker.selected, "the active game must start selected")
}

// TestGameSwitchRebindsProvidersResetsAndReloads is the end-to-end happy
// path: choosing a non-active game rebinds BOTH m.provider and m.actions
// (rebindGame, actions.go), resets the session's data-derived state to its
// "nothing loaded yet" shape, re-seeds sources from the NEW game, and
// returns a cmd chain that resolves to a fresh dataLoadedMsg.
func TestGameSwitchRebindsProvidersResetsAndReloads(t *testing.T) {
	t.Parallel()

	games := []GameInfo{
		{ID: "fallout4", Name: "Fallout 4", Active: true},
		{ID: "skyrim", Name: "Skyrim", Active: false},
	}
	provider := &recordingProvider{
		delegate:        NewPrototypeProvider(),
		ListGamesResult: games,
		AltSourceInfos:  []SourceInfo{{ID: "alt-source", Name: "Alt Source", Type: "built-in"}},
		AltSources:      []string{"alt-source"},
	}
	actions := &recordingActions{}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: actions})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	// Dirty the state a real switch must reset, so zeroing/clearing is
	// actually observable rather than trivially already-true.
	model.selected[ScreenInstalledMods] = 1
	model.selected[ScreenSearch] = 2
	model.summary.Updates = 7

	updated, cmd := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.picker)

	// Option 1 ("Skyrim") is the non-active game; digit quick-select chooses
	// it directly (picker.go's digitQuickSelect is 1-based).
	updated, chooseCmd := model.Update(keyRunes("2"))
	model = updated.(Model)
	require.Nil(t, model.picker, "choosing an option must clear the picker")
	require.NotNil(t, chooseCmd)

	gameMsg := chooseCmd()
	require.IsType(t, gameChosenMsg{}, gameMsg)
	require.Equal(t, "skyrim", gameMsg.(gameChosenMsg).id)

	updated, loadCmd := model.Update(gameMsg)
	model = updated.(Model)

	require.Equal(t, []string{"skyrim"}, provider.SetGameCalls)
	require.Equal(t, []string{"skyrim"}, actions.SetGameCalls)

	require.Equal(t, stateLoading, model.state)
	require.Nil(t, model.mods)
	require.Nil(t, model.profiles)
	require.Equal(t, -1, model.summary.Updates)
	require.Equal(t, -1, model.summary.Conflicts)
	require.Equal(t, 0, model.selected[ScreenInstalledMods])
	require.Equal(t, 0, model.selected[ScreenSearch])

	require.Equal(t, provider.AltSourceInfos, model.sources, "sources must re-seed from the NEW game's SourceInfos()")
	require.Equal(t, []string{"", "alt-source"}, model.search.sources, "search sources must re-seed from the NEW game's Sources()")

	require.NotNil(t, loadCmd)
	dataMsg := loadCmd()
	require.IsType(t, dataLoadedMsg{}, dataMsg)

	updated, _ = model.Update(dataMsg)
	model = updated.(Model)
	require.Equal(t, stateReady, model.state)
}

// TestGameSwitchBlockedWhileActionRunning covers the single-flight guard:
// while an action is running, 'g' refuses synchronously with an explicit
// "action in progress" status (unlike promptPicker's own silent no-op -
// see openGameSwitcher's doc comment for why this task's own test demands
// an explicit message) and opens no picker at all.
func TestGameSwitchBlockedWhileActionRunning(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.running = true

	updated, cmd := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
	require.Equal(t, "action in progress", model.action.status)
	require.True(t, model.action.statusIsError)
}

// TestGameSwitchSameGameIsNoop covers choosing the ALREADY-active game:
// openGameSwitcher's picker closure returns a nil cmd for that option (see
// its own doc comment), so no gameChosenMsg is ever produced - no SetGame
// call on either provider or actions, and no session reset.
func TestGameSwitchSameGameIsNoop(t *testing.T) {
	t.Parallel()

	games := []GameInfo{
		{ID: "fallout4", Name: "Fallout 4", Active: true},
		{ID: "skyrim", Name: "Skyrim", Active: false},
	}
	provider := &recordingProvider{delegate: NewPrototypeProvider(), ListGamesResult: games}
	actions := &recordingActions{}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: actions})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	stateBefore := model.state

	updated, _ := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.NotNil(t, model.picker)
	require.Equal(t, 0, model.picker.selected, "the active game (index 0) must start selected")

	// Select (enter) chooses whatever's currently selected - the active
	// game itself, with no navigation first.
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, model.picker, "choosing must still clear the picker")
	require.Nil(t, cmd, "choosing the active game must dispatch no cmd at all")

	require.Empty(t, provider.SetGameCalls)
	require.Empty(t, actions.SetGameCalls)
	require.Equal(t, stateBefore, model.state, "nothing about the session's data must reset")
}

// TestGameKeySwallowedByFocusedSearchInput proves 'g' types into the search
// box instead of opening the switcher while ScreenSearch is focused - the
// existing focused-input swallow branch (updateKey, app.go) runs before the
// mutation-key switch this is dispatched from, mirroring every other
// single-letter mutation key's own test of the same guard (e.g.
// TestFilesKeySwallowedByFocusedSearchInput).
func TestGameKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "g")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "g")
	require.Nil(t, updated.picker)
}

// TestGameSwitchOnlyOneGameConfiguredStatus covers ListGames returning
// exactly one entry: a status line ("only one game configured", not an
// error) instead of an empty/single-option picker, per task-8-brief.md's
// own framing ("nicer than an empty pick").
func TestGameSwitchOnlyOneGameConfiguredStatus(t *testing.T) {
	t.Parallel()

	provider := &recordingProvider{
		delegate:        NewPrototypeProvider(),
		ListGamesResult: []GameInfo{{ID: "only", Name: "Only Game", Active: true}},
	}
	model := modelWithProvider(t, provider)

	updated, cmd := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
	require.Equal(t, "only one game configured", model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestGameSwitchCancelsInFlightSearchAndDiscardsLateResult mirrors
// TestSwitchDoneCancelsInFlightSearchAndDiscardsLateResult (the
// profile-switch precedent for this exact mechanism, immediately above in
// this file): a fresh game switch must cancel any in-flight search's
// context and bump search.gen, so a late result computed against the
// NOW-STALE old game's installed-marks/sources is discarded by the ordinary
// stale-gen check (searchResultMsg's case in app.go) rather than applied.
func TestGameSwitchCancelsInFlightSearchAndDiscardsLateResult(t *testing.T) {
	t.Parallel()

	provider := &searchCancelProvider{listGames: []GameInfo{
		{ID: "current", Name: "Current Game", Active: true},
		{ID: "other", Name: "Other Game", Active: false},
	}}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: &recordingActions{}})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	model, searchCmd := model.startSearch("skyui", 0)
	require.NotNil(t, searchCmd, "startSearch must dispatch a query given a real configured source")
	gen1 := model.search.gen
	require.NotNil(t, model.search.cancel)

	msg := searchCmd() // runs provider.Search, capturing its ctx
	require.NotNil(t, provider.capturedCtx)
	require.NoError(t, provider.capturedCtx.Err(), "search's ctx must not be cancelled yet")

	updated, _ := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.NotNil(t, model.picker)

	updated, chooseCmd := model.Update(keyRunes("2")) // "Other Game", not active
	model = updated.(Model)
	require.NotNil(t, chooseCmd)
	gameMsg := chooseCmd()

	updated, _ = model.Update(gameMsg)
	model = updated.(Model)

	require.Error(t, provider.capturedCtx.Err(), "game switch must cancel the in-flight search's context")
	require.ErrorIs(t, provider.capturedCtx.Err(), context.Canceled)
	require.NotEqual(t, gen1, model.search.gen, "game switch must bump the search generation")

	// The late result (tagged with the now-stale gen1) must be discarded,
	// exactly like any other superseded search result.
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotEqual(t, searchReady, model.search.state, "a late, now-stale search result must not be applied")
}

// TestGameSwitchDropsStaleInFlightLoad guards the review's Important
// finding: dataLoadedMsg used to be applied unconditionally, so a load
// dispatched BEFORE a game switch (game A's Overview/Profiles, possibly
// even read mid-rebind) could land AFTER resolveGameSwitch's reset and
// repopulate the model with the old game's rows while the providers are
// already bound to game B. The fix mirrors the search-gen mechanism
// (searchResultMsg/searchFailedMsg): loads are stamped with m.loadGen at
// dispatch, resolveGameSwitch bumps it, and Update discards a stale-gen
// dataLoadedMsg/loadFailedMsg entirely.
func TestGameSwitchDropsStaleInFlightLoad(t *testing.T) {
	t.Parallel()

	games := []GameInfo{
		{ID: "gameA", Name: "Game A", Active: true},
		{ID: "gameB", Name: "Game B", Active: false},
	}
	provider := &recordingProvider{delegate: NewPrototypeProvider(), ListGamesResult: games}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: &recordingActions{}})
	require.NoError(t, err)

	// Capture (but do not deliver) the initial load's message - this is the
	// in-flight load-A whose result must go stale the moment the switch
	// resets the session.
	staleLoadMsg := model.Init()()
	require.IsType(t, dataLoadedMsg{}, staleLoadMsg)

	updated, _ := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.NotNil(t, model.picker)
	updated, chooseCmd := model.Update(keyRunes("2")) // "Game B", not active
	model = updated.(Model)
	require.NotNil(t, chooseCmd)

	updated, loadCmd := model.Update(chooseCmd())
	model = updated.(Model)
	require.Equal(t, stateLoading, model.state)
	require.NotNil(t, loadCmd)

	// The OLD load's message lands after the reset: it must be discarded
	// whole - no state transition, no repopulated rows.
	updated, _ = model.Update(staleLoadMsg)
	model = updated.(Model)
	require.Equal(t, stateLoading, model.state, "a stale in-flight load must not resolve the post-switch loading state")
	require.Nil(t, model.mods, "a stale in-flight load must not repopulate the old game's rows")

	// The NEW load (dispatched by the switch itself, stamped with the
	// bumped gen) still applies normally.
	updated, _ = model.Update(loadCmd())
	model = updated.(Model)
	require.Equal(t, stateReady, model.state)
}

// TestGameSwitcherListGamesErrorGoesToStatusLine covers openGameSwitcher's
// ListGames-failure branch: the error renders on the status line
// (statusIsError set) and no picker opens - mirroring
// TestFilesKeyErrorGoesToStatusLine's shape for the other synchronous
// provider read.
func TestGameSwitcherListGamesErrorGoesToStatusLine(t *testing.T) {
	t.Parallel()

	provider := &recordingProvider{delegate: NewPrototypeProvider(), ListGamesErr: errors.New("games.yaml unreadable")}
	model := modelWithProvider(t, provider)

	updated, cmd := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
	require.True(t, model.action.statusIsError)
	require.Equal(t, singleLine("games.yaml unreadable"), model.action.status)
}

// TestGameSwitchNoGamesConfiguredStatus covers ListGames returning ZERO
// entries (unreachable via coreProvider, whose session is always bound to
// a configured game, but the message must not lie): "no games configured",
// not "only one game configured".
func TestGameSwitchNoGamesConfiguredStatus(t *testing.T) {
	t.Parallel()

	provider := &recordingProvider{delegate: NewPrototypeProvider(), ListGamesResult: nil}
	model := modelWithProvider(t, provider)

	updated, cmd := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.picker)
	require.Equal(t, "no games configured", model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestGameSwitchResetClosesOpenModals guards the final-review pre-merge
// finding: a deferred gameChosenMsg resolves on the tick AFTER the pick,
// and type-ahead widens that window - another modal (e.g. 'c' on Profiles
// opening the "new profile" input modal) can be up by the time the switch
// resolves. resolveGameSwitch's reset must close it: a modal built against
// the OLD game's data (the input modal's validate closure captured the old
// profile list) operating over reset state bound to the NEW game is
// exactly the stale-capture class the reset exists to prevent.
func TestGameSwitchResetClosesOpenModals(t *testing.T) {
	t.Parallel()

	games := []GameInfo{
		{ID: "gameA", Name: "Game A", Active: true},
		{ID: "gameB", Name: "Game B", Active: false},
	}
	provider := &recordingProvider{delegate: NewPrototypeProvider(), ListGamesResult: games}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: &recordingActions{}})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	// Pick the non-active game; hold its gameChosenMsg undelivered - this
	// is the pick→resolution window.
	updated, _ := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.NotNil(t, model.picker)
	updated, chooseCmd := model.Update(keyRunes("2"))
	model = updated.(Model)
	require.NotNil(t, chooseCmd)
	pendingMsg := chooseCmd()

	// Type-ahead: open the "new profile" input modal before the switch
	// resolves (its validate closure captured the OLD game's profile list).
	model.screen = ScreenProfiles
	updated, _ = model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, model.inputModal, "arrange: the input modal must be up when the switch resolves")

	updated, loadCmd := model.Update(pendingMsg)
	model = updated.(Model)
	require.Equal(t, stateLoading, model.state)
	require.NotNil(t, loadCmd)
	require.Nil(t, model.inputModal, "the reset must close a modal captured against the old game's data")
	require.Nil(t, model.picker)
	require.Nil(t, model.overlay)
}

// --- Prototype demo ---

// TestPrototypeGameSwitchFlipsData proves the switcher visibly works in
// --prototype mode end to end: picking the alt canned game (Data.AltGame)
// flips Overview to report it, with AltMods' own names showing up as the
// Installed Mods list. NewPrototypeModel wires m.provider and m.actions
// from the SAME prototypeProvider instance (see that constructor's doc
// comment), so this also exercises rebindGame's documented double-call onto
// one instance (gameRebinder's own doc comment, actions.go).
func TestPrototypeGameSwitchFlipsData(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	updated, _ := model.Update(keyRunes("g"))
	model = updated.(Model)
	require.NotNil(t, model.picker)

	targetIdx := -1
	for i, opt := range model.picker.options {
		if opt.Note != "active" {
			targetIdx = i
		}
	}
	require.GreaterOrEqual(t, targetIdx, 0, "exactly one option must be the non-active alt game")

	updated, chooseCmd := model.Update(keyRunes(strconv.Itoa(targetIdx + 1)))
	model = updated.(Model)
	require.NotNil(t, chooseCmd)
	gameMsg := chooseCmd()

	updated, loadCmd := model.Update(gameMsg)
	model = updated.(Model)
	require.Equal(t, stateLoading, model.state)
	require.NotNil(t, loadCmd)

	dataMsg := loadCmd()
	updated, _ = model.Update(dataMsg)
	model = updated.(Model)

	require.Equal(t, "Fallout 4", model.summary.GameName)
	require.NotEmpty(t, model.mods)
	names := make([]string, 0, len(model.mods))
	for _, mod := range model.mods {
		names = append(names, mod.Name)
	}
	require.Contains(t, names, "Fallout 4 Script Extender")
}

// --- Prototype mutations after a game switch (Copilot PR #69) ---
//
// Every prototypeProvider mutation/read used to address p.data.InstalledMods
// and p.data.Stats directly, ignoring the altActive binding Task 8's
// Overview flip introduced: after a --prototype game switch, an operation
// either failed "mod not found" (the target lives in AltMods) or silently
// mutated the WRONG game's dataset and corrupted the primary game's canned
// Stats. These tests drive the provider through its own SetGame seam (the
// exact call rebindGame makes) and assert each operation targets the ACTIVE
// game's mods, leaving the primary dataset and Stats untouched.

// prototypeProviderOnAltGame returns a prototypeProvider already switched to
// the alt canned game, plus deep-ish copies of the primary dataset taken
// BEFORE the switch so tests can prove it stayed untouched.
func prototypeProviderOnAltGame(t *testing.T) (*prototypeProvider, []prototype.Mod, prototype.Stats) {
	t.Helper()
	p := NewPrototypeProvider().(*prototypeProvider)
	primaryBefore := make([]prototype.Mod, len(p.data.InstalledMods))
	copy(primaryBefore, p.data.InstalledMods)
	statsBefore := p.data.Stats
	require.NoError(t, p.SetGame(p.data.AltGame.ID))
	return p, primaryBefore, statsBefore
}

func TestPrototypePolicyEditAfterGameSwitchTargetsActiveGame(t *testing.T) {
	t.Parallel()

	p, primaryBefore, _ := prototypeProviderOnAltGame(t)

	_, err := p.SetUpdatePolicy(context.Background(), ModItem{ID: "f4se", Source: "nexusmods", Name: "Fallout 4 Script Extender"}, "pin")
	require.NoError(t, err, "the alt game's own mod must be addressable after the switch")
	require.Equal(t, "pin", p.data.AltMods[0].UpdatePolicy, "the ACTIVE game's row must carry the new policy")
	require.Equal(t, primaryBefore, p.data.InstalledMods, "the primary game's rows must be untouched")
}

func TestPrototypePurgeAfterGameSwitchTargetsActiveGame(t *testing.T) {
	t.Parallel()

	p, primaryBefore, statsBefore := prototypeProviderOnAltGame(t)

	outcome, err := p.PurgeProfile(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "Purged 2 mod(s)", outcome.Message, "must purge the ACTIVE game's 2 mods, not the primary's 5")
	for _, mod := range p.data.AltMods {
		require.Equal(t, "disabled", mod.Status)
	}
	require.Equal(t, primaryBefore, p.data.InstalledMods, "the primary game's rows must be untouched")
	require.Equal(t, statsBefore, p.data.Stats, "the primary game's canned Stats must not be corrupted")
}

func TestPrototypeUninstallAfterGameSwitchTargetsActiveGame(t *testing.T) {
	t.Parallel()

	p, primaryBefore, statsBefore := prototypeProviderOnAltGame(t)

	_, err := p.UninstallMod(context.Background(), ModItem{ID: "f4se", Source: "nexusmods", Name: "Fallout 4 Script Extender"})
	require.NoError(t, err)
	require.Len(t, p.data.AltMods, 1, "the ACTIVE game's list must shrink")
	require.Equal(t, primaryBefore, p.data.InstalledMods, "the primary game's rows must be untouched")
	require.Equal(t, statsBefore, p.data.Stats, "the primary game's canned Stats must not be corrupted")
}

func TestPrototypeEnableAfterGameSwitchLeavesPrimaryStats(t *testing.T) {
	t.Parallel()

	p, primaryBefore, statsBefore := prototypeProviderOnAltGame(t)

	// "unofficial-patch" is canned disabled (see prototype.Load's AltMods).
	_, err := p.EnableMod(context.Background(), ModItem{ID: "unofficial-patch", Source: "nexusmods", Name: "Unofficial Fallout 4 Patch"})
	require.NoError(t, err)
	require.Equal(t, "installed", p.data.AltMods[1].Status, "the ACTIVE game's row must flip")
	require.Equal(t, primaryBefore, p.data.InstalledMods, "the primary game's rows must be untouched")
	require.Equal(t, statsBefore, p.data.Stats,
		"the alt game derives its counts live from AltMods - the primary's canned Stats must not move")
}

func TestPrototypeCheckUpdatesAfterGameSwitchUsesActiveMods(t *testing.T) {
	t.Parallel()

	p, _, _ := prototypeProviderOnAltGame(t)

	view, err := p.CheckUpdates(context.Background())
	require.NoError(t, err)
	require.Empty(t, view.Updates,
		"AltMods carry no AvailableVersion entries - reporting the PRIMARY game's canned updates after the switch is the bug")
}

func TestPrototypeDeployedFilesAfterGameSwitchUsesActiveMods(t *testing.T) {
	t.Parallel()

	p, _, _ := prototypeProviderOnAltGame(t)

	files, err := p.DeployedFiles("nexusmods", "f4se")
	require.NoError(t, err)
	require.Contains(t, files, "Fallout 4 Script Extender.esp",
		"the display-name lookup must resolve against the ACTIVE game's mods, not fall back to the raw mod ID")

	outcome, err := p.DeployProfile(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Deployed 1 mod(s)", outcome.Message,
		"deploy must count the ACTIVE game's enabled mods (1 of 2), not the primary's")
}
