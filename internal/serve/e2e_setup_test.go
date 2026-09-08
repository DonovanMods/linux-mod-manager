package serve_test

// chromedp coverage for the Setup surface (issue 333, unit7b-task-report.md):
// first-run detect/add, the Setup page's Games/Auth/Sources/Archive
// import/Adopt sections. Builds on e2e_harness_test.go's reusable half -
// sandboxE2EEnv, startE2EServer, newE2EBrowser, e2eZipWith - the same way
// every other unit's own E2E file does.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// retrySetValue is chromedp.SetValue with a few retries: setting a
// controlled <select>'s value dispatches input+change synchronously
// (chromedp's own js/setAttribute.js), which can run this application's
// onChange handler - and its re-render - INSIDE that same call, before
// chromedp reads the value back to confirm it stuck. Occasionally (observed
// under `-race`, where everything runs slower) that re-render lands between
// the write and the read-back and chromedp reports "could not set value on
// node N" even though the click that follows would have worked fine a beat
// later. A plain retry is the honest fix: the interaction itself is not
// flaky, only this one JS round trip's timing is.
func retrySetValue(sel, value string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if err = chromedp.SetValue(sel, value, chromedp.ByQuery).Do(ctx); err == nil {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return err
	})
}

// SetupPath is the Setup page's own route, optionally deep-linked to a
// section - router.js's setupPath mirrored for the browser side.
func (f e2eFixture) SetupPath(section string) string {
	p := f.HomePath() + "/setup"
	if section != "" {
		p += "?section=" + section
	}
	return p
}

// e2eNoGamesFixture is newE2EFixture's shape for the first-run state: no
// game exists yet, so there is no Game/Profile to carry.
type e2eNoGamesFixture struct {
	Ctx           context.Context
	BaseURL       string
	Svc           *core.Service
	BrowserErrors func() []string
}

// newE2EFixtureNoGames seeds a Service with nothing configured - the
// first-run state the "/" route's setup flow exists for - and opens a
// browser against it.
func newE2EFixtureNoGames(t *testing.T) e2eNoGamesFixture {
	t.Helper()
	sandboxE2EEnv(t)

	svc := newFixtureServiceNoGames(t)
	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eNoGamesFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, BrowserErrors: browserErrors}
}

func (f e2eNoGamesFixture) runInBrowser(t *testing.T, actions ...chromedp.Action) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.Ctx, e2eTimeout)
	defer cancel()
	require.NoError(t, chromedp.Run(ctx, actions...))
}

// e2eGameCatalogSource is a fake source.GameCatalog for the manual-add
// form's catalog-pick path.
type e2eGameCatalogSource struct {
	*fakeSource
	entries []source.GameEntry
}

func newE2EGameCatalogSource(id string) *e2eGameCatalogSource {
	return &e2eGameCatalogSource{fakeSource: newFakeSource(id)}
}

func (s *e2eGameCatalogSource) ListGames(context.Context) ([]source.GameEntry, error) {
	return s.entries, nil
}

var _ source.GameCatalog = (*e2eGameCatalogSource)(nil)

// e2eAuthRequiredCatalogSource is a fake source.GameCatalog whose
// ListGames always answers domain.ErrAuthRequired - exactly what
// CurseForge does before `auth login` - for Important 1's regression: a
// catalog search behind this source must render "authenticate ... first",
// never "this source has no searchable catalog".
type e2eAuthRequiredCatalogSource struct {
	*fakeSource
}

func newE2EAuthRequiredCatalogSource(id string) *e2eAuthRequiredCatalogSource {
	return &e2eAuthRequiredCatalogSource{fakeSource: newFakeSource(id)}
}

func (s *e2eAuthRequiredCatalogSource) ListGames(context.Context) ([]source.GameEntry, error) {
	return nil, domain.ErrAuthRequired
}

var _ source.GameCatalog = (*e2eAuthRequiredCatalogSource)(nil)

// e2eAuthSource is a fake auth-capable source with a LIVE key validator
// (source.KeyValidator) - the Auth section's login/logout scenario needs a
// source that actually accepts or rejects a submitted key, the way
// NexusMods/CurseForge do, rather than the "stored, checked on first use"
// shape every other fake source in this package has.
type e2eAuthSource struct {
	*fakeSource
	acceptKey string

	mu      sync.Mutex
	lastKey string
}

func newE2EAuthSource(id, acceptKey string) *e2eAuthSource {
	return &e2eAuthSource{fakeSource: newFakeSource(id), acceptKey: acceptKey}
}

func (s *e2eAuthSource) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Auth: true}
}

func (s *e2eAuthSource) ValidateKey(_ context.Context, key string) error {
	if key != s.acceptKey {
		return assertKeyRejected{}
	}
	return nil
}

// SetAPIKey records the key the running source was re-keyed with
// (app.RekeySource's own seam: `interface{ SetAPIKey(string) }`) - not
// asserted on directly by these scenarios (the wire's own re-key tests
// cover that), but required for the type to participate in the live re-key
// path without a nil-method panic.
func (s *e2eAuthSource) SetAPIKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastKey = key
}

// assertKeyRejected is a plain sentinel error - its message is deliberately
// generic (never echoing the key) since ValidateSourceKey wraps it as the
// 400's message the browser renders.
type assertKeyRejected struct{}

func (assertKeyRejected) Error() string { return "the key was not accepted" }

var (
	_ source.ModSource          = (*e2eAuthSource)(nil)
	_ source.KeyValidator       = (*e2eAuthSource)(nil)
	_ source.CapabilityReporter = (*e2eAuthSource)(nil)
)

// e2eSteamDetectFixture fakes exactly enough of a Steam install for
// app.DetectGames (internal/source/steam) to find one moddable game: a
// STEAM_ROOT env override (steam.FindSteamRoots' own seam) pointing at a
// throwaway root with one appmanifest, plus a <configDir>/steam-games.yaml
// override (steam.LoadKnownGames' own seam) naming a fake app id so the
// scan needs no real Steam library or a genuine entry in the embedded
// default list.
type e2eSteamDetectFixture struct {
	Slug string
	Name string
}

// writeE2ESteamDetectFixture wires the fake Steam root and known-games
// override into configDir, and creates the "installed" game directory the
// scan's os.Stat check requires.
func writeE2ESteamDetectFixture(t *testing.T, configDir string) e2eSteamDetectFixture {
	t.Helper()
	const appID = "999999"
	const installDir = "E2EDetectGame"
	slug := "e2e-detect-game"
	name := "E2E Detect Game"

	steamRoot := t.TempDir()
	steamapps := filepath.Join(steamRoot, "steamapps")
	require.NoError(t, os.MkdirAll(filepath.Join(steamapps, "common", installDir), 0o755))
	manifest := `"AppState"
{
	"appid"		"` + appID + `"
	"name"		"` + name + `"
	"installdir"		"` + installDir + `"
}
`
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "appmanifest_"+appID+".acf"), []byte(manifest), 0o644))
	t.Setenv("STEAM_ROOT", steamRoot)

	override := appID + `:
  slug: ` + slug + `
  name: "` + name + `"
  nexus_id: ` + slug + `
  mod_path: Data
`
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "steam-games.yaml"), []byte(override), 0o644))

	return e2eSteamDetectFixture{Slug: slug, Name: name}
}

// TestE2E_FirstRunDetect_AddsTheGameAndLandsOnMissionControl drives the
// whole first-run detect flow: "/" with zero games renders the real setup
// flow (not the old placeholder text), the scan finds the fake Steam game,
// selecting it and confirming lands on that game's Mission Control.
func TestE2E_FirstRunDetect_AddsTheGameAndLandsOnMissionControl(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	fixture := writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="first-run-setup"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.setup-detect__row`, chromedp.ByQuery),
		chromedp.Click(`.setup-detect__row input[type="checkbox"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="add-detected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-hydrated="true"].mission-control`, chromedp.ByQuery),
	)

	var url string
	f.runInBrowser(t, chromedp.Location(&url))
	assert.Contains(t, url, "/g/"+fixture.Slug+"/")

	got, err := f.Svc.GetGame(fixture.Slug)
	require.NoError(t, err)
	assert.Equal(t, fixture.Name, got.Name)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FirstRunManualAdd_CatalogPickLandsOnMissionControl drives the
// manual-add form's catalog path: pick a source with a catalog, search,
// click a match, submit, land on Mission Control.
func TestE2E_FirstRunManualAdd_CatalogPickLandsOnMissionControl(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	cat := newE2EGameCatalogSource("catalogsrc")
	cat.entries = []source.GameEntry{{ID: "432", Name: "Minecraft", Slug: "minecraft"}}
	f.Svc.RegisterSource(cat)

	install := t.TempDir()
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		retrySetValue(`select[name="add-source"]`, "catalogsrc"),
		chromedp.WaitVisible(`input[name="add-query"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-query"]`, "mine", chromedp.ByQuery),
		chromedp.Click(`.setup-add__catalog button.button--small`, chromedp.ByQuery),
		chromedp.WaitVisible(`.setup-add__matches button`, chromedp.ByQuery),
		chromedp.Click(`.setup-add__matches button`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-install-path"]`, install, chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-hydrated="true"].mission-control`, chromedp.ByQuery),
	)

	got, err := f.Svc.GetGame("minecraft")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"catalogsrc": "432"}, got.SourceIDs)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FirstRunManualAdd_CatalogAuthRequiredNamesTheSourceNotADeadEnd
// pins Important 1 (unit7-review.md): a 401 from GET /api/v1/games/catalog
// (the source needs a credential) must render "Authenticate ... first",
// never the "no searchable catalog" fallback meant for a source with no
// catalog at all - the single most likely first-run path the review found
// broken.
func TestE2E_FirstRunManualAdd_CatalogAuthRequiredNamesTheSourceNotADeadEnd(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	cat := newE2EAuthRequiredCatalogSource("catalogsrc")
	f.Svc.RegisterSource(cat)

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		retrySetValue(`select[name="add-source"]`, "catalogsrc"),
		chromedp.WaitVisible(`input[name="add-query"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-query"]`, "mine", chromedp.ByQuery),
		chromedp.Click(`.setup-add__catalog button.button--small`, chromedp.ByQuery),
		chromedp.WaitVisible(`.setup-add__catalog .modal__error`, chromedp.ByQuery),
	)

	var text string
	f.runInBrowser(t, chromedp.Text(`.setup-add__catalog .modal__error`, &text, chromedp.ByQuery))
	assert.Contains(t, text, "Authenticate")
	assert.Contains(t, text, "Authentication section")
	assert.NotContains(t, text, "no searchable catalog")

	var noCatalogHint int
	f.runInBrowser(t, chromedp.Evaluate(
		`document.querySelectorAll('.setup-add__catalog .plan__note').length`, &noCatalogHint))
	assert.Zero(t, noCatalogHint, "the noCatalog identifier-field hint must not also render")
	// The rejected search is a real network 401 Chrome logs as an error
	// entry independently of the SPA's own handling of it - expected, not
	// a bug (the same rule TestE2E_FirstRunManualAdd_IdentifierFieldErrorThenSucceeds
	// applies to its own rejected submit).
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_ManualAdd_UnrenderableFieldErrorFallsBackToFormBanner pins Minor 2
// (unit7-review.md): a GameSpecError naming a field this form has no input
// for (Field: "game_id" - only ever set from a catalog match's own
// game_id, never typed directly) used to un-busy the button with nothing
// shown at all. A catalog whose slug derives to a path-unsafe game id
// (DeriveGameID lower-cases and dashes spaces, but does not strip "/")
// reproduces it from the browser.
func TestE2E_ManualAdd_UnrenderableFieldErrorFallsBackToFormBanner(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	cat := newE2EGameCatalogSource("catalogsrc")
	cat.entries = []source.GameEntry{{ID: "1", Name: "Weird Game", Slug: "weird/slug"}}
	f.Svc.RegisterSource(cat)

	install := t.TempDir()
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		retrySetValue(`select[name="add-source"]`, "catalogsrc"),
		chromedp.WaitVisible(`input[name="add-query"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-query"]`, "weird", chromedp.ByQuery),
		chromedp.Click(`.setup-add__catalog button.button--small`, chromedp.ByQuery),
		chromedp.WaitVisible(`.setup-add__matches button`, chromedp.ByQuery),
		chromedp.Click(`.setup-add__matches button`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-install-path"]`, install, chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-game"] .modal__error`, chromedp.ByQuery),
	)

	var errorText string
	f.runInBrowser(t, chromedp.Text(`[data-testid="setup-add-game"] .modal__error`, &errorText, chromedp.ByQuery))
	assert.Contains(t, errorText, "game_id", "the form-wide banner must carry the server's own message when no input matches the field")

	_, err := f.Svc.GetGame("weird/slug")
	require.Error(t, err, "an id that failed validation must never be written")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FirstRunManualAdd_IdentifierFieldErrorThenSucceeds drives the
// manual-identifier path (a source with no catalog: the search box never
// even renders) and pins the field-error round trip: a bad install path
// marks THAT field, and fixing it and resubmitting succeeds without
// re-entering anything else.
func TestE2E_FirstRunManualAdd_IdentifierFieldErrorThenSucceeds(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	f.Svc.RegisterSource(newFakeSource("plain")) // no GameCatalog - identifier only

	install := t.TempDir()
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		retrySetValue(`select[name="add-source"]`, "plain"),
		chromedp.SendKeys(`input[name="add-identifier"]`, "acme", chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-name"]`, "Acme Game", chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-install-path"]`, "/definitely/not/a/real/path", chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-game"] .modal__error`, chromedp.ByQuery),
	)

	// The field error is rendered directly under the offending input - the
	// form itself must still be here (no navigation happened) and every
	// other value must have survived, so fixing just the one field and
	// resubmitting is all that is needed.
	var stillOnForm bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-testid="setup-add-game"]') !== null`, &stillOnForm))
	require.True(t, stillOnForm, "a field error must not navigate away")

	var errorText, identifierValue, nameValue string
	f.runInBrowser(t,
		chromedp.Text(`[data-testid="setup-add-game"] .modal__error`, &errorText, chromedp.ByQuery),
		chromedp.Value(`input[name="add-identifier"]`, &identifierValue, chromedp.ByQuery),
		chromedp.Value(`input[name="add-name"]`, &nameValue, chromedp.ByQuery),
	)
	assert.Contains(t, errorText, "path does not exist", "the error names the install-path field's own reason")
	assert.Equal(t, "acme", identifierValue, "an unrelated field must survive the rejected submit")
	assert.Equal(t, "Acme Game", nameValue)

	f.runInBrowser(t,
		chromedp.SetValue(`input[name="add-install-path"]`, install, chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-hydrated="true"].mission-control`, chromedp.ByQuery),
	)

	got, err := f.Svc.GetGame("acme")
	require.NoError(t, err)
	assert.Equal(t, "Acme Game", got.Name)
	// The rejected first submit is a real network 400 Chrome logs as an
	// error entry independently of the SPA's own handling of it (this
	// suite's own assertNoUncaughtErrors doc comment) - the fixture working
	// as intended, not a bug. An uncaught JS exception is not, and still
	// fails this assertion.
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_Auth_RejectedThenAcceptedNeverExposesTheKey drives the Auth
// section's login/logout, and is the one scenario in this file whose
// assertion is about what the page does NOT say: at no point does the
// submitted key text appear anywhere in the rendered page, success or
// failure (api_auth.go's own SECRET HANDLING rule, proven from the browser
// side rather than only over httptest).
func TestE2E_Auth_RejectedThenAcceptedNeverExposesTheKey(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))
	authSrc := newE2EAuthSource("authy", "good-key-xyz")
	f.Svc.RegisterSource(authSrc)

	const badKey = "bad-key-should-never-render"
	const goodKey = "good-key-xyz"

	assertKeyNeverRendered := func(t *testing.T, key string) {
		t.Helper()
		var html string
		f.runInBrowser(t, chromedp.OuterHTML("html", &html, chromedp.ByQuery))
		assert.NotContains(t, html, key, "the submitted key must never appear in the rendered page")
	}

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("auth")),
		chromedp.WaitVisible(`[data-source="authy"]`, chromedp.ByQuery),
	)
	// #333 Minor #5: the environment-variable hint must show for a row
	// that has NEVER been authenticated, not only one authenticated via
	// env - app.AuthStatus now fills EnvVar for every auth-capable row.
	var placeholder string
	f.runInBrowser(t, chromedp.AttributeValue(
		`[data-source="authy"] input[type="password"]`, "placeholder", &placeholder, nil, chromedp.ByQuery))
	assert.Equal(t, "or set LMM_AUTHY_API_KEY", placeholder)

	f.runInBrowser(t,
		chromedp.SendKeys(`[data-source="authy"] input[type="password"]`, badKey, chromedp.ByQuery),
		chromedp.Click(`[data-source="authy"] button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-source="authy"] .modal__error`, chromedp.ByQuery),
	)
	assertKeyNeverRendered(t, badKey)

	f.runInBrowser(t,
		chromedp.SendKeys(`[data-source="authy"] input[type="password"]`, goodKey, chromedp.ByQuery),
		chromedp.Click(`[data-source="authy"] button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-source="authy"] .badge--good`, chromedp.ByQuery),
	)
	assertKeyNeverRendered(t, goodKey)

	var maskedVisible bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-source="authy"] .mono') !== null`, &maskedVisible))
	assert.True(t, maskedVisible, "the masked form must render once authenticated")

	f.runInBrowser(t,
		chromedp.Click(`[data-source="authy"] button.button--small`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-source="authy"] input[type="password"]`, chromedp.ByQuery),
	)
	assertKeyNeverRendered(t, goodKey)
	// The rejected login is a real network 400 - expected, not a bug (see
	// assertNoUncaughtErrors' own doc comment).
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_Sources_CreateValidateFixSaveEditDelete drives the custom-source
// editor end to end: an invalid draft's Validate shows a finding, fixing it
// and saving hot-registers the source (it appears in the list with no
// restart), editing it round-trips the same YAML, and deleting it removes
// the row.
func TestE2E_Sources_CreateValidateFixSaveEditDelete(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))
	dir := t.TempDir()

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="new-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
	)

	// An invalid id (uppercase) - the draft Validate must refuse.
	invalid := "id: BAD ID\nname: My Mods\ntype: directory\ndirectory:\n  path: " + dir + "\n"
	f.runInBrowser(t,
		chromedp.SetValue(`.source-editor__textarea`, invalid, chromedp.ByQuery),
		chromedp.Click(`[data-action="validate-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.source-editor__report .modal__error`, chromedp.ByQuery),
	)

	var saveDisabled bool
	f.runInBrowser(t, chromedp.Evaluate(
		`document.querySelector('[data-action="save-source"]').disabled`, &saveDisabled))
	assert.True(t, saveDisabled, "Save must stay disabled until Validate reports valid")

	valid := "id: my-mods\nname: My Mods\ntype: directory\ndirectory:\n  path: " + dir + "\n"
	f.runInBrowser(t,
		chromedp.SetValue(`.source-editor__textarea`, valid, chromedp.ByQuery),
		chromedp.Click(`[data-action="validate-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.source-editor__report .plan__note`, chromedp.ByQuery),
		chromedp.Click(`[data-action="save-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`tr[data-source="my-mods"]`, chromedp.ByQuery),
	)

	infos := f.Svc.ListGames() // sanity: registry mutation did not disturb games
	assert.Len(t, infos, 1)

	f.runInBrowser(t,
		chromedp.Click(`tr[data-source="my-mods"] [data-action="edit-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
		// getSourceDefinition's own fetch resolves asynchronously after the
		// editor mounts - poll rather than reading .value immediately.
		chromedp.Poll(`document.querySelector('.source-editor__textarea').value.includes('my-mods')`, nil),
	)
	var loaded string
	f.runInBrowser(t, chromedp.Value(`.source-editor__textarea`, &loaded, chromedp.ByQuery))
	assert.Contains(t, loaded, "my-mods")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="cancel-source-edit"]`, chromedp.ByQuery),
		chromedp.Click(`tr[data-source="my-mods"] [data-action="delete-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`tr[data-source="my-mods"] [data-action="confirm-delete-source"]`, chromedp.ByQuery),
		chromedp.Click(`tr[data-source="my-mods"] [data-action="confirm-delete-source"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`tr[data-source="my-mods"]`, chromedp.ByQuery),
	)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_Sources_InUseNamesTheGameNotItsID pins Minor 8 (unit7-review.md):
// core.SourceInUseError.Games carries ids by design, but a user knows their
// games by title - both the "In use" column and the delete refusal must
// translate id -> name via the SPA's own listGames(), not print the id
// back at the user.
func TestE2E_Sources_InUseNamesTheGameNotItsID(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	dir := t.TempDir()
	yaml := []byte("id: inuse-src\nname: In Use Source\ntype: directory\ndirectory:\n  path: " + dir + "\n")
	_, err := app.SaveSourceDefinition(f.Ctx, f.Svc, "", yaml)
	require.NoError(t, err)
	f.Game.SourceIDs["inuse-src"] = ""
	require.NoError(t, f.Svc.SaveGame(f.Ctx, f.Game))

	var inUseCell string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`tr[data-source="inuse-src"]`, chromedp.ByQuery),
		chromedp.Text(`tr[data-source="inuse-src"] td:nth-child(4)`, &inUseCell, chromedp.ByQuery),
	)
	assert.Equal(t, f.Game.Name, strings.TrimSpace(inUseCell), `the "In use" column must name the game, not its id`)
	assert.NotContains(t, inUseCell, f.Game.ID)

	var refusalText string
	f.runInBrowser(t,
		chromedp.Click(`tr[data-source="inuse-src"] [data-action="delete-source"]`, chromedp.ByQuery),
		chromedp.Click(`tr[data-source="inuse-src"] [data-action="confirm-delete-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`tr[data-source="inuse-src"] .modal__error`, chromedp.ByQuery),
		chromedp.Text(`tr[data-source="inuse-src"] .modal__error`, &refusalText, chromedp.ByQuery),
	)
	assert.Contains(t, refusalText, f.Game.Name, "the delete refusal must name the game, not its id")
	assert.NotContains(t, refusalText, f.Game.ID)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_ArchiveImport_ConflictOverwriteRoundTrip uploads a real zip via a
// real <input type="file">, previews the plan (which already names the
// conflict with no ingest at all), confirms into a refusal, then answers it
// with the tray's Overwrite affordance - the same round trip install's own
// conflict scenario drives, now proven for import_archive.
func TestE2E_ArchiveImport_ConflictOverwriteRoundTrip(t *testing.T) {
	sandboxE2EEnv(t)
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)

	const deployedFile = "Mods/alpha.pak"
	seedInstalledMod(t, svc, game,
		domain.Mod{ID: "alpha", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: game.ID},
		true, map[string][]byte{deployedFile: []byte("alpha content")})
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "alpha", Version: "1.0"}))
	_, err := svc.DeployProfile(t.Context(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	zipPath := filepath.Join(t.TempDir(), "Clash-1.0.zip")
	require.NoError(t, os.WriteFile(zipPath, e2eZipWith(deployedFile, "clashing bytes"), 0o644))

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("archive")),
		chromedp.WaitVisible(`[data-testid="setup-import-archive"]`, chromedp.ByQuery),
		chromedp.SetUploadFiles(`[data-testid="setup-import-archive"] input[type="file"]`, []string{zipPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="staged-upload"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="import-archive"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="import_archive"] .plan`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal .plan__heading--warn`, chromedp.ByQuery), // the plan previews the conflict
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-import-archive"] .job-progress[data-state="failed"]`, chromedp.ByQuery),
	)

	before, err := os.ReadFile(filepath.Join(game.ModPath, deployedFile))
	require.NoError(t, err)
	assert.Equal(t, "alpha content", string(before), "a refused conflict must not have touched the deployed file")

	f.runInBrowser(t,
		chromedp.Click(`[data-testid="setup-import-archive"] button[data-action="overwrite"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-import-archive"] .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	after, err := os.ReadFile(filepath.Join(game.ModPath, deployedFile))
	require.NoError(t, err)
	assert.Equal(t, "clashing bytes", string(after), "the overwrite must have replaced the contested path")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ArchiveImport_ConfirmModalRendersReadableUnlinkedSummary pins
// Minor 1 (unit7-review.md): the import confirm modal's headline used to
// fuse words together with no space ("BetaMod-1.0.zip asBetaMod-1.01.0")
// because htm drops whitespace-only text between interpolations, and an
// unlinked import ("None - unlinked import" in the Setup page's own
// dropdown) rendered the nonsensical "(linked to local)" instead of saying
// what actually happened. "BetaMod-1.0.zip" deliberately does not match
// the NexusMods filename pattern (filename_parser.go), so this exercises
// the minted-uuid unlinked path (LinkedSource == "local", AutoDetected ==
// false) rather than the auto-detected one.
func TestE2E_ArchiveImport_ConfirmModalRendersReadableUnlinkedSummary(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	zipPath := filepath.Join(t.TempDir(), "BetaMod-1.0.zip")
	require.NoError(t, os.WriteFile(zipPath, e2eZipWith("BetaMod/data.txt", "beta content"), 0o644))

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("archive")),
		chromedp.WaitVisible(`[data-testid="setup-import-archive"]`, chromedp.ByQuery),
		chromedp.SetUploadFiles(`[data-testid="setup-import-archive"] input[type="file"]`, []string{zipPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="staged-upload"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="import-archive"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="import_archive"] .plan`, chromedp.ByQuery),
	)

	var summary, note string
	f.runInBrowser(t,
		chromedp.Text(`.modal .plan__summary`, &summary, chromedp.ByQuery),
		chromedp.Text(`.modal .plan__note`, &note, chromedp.ByQuery),
	)
	assert.Contains(t, summary, "BetaMod-1.0.zip as", "the archive name and \"as\" must not fuse together")
	assert.NotContains(t, summary, "asBetaMod", "\"as\" and the mod name must not fuse together")
	assert.NotContains(t, summary, "(linked to local)", "an unlinked import must never say it is linked to \"local\"")
	assert.Contains(t, note, "Local mods won't receive update notifications.", "the CLI's own note must survive to the SPA")

	var stagedText string
	f.runInBrowser(t,
		chromedp.Click(`.modal button[data-action="cancel"]`, chromedp.ByQuery),
		chromedp.Text(`[data-testid="staged-upload"]`, &stagedText, chromedp.ByQuery),
	)
	assert.NotContains(t, stagedText, ".zip(", "the filename and its size must not fuse together")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Adopt_SeededUntrackedModBecomesTracked seeds a hand-placed,
// untracked mod directory in the game's own mod path, scans for it from the
// Setup page's Adopt section, and confirms - the same "Import untracked
// mods" flow task-A2's own live walk drove from the CLI, now from the
// browser.
func TestE2E_Adopt_SeededUntrackedModBecomesTracked(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	handPlaced := filepath.Join(f.Game.ModPath, "HandPlacedMod")
	require.NoError(t, os.MkdirAll(handPlaced, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(handPlaced, "data.txt"), []byte("x"), 0o644))

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("adopt")),
		chromedp.WaitVisible(`[data-testid="setup-adopt"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="plan-adopt"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="adopt"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-adopt"] .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	installed, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	var found bool
	for _, im := range installed {
		if im.SourceID == "local" || im.Name == "HandPlacedMod" {
			found = true
		}
	}
	assert.True(t, found, "the hand-placed directory must now be a tracked installed mod")
	assert.Empty(t, f.BrowserErrors())
}
