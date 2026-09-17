package serve_test

// #463, the web half of the profile-safety unit (#441/#444/#445/#446): the
// profiles modal offers no Delete on the active profile, presents a
// non-active profile's purge as the recorded-only clean-up it is - with
// what it removes and what it keeps, and why - and the listing's, the sync
// plan's and a flag-only switch's own warnings are on screen.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// newE2EFixtureWithASharedDeployment is the flow suite's mixedFlowServer
// in the browser: default lists and deploys shared.pak; alt lists shared
// and its own own.pak, and deployed both while it was briefly active;
// default is active again. So a clean-up of alt removes own.pak and keeps
// shared.pak, which default records too.
func newE2EFixtureWithASharedDeployment(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	ctx := t.Context()
	pm := f.Svc.NewProfileManager()
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "shared", SourceID: "fake", Name: "Shared Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"shared.pak": []byte("shared")})
	require.NoError(t, pm.AddMod(ctx, f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: "shared", Version: "1.0"}))
	require.NoError(t, f.Svc.GetGameCache(f.Game).Store(f.Game.ID, "fake", "own", "1.0", "own.pak", []byte("own")))
	_, err := pm.Create(ctx, f.Game.ID, "alt")
	require.NoError(t, err)
	for _, m := range []struct{ id, name string }{{"shared", "Shared Mod"}, {"own", "Own Mod"}} {
		require.NoError(t, f.Svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:          domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: f.Game.ID},
			ProfileName:  "alt",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
		}))
		require.NoError(t, pm.AddMod(ctx, f.Game.ID, "alt", domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0"}))
	}
	require.NoError(t, pm.SetDefault(ctx, f.Game.ID, "default"))
	_, err = f.Svc.DeployProfile(ctx, f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, f.Game.ID, "alt"))
	_, err = f.Svc.DeployProfile(ctx, f.Game, "alt", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, f.Game.ID, "default"))
	require.FileExists(t, filepath.Join(f.Game.ModPath, "own.pak"))
	return f
}

// openProfilesModalAction opens Manage profiles from the profile picker.
func openProfilesModalAction() chromedp.Action {
	return chromedp.Tasks{
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".profile-picker__menu button, .profile-picker__menu [role=menuitem]"))
			.find((b) => b.textContent.includes("Manage profiles")).click()`, nil),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".profiles-row").length === 2`),
	}
}

// rowJS is the profiles-modal row for profile name.
func rowJS(name string) string {
	return `Array.from(document.querySelectorAll(".profiles-row")).find((r) => r.querySelector(".profiles-row__name").textContent.trim().startsWith(` + jsString(name) + `))`
}

func TestE2E_ProfilesModal_ActiveRowHasNoDeleteAndACleanUpListsWhatItKeeps(t *testing.T) {
	f := newE2EFixtureWithASharedDeployment(t)

	var defaultDeletes, altDeletes int
	var defaultPurge, altPurge, note, summary, cleanUpConfirmLabel string
	var remove, kept []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		openProfilesModalAction(),
		chromedp.Evaluate(rowJS("default")+`.querySelectorAll('[data-action="delete-profile"]').length`, &defaultDeletes),
		chromedp.Evaluate(rowJS("alt")+`.querySelectorAll('[data-action="delete-profile"]').length`, &altDeletes),
		chromedp.Evaluate(rowJS("default")+`.querySelector('[data-testid="active-profile-note"]').textContent`, &note),
		textContent(`[data-action="purge-profile"][data-profile="default"]`, &defaultPurge),
		textContent(`[data-action="purge-profile"][data-profile="alt"]`, &altPurge),
		chromedp.Click(`[data-action="purge-profile"][data-profile="alt"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.modal[data-kind="purge"] [data-testid="purge-recorded-only"]') !== null`),
		textContent(`[data-testid="purge-recorded-only"] .plan__summary`, &summary),
		textContent(`.modal [data-action="confirm"]`, &cleanUpConfirmLabel),
		chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="purge-remove"] li')].map(li => li.textContent.trim())`, &remove),
		chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="purge-kept"] li')].map(li => li.dataset.reason + ": " + li.textContent.trim().replace(/\s+/g, " "))`, &kept),
	)
	assert.Zero(t, defaultDeletes, "no Delete on the active profile")
	assert.Equal(t, 1, altDeletes, "a non-active profile can still be deleted")
	assert.Contains(t, note, "can't be deleted")
	assert.Equal(t, "Purge…", strings.TrimSpace(defaultPurge))
	assert.Equal(t, "Clean up…", strings.TrimSpace(altPurge))
	assert.Contains(t, summary, "alt is not the active profile (default is). This clean-up removes only the files alt recorded deploying")
	assert.Equal(t, "Clean up", strings.TrimSpace(cleanUpConfirmLabel), "a non-active clean-up's confirm button does not claim to Purge")
	assert.Equal(t, []string{"own.pak"}, remove)
	assert.Equal(t, []string{"recorded: shared.pak — profile default records it too"}, kept)

	// Confirm (type the name): own.pak goes, shared.pak stays.
	f.runInBrowser(t,
		chromedp.SendKeys(`input[name="purge-confirm"]`, "alt", chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(f.Game.ModPath, "own.pak"))
		return os.IsNotExist(err)
	}, e2eTimeout, e2ePollInterval, "the clean-up removes alt's own file")
	assert.FileExists(t, filepath.Join(f.Game.ModPath, "shared.pak"), "the file default records too is kept")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_KeptReasons_SayForWhomEachFileIsKept pins the four reasons'
// words, including the one only an adapter-routed file produces.
func TestE2E_KeptReasons_SayForWhomEachFileIsKept(t *testing.T) {
	f := newE2EFixture(t)
	var got []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { keptReason } = await import("/static/app/components/plan_purge.js");
			return [
				keptReason({reason: "listed", profiles: ["main"]}),
				keptReason({reason: "recorded", profiles: ["default"]}),
				keptReason({reason: "recorded", profiles: ["default", "imported"]}),
				keptReason({reason: "other_game", games: ["g2"]}),
				keptReason({reason: "other_game", games: ["g2", "g3"]}),
				keptReason({reason: "user_file"}),
			];
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
	)
	assert.Equal(t, []string{
		"the active profile main lists its mod, so it may be live",
		"profile default records it too",
		"profiles default and imported record it too",
		"game g2 records it too",
		"games g2 and g3 record it too",
		"kept your file; lmm no longer tracks it",
	}, got)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_NoActiveProfile_WarnsAndTheSwitchOnlyMarks: with no profile file
// marked active, the profiles listing warns, a deploy plan's refusal names
// `lmm profile list`, and a switch previews - and finishes with - the
// flag-only recovery notice.
func TestE2E_NoActiveProfile_WarnsAndTheSwitchOnlyMarks(t *testing.T) {
	f := newE2EFixtureWithASharedDeployment(t)
	path := filepath.Join(f.Svc.ConfigDir(), "games", f.Game.ID, "profiles", "default.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "is_default: true\n", "")), 0o644))

	var listWarn, deployErr string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="deploy"]`),
		pollUntil(`document.querySelector('.modal[data-kind="deploy"] .modal__error') !== null`),
		textContent(`.modal[data-kind="deploy"] .modal__error`, &deployErr),
		chromedp.Click(`.modal [data-action="cancel"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		openProfilesModalAction(),
		pollUntil(`document.querySelector('[data-testid="profiles-warnings"]') !== null`),
		textContent(`[data-testid="profiles-warnings"]`, &listWarn),
	)
	assert.Contains(t, deployErr, "lmm profile list", "the refusal names the command that shows the profiles")
	assert.Contains(t, listWarn, "is_default")

	var preview, recovery, notice, switchConfirmLabel string
	f.runInBrowser(t,
		chromedp.Click(`.modal .modal__close`, chromedp.ByQuery),
		waitGone(`.modal`),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Click(`[data-action="switch"][data-profile="alt"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="switch-flag-only"]') !== null`),
		textContent(`[data-testid="switch-flag-only"] [data-testid="plan-warnings"]`, &preview),
		textContent(`[data-testid="switch-flag-only-recovery"]`, &recovery),
		textContent(`.modal [data-action="confirm"]`, &switchConfirmLabel),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		pollUntil(`document.querySelector('[data-testid="job-result-warning"]') !== null`),
		textContent(`[data-testid="job-result-warning"]`, &notice),
	)
	assert.Contains(t, preview, "only marks alt as the active profile")
	assert.Contains(t, recovery, "lmm profile apply alt --game "+f.Game.ID)
	assert.Equal(t, "Mark as active", strings.TrimSpace(switchConfirmLabel), "the flag-only switch's confirm button does not claim to deploy")
	assert.Contains(t, notice, "alt is now the active profile")
	assert.Contains(t, notice, "lmm purge -p default --game "+f.Game.ID)
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/plans/deploy")
}

// TestE2E_SyncPlan_ShowsTheModsItKeepsAndWhy (#444): a mod the active
// profile lists as on, whose row is off and undeployed, is kept by a sync
// with a warning naming both ways to settle it - on screen before Confirm.
func TestE2E_SyncPlan_ShowsTheModsItKeepsAndWhy(t *testing.T) {
	f := newE2EFixture(t)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "off", SourceID: "fake", Name: "Off Mod", Version: "1.0", GameID: f.Game.ID},
		false, map[string][]byte{"off.pak": []byte("off")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "off", Version: "1.0"}))

	var warning string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".profile-picker__menu button"))
			.find((b) => b.textContent.includes("Manage profiles")).click()`, nil),
		chromedp.WaitVisible(`[data-action="sync-profile"][data-profile="default"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="sync-profile"][data-profile="default"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="sync-warnings"]') !== null`),
		textContent(`[data-testid="sync-warnings"]`, &warning),
	)
	assert.Contains(t, warning, "Off Mod is disabled but profile default lists it as enabled")
	assert.Contains(t, warning, "lmm profile apply default")
	assert.Empty(t, f.BrowserErrors())
}
