package tui

import (
	"context"
	"errors"
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

// TestPrototypeProviderActions_ApplyProfileSwitch_RefusesNeedsDownloadsCannedScenario
// proves the refusal is enforced at Apply time too, mirroring
// coreProvider's own NeedsDownloads refusal test.
func TestPrototypeProviderActions_ApplyProfileSwitch_RefusesNeedsDownloadsCannedScenario(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	_, err := actions.ApplyProfileSwitch(context.Background(), prototype.NeedsDownloadProfileName)
	require.ErrorIs(t, err, errProfileNeedsDownloads)
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

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "vanilla-plus")
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

	outcome, err := actions.ApplyProfileSwitch(context.Background(), "survival")
	require.NoError(t, err)
	assert.Equal(t, `Already on profile "survival"`, outcome.Message)
}

func TestPrototypeProviderActions_ApplyProfileSwitch_UnknownProfileErrors(t *testing.T) {
	t.Parallel()

	actions := NewPrototypeProvider().(ActionProvider)

	_, err := actions.ApplyProfileSwitch(context.Background(), "does-not-exist")
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
	EnableCalls    []ModItem
	DisableCalls   []ModItem
	UninstallCalls []ModItem
	DeployCalls    int
	PlanCalls      []string
	ApplyCalls     []string

	EnableOutcome, DisableOutcome, UninstallOutcome, DeployOutcome, ApplyOutcome ActionOutcome
	PlanView                                                                     SwitchPlanView

	EnableErr, DisableErr, UninstallErr, DeployErr, PlanErr, ApplyErr error
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

func (r *recordingActions) ApplyProfileSwitch(_ context.Context, profile string) (ActionOutcome, error) {
	r.ApplyCalls = append(r.ApplyCalls, profile)
	return r.ApplyOutcome, r.ApplyErr
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

func (f failingActions) ApplyProfileSwitch(context.Context, string) (ActionOutcome, error) {
	return ActionOutcome{}, f.err()
}

func TestRecordingActionsRecordsCallsAndReturnsConfiguredOutcomes(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		EnableOutcome:    ActionOutcome{Message: "enabled"},
		DisableOutcome:   ActionOutcome{Message: "disabled"},
		UninstallOutcome: ActionOutcome{Message: "uninstalled"},
		DeployOutcome:    ActionOutcome{Message: "deployed"},
		ApplyOutcome:     ActionOutcome{Message: "applied"},
		PlanView:         SwitchPlanView{From: "a", To: "b"},
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

	applyOutcome, err := rec.ApplyProfileSwitch(ctx, "target")
	require.NoError(t, err)
	assert.Equal(t, "applied", applyOutcome.Message)

	assert.Equal(t, []ModItem{item}, rec.EnableCalls)
	assert.Equal(t, []ModItem{item}, rec.DisableCalls)
	assert.Equal(t, []ModItem{item}, rec.UninstallCalls)
	assert.Equal(t, 1, rec.DeployCalls)
	assert.Equal(t, []string{"target"}, rec.PlanCalls)
	assert.Equal(t, []string{"target"}, rec.ApplyCalls)
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
	_, err = f.ApplyProfileSwitch(ctx, "target")
	assert.ErrorIs(t, err, sentinel)
}

func TestFailingActionsDefaultsToGenericErrorWhenUnconfigured(t *testing.T) {
	t.Parallel()

	f := failingActions{}
	_, err := f.EnableMod(context.Background(), ModItem{})
	require.Error(t, err)
}
