package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// --- Toggle enable/disable ('e' on Installed Mods) ---

// TestToggleEnableKeyOnDisabledModPromptsEnable covers the full round trip
// for the enable direction: 'e' on a disabled mod builds+shows the Enable
// modal (nothing calls the provider yet), 'y' dispatches EnableMod with the
// SELECTED item, and the resulting actionDoneMsg triggers a refresh.
func TestToggleEnableKeyOnDisabledModPromptsEnable(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{EnableOutcome: ActionOutcome{Message: `Enabled "Alternate Start"`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 4 // "Alternate Start", status "disabled"
	require.Equal(t, "disabled", model.mods[4].Status)

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionEnable, model.action.pending.kind)
	require.Equal(t, `Enable "Alternate Start"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Game: Skyrim Special Edition")
	require.Contains(t, model.action.pending.detail, "Profile: survival")
	require.Contains(t, model.action.pending.detail, "Files will be deployed to the game directory.")
	require.Empty(t, rec.EnableCalls, "nothing must mutate before confirm")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.Nil(t, model.action.pending)
	require.NotNil(t, confirmCmd)

	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Len(t, rec.EnableCalls, 1)
	require.Equal(t, "alternate-start", rec.EnableCalls[0].ID)
	require.Equal(t, "nexusmods", rec.EnableCalls[0].Source)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Enabled "Alternate Start"`, model.action.status)
	require.IsType(t, dataLoadedMsg{}, refreshCmd())
}

// TestToggleEnableKeyOnEnabledModPromptsDisable covers the disable
// direction: any status other than "disabled" toggles to Disable.
func TestToggleEnableKeyOnEnabledModPromptsDisable(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{DisableOutcome: ActionOutcome{Message: `Disabled "SkyUI"`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI", status "installed"
	require.NotEqual(t, "disabled", model.mods[0].Status)

	updated, _ := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDisable, model.action.pending.kind)
	require.Equal(t, `Disable "SkyUI"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Files will be removed from the game directory (cache kept).")

	_, confirmCmd := model.Update(keyRunes("y"))
	require.NotNil(t, confirmCmd)
	runActionCmd(t, confirmCmd)
	require.Len(t, rec.DisableCalls, 1)
	require.Equal(t, "skyui", rec.DisableCalls[0].ID)
}

// TestToggleEnableKeyCancelDoesNotCallProvider proves 'n' dismisses the
// modal without ever touching the provider.
func TestToggleEnableKeyCancelDoesNotCallProvider(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 4

	updated, _ := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("n"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.EnableCalls)
	require.Empty(t, rec.DisableCalls)
}

// TestToggleEnableKeyWrongScreenIsNoop proves 'e' only fires on Installed
// Mods.
func TestToggleEnableKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

// TestToggleEnableKeyEmptyListIsNoop proves an empty mods list can never
// crash or open a modal for a nonexistent selection.
func TestToggleEnableKeyEmptyListIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.mods = nil
	model.selected[ScreenInstalledMods] = 0

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

// TestToggleEnableKeyInertWhileRunning proves the single-flight guard: an
// in-flight action blocks a brand-new prompt, verified via m.action.pending
// staying nil (buildAction's guard-refusal returns a no-op pendingAction
// rather than a nil return value - see actions.go's doc comment).
func TestToggleEnableKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 4
	model.action.running = true

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.EnableCalls)
}

// TestToggleEnableKeyInertWhileAnotherModalPending proves a DIFFERENT
// already-pending modal is left completely undisturbed by a second
// mutation key.
func TestToggleEnableKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("D")) // opens the Deploy modal
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind)

	updated, cmd := model.Update(keyRunes("e"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.EnableCalls)
	require.Empty(t, rec.DisableCalls)
}

// TestToggleEnableKeyInertWhileSearchFocused proves 'e' types into the
// search box instead of triggering a mutation while ScreenSearch is
// focused - the existing focused-input swallow branch runs before the
// mutation-key switch, so this is inertness by construction, not a special
// case.
func TestToggleEnableKeyInertWhileSearchFocused(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "e")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "e")
	require.Nil(t, updated.action.pending)
}

// --- Uninstall ('x' on Installed Mods) ---

func TestUninstallKeyPromptsAndConfirmCallsProviderWithSelectedItem(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UninstallOutcome: ActionOutcome{Message: `Uninstalled "SkyUI"`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // "SkyUI"

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionUninstall, model.action.pending.kind)
	require.Equal(t, `Uninstall "SkyUI"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Removes deployed files, cache, and profile entry. Uninstall hooks will run.")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.Len(t, rec.UninstallCalls, 1)
	require.Equal(t, "skyui", rec.UninstallCalls[0].ID)
	require.Equal(t, "nexusmods", rec.UninstallCalls[0].Source)

	updated, refreshCmd := model.Update(doneMsg)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Uninstalled "SkyUI"`, updated.(Model).action.status)
}

func TestUninstallKeyCancelDoesNotCallProvider(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0

	updated, _ := model.Update(keyRunes("x"))
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.UninstallCalls)
}

func TestUninstallKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenProfiles

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

func TestUninstallKeyEmptyListIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenInstalledMods
	model.mods = nil

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

func TestUninstallKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	model.action.running = true

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.UninstallCalls)
}

// --- Deploy ('D' on Dashboard and Installed Mods) ---

func TestDeployKeyFromDashboardPromptsAndConfirmCallsProvider(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{DeployOutcome: ActionOutcome{Message: "Deployed 5 mod(s)"}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind)
	require.Equal(t, `Deploy profile "survival"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Game: Skyrim Special Edition")
	require.Contains(t, model.action.pending.detail, "Mods: 39 enabled")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.Equal(t, 1, rec.DeployCalls)

	updated, refreshCmd := model.Update(doneMsg)
	require.NotNil(t, refreshCmd)
	require.Equal(t, "Deployed 5 mod(s)", updated.(Model).action.status)
}

func TestDeployKeyFromInstalledModsPrompts(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods

	updated, _ := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, actionDeploy, model.action.pending.kind)
}

func TestDeployKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenSearch, ScreenProfiles, ScreenSources} {
		model := modelWithActions(t, &recordingActions{})
		model.screen = screen

		updated, cmd := model.Update(keyRunes("D"))
		model = updated.(Model)
		require.Nil(t, cmd)
		require.Nil(t, model.action.pending, "screen %v", screen)
	}
}

func TestDeployKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model.action.running = true

	updated, cmd := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Equal(t, 0, rec.DeployCalls)
}

// --- Profile switch ('enter' on Profiles) ---

func TestSwitchKeyOnActiveProfileSetsStatusNoModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 0 // "survival", active
	require.True(t, model.profiles[0].Active)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Equal(t, `Already on profile "survival"`, model.action.status)
	require.False(t, model.action.statusIsError)
	require.Empty(t, rec.PlanCalls, "must never call PlanProfileSwitch for the active profile")
}

func TestSwitchKeyEmptyProfileListIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenProfiles
	model.profiles = nil

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
}

func TestSwitchKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenInstalledMods

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenInstalledMods, updated.(Model).CurrentScreen())
	require.Nil(t, cmd)
}

func TestSwitchKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1
	model.action.running = true

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.PlanCalls)
}

// TestSwitchKeyDispatchesAsyncPlanFetch proves enter on a non-active
// profile shows a "Planning switch…" status and returns a command instead
// of synchronously calling the provider.
func TestSwitchKeyDispatchesAsyncPlanFetch(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{PlanView: SwitchPlanView{From: "survival", To: "vanilla-plus", NoChanges: true}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1 // "vanilla-plus"

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)
	require.Equal(t, "Planning switch…", model.action.status)
	require.False(t, model.action.statusIsError)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.PlanCalls, "the provider call happens when the returned cmd runs, not synchronously")

	msg := cmd()
	require.IsType(t, planResultMsg{}, msg)
	require.Equal(t, []string{"vanilla-plus"}, rec.PlanCalls)
}

// TestSwitchPlanStaleResultDiscarded proves a plan result tagged with an
// old gen is discarded entirely - no modal, no status change, no running
// flip - mirroring actionDoneMsg's own staleness contract (rule 4 in
// actions_test.go).
func TestSwitchPlanStaleResultDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true
	model.action.status = ""

	updated, cmd := model.Update(planResultMsg{gen: 4, view: SwitchPlanView{From: "a", To: "b"}})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running, "stale result must not clear running")
	require.Nil(t, m.action.pending, "stale result must never open a modal")
	require.Empty(t, m.action.status)
}

func TestSwitchPlanStaleFailureDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true

	updated, cmd := model.Update(planFailedMsg{gen: 1, err: errors.New("boom")})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running)
	require.Empty(t, m.action.status)
}

// TestSwitchPlanErrorShowsStatusNoModal covers the plan-error path end to
// end: enter -> async fetch -> planFailedMsg -> error status, no modal.
func TestSwitchPlanErrorShowsStatusNoModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{PlanErr: errors.New("plan boom")}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, planFailedMsg{}, msg)

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "plan boom")
	require.Nil(t, model.action.pending)
}

// TestSwitchPlanAlreadyActiveDefensive exercises resolvePlanResult's
// defensive AlreadyActive branch directly (see task-7-brief.md's
// profile-switch flow: normally pre-filtered by the synchronous active
// check in the key handler, so this path is otherwise unreachable through
// a real keypress).
func TestSwitchPlanAlreadyActiveDefensive(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 1
	model.action.running = true

	updated, cmd := model.Update(planResultMsg{gen: 1, view: SwitchPlanView{From: "x", To: "x", AlreadyActive: true}})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.False(t, m.action.running)
	require.Nil(t, m.action.pending)
	require.Equal(t, `Already on profile "x"`, m.action.status)
	require.False(t, m.action.statusIsError)
}

// TestSwitchDetailLinesIncludesDownloadDisclosureForNeedsDownloads guards
// the Task 4 review finding: switchDetailLines rendered only the
// Enable/Disable/NoChanges buckets, never NeedsDownloads - since Task 4
// lifted the switch refusal, a purely-downloading profile switch opened its
// confirmation modal with NO indication that confirming starts network
// downloads, less disclosure than the CLI's own "Will install N mod(s):" +
// per-ref lines before its Proceed? prompt (cmd/lmm/profile.go's
// doProfileSwitch, the fidelity reference). switchDetailLines must append a
// download-disclosure header plus one line per ref, in the SAME "%s:%s
// v%s" display form switchPlanView already produces, when NeedsDownloads is
// non-empty.
func TestSwitchDetailLinesIncludesDownloadDisclosureForNeedsDownloads(t *testing.T) {
	t.Parallel()

	lines := switchDetailLines(SwitchPlanView{
		From:           "survival",
		To:             "vanilla-plus",
		NeedsDownloads: []string{"nexusmods:foo v1.0", "nexusmods:bar v2.0"},
	})

	require.Contains(t, lines, "From: survival")
	require.Contains(t, lines, "Will download & install 2 mod(s):")
	require.Contains(t, lines, "↓ nexusmods:foo v1.0")
	require.Contains(t, lines, "↓ nexusmods:bar v2.0")
}

// TestSwitchPlanNeedsDownloadsOpensModalAndAppliesOnConfirm covers the
// Phase 5b Task 4 switch-refusal LIFT at the Model level: a plan with
// NeedsDownloads entries used to short-circuit to a no-modal refusal status
// (see this test's prior form, formerly named
// TestSwitchPlanNeedsDownloadsRefusesNoModal, in this file's git history);
// it now opens the SAME confirmation modal as any other plan, its detail
// lines carry the download disclosure (see
// TestSwitchDetailLinesIncludesDownloadDisclosureForNeedsDownloads above),
// and confirming it calls ApplyProfileSwitch with a progress callback (the
// pump), exactly like the NoChanges/Enable-Disable cases below.
func TestSwitchPlanNeedsDownloadsOpensModalAndAppliesOnConfirm(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		PlanView:     SwitchPlanView{From: "survival", To: "vanilla-plus", NeedsDownloads: []string{"nexusmods:foo v1.0"}},
		ApplyOutcome: ActionOutcome{Message: `Switched to "vanilla-plus"`},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg := cmd()

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.NotNil(t, model.action.pending, "a NeedsDownloads plan must now open the confirmation modal like any other")
	require.Equal(t, actionSwitch, model.action.pending.kind)
	require.Contains(t, model.action.pending.detail, "Will download & install 1 mod(s):",
		"the modal must disclose that confirming starts a network download, not just show Enable/Disable")
	require.Contains(t, model.action.pending.detail, "↓ nexusmods:foo v1.0")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, []string{"vanilla-plus"}, rec.ApplyCalls, "confirming must call ApplyProfileSwitch")
}

// erroringModSource is a minimal source.ModSource whose GetMod always fails,
// used below to drive a REAL per-mod install failure through
// core.Service.ApplyProfileSwitch's install loop against a real
// coreProvider/coreActions pair (as opposed to recordingActions, which only
// replays canned outcomes and so can't exercise the Fix wave 2 fix itself).
// Mirrors service_core_test.go's stubSource/netSource (package tui_test,
// unreachable from this package tui test file - see customSourceType's doc
// comment in service_core.go for why cross-package test doubles aren't
// shared) and sources_view_test.go's builtinStubSource (package tui, same
// file split, narrower stub).
type erroringModSource struct {
	id  string
	err error
}

func (s *erroringModSource) ID() string      { return s.id }
func (s *erroringModSource) Name() string    { return "Erroring Source" }
func (s *erroringModSource) AuthURL() string { return "" }
func (s *erroringModSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *erroringModSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, errors.New("not implemented")
}
func (s *erroringModSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, s.err
}
func (s *erroringModSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, errors.New("not implemented")
}
func (s *erroringModSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, errors.New("not implemented")
}
func (s *erroringModSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", errors.New("not implemented")
}
func (s *erroringModSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, errors.New("not implemented")
}

// TestSwitchConfirmSurfacesInstallFailureWarningInStatusLine is the
// Model-level RED test for the Fix wave 2 review finding's item 3(c): once
// coreProvider.ApplyProfileSwitch correctly folds a failed per-mod install
// into ActionOutcome.Warnings (the sandbox-level RED test in
// service_core_test.go), the existing formatOutcomeStatus/actionDoneMsg
// machinery (actions.go/app.go, already proven by
// TestSwitchConfirmCallsApplyProfileSwitchWithTargetName et al.) must render
// it in the one-line status suffix with no further wiring - this drives a
// REAL coreProvider/coreActions pair (not recordingActions) through the full
// confirm -> ApplyProfileSwitch -> actionDoneMsg round trip to prove that
// end to end. Against the pre-fix implementation this fails: the status line
// reads bare `Switched to "vanilla-plus"` with no warning suffix at all,
// silently hiding the failed install (the finding's exact symptom).
func TestSwitchConfirmSurfacesInstallFailureWarningInStatusLine(t *testing.T) {
	t.Parallel()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID: "test-game", Name: "Test Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	// "vanilla-plus" matches modelWithActions' prototype.Profile at index 1,
	// used only to keep the plan-fetch's target name recognizable if this
	// test is read alongside the recordingActions-backed switch tests above
	// - the profile list itself comes from THIS sandbox's real coreProvider,
	// found by name below rather than assumed by index.
	_, err = pm.Create(game.ID, "vanilla-plus")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "vanilla-plus", domain.ModReference{SourceID: "src", ModID: "modZ", Version: "1.0"}))

	svc.RegisterSource(&erroringModSource{id: "src", err: errors.New("connection refused")})

	provider := NewCoreProvider(svc, game, "default")
	actions := NewCoreActions(svc, game, "default")

	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: actions})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	idx := -1
	for i, p := range model.profiles {
		if p.Name == "vanilla-plus" {
			idx = i
		}
	}
	require.GreaterOrEqualf(t, idx, 0, "the real sandbox profile list must include vanilla-plus")
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = idx

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending, "a NeedsDownloads plan must open the confirmation modal")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)

	updated, _ = model.Update(doneMsg)
	model = updated.(Model)
	require.False(t, model.action.statusIsError)
	require.Contains(t, model.action.status, `Switched to "vanilla-plus"`)
	require.Contains(t, model.action.status, "modZ",
		"a failed per-mod install during the switch must surface in the status line, not vanish silently")
}

// TestSwitchPlanNoChangesShowsSetAsDefaultModal covers the NoChanges
// wording mandated by task-7-brief.md.
func TestSwitchPlanNoChangesShowsSetAsDefaultModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{PlanView: SwitchPlanView{From: "survival", To: "vanilla-plus", NoChanges: true}}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Equal(t, actionSwitch, model.action.pending.kind)
	require.Equal(t, `Switch to "vanilla-plus"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "From: survival")
	require.Contains(t, model.action.pending.detail, "No mod changes; set as default.")
}

// TestSwitchConfirmCallsApplyProfileSwitchWithTargetName covers the full
// happy path: plan with real Enable/Disable buckets -> modal detail lines
// -> confirm -> ApplyProfileSwitch(target) -> refresh.
func TestSwitchConfirmCallsApplyProfileSwitchWithTargetName(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		PlanView:     SwitchPlanView{From: "survival", To: "vanilla-plus", Enable: []string{"Frostfall"}, Disable: []string{"SkyUI"}},
		ApplyOutcome: ActionOutcome{Message: `Switched to "vanilla-plus"`},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Contains(t, model.action.pending.detail, "+ Frostfall")
	require.Contains(t, model.action.pending.detail, "- SkyUI")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, []string{"vanilla-plus"}, rec.ApplyCalls)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Switched to "vanilla-plus"`, model.action.status)
}

// TestPrototypeSwitchPlanNeedsDownloadsCannedScenario exercises the Phase
// 5b Task 4 switch-refusal LIFT end to end through the REAL
// prototypeProvider (not recordingActions): a canned profile whose mod list
// references an ID absent from InstalledMods used to drive a no-modal
// refusal (see this test's prior form, in this file's git history); it now
// opens the confirmation modal like any other plan, and confirming it
// actually completes the switch (prototypeProvider.ApplyProfileSwitch's own
// working download demo - actions_provider.go).
func TestPrototypeSwitchPlanNeedsDownloadsCannedScenario(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenProfiles

	idx := -1
	for i, p := range model.profiles {
		if p.Name == prototype.NeedsDownloadProfileName {
			idx = i
		}
	}
	require.GreaterOrEqualf(t, idx, 0, "canned needs-download profile %q must be present in prototype data", prototype.NeedsDownloadProfileName)
	model.selected[ScreenProfiles] = idx

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, planResultMsg{}, msg, "a needs-downloads plan is a successful plan fetch, not a fetch error")

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.NotNil(t, model.action.pending, "a NeedsDownloads plan must now open the confirmation modal like any other")
	require.Contains(t, model.action.pending.detail, "Will download & install 1 mod(s):",
		"the modal must disclose that confirming starts a network download, not just show Enable/Disable")
	require.Contains(t, model.action.pending.detail, "↓ nexusmods:requiem-legendary v1.0")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)

	updated, _ = model.Update(doneMsg)
	model = updated.(Model)
	require.False(t, model.action.statusIsError)
	require.Equal(t, fmt.Sprintf("Switched to %q", prototype.NeedsDownloadProfileName), model.action.status)
}

// --- Profile rebind after switch (C1) ---

// fakeSwitchableProvider is a minimal DataProvider that also implements the
// profileRebinder hook (see app.go's rebindProfile), letting these tests
// simulate coreProvider's per-instance p.profile binding - including the
// two-SEPARATE-instances wiring cmd/lmm/tui.go actually uses
// (NewCoreProvider and NewCoreActions each build their own *coreProvider;
// see their doc comments in service_core.go) - without a real
// core.Service/SQLite sandbox (that integration is covered directly by
// service_core_test.go). Profiles()'s Active flag is derived from
// `current`, which only SetProfile ever changes, so a test can prove
// app.go's rebindProfile is what moved it, not some other code path.
type fakeSwitchableProvider struct {
	names   []string
	current string
}

func (f *fakeSwitchableProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{GameName: "Game", ProfileName: f.current}, nil, nil
}

func (f *fakeSwitchableProvider) Sources() []string         { return nil }
func (f *fakeSwitchableProvider) SourceInfos() []SourceInfo { return nil }

func (f *fakeSwitchableProvider) Search(context.Context, string, string, int) (SearchPage, error) {
	return SearchPage{}, nil
}

func (f *fakeSwitchableProvider) Profiles(context.Context) ([]ProfileItem, error) {
	items := make([]ProfileItem, 0, len(f.names))
	for _, name := range f.names {
		items = append(items, ProfileItem{Name: name, Active: name == f.current})
	}
	return items, nil
}

// SetProfile implements app.go's profileRebinder hook.
func (f *fakeSwitchableProvider) SetProfile(name string) { f.current = name }

// TestSwitchDoneRebindsProviderSoOldActiveProfileReopensPlanModal guards
// finding C1 end to end at the Model level: a completed switch must rebind
// which profile the DataProvider reports Active BEFORE the post-action
// refresh reads it, so Profiles() reflects the target immediately - and a
// second 'enter' on the now-inactive OLD profile must dispatch a fresh plan
// fetch instead of hitting switchSelectedProfile's Already-on-profile
// pre-filter (mutations.go). That pre-filter dead end is exactly what C1
// reported: without the rebind, the old profile keeps reading Active
// forever and the user can never switch back to it.
func TestSwitchDoneRebindsProviderSoOldActiveProfileReopensPlanModal(t *testing.T) {
	t.Parallel()

	provider := &fakeSwitchableProvider{names: []string{"survival", "vanilla-plus"}, current: "survival"}
	rec := &recordingActions{
		PlanView:     SwitchPlanView{From: "survival", To: "vanilla-plus", NoChanges: true},
		ApplyOutcome: ActionOutcome{Message: `Switched to "vanilla-plus"`},
	}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: rec})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenProfiles
	model.selected[ScreenProfiles] = 1 // "vanilla-plus", not active yet
	require.False(t, model.profiles[1].Active)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	require.NotNil(t, model.action.pending, "a NoChanges plan still opens a confirmation modal")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, "vanilla-plus", provider.current, "the actionDoneMsg handler must rebind the provider before the refresh runs")

	updated, _ = model.Update(refreshCmd())
	model = updated.(Model)
	require.True(t, model.profiles[1].Active, "Profiles() must mark the switch target Active after refresh")
	require.False(t, model.profiles[0].Active, "the old profile must no longer read as active")

	model.selected[ScreenProfiles] = 0 // "survival", the former active profile
	updated, cmd2 := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd2, "must dispatch an async plan fetch for the now-inactive old profile, not hit the Already-on-profile dead end")
	require.NotEqual(t, `Already on profile "survival"`, model.action.status)
}

// TestStaleSwitchDoneMsgNeverRebindsProfile extends Rule 4's staleness
// guard (actions_test.go) to the switchedTo field added for C1: a
// superseded actionDoneMsg must be discarded whole, including its switch
// target - a stale rebind would silently move the session to a profile the
// user never actually finished switching to.
func TestStaleSwitchDoneMsgNeverRebindsProfile(t *testing.T) {
	t.Parallel()

	provider := &fakeSwitchableProvider{names: []string{"survival", "vanilla-plus"}, current: "survival"}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Actions: &recordingActions{}})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.action.gen = 5
	model.action.running = true

	updated, cmd := model.Update(actionDoneMsg{
		gen: 4, kind: actionSwitch,
		outcome:    ActionOutcome{Message: `Switched to "vanilla-plus"`},
		switchedTo: "vanilla-plus",
	})
	m := updated.(Model)
	require.Nil(t, cmd, "a stale result must not dispatch a refresh")
	require.True(t, m.action.running, "stale result must not clear running")
	require.Equal(t, "survival", provider.current, "a stale switchedTo must never rebind the provider")
}

// --- Help/footer content ---

func TestHelpOverlayDocumentsMutationKeysAndDropsStaleReadOnlyClaim(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 120, 38)
	model = updateWithRunes(t, model, "?")
	view := model.View()

	require.Contains(t, view, "toggle enable/disable")
	require.Contains(t, view, "uninstall selected mod")
	require.Contains(t, view, "deploy active profile")
	require.Contains(t, view, "switch profile")
	require.NotContains(t, view, "Browsing is read-only",
		"the help copy must no longer claim the TUI is read-only now that mutations exist")
}

// TestFooterHintNamesEachMutationAction covers Finding 3 (smoke test): the
// old "e/x/D: mutate" hint named the keys but never said what they DO. The
// footer must spell out each action explicitly instead. Per the smoke
// tester's follow-up guidance, 160 columns (not 80) is the normal case to
// design and assert full clarity against — narrower terminals degrade via
// truncation (see TestFooterFitsNarrowTerminalViaTruncation) rather than by
// cramming the wording.
func TestFooterHintNamesEachMutationAction(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	view := model.View()

	require.Contains(t, view, "e: enable/disable")
	require.Contains(t, view, "x: uninstall")
	require.Contains(t, view, "D: deploy")
	require.Contains(t, view, "enter: switch")
	require.NotContains(t, view, "mutate",
		"the terse, unexplained 'mutate' wording must be gone")
}

// TestFooterFitsNarrowTerminalViaTruncation proves the footer degrades
// gracefully at a narrower terminal: it must be hard-truncated to the
// available width rather than left to lipgloss's automatic word-wrap, which
// would silently grow the view past its fixed-height budget (see
// contentChromeHeight's footerHeight == 1 assumption).
func TestFooterFitsNarrowTerminalViaTruncation(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 80, 24)
	view := model.View()

	require.Equal(t, 80, lipgloss.Width(view))
	require.Equal(t, 24, lipgloss.Height(view))
}

// --- Install from search ('i' on Search, blurred, a result selected) ---

// searchReadyModel returns a Model on ScreenSearch with a searchReady page
// (input blurred, textinput's default focus state) containing results - the
// precondition installSelectedSearchResult requires.
func searchReadyModel(t *testing.T, actions ActionProvider, results []ModItem) Model {
	t.Helper()
	model := modelWithActions(t, actions)
	model.screen = ScreenSearch
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", PageSize: 10, Results: results}
	model.selected[ScreenSearch] = 0
	return model
}

// TestInstallKeyDispatchesAsyncPlanFetch proves 'i' on a selected search
// result shows a "Planning install…" status and returns a command instead
// of synchronously calling the provider - mirroring
// TestSwitchKeyDispatchesAsyncPlanFetch's shape for switch.
func TestInstallKeyDispatchesAsyncPlanFetch(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{InstallPlanViewOut: InstallPlanView{Name: "Campfire", Version: "1.12", Source: "nexusmods", SizeLabel: "4.5 MB"}}
	model := searchReadyModel(t, rec, []ModItem{item})

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)
	require.Equal(t, "Planning install…", model.action.status)
	require.False(t, model.action.statusIsError)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.PlanInstallCalls, "the provider call happens when the returned cmd runs, not synchronously")

	msg := cmd()
	require.IsType(t, installPlanResultMsg{}, msg)
	require.Equal(t, []ModItem{item}, rec.PlanInstallCalls)
}

// TestInstallKeyOpensPlanModalFreshItem covers the fresh-item modal variant:
// title "Install ...?" and the version/size/source/files detail lines.
func TestInstallKeyOpensPlanModalFreshItem(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{InstallPlanViewOut: InstallPlanView{
		Name: "Campfire", Version: "1.12", Source: "nexusmods", SizeLabel: "4.5 MB",
		Files: []string{"campfire.7z"},
	}}
	model := searchReadyModel(t, rec, []ModItem{item})

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	msg := cmd()
	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)

	require.NotNil(t, model.action.pending)
	require.Equal(t, actionInstall, model.action.pending.kind)
	require.Equal(t, `Install "Campfire"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Version: 1.12 (4.5 MB)")
	require.Contains(t, model.action.pending.detail, "Source: nexusmods")
	require.Contains(t, model.action.pending.detail, "Files: campfire.7z")
}

// TestInstallKeyOpensReinstallModalForAlreadyInstalledItem covers the
// Reinstall title variant: the same 'i' key on an item InstallPlanView
// reports as already installed.
func TestInstallKeyOpensReinstallModalForAlreadyInstalledItem(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"}
	rec := &recordingActions{InstallPlanViewOut: InstallPlanView{Name: "SkyUI", Version: "5.3", Source: "nexusmods", SizeLabel: "size unknown", Reinstall: true}}
	model := searchReadyModel(t, rec, []ModItem{item})

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Equal(t, `Reinstall "SkyUI"?`, model.action.pending.title)
}

// TestInstallDetailLinesIncludesDependenciesDownloadDisclosure and its
// siblings below exercise installDetailLines directly for each modal
// content variant task-5-brief.md enumerates, independent of the async key
// flow above.
func TestInstallDetailLinesIncludesDependenciesDownloadDisclosure(t *testing.T) {
	t.Parallel()

	lines := installDetailLines(InstallPlanView{
		Version: "1.0", SizeLabel: "1.2 MiB", Source: "nexusmods",
		Files:        []string{"mod.7z"},
		Dependencies: []string{"SKSE64 v2.0", "Address Library v1.0"},
	})

	require.Contains(t, lines, "Dependencies: SKSE64 v2.0, Address Library v1.0")
	require.Contains(t, lines, "Will download & install 2 mod(s)")
}

func TestInstallDetailLinesIncludesConflictLines(t *testing.T) {
	t.Parallel()

	lines := installDetailLines(InstallPlanView{
		Version: "1.0", SizeLabel: "1.2 MiB", Source: "nexusmods",
		Conflicts: []string{"textures/frost.dds (owned by ussep)", "meshes/foo.nif (owned by bar)"},
	})

	require.Contains(t, lines, "Conflicts: textures/frost.dds (owned by ussep)")
	require.Contains(t, lines, "Conflicts: meshes/foo.nif (owned by bar)")
}

func TestInstallDetailLinesIncludesMissingDependencyWarning(t *testing.T) {
	t.Parallel()

	lines := installDetailLines(InstallPlanView{
		Version: "1.0", SizeLabel: "1.2 MiB", Source: "nexusmods",
		MissingDependencies: []string{"nexusmods:missing-dep"},
	})

	require.Contains(t, lines, "⚠ 1 dependency(ies) unavailable")
}

func TestInstallDetailLinesIncludesCycleWarning(t *testing.T) {
	t.Parallel()

	lines := installDetailLines(InstallPlanView{
		Version: "1.0", SizeLabel: "1.2 MiB", Source: "nexusmods",
		CycleWarning: true,
	})

	require.Contains(t, lines, "⚠ circular dependency detected")
}

// TestInstallConfirmCallsApplyInstallWithSelectedItemThenRefreshes covers
// the full happy path: plan -> modal -> confirm -> ApplyInstall(SELECTED
// item) -> refresh.
func TestInstallConfirmCallsApplyInstallWithSelectedItemThenRefreshes(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{
		InstallPlanViewOut:  InstallPlanView{Name: "Campfire", Version: "1.12", Source: "nexusmods", SizeLabel: "4.5 MB"},
		ApplyInstallOutcome: ActionOutcome{Message: `Installed "Campfire"`},
	}
	model := searchReadyModel(t, rec, []ModItem{item})

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, []ModItem{item}, rec.ApplyInstallCalls)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Installed "Campfire"`, model.action.status)
}

// TestInstallConfirmIsImmuneToSelectionDriftWhilePlanIsPending is a
// regression/refactor guard for installPlanResultMsg's own doc comment: item
// is captured in installSelectedSearchResult's dispatch closure and carried
// unchanged through resolveInstallPlanResult, NOT re-read from
// m.selected[ScreenSearch] on arrival. moveSelection (app.go's Down key
// handler) is never gated on m.action.running, so a user is free to move the
// cursor while "Planning install…" is in flight; this pins that doing so
// cannot change which mod ends up installed. Select result A, press 'i',
// move the cursor to result B before resolving the pending plan fetch,
// confirm, and assert ApplyInstall was called with A - never B.
func TestInstallConfirmIsImmuneToSelectionDriftWhilePlanIsPending(t *testing.T) {
	t.Parallel()

	itemA := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	itemB := ModItem{ID: "frostfall", Source: "nexusmods", Name: "Frostfall"}
	rec := &recordingActions{
		InstallPlanViewOut:  InstallPlanView{Name: "Campfire", Version: "1.12", Source: "nexusmods", SizeLabel: "4.5 MB"},
		ApplyInstallOutcome: ActionOutcome{Message: `Installed "Campfire"`},
	}
	model := searchReadyModel(t, rec, []ModItem{itemA, itemB})
	require.Equal(t, 0, model.selected[ScreenSearch], "A must be selected at dispatch time")

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running, "plan fetch is in flight, not yet resolved")

	// Selection drift while the plan fetch is pending: moveSelection isn't
	// gated on m.action.running, so this must succeed - and must not affect
	// which item the eventual install targets.
	updated, moveCmd := model.Update(keyRunes("j"))
	model = updated.(Model)
	require.Nil(t, moveCmd)
	require.Equal(t, 1, model.selected[ScreenSearch], "cursor moved to B while the plan fetch was pending")

	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, `Install "Campfire"?`, model.action.pending.title, "the resolved plan is still A's, not B's")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)

	require.Equal(t, []ModItem{itemA}, rec.ApplyInstallCalls, "ApplyInstall must target A (selected at dispatch), never B (selected at resolution)")
	require.Equal(t, []ModItem{itemA}, rec.PlanInstallCalls, "no second PlanInstall was triggered by the selection drift")
}

func TestInstallCancelDoesNotCallApplyInstall(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{InstallPlanViewOut: InstallPlanView{Name: "Campfire"}}
	model := searchReadyModel(t, rec, []ModItem{item})

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd2 := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.ApplyInstallCalls)
}

// --- Install: inert-key matrix ---

// TestInstallKeyTypesIntoFocusedSearchInput proves 'i' types into the search
// box instead of triggering an install while ScreenSearch is focused -
// mirrors TestToggleEnableKeyInertWhileSearchFocused's shape.
func TestInstallKeyTypesIntoFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "i")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "i")
	require.Nil(t, updated.action.pending)
}

func TestInstallKeyEmptyResultsIsNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := searchReadyModel(t, rec, nil)

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.False(t, model.action.running)
	require.Empty(t, rec.PlanInstallCalls)
}

func TestInstallKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenInstalledMods, ScreenProfiles, ScreenSources} {
		rec := &recordingActions{}
		model := modelWithActions(t, rec)
		model.screen = screen

		updated, cmd := model.Update(keyRunes("i"))
		model = updated.(Model)
		require.Nil(t, cmd, "screen %v", screen)
		require.False(t, model.action.running, "screen %v", screen)
		require.Empty(t, rec.PlanInstallCalls, "screen %v", screen)
	}
}

func TestInstallKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{}
	model := searchReadyModel(t, rec, []ModItem{item})
	model.action.running = true

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.PlanInstallCalls)
}

func TestInstallKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{}
	model := searchReadyModel(t, rec, []ModItem{item})
	// A pending action from a different flow (Deploy, a convenient
	// stand-in - any pending modal must block a new 'i') must remain
	// untouched.
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.PlanInstallCalls)
}

// --- Install: stale plan-fetch discard + plan error ---

// TestInstallPlanStaleResultDiscarded mirrors TestSwitchPlanStaleResultDiscarded.
func TestInstallPlanStaleResultDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true
	model.action.status = ""

	updated, cmd := model.Update(installPlanResultMsg{gen: 4, item: ModItem{ID: "x"}, view: InstallPlanView{Name: "X"}})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running, "stale result must not clear running")
	require.Nil(t, m.action.pending, "stale result must never open a modal")
	require.Empty(t, m.action.status)
}

func TestInstallPlanStaleFailureDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true

	updated, cmd := model.Update(installPlanFailedMsg{gen: 1, err: errors.New("boom")})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running)
	require.Empty(t, m.action.status)
}

// TestInstallPlanErrorShowsStatusNoModal covers the plan-error path end to
// end: 'i' -> async fetch -> installPlanFailedMsg -> error status, no modal.
// err here mirrors the per-action mapped message coreProvider.PlanInstall
// composes (mapInstallNetworkError, service_core_test.go's own tests cover
// the mapping itself) - this proves only that the mapped text reaches the
// status line unmodified.
func TestInstallPlanErrorShowsStatusNoModal(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{PlanInstallErr: errors.New(`source "local-mods" does not support installing; use 'lmm install --source local-mods --id campfire' from a shell`)}
	model := searchReadyModel(t, rec, []ModItem{item})

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, installPlanFailedMsg{}, msg)

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "does not support installing")
	require.Nil(t, model.action.pending)
}

// --- Install: progress streaming ---

// TestInstallProgressStreamsIntoStatusLine proves ApplyInstall's progress
// ticks (recordingActions.ApplyInstallTicks - the Task 4 replay fake) reach
// the status line through the SAME pump every other network action uses
// (actions.go's Rule 11 machinery, already exhaustively tested there); this
// only proves the install key's own confirm closure actually wires the
// progress parameter through, end to end.
func TestInstallProgressStreamsIntoStatusLine(t *testing.T) {
	t.Parallel()

	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}
	rec := &recordingActions{
		InstallPlanViewOut:  InstallPlanView{Name: "Campfire"},
		ApplyInstallOutcome: ActionOutcome{Message: `Installed "Campfire"`},
		ApplyInstallTicks:   []ActionProgress{{Line: "Installing Campfire: 42%", Percent: 42}},
	}
	model := searchReadyModel(t, rec, []ModItem{item})

	updated, cmd := model.Update(keyRunes("i"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	batchMsg := confirmCmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, 2)

	actionMsg := batch[0]()
	require.IsType(t, actionDoneMsg{}, actionMsg)

	progressMsg := batch[1]()
	updated, _ = model.Update(progressMsg)
	model = updated.(Model)
	require.Contains(t, model.statusLine(), "Installing Campfire: 42%")
}

// --- Install: refresh covers the search results' installed-flag ---

// TestActionDoneAfterInstallBatchesSearchRefreshWhenSearchReady guards the
// task-5-brief.md finding: the generic post-action refresh (m.loadData)
// only re-fetches Overview/Profiles, never the search page, so a completed
// install used to leave the just-installed search result's "installed"
// marker stale until the user searched again by hand. An actionDoneMsg
// tagged actionInstall must batch a search re-run cmd alongside the usual
// refresh whenever a search is actually ready to refresh.
func TestActionDoneAfterInstallBatchesSearchRefreshWhenSearchReady(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenSearch
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "camp", Source: "nexusmods", PageSize: 10}
	model.action.gen = 3
	model.action.running = true

	updated, cmd := model.Update(actionDoneMsg{gen: 3, kind: actionInstall, outcome: ActionOutcome{Message: `Installed "Campfire"`}})
	m := updated.(Model)
	require.False(t, m.action.running)
	require.NotNil(t, cmd)

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "an install refresh with a ready search must batch the data reload with a search re-run")
	require.Len(t, batch, 2)
}

// TestActionDoneAfterInstallSkipsSearchRefreshWhenNoSearchIsReady proves the
// refresh cmd still collapses to the plain (unwrapped) data-reload cmd every
// OTHER existing test already asserts the type of, when there's no
// completed search to refresh - guarding against a regression that would
// batch unconditionally and break every pre-existing
// `require.IsType(t, dataLoadedMsg{}, refreshCmd())` assertion.
func TestActionDoneAfterInstallSkipsSearchRefreshWhenNoSearchIsReady(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.action.gen = 3
	model.action.running = true
	// model.search.state defaults to searchIdle (zero value): no search to refresh.

	updated, cmd := model.Update(actionDoneMsg{gen: 3, kind: actionInstall, outcome: ActionOutcome{Message: `Installed "Campfire"`}})
	m := updated.(Model)
	require.False(t, m.action.running)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, dataLoadedMsg{}, msg, "no ready search to refresh: must collapse to the plain data-reload cmd, unwrapped")
}

// TestActionDoneAfterNonInstallActionNeverBatchesSearchRefresh proves the
// search-refresh batching is specific to actionInstall - every other action
// kind must never trigger it, even with a ready search sitting in the model.
func TestActionDoneAfterNonInstallActionNeverBatchesSearchRefresh(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenSearch
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "camp", Source: "nexusmods", PageSize: 10}
	model.action.gen = 3
	model.action.running = true

	updated, cmd := model.Update(actionDoneMsg{gen: 3, kind: actionEnable, outcome: ActionOutcome{Message: `Enabled "SkyUI"`}})
	m := updated.(Model)
	require.False(t, m.action.running)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, dataLoadedMsg{}, msg, "a non-install action must never trigger a search refresh, even with a ready search")
}

// --- Install: prototype end-to-end (task-5-brief.md's Prototype parity) ---

// TestPrototypeInstallEndToEndRefreshesSearchInstalledFlag drives the FULL
// install flow through the real prototypeProvider (Provider and Actions
// from the SAME instance, like NewPrototypeModel wires them): search ->
// select -> 'i' -> plan modal (with campfire's canned dependency, proving
// the deps/download-disclosure modal variant against real demo data) ->
// confirm -> ApplyInstall's fake progress ticks -> refresh -> the
// just-installed result shows "installed" in BOTH the re-run search page
// and the Installed screen, without any manual re-search.
func TestPrototypeInstallEndToEndRefreshesSearchInstalledFlag(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	model, cmd := model.startSearch("campfire", 0)
	require.NotNil(t, cmd)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	require.Equal(t, searchReady, model.search.state)
	require.NotEmpty(t, model.search.page.Results)
	require.Equal(t, "available", requireModByID(t, model.search.page.Results, "campfire").Status)

	model.screen = ScreenSearch
	model.selected[ScreenSearch] = 0

	updated, cmd = model.Update(keyRunes("i"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.Equal(t, "Planning install…", model.action.status)

	msg := cmd()
	require.IsType(t, installPlanResultMsg{}, msg)

	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, `Install "Campfire"?`, model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "Dependencies: SKSE64", "campfire's canned dependency must be visible in the modal")
	require.Contains(t, model.action.pending.detail, "Will download & install 1 mod(s)")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, `Installed "Campfire"`, model.action.status)

	refreshMsg := refreshCmd()
	batch, ok := refreshMsg.(tea.BatchMsg)
	require.True(t, ok, "install refresh must batch the data reload with a search re-run")
	require.Len(t, batch, 2)
	for _, sub := range batch {
		subMsg := sub()
		updated, _ = model.Update(subMsg)
		model = updated.(Model)
	}

	require.Equal(t, "installed", requireModByID(t, model.search.page.Results, "campfire").Status,
		"the just-installed search result must show installed after refresh, without a manual re-search")
	require.Equal(t, "installed", requireModByID(t, model.mods, "campfire").Status,
		"the Installed screen must also reflect the new mod after refresh")
}

// TestPrototypeInstallPlanShowsConflictForFrostfall covers the Conflicts
// modal variant against real prototype demo data (frostfall's canned
// conflict - prototype/data.go).
func TestPrototypeInstallPlanShowsConflictForFrostfall(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	model, cmd := model.startSearch("frostfall", 0)
	require.NotNil(t, cmd)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.screen = ScreenSearch
	model.selected[ScreenSearch] = 0

	updated, cmd = model.Update(keyRunes("i"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Contains(t, model.action.pending.detail, "Conflicts: textures/frost.dds (owned by ussep)")
}

// TestPrototypeInstallPlanReinstallForSkyUI covers the Reinstall path
// against real prototype demo data: the skyui SearchResults entry
// deliberately matches an InstalledMods entry (prototype/data.go) so 'i' on
// it demos Reinstall without any extra plumbing.
func TestPrototypeInstallPlanReinstallForSkyUI(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	model, cmd := model.startSearch("skyui", 0)
	require.NotNil(t, cmd)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.screen = ScreenSearch
	model.selected[ScreenSearch] = 0

	updated, cmd = model.Update(keyRunes("i"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Equal(t, `Reinstall "SkyUI"?`, model.action.pending.title)
}

// --- Install: help/footer content ---

func TestHelpOverlayDocumentsInstallKey(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model = updateWithRunes(t, model, "?")
	view := model.View()

	require.Contains(t, view, "install the selected search result")
}

func TestFooterHintNamesInstallAction(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	view := model.View()

	require.Contains(t, view, "i: install")
}

// --- Check/apply updates ('u' on Dashboard and Installed Mods) ---

// TestCheckUpdatesKeyDispatchesAsyncFetchFromDashboard proves 'u' on
// Dashboard shows a "Checking for updates…" status and returns a command
// instead of synchronously calling the provider - mirrors
// TestSwitchKeyDispatchesAsyncPlanFetch/TestInstallKeyDispatchesAsyncPlanFetch's
// shape.
func TestCheckUpdatesKeyDispatchesAsyncFetchFromDashboard(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)
	require.Equal(t, "Checking for updates…", model.action.status)
	require.False(t, model.action.statusIsError)
	require.Empty(t, rec.CheckUpdatesCalls, "the provider call happens when the returned cmd runs, not synchronously")

	msg := cmd()
	require.IsType(t, checkUpdatesResultMsg{}, msg)
	require.Equal(t, 1, rec.CheckUpdatesCalls)
}

// TestCheckUpdatesKeyDispatchesAsyncFetchFromInstalledMods proves the same
// binding also fires from the Installed Mods screen (task-5-brief.md:
// "'u' on ScreenDashboard and ScreenInstalledMods").
func TestCheckUpdatesKeyDispatchesAsyncFetchFromInstalledMods(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)

	msg := cmd()
	require.IsType(t, checkUpdatesResultMsg{}, msg)
}

func TestCheckUpdatesKeyWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenSearch, ScreenProfiles, ScreenSources} {
		rec := &recordingActions{}
		model := modelWithActions(t, rec)
		model.screen = screen

		updated, cmd := model.Update(keyRunes("u"))
		model = updated.(Model)
		require.Nil(t, cmd, "screen %v", screen)
		require.False(t, model.action.running, "screen %v", screen)
		require.Empty(t, rec.CheckUpdatesCalls, "screen %v", screen)
	}
}

func TestCheckUpdatesKeyInertWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model.action.running = true

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Empty(t, rec.CheckUpdatesCalls)
}

// TestCheckUpdatesKeyInertWhileAnotherModalPending proves a DIFFERENT
// already-pending modal is left completely undisturbed by 'u'.
func TestCheckUpdatesKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.CheckUpdatesCalls)
}

// --- Check/apply updates: zero-updates status line ---

func TestCheckUpdatesZeroUpdatesShowsStatusLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, cmd2 := model.Update(msg)
	model = updated.(Model)

	require.Nil(t, cmd2)
	require.Nil(t, model.action.pending)
	require.False(t, model.action.running)
	require.Equal(t, "No updates available.", model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestCheckUpdatesZeroUpdatesWithWarningsSuffix guards the "(N warnings)"
// suffix task-5-brief.md mandates when CheckUpdates surfaces per-source
// diagnostics alongside zero resolved updates - reusing
// formatOutcomeStatus's own Message-plus-Warnings rendering convention
// rather than inventing a second formatter (see
// resolveCheckUpdatesResult's doc comment).
func TestCheckUpdatesZeroUpdatesWithWarningsSuffix(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Warnings: []string{
		`source "local-mods" does not support checking for updates; run 'lmm update' from a shell instead`,
	}}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.Nil(t, model.action.pending)
	require.Contains(t, model.action.status, "No updates available.")
	require.Contains(t, model.action.status, "does not support checking for updates")
}

// TestCheckUpdatesAuthRequiredWarningRendersAsStatusLine covers the other
// half of task-5-brief.md's capability/auth item: an ErrAuthRequired-mapped
// message (coreProvider.CheckUpdates' own wording - service_core_test.go
// covers the mapping itself) must render just as cleanly.
func TestCheckUpdatesAuthRequiredWarningRendersAsStatusLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Warnings: []string{
		"Authentication required for one or more sources. Run 'lmm auth login <source>' in a shell, then try again (auth required)",
	}}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.Contains(t, model.action.status, "Authentication required")
	require.False(t, model.action.statusIsError, "a per-source warning folded into UpdatesView.Warnings is not itself a failed check")
}

// TestCheckUpdatesWarningStatusTruncatesAtRenderTime extends rule 5's
// render-time truncation contract (TestFailedStatusTruncatesAtRenderTimeNotSetTime)
// to the zero-updates-with-warnings status line.
func TestCheckUpdatesWarningStatusTruncatesAtRenderTime(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 300)
	rec := &recordingActions{UpdatesViewOut: UpdatesView{Warnings: []string{long}}}
	model := sizedModelWithActions(t, rec, 100, 30)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	wide := model.statusLine()
	require.LessOrEqual(t, lipgloss.Width(wide), model.availableWidth())
}

// --- Check/apply updates: modal content ---

func TestCheckUpdatesModalContentListsUpdatesAndWarningsCount(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{
		Updates: []UpdateItem{
			{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3"},
			{Source: "nexusmods", ID: "ussep", Name: "USSEP", FromVersion: "4.3", ToVersion: "4.4"},
		},
		Warnings: []string{"source foo: boom"},
	}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.action.pending)
	require.Equal(t, actionUpdate, model.action.pending.kind)
	require.Equal(t, "Apply 2 update(s)?", model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "SkyUI 5.2 → 5.3")
	require.Contains(t, model.action.pending.detail, "USSEP 4.3 → 4.4")
	require.Contains(t, model.action.pending.detail, "1 warning(s) during check")
}

// --- Check/apply updates: confirm applies all sequentially ---

// TestCheckUpdatesConfirmAppliesAllSequentiallyInOrder proves confirming
// the batch modal calls ApplyUpdate once per update, in the SAME order
// CheckUpdates reported them - task-5-brief.md: "confirm applies all
// sequentially (recording asserts order)".
func TestCheckUpdatesConfirmAppliesAllSequentiallyInOrder(t *testing.T) {
	t.Parallel()

	updates := []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3"},
		{Source: "nexusmods", ID: "ussep", Name: "USSEP", FromVersion: "4.3", ToVersion: "4.4"},
	}
	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: updates}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)
	require.Equal(t, updates, rec.ApplyUpdateCalls, "must apply every update, in order")

	outcome := doneMsg.(actionDoneMsg).outcome
	require.Equal(t, "Applied 2 update(s)", outcome.Message)
}

// TestCheckUpdatesMidBatchFailureContinuesAndWarns guards the batch's
// failure-tolerance contract: one update failing must not abort the rest
// (task-5-brief.md: "a mid-batch failure continues to the next update...
// with the failure warned, not fatal"), and the aggregate outcome folds the
// failure into Warnings (matching ApplyInstall's own Failed-into-Warnings
// precedent) rather than surfacing as an actionFailedMsg.
func TestCheckUpdatesMidBatchFailureContinuesAndWarns(t *testing.T) {
	t.Parallel()

	updates := []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3"},
		{Source: "nexusmods", ID: "ussep", Name: "USSEP", FromVersion: "4.3", ToVersion: "4.4"},
	}
	rec := &recordingActions{
		UpdatesViewOut:     UpdatesView{Updates: updates},
		ApplyUpdateErrByID: map[string]error{"skyui": errors.New("connection refused")},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg, "a mid-batch failure must still resolve as a done outcome, not actionFailedMsg")
	require.Equal(t, updates, rec.ApplyUpdateCalls, "the batch must continue past the failed update, not abort")

	outcome := doneMsg.(actionDoneMsg).outcome
	require.Equal(t, "Applied 1 update(s)", outcome.Message)
	require.Len(t, outcome.Warnings, 1)
	require.Contains(t, outcome.Warnings[0], "SkyUI")
	require.Contains(t, outcome.Warnings[0], "connection refused")
}

func TestCheckUpdatesCancelDoesNotCallApplyUpdate(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3"},
	}}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd2 := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.ApplyUpdateCalls)
}

// --- Check/apply updates: stale plan-fetch discard + fetch error ---

func TestCheckUpdatesPlanStaleResultDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true
	model.action.status = ""

	updated, cmd := model.Update(checkUpdatesResultMsg{gen: 4, view: UpdatesView{}})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running, "stale result must not clear running")
	require.Nil(t, m.action.pending)
	require.Empty(t, m.action.status)
}

func TestCheckUpdatesStaleFailureDiscarded(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.action.gen = 9
	model.action.running = true

	updated, cmd := model.Update(checkUpdatesFailedMsg{gen: 1, err: errors.New("boom")})
	m := updated.(Model)
	require.Nil(t, cmd)
	require.True(t, m.action.running)
	require.Empty(t, m.action.status)
}

func TestCheckUpdatesErrorShowsStatusNoModal(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{CheckUpdatesErr: errors.New("loading installed mods for skyrim-se/survival: boom")}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.NotNil(t, cmd)

	msg := cmd()
	require.IsType(t, checkUpdatesFailedMsg{}, msg)

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "boom")
	require.Nil(t, model.action.pending)
}

// --- Check/apply updates: progress streaming ---

func TestCheckUpdatesApplyProgressStreamsIntoStatusLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
			{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3"},
		}},
		ApplyUpdateTicks: []ActionProgress{{Line: "Updating SkyUI: 42%", Percent: 42}},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	batchMsg := confirmCmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, 2)

	actionMsg := batch[0]()
	require.IsType(t, actionDoneMsg{}, actionMsg)

	progressMsg := batch[1]()
	updated, _ = model.Update(progressMsg)
	model = updated.(Model)
	require.Contains(t, model.statusLine(), "Updating SkyUI: 42%")
}

// --- Check/apply updates: Dashboard summary tie-in ---

// TestCheckUpdatesUpdatesSummaryCountAfterCheck guards task-5-brief.md's
// Dashboard summary tie-in: Summary.Updates renders the "?" sentinel (-1)
// until a check has actually run; a successful check must set it to the
// real, already-in-hand count (no DataProvider change - see
// resolveCheckUpdatesResult's doc comment for the accepted "reverts to
// unknown on the next unrelated refresh" tradeoff).
func TestCheckUpdatesUpdatesSummaryCountAfterCheck(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3"},
	}}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model.summary.Updates = -1 // simulate coreProvider's "?" sentinel before any check has run

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.Equal(t, 1, model.summary.Updates, "the dashboard summary must reflect the real count after a check")
}

// TestCheckUpdatesUpdatesSummaryCountToZeroWhenNoneFound proves zero
// updates becomes the KNOWN count 0, not left at the unknown sentinel -1.
func TestCheckUpdatesUpdatesSummaryCountToZeroWhenNoneFound(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard
	model.summary.Updates = -1

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.Equal(t, 0, model.summary.Updates, "zero updates is a KNOWN count (0), not the unknown sentinel (-1)")
}

// TestDataLoadedMsgPreservesKnownUpdatesCountAcrossUnrelatedRefresh guards
// against reverting the dashboard's just-checked Updates count to the "?"
// sentinel on ANY subsequent, unrelated refresh. resolveCheckUpdatesResult
// sets m.summary.Updates to the real, in-memory count (see its own doc
// comment), but the dataLoadedMsg handler wholesale-overwrote m.summary
// with whatever the DataProvider reports - and coreProvider.Overview always
// reports Updates: -1 (no persistent count until Phase 6) - so an unrelated
// refresh (enable/disable/deploy/switch/install; anything that re-runs
// m.loadData) reverted a just-checked "3 updates" straight back to "?" even
// though nothing about updates changed. A fresh NON-sentinel count from the
// provider must still win over a preserved one - see
// TestActionDoneReSentinelsUpdatesCountAfterApplyingUpdates for the one case
// that SHOULD go stale.
func TestDataLoadedMsgPreservesKnownUpdatesCountAcrossUnrelatedRefresh(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "a", Name: "A", FromVersion: "1", ToVersion: "2"},
		{Source: "nexusmods", ID: "b", Name: "B", FromVersion: "1", ToVersion: "2"},
		{Source: "nexusmods", ID: "c", Name: "C", FromVersion: "1", ToVersion: "2"},
	}}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.Equal(t, 3, model.summary.Updates, "sanity: the check set the real count")

	// An unrelated refresh - e.g. the one enable/disable/deploy/switch
	// already trigger - carrying the DataProvider's own "?" sentinel
	// summary must not wipe out what was just learned.
	updated, _ = model.Update(dataLoadedMsg{summary: Summary{Updates: -1, Conflicts: -1}, mods: model.mods, profiles: model.profiles})
	model = updated.(Model)

	require.Equal(t, 3, model.summary.Updates, "an unrelated refresh must not revert a known Updates count to the unknown sentinel")
}

// TestDataLoadedMsgFreshNonSentinelUpdatesCountWins proves a genuinely fresh
// (non-sentinel) Updates count from the DataProvider always wins over a
// preserved one - the preserve behavior above only applies when the
// incoming summary reports the -1 sentinel.
func TestDataLoadedMsgFreshNonSentinelUpdatesCountWins(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.summary.Updates = 3

	updated, _ := model.Update(dataLoadedMsg{summary: Summary{Updates: 7, Conflicts: -1}, mods: model.mods, profiles: model.profiles})
	model = updated.(Model)

	require.Equal(t, 7, model.summary.Updates, "a fresh non-sentinel count must overwrite a previously known one")
}

// TestActionDoneReSentinelsUpdatesCountAfterApplyingUpdates covers the
// companion case: applying updates REDUCES how many are available, so the
// just-checked count is now wrong, not merely unrelated. Phase 5b has no way
// to compute the new real count without re-running CheckUpdates (see
// resolveCheckUpdatesResult's doc comment on the "no DataProvider change"
// tradeoff), so the least-surprising behavior is to re-sentinel Updates back
// to "?" in the update-apply action's own done-path, rather than leave a
// stale count on screen or have the very next unrelated refresh's preserve
// logic (see the pair of tests above) keep it alive indefinitely.
func TestActionDoneReSentinelsUpdatesCountAfterApplyingUpdates(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
			{Source: "nexusmods", ID: "a", Name: "A", FromVersion: "1", ToVersion: "2"},
		}},
		ApplyUpdateOutcome: ActionOutcome{Message: `Updated "A"`},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, 1, model.summary.Updates, "sanity: the check set the real count")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)

	require.Equal(t, -1, model.summary.Updates, "a completed update-apply batch must re-sentinel Updates back to unknown - the just-checked count is now stale")
}

// --- Check/apply updates: prototype end-to-end ---

// TestPrototypeUpdatesEndToEndKeyFlow drives the FULL updates flow through
// the real prototypeProvider: 'u' -> canned updates modal (skyui + ussep,
// the two canned auto/notify updates - prototype/data.go) -> confirm ->
// ApplyUpdate's fake progress ticks per mod -> refresh -> both versions
// bump, visible via a repeated Overview through the SAME provider instance.
func TestPrototypeUpdatesEndToEndKeyFlow(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.Equal(t, "Checking for updates…", model.action.status)

	msg := cmd()
	require.IsType(t, checkUpdatesResultMsg{}, msg)

	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending)
	require.Equal(t, "Apply 2 update(s)?", model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "SkyUI 5.2 → 5.3")
	require.Contains(t, model.action.pending.detail, "USSEP 4.3 → 4.4")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)
	require.IsType(t, actionDoneMsg{}, doneMsg)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	require.Equal(t, "Applied 2 update(s)", model.action.status)

	loadedMsg := refreshCmd()
	require.IsType(t, dataLoadedMsg{}, loadedMsg, "the updates flow never triggers the install-only search-refresh batching")
	updated, _ = model.Update(loadedMsg)
	model = updated.(Model)

	skyui := requireModByID(t, model.mods, "skyui")
	require.Equal(t, "5.3", skyui.Version)
	ussep := requireModByID(t, model.mods, "ussep")
	require.Equal(t, "4.4", ussep.Version)
}

// --- Check/apply updates: help/footer content ---

func TestHelpOverlayDocumentsCheckUpdatesKey(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model = updateWithRunes(t, model, "?")
	view := model.View()

	require.Contains(t, view, "check for updates")
}

func TestFooterHintNamesCheckUpdatesAction(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	view := model.View()

	require.Contains(t, view, "u: check updates")
}
