package tui

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// --- prototypeProvider: ActionProvider ---

// TestPrototypeProviderImplementsActionProvider guards the design decision
// that a single *prototypeProvider instance satisfies both DataProvider and
// ActionProvider: NewPrototypeProvider's static return type stays
// DataProvider (unchanged, so no existing callers break), but the
// underlying concrete value can be type-asserted to ActionProvider so a
// caller (Task 6/7's --prototype wiring) can drive both roles from ONE
// instance and see actions reflected in subsequent reads - see the actions
// tests below, which rely on exactly this.
func TestPrototypeProviderImplementsActionProvider(t *testing.T) {
	t.Parallel()

	_, ok := NewPrototypeProvider().(ActionProvider)
	require.True(t, ok, "prototypeProvider must implement ActionProvider so a single instance can serve both roles")
}

func TestPrototypeProviderActions_EnableMod_FlipsStatusVisibleInRepeatedOverview(t *testing.T) {
	t.Parallel()

	provider := NewPrototypeProvider()
	actions := provider.(ActionProvider)

	outcome, err := actions.EnableMod(context.Background(), ModItem{ID: "alternate-start", Source: "nexusmods", Name: "Alternate Start"})
	require.NoError(t, err)
	assert.Equal(t, `Enabled "Alternate Start"`, outcome.Message)

	_, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	item := requireModByID(t, mods, "alternate-start")
	assert.NotEqual(t, "disabled", item.Status, "the SAME provider instance must reflect the enable on the next Overview call")
}

func TestPrototypeProviderActions_EnableMod_AlreadyEnabledMessage(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	outcome, err := actions.EnableMod(context.Background(), ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"})
	require.NoError(t, err)
	assert.Equal(t, `"SkyUI" is already enabled`, outcome.Message)
}

func TestPrototypeProviderActions_DisableMod_FlipsStatusVisibleInRepeatedOverview(t *testing.T) {
	t.Parallel()

	provider := NewPrototypeProvider()
	actions := provider.(ActionProvider)

	outcome, err := actions.DisableMod(context.Background(), ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"})
	require.NoError(t, err)
	assert.Equal(t, `Disabled "SkyUI"`, outcome.Message)

	_, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	item := requireModByID(t, mods, "skyui")
	assert.Equal(t, "disabled", item.Status)
}

func TestPrototypeProviderActions_DisableMod_AlreadyDisabledMessage(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	outcome, err := actions.DisableMod(context.Background(), ModItem{ID: "alternate-start", Source: "nexusmods", Name: "Alternate Start"})
	require.NoError(t, err)
	assert.Equal(t, `"Alternate Start" is already disabled`, outcome.Message)
}

func TestPrototypeProviderActions_UninstallMod_RemovesFromRepeatedOverview(t *testing.T) {
	t.Parallel()

	provider := NewPrototypeProvider()
	actions := provider.(ActionProvider)

	_, before, err := provider.Overview(context.Background())
	require.NoError(t, err)

	outcome, err := actions.UninstallMod(context.Background(), ModItem{ID: "skyui", Source: "nexusmods", Name: "SkyUI"})
	require.NoError(t, err)
	assert.Equal(t, `Uninstalled "SkyUI"`, outcome.Message)

	_, after, err := provider.Overview(context.Background())
	require.NoError(t, err)
	assert.Len(t, after, len(before)-1)
	for _, m := range after {
		assert.NotEqual(t, "skyui", m.ID, "an uninstalled mod must be gone from a repeated Overview")
	}
}

func TestPrototypeProviderActions_UnknownModErrors(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)
	unknown := ModItem{ID: "does-not-exist", Source: "nexusmods", Name: "Nope"}

	_, err := actions.EnableMod(context.Background(), unknown)
	assert.Error(t, err)
	_, err = actions.DisableMod(context.Background(), unknown)
	assert.Error(t, err)
	_, err = actions.UninstallMod(context.Background(), unknown)
	assert.Error(t, err)
}

func TestPrototypeProviderActions_DeployProfile_ReturnsPlausibleOutcome(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	outcome, err := actions.DeployProfile(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, outcome.Message)
}

func TestPrototypeProviderActions_PlanProfileSwitch_ComputesFakeConsistentPlan(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	view, err := actions.PlanProfileSwitch(context.Background(), "vanilla-plus")
	require.NoError(t, err)
	assert.Equal(t, "survival", view.From)
	assert.Equal(t, "vanilla-plus", view.To)
	assert.False(t, view.AlreadyActive)
	assert.Empty(t, view.NeedsDownloads, "the prototype never invents phantom downloads")
}

func TestPrototypeProviderActions_PlanProfileSwitch_AlreadyActive(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	view, err := actions.PlanProfileSwitch(context.Background(), "survival")
	require.NoError(t, err)
	assert.True(t, view.AlreadyActive)
	assert.Equal(t, "survival", view.From)
	assert.Equal(t, "survival", view.To)
}

// TestPrototypeProviderActions_PlanProfileSwitch_NeedsDownloadsCannedScenario
// guards the Task 7 mandated prototype data addition: the one canned
// profile whose Mods list references an ID absent from InstalledMods
// (prototype.NeedsDownloadProfileName) must produce a NeedsDownloads plan,
// unlike every other canned profile (see the "vanilla-plus" test above,
// which asserts the opposite for the general case).
func TestPrototypeProviderActions_PlanProfileSwitch_NeedsDownloadsCannedScenario(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	view, err := actions.PlanProfileSwitch(context.Background(), prototype.NeedsDownloadProfileName)
	require.NoError(t, err)
	assert.Equal(t, "survival", view.From)
	assert.Equal(t, prototype.NeedsDownloadProfileName, view.To)
	assert.False(t, view.AlreadyActive)
	assert.NotEmpty(t, view.NeedsDownloads)
}

// TestPrototypeProviderActions_ApplyProfileSwitch_DownloadsAndAppliesNeedsDownloadsCannedScenario
// proves the Phase 5b Task 4 switch-refusal LIFT for the prototype: applying
// a plan with NeedsDownloads entries used to refuse outright
// (errProfileNeedsDownloads - see the git history of this test, formerly
// TestPrototypeProviderActions_ApplyProfileSwitch_RefusesNeedsDownloadsCannedScenario);
// it now streams a few fake download ticks per missing mod, materializes
// them into InstalledMods, and completes the switch normally - a WORKING
// downloading-switch demo replacing the refusal one, per the task brief.
// This is the RED test proving the lift: against the still-refusing
// implementation (this task's first commit), it fails with
// errProfileNeedsDownloads instead of succeeding.
func TestPrototypeProviderActions_ApplyProfileSwitch_DownloadsAndAppliesNeedsDownloadsCannedScenario(t *testing.T) {
	t.Parallel()

	provider := NewPrototypeProvider()
	actions := provider.(ActionProvider)

	var ticks []ActionProgress
	outcome, err := actions.ApplyProfileSwitch(context.Background(), prototype.NeedsDownloadProfileName,
		func(p ActionProgress) { ticks = append(ticks, p) })
	require.NoError(t, err, "a plan needing downloads must now proceed instead of being refused")
	assert.Equal(t, fmt.Sprintf("Switched to %q", prototype.NeedsDownloadProfileName), outcome.Message)
	assert.NotEmpty(t, ticks, "downloading the missing mod(s) must stream progress")

	_, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, requireModByID(t, mods, "requiem-legendary"),
		"the previously-missing mod must now be materialized as installed")

	profiles, err := provider.Profiles(context.Background())
	require.NoError(t, err)
	byName := map[string]ProfileItem{}
	for _, p := range profiles {
		byName[p.Name] = p
	}
	assert.True(t, byName[prototype.NeedsDownloadProfileName].Active, "the switch must still complete and mark the target active")
}

func TestPrototypeProviderActions_PlanProfileSwitch_UnknownProfileErrors(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	_, err := actions.PlanProfileSwitch(context.Background(), "does-not-exist")
	assert.Error(t, err)
}

func TestPrototypeProviderActions_ApplyProfileSwitch_UpdatesActiveProfileVisibleInProfiles(t *testing.T) {
	t.Parallel()

	provider := NewPrototypeProvider()
	actions := provider.(ActionProvider)

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "vanilla-plus", nil)
	require.NoError(t, err)
	assert.Equal(t, `Switched to "vanilla-plus"`, outcome.Message)

	profiles, err := provider.Profiles(context.Background())
	require.NoError(t, err)
	byName := map[string]ProfileItem{}
	for _, p := range profiles {
		byName[p.Name] = p
	}
	assert.True(t, byName["vanilla-plus"].Active, "the SAME provider instance must reflect the switch")
	assert.False(t, byName["survival"].Active)
}

func TestPrototypeProviderActions_ApplyProfileSwitch_AlreadyActive(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "survival", nil)
	require.NoError(t, err)
	assert.Equal(t, `Already on profile "survival"`, outcome.Message)
}

func TestPrototypeProviderActions_ApplyProfileSwitch_UnknownProfileErrors(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	_, err := actions.ApplyProfileSwitch(context.Background(), "does-not-exist", nil)
	assert.Error(t, err)
}

// --- prototypeProvider: PlanInstall/ApplyInstall/CheckUpdates/ApplyUpdate
// (Phase 5b Task 4) ---

// TestPrototypeProviderActions_PlanInstall_ComputesDeterministicPlanWithDepsAndSize
// guards the canned-data-driven fake plan: a search-result mod not yet
// installed reports its files, at least one fake dependency (the brief's
// "a dep for at least one mod"), and a size label - deterministic given the
// same canned data every time.
func TestPrototypeProviderActions_PlanInstall_ComputesDeterministicPlanWithDepsAndSize(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)
	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}

	view, err := actions.PlanInstall(context.Background(), item)
	require.NoError(t, err)
	assert.Equal(t, "Campfire", view.Name)
	assert.Equal(t, "nexusmods", view.Source)
	assert.NotEmpty(t, view.Files)
	assert.NotEmpty(t, view.Dependencies, "at least one canned mod must report a fake dependency")
	assert.NotEmpty(t, view.SizeLabel)
	assert.False(t, view.Reinstall, "campfire is not yet installed")

	again, err := actions.PlanInstall(context.Background(), item)
	require.NoError(t, err)
	assert.Equal(t, view, again, "the fake plan must be deterministic for the same canned mod")
}

// TestPrototypeProviderActions_PlanInstall_ReportsConflictForAtLeastOneMod
// guards the brief's "a conflict for at least one" canned-data requirement.
func TestPrototypeProviderActions_PlanInstall_ReportsConflictForAtLeastOneMod(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	view, err := actions.PlanInstall(context.Background(), ModItem{ID: "frostfall", Source: "nexusmods", Name: "Frostfall"})
	require.NoError(t, err)
	assert.NotEmpty(t, view.Conflicts, "at least one canned mod must report a fake conflict")
}

// TestPrototypeProviderActions_PlanInstall_SizeUnknownForAModWithNoDeclaredSize
// guards the "size unknown" branch: at least one canned mod deliberately
// leaves its size undeclared.
func TestPrototypeProviderActions_PlanInstall_SizeUnknownForAModWithNoDeclaredSize(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	view, err := actions.PlanInstall(context.Background(), ModItem{ID: "frostfall", Source: "nexusmods", Name: "Frostfall"})
	require.NoError(t, err)
	assert.Equal(t, "size unknown", view.SizeLabel)
}

func TestPrototypeProviderActions_PlanInstall_UnknownModErrors(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	_, err := actions.PlanInstall(context.Background(), ModItem{ID: "does-not-exist", Source: "nexusmods"})
	assert.Error(t, err)
}

// TestPrototypeProviderActions_ApplyInstall_TicksProgressAndInstallsVisibleInOverview
// guards the brief's "emits 2-3 fake progress ticks (0/50/100%), then
// mutates its in-memory data so the mod shows installed in Overview and
// Search" requirement.
func TestPrototypeProviderActions_ApplyInstall_TicksProgressAndInstallsVisibleInOverview(t *testing.T) {
	t.Parallel()

	provider := NewPrototypeProvider()
	actions := provider.(ActionProvider)
	item := ModItem{ID: "campfire", Source: "nexusmods", Name: "Campfire"}

	var ticks []ActionProgress
	outcome, err := actions.ApplyInstall(context.Background(), item, func(p ActionProgress) { ticks = append(ticks, p) })
	require.NoError(t, err)
	assert.Equal(t, `Installed "Campfire"`, outcome.Message)
	require.GreaterOrEqual(t, len(ticks), 2, "must emit at least 2 fake progress ticks")
	assert.Equal(t, float64(100), ticks[len(ticks)-1].Percent, "the final tick must reach 100%")

	_, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	installed := requireModByID(t, mods, "campfire")
	assert.Equal(t, "installed", installed.Status)

	page, err := provider.Search(context.Background(), "nexusmods", "campfire", 0)
	require.NoError(t, err)
	require.NotEmpty(t, page.Results)
	assert.Equal(t, "installed", requireModByID(t, page.Results, "campfire").Status,
		"Search must also reflect the install through the SAME prototype instance")
}

func TestPrototypeProviderActions_ApplyInstall_UnknownModErrors(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	_, err := actions.ApplyInstall(context.Background(), ModItem{ID: "does-not-exist", Source: "nexusmods"}, nil)
	assert.Error(t, err)
}

// TestPrototypeProviderActions_CheckUpdates_ReturnsCannedUpdateSet guards
// the brief's "canned set — at least one auto-policy and one notify update"
// requirement: prototype/data.go seeds skyui (auto) and ussep (notify) with
// an AvailableVersion, so both surface here (UpdateItem itself carries no
// policy field - see its doc comment - the policy split lives only in the
// canned data for a future keybinding layer to consult).
func TestPrototypeProviderActions_CheckUpdates_ReturnsCannedUpdateSet(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	view, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Updates, 2, "the canned set has exactly two available updates (see prototype/data.go)")

	byID := map[string]UpdateItem{}
	for _, u := range view.Updates {
		byID[u.ID] = u
	}
	require.Contains(t, byID, "skyui")
	assert.NotEqual(t, byID["skyui"].FromVersion, byID["skyui"].ToVersion)
	require.Contains(t, byID, "ussep")
	assert.NotEqual(t, byID["ussep"].FromVersion, byID["ussep"].ToVersion)
}

// TestPrototypeProviderActions_ApplyUpdate_TicksProgressAndBumpsVersionVisibleInOverview
// guards the brief's "ApplyUpdate ticks progress and bumps the version in
// data" requirement, and that the applied mod stops reporting an available
// update afterward.
func TestPrototypeProviderActions_ApplyUpdate_TicksProgressAndBumpsVersionVisibleInOverview(t *testing.T) {
	t.Parallel()

	provider := NewPrototypeProvider()
	actions := provider.(ActionProvider)

	before, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	var target UpdateItem
	for _, u := range before.Updates {
		if u.ID == "skyui" {
			target = u
		}
	}
	require.NotEmpty(t, target.ID, "skyui must be one of the canned updates")

	var ticks []ActionProgress
	outcome, err := actions.ApplyUpdate(context.Background(), target, func(p ActionProgress) { ticks = append(ticks, p) })
	require.NoError(t, err)
	assert.Contains(t, outcome.Message, target.ToVersion)
	require.GreaterOrEqual(t, len(ticks), 2)

	_, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	updated := requireModByID(t, mods, "skyui")
	assert.Equal(t, target.ToVersion, updated.Version)

	after, err := actions.CheckUpdates(context.Background())
	require.NoError(t, err)
	for _, u := range after.Updates {
		assert.NotEqual(t, "skyui", u.ID, "skyui must no longer report an available update after applying it")
	}
}

func TestPrototypeProviderActions_ApplyUpdate_UnknownModErrors(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	_, err := actions.ApplyUpdate(context.Background(), UpdateItem{ID: "does-not-exist", Source: "nexusmods"}, nil)
	assert.Error(t, err)
}

func requireModByID(t *testing.T, mods []ModItem, id string) ModItem {
	t.Helper()
	for _, m := range mods {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("mod %q not found in %+v", id, mods)
	return ModItem{}
}

// --- Test fakes for Tasks 6-7 ---

// recordingActions implements ActionProvider, recording every call's
// arguments and returning caller-configured outcomes/errors - for Tasks 6-7
// to verify keybinding/modal wiring calls the right method with the right
// arguments, without needing a real core.Service or the prototype dataset.
// Mirrors app_test.go's recordingProvider (the equivalent DataProvider
// fake), one field set per method rather than a single delegate+hook, since
// ActionProvider callers need to configure a distinct outcome per method.
type recordingActions struct {
	EnableCalls       []ModItem
	DisableCalls      []ModItem
	UninstallCalls    []ModItem
	DeployCalls       int
	PlanCalls         []string
	ApplyCalls        []string
	PlanInstallCalls  []ModItem
	ApplyInstallCalls []ModItem
	CheckUpdatesCalls int
	ApplyUpdateCalls  []UpdateItem

	EnableOutcome, DisableOutcome, UninstallOutcome, DeployOutcome, ApplyOutcome ActionOutcome
	ApplyInstallOutcome, ApplyUpdateOutcome                                      ActionOutcome
	PlanView                                                                     SwitchPlanView
	InstallPlanViewOut                                                           InstallPlanView
	UpdatesViewOut                                                               UpdatesView

	// ApplySwitchTicks/ApplyInstallTicks/ApplyUpdateTicks, if set, are
	// replayed through the matching method's progress callback (in order)
	// whenever it's non-nil - lets a caller assert the pump actually
	// observes ticks a provider reports (Phase 5b Task 4).
	ApplySwitchTicks  []ActionProgress
	ApplyInstallTicks []ActionProgress
	ApplyUpdateTicks  []ActionProgress

	EnableErr, DisableErr, UninstallErr, DeployErr, PlanErr, ApplyErr error
	PlanInstallErr, ApplyInstallErr, CheckUpdatesErr, ApplyUpdateErr  error
}

func (r *recordingActions) EnableMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	r.EnableCalls = append(r.EnableCalls, item)
	return r.EnableOutcome, r.EnableErr
}

func (r *recordingActions) DisableMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	r.DisableCalls = append(r.DisableCalls, item)
	return r.DisableOutcome, r.DisableErr
}

func (r *recordingActions) UninstallMod(_ context.Context, item ModItem) (ActionOutcome, error) {
	r.UninstallCalls = append(r.UninstallCalls, item)
	return r.UninstallOutcome, r.UninstallErr
}

func (r *recordingActions) DeployProfile(_ context.Context) (ActionOutcome, error) {
	r.DeployCalls++
	return r.DeployOutcome, r.DeployErr
}

func (r *recordingActions) PlanProfileSwitch(_ context.Context, profile string) (SwitchPlanView, error) {
	r.PlanCalls = append(r.PlanCalls, profile)
	return r.PlanView, r.PlanErr
}

func (r *recordingActions) ApplyProfileSwitch(_ context.Context, profile string, progress func(ActionProgress)) (ActionOutcome, error) {
	r.ApplyCalls = append(r.ApplyCalls, profile)
	for _, p := range r.ApplySwitchTicks {
		if progress != nil {
			progress(p)
		}
	}
	return r.ApplyOutcome, r.ApplyErr
}

func (r *recordingActions) PlanInstall(_ context.Context, item ModItem) (InstallPlanView, error) {
	r.PlanInstallCalls = append(r.PlanInstallCalls, item)
	return r.InstallPlanViewOut, r.PlanInstallErr
}

func (r *recordingActions) ApplyInstall(_ context.Context, item ModItem, progress func(ActionProgress)) (ActionOutcome, error) {
	r.ApplyInstallCalls = append(r.ApplyInstallCalls, item)
	for _, p := range r.ApplyInstallTicks {
		if progress != nil {
			progress(p)
		}
	}
	return r.ApplyInstallOutcome, r.ApplyInstallErr
}

func (r *recordingActions) CheckUpdates(_ context.Context) (UpdatesView, error) {
	r.CheckUpdatesCalls++
	return r.UpdatesViewOut, r.CheckUpdatesErr
}

func (r *recordingActions) ApplyUpdate(_ context.Context, u UpdateItem, progress func(ActionProgress)) (ActionOutcome, error) {
	r.ApplyUpdateCalls = append(r.ApplyUpdateCalls, u)
	for _, p := range r.ApplyUpdateTicks {
		if progress != nil {
			progress(p)
		}
	}
	return r.ApplyUpdateOutcome, r.ApplyUpdateErr
}

// failingActions implements ActionProvider with every method returning a
// fixed error (Err, or a generic one if Err is unset) - for Tasks 6-7 to
// verify error-path UI (status line rendering, modal dismissal) without
// per-test stubbing.
type failingActions struct{ Err error }

func (f failingActions) err() error {
	if f.Err != nil {
		return f.Err
	}
	return errors.New("failingActions: forced failure")
}

func (f failingActions) EnableMod(context.Context, ModItem) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func (f failingActions) DisableMod(context.Context, ModItem) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func (f failingActions) UninstallMod(context.Context, ModItem) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func (f failingActions) DeployProfile(context.Context) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func (f failingActions) PlanProfileSwitch(context.Context, string) (SwitchPlanView, error) {
	return SwitchPlanView{}, f.err()
}

func (f failingActions) ApplyProfileSwitch(context.Context, string, func(ActionProgress)) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func (f failingActions) PlanInstall(context.Context, ModItem) (InstallPlanView, error) {
	return InstallPlanView{}, f.err()
}

func (f failingActions) ApplyInstall(context.Context, ModItem, func(ActionProgress)) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func (f failingActions) CheckUpdates(context.Context) (UpdatesView, error) {
	return UpdatesView{}, f.err()
}

func (f failingActions) ApplyUpdate(context.Context, UpdateItem, func(ActionProgress)) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func TestRecordingActionsRecordsCallsAndReturnsConfiguredOutcomes(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		EnableOutcome:       ActionOutcome{Message: "enabled"},
		DisableOutcome:      ActionOutcome{Message: "disabled"},
		UninstallOutcome:    ActionOutcome{Message: "uninstalled"},
		DeployOutcome:       ActionOutcome{Message: "deployed"},
		ApplyOutcome:        ActionOutcome{Message: "applied"},
		ApplyInstallOutcome: ActionOutcome{Message: "installed"},
		ApplyUpdateOutcome:  ActionOutcome{Message: "updated"},
		PlanView:            SwitchPlanView{From: "a", To: "b"},
		InstallPlanViewOut:  InstallPlanView{Name: "Campfire"},
		UpdatesViewOut:      UpdatesView{Updates: []UpdateItem{{ID: "1", Name: "Mod"}}},
	}

	item := ModItem{ID: "1", Source: "src", Name: "Test"}
	ctx := context.Background()

	enableOutcome, err := rec.EnableMod(ctx, item)
	require.NoError(t, err)
	assert.Equal(t, "enabled", enableOutcome.Message)

	disableOutcome, err := rec.DisableMod(ctx, item)
	require.NoError(t, err)
	assert.Equal(t, "disabled", disableOutcome.Message)

	uninstallOutcome, err := rec.UninstallMod(ctx, item)
	require.NoError(t, err)
	assert.Equal(t, "uninstalled", uninstallOutcome.Message)

	deployOutcome, err := rec.DeployProfile(ctx)
	require.NoError(t, err)
	assert.Equal(t, "deployed", deployOutcome.Message)

	view, err := rec.PlanProfileSwitch(ctx, "target")
	require.NoError(t, err)
	assert.Equal(t, "a", view.From)

	applyOutcome, err := rec.ApplyProfileSwitch(ctx, "target", nil)
	require.NoError(t, err)
	assert.Equal(t, "applied", applyOutcome.Message)

	planInstallView, err := rec.PlanInstall(ctx, item)
	require.NoError(t, err)
	assert.Equal(t, "Campfire", planInstallView.Name)

	applyInstallOutcome, err := rec.ApplyInstall(ctx, item, nil)
	require.NoError(t, err)
	assert.Equal(t, "installed", applyInstallOutcome.Message)

	updatesView, err := rec.CheckUpdates(ctx)
	require.NoError(t, err)
	require.Len(t, updatesView.Updates, 1)
	assert.Equal(t, "Mod", updatesView.Updates[0].Name)

	updateItem := UpdateItem{ID: "1", Name: "Mod", FromVersion: "1.0", ToVersion: "2.0"}
	applyUpdateOutcome, err := rec.ApplyUpdate(ctx, updateItem, nil)
	require.NoError(t, err)
	assert.Equal(t, "updated", applyUpdateOutcome.Message)

	assert.Equal(t, []ModItem{item}, rec.EnableCalls)
	assert.Equal(t, []ModItem{item}, rec.DisableCalls)
	assert.Equal(t, []ModItem{item}, rec.UninstallCalls)
	assert.Equal(t, 1, rec.DeployCalls)
	assert.Equal(t, []string{"target"}, rec.PlanCalls)
	assert.Equal(t, []string{"target"}, rec.ApplyCalls)
	assert.Equal(t, []ModItem{item}, rec.PlanInstallCalls)
	assert.Equal(t, []ModItem{item}, rec.ApplyInstallCalls)
	assert.Equal(t, 1, rec.CheckUpdatesCalls)
	assert.Equal(t, []UpdateItem{updateItem}, rec.ApplyUpdateCalls)
}

// TestRecordingActionsRelaysConfiguredProgressTicksToEveryNetworkMethod
// proves the recording fake actually invokes a caller-supplied progress
// callback with the configured ticks, for every one of the three network
// methods that accept one (ApplyProfileSwitch/ApplyInstall/ApplyUpdate) -
// this is what Task 5's eventual keybinding wiring (and this task's own
// pump tests) rely on to observe streaming without a real core.Service.
func TestRecordingActionsRelaysConfiguredProgressTicksToEveryNetworkMethod(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		ApplySwitchTicks:  []ActionProgress{{Line: "switch 50%", Percent: 50}},
		ApplyInstallTicks: []ActionProgress{{Line: "install 50%", Percent: 50}},
		ApplyUpdateTicks:  []ActionProgress{{Line: "update 50%", Percent: 50}},
	}
	ctx := context.Background()

	var switchTicks, installTicks, updateTicks []ActionProgress
	_, err := rec.ApplyProfileSwitch(ctx, "target", func(p ActionProgress) { switchTicks = append(switchTicks, p) })
	require.NoError(t, err)
	_, err = rec.ApplyInstall(ctx, ModItem{}, func(p ActionProgress) { installTicks = append(installTicks, p) })
	require.NoError(t, err)
	_, err = rec.ApplyUpdate(ctx, UpdateItem{}, func(p ActionProgress) { updateTicks = append(updateTicks, p) })
	require.NoError(t, err)

	assert.Equal(t, []ActionProgress{{Line: "switch 50%", Percent: 50}}, switchTicks)
	assert.Equal(t, []ActionProgress{{Line: "install 50%", Percent: 50}}, installTicks)
	assert.Equal(t, []ActionProgress{{Line: "update 50%", Percent: 50}}, updateTicks)
}

func TestFailingActionsErrorsOnEveryMethod(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	f := failingActions{Err: sentinel}
	item := ModItem{ID: "1", Source: "src", Name: "Test"}
	ctx := context.Background()

	_, err := f.EnableMod(ctx, item)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.DisableMod(ctx, item)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.UninstallMod(ctx, item)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.DeployProfile(ctx)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.PlanProfileSwitch(ctx, "target")
	assert.ErrorIs(t, err, sentinel)
	_, err = f.ApplyProfileSwitch(ctx, "target", nil)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.PlanInstall(ctx, item)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.ApplyInstall(ctx, item, nil)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.CheckUpdates(ctx)
	assert.ErrorIs(t, err, sentinel)
	_, err = f.ApplyUpdate(ctx, UpdateItem{}, nil)
	assert.ErrorIs(t, err, sentinel)
}

func TestFailingActionsDefaultsToGenericErrorWhenUnconfigured(t *testing.T) {
	t.Parallel()

	f := failingActions{}
	_, err := f.EnableMod(context.Background(), ModItem{})
	require.Error(t, err)
}
