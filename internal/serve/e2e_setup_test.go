package serve_test

// chromedp coverage for the Setup surface (issue 333, unit7b-task-report.md):
// first-run detect/add, the Setup page's Games/Auth/Sources/Archive
// import/Adopt sections. Builds on e2e_harness_test.go's reusable half -
// sandboxE2EEnv, startE2EServer, newE2EBrowser, e2eZipWith - the same way
// every other unit's own E2E file does.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
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
// app.DetectGames (internal/source/steam) to find one CURATED moddable
// game plus one UNCURATED one (#206): a STEAM_ROOT env override
// (steam.FindSteamRoots' own seam) pointing at a throwaway root with two
// appmanifests, plus a <configDir>/steam-games.yaml override
// (steam.LoadKnownGames' own seam) naming only the first app id, so the
// scan needs no real Steam library or a genuine entry in the embedded
// default list. The second app id has no known-games entry, matching what
// TestE2E_FirstRunUncuratedGame_AddWithDetailsCatalogPick and its siblings
// need: a real "known: false" row from a live scan, not a hand-built one.
type e2eSteamDetectFixture struct {
	Slug string
	Name string

	UnknownAppID string
	UnknownName  string
	UnknownSlug  string

	// SteamRoot is the fake root writeE2ESteamDetectFixture built - handed
	// back so a stale-scan scenario can remove an appmanifest out from
	// under a live server and prove a re-scan actually misses it, rather
	// than asserting on a canned 400.
	SteamRoot string
}

// writeSteamAppManifest writes one appmanifest_<appID>.acf under steamRoot
// and creates the install directory the scan's os.Stat check requires -
// the one appmanifest-writing step writeE2ESteamDetectFixture repeats for
// its curated and uncurated rows.
func writeSteamAppManifest(t *testing.T, steamRoot, appID, installDir, name string) {
	t.Helper()
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
}

// writeE2ESteamDetectFixture wires the fake Steam root and known-games
// override into configDir. The curated row (app 999999, "E2E Detect Game")
// keeps its long-standing slug/mod_path so every existing scenario using
// it is unaffected; the uncurated row (app 888888, "E2E Uncurated Game")
// carries no known-games entry, so a live scan reports it known:false with
// an empty mod_path and a slug DERIVED by the scan itself
// (steam.deriveSlug) - not hand-computed here, so a change to that
// derivation cannot silently desync this fixture from what the scan
// actually returns.
func writeE2ESteamDetectFixture(t *testing.T, configDir string) e2eSteamDetectFixture {
	t.Helper()
	const appID = "999999"
	const installDir = "E2EDetectGame"
	slug := "e2e-detect-game"
	name := "E2E Detect Game"

	const unknownAppID = "888888"
	const unknownInstallDir = "E2EUncuratedGame"
	unknownName := "E2E Uncurated Game"
	unknownSlug := "e2e-uncurated-game"

	steamRoot := t.TempDir()
	writeSteamAppManifest(t, steamRoot, appID, installDir, name)
	writeSteamAppManifest(t, steamRoot, unknownAppID, unknownInstallDir, unknownName)
	t.Setenv("STEAM_ROOT", steamRoot)

	override := appID + `:
  slug: ` + slug + `
  name: "` + name + `"
  nexus_id: ` + slug + `
  mod_path: Data
`
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "steam-games.yaml"), []byte(override), 0o644))

	return e2eSteamDetectFixture{
		Slug: slug, Name: name,
		UnknownAppID: unknownAppID, UnknownName: unknownName, UnknownSlug: unknownSlug,
		SteamRoot: steamRoot,
	}
}

// removeSteamAppManifest deletes one appmanifest from a fixture's fake
// Steam root, so the NEXT scan (a live server re-scans on every request -
// api_games.go's own rule, never trusting a client-supplied row) misses
// that app id entirely - the "stale scan" case a picked row's own
// from_steam_app_id can hit if the game is uninstalled between the scan
// that offered it and the submit that names it.
func removeSteamAppManifest(t *testing.T, steamRoot, appID string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(steamRoot, "steamapps", "appmanifest_"+appID+".acf")))
}

// TestE2E_FirstRunDetect_AddsTheGameAndLandsOnMissionControl drives the
// whole first-run detect flow: "/" with zero games renders the real setup
// flow (not the old placeholder text), the scan finds the fake Steam game -
// widened by #206 to include an UNCURATED one alongside it - selecting the
// curated row by its checkbox and confirming lands on that game's Mission
// Control, exactly as it always has, with the uncurated row shown (not
// hidden) beside it and carrying no checkbox of its own.
func TestE2E_FirstRunDetect_AddsTheGameAndLandsOnMissionControl(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	fixture := writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="first-run-setup"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.setup-detect__row`, chromedp.ByQuery),
	)

	// #206: both rows must be present - the curated one checkable, the
	// uncurated one shown with "Add with details…" and no checkbox at all.
	var rowCount, checkboxCount, addDetailsCount int
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelectorAll('.setup-detect__row').length`, &rowCount),
		chromedp.Evaluate(`document.querySelectorAll('.setup-detect__row input[type="checkbox"]').length`, &checkboxCount),
		chromedp.Evaluate(`document.querySelectorAll('[data-action="add-with-details"]').length`, &addDetailsCount),
	)
	assert.Equal(t, 2, rowCount, "the curated and uncurated rows must both be listed")
	assert.Equal(t, 1, checkboxCount, "only the curated row gets a checkbox")
	assert.Equal(t, 1, addDetailsCount, "only the uncurated row gets Add with details…")

	var knownBadgeCount int
	f.runInBrowser(t, chromedp.Evaluate(
		`document.querySelectorAll('.setup-detect__row .badge--policy').length`, &knownBadgeCount))
	assert.Equal(t, 1, knownBadgeCount, "exactly the curated row is badged Known")

	// First-run readiness item 9: the game name must get its own room
	// (never wrap mid-word) and the path - the full value still available
	// via `title` - is what truncates instead.
	var nameWhiteSpace, pathTitle string
	f.runInBrowser(t,
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.setup-detect__name')).whiteSpace`, &nameWhiteSpace),
		chromedp.AttributeValue(`.setup-detect__path`, "title", &pathTitle, nil, chromedp.ByQuery),
	)
	assert.Equal(t, "nowrap", nameWhiteSpace, "the game name must never wrap mid-word")
	assert.NotEmpty(t, pathTitle, "the truncated path must carry its full value via title")

	f.runInBrowser(t,
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

// TestE2E_FirstRunUncuratedGame_AddWithDetailsCatalogPick drives #206's
// headline flow: an uncurated Steam game's "Add with details…" opens the
// manual add form prefilled from the scan, choosing a source auto-searches
// its catalog by the game's own name and pre-selects the single exact-name
// match WITHOUT submitting anything, and confirming lands on Mission
// Control with the games.yaml row core actually derived (not one the SPA
// invented) - a real from_steam_app_id round trip, not a canned response.
func TestE2E_FirstRunUncuratedGame_AddWithDetailsCatalogPick(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	fixture := writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())
	cat := newE2EGameCatalogSource("catalogsrc")
	cat.entries = []source.GameEntry{
		{ID: "77", Name: fixture.UnknownName, Slug: "uncurated-catalog-slug"},
		{ID: "78", Name: "Some Other Game", Slug: "some-other-game"},
	}
	f.Svc.RegisterSource(cat)

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-action="add-with-details"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="add-with-details"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-detected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="add-install-path-readonly"]`, chromedp.ByQuery),
	)

	// The form must show what the scan found - no editable install path,
	// the detected name already in the Display name field - before the
	// user has picked anything.
	var installPathText, nameValue string
	f.runInBrowser(t,
		chromedp.Text(`[data-testid="add-install-path-readonly"]`, &installPathText, chromedp.ByQuery),
		chromedp.Value(`input[name="add-name"]`, &nameValue, chromedp.ByQuery),
	)
	assert.Contains(t, installPathText, "E2EUncuratedGame")
	assert.Equal(t, fixture.UnknownName, nameValue)

	f.runInBrowser(t, retrySetValue(`select[name="add-source"]`, "catalogsrc"))

	// The auto-search runs on choosing the source (no Search click needed)
	// and pre-selects the exact-name match - visible as the primary button
	// - without submitting the form.
	f.runInBrowser(t,
		chromedp.WaitVisible(`.setup-add__matches button.button--primary`, chromedp.ByQuery),
	)
	var preselectedText string
	f.runInBrowser(t, chromedp.Text(`.setup-add__matches button.button--primary`, &preselectedText, chromedp.ByQuery))
	assert.Equal(t, fixture.UnknownName, strings.TrimSpace(preselectedText))
	var stillOnForm bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-hydrated="true"].mission-control') === null`, &stillOnForm))
	assert.True(t, stillOnForm, "an exact catalog match pre-selects, it never auto-submits")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-hydrated="true"].mission-control`, chromedp.ByQuery),
	)

	var url string
	f.runInBrowser(t, chromedp.Location(&url))
	assert.Contains(t, url, "/g/"+fixture.UnknownSlug+"/")

	got, err := f.Svc.GetGame(fixture.UnknownSlug)
	require.NoError(t, err)
	assert.Equal(t, fixture.UnknownName, got.Name)
	assert.Equal(t, map[string]string{"catalogsrc": "77"}, got.SourceIDs)
	assert.Contains(t, got.ModPath, "mods", "core derived the mod path guess, the form never sent one unedited")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ManualAdd_PickInstalledGamePrefillsForm drives the manual add
// form's OWN "Pick an installed game…" control (independent of
// GameDetectSection's "Add with details…") - opening it, picking a row,
// and confirming the form prefills exactly the way a hand-off from
// GameDetectSection does: read-only install path, the name already filled
// in, and a submit that still needs a source before it will go.
func TestE2E_ManualAdd_PickInstalledGamePrefillsForm(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	fixture := writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())
	f.Svc.RegisterSource(newFakeSource("plain")) // no GameCatalog - identifier only
	// The curated row's own known-games entry implies a NexusMods mapping
	// (nexus_id in the steam-games.yaml override), layered on top of
	// whatever source this test picks (GameSpecFromDetected's own rule) -
	// AddGame validates every id in the resulting map is registered, so
	// this fixture needs "nexusmods" present even though the test never
	// searches it directly.
	f.Svc.RegisterSource(newFakeSource("nexusmods"))

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="pick-installed"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-picker"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="pick-installed-row"]`, chromedp.ByQuery),
	)

	// Two rows on offer (curated + uncurated); pick the CURATED one here -
	// the uncurated pick is exercised end to end by the "Add with
	// details…" test above, so this one covers the other entry point.
	var pickCount int
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelectorAll('[data-action="pick-installed-row"]').length`, &pickCount))
	assert.Equal(t, 2, pickCount)

	f.runInBrowser(t,
		chromedp.Evaluate(fmt.Sprintf(
			`Array.from(document.querySelectorAll('[data-action="pick-installed-row"]')).find(b => b.textContent.includes(%q)).click()`,
			fixture.Name), nil),
		chromedp.WaitVisible(`[data-testid="setup-add-detected"]`, chromedp.ByQuery),
	)

	var nameValue, installPathText string
	f.runInBrowser(t,
		chromedp.Value(`input[name="add-name"]`, &nameValue, chromedp.ByQuery),
		chromedp.Text(`[data-testid="add-install-path-readonly"]`, &installPathText, chromedp.ByQuery),
	)
	assert.Equal(t, fixture.Name, nameValue)
	assert.Contains(t, installPathText, "E2EDetectGame")

	// The curated row's own source map is prefilled server-side, not shown
	// here - the manual form still requires a source to be CHOSEN before it
	// will submit, same as any other row: picking a detected row prefills
	// display fields, it does not silently pick a source for the user.
	var submitDisabled bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-action="add-game"]').disabled`, &submitDisabled))
	assert.True(t, submitDisabled, "a source must still be chosen before submit is enabled")

	f.runInBrowser(t,
		retrySetValue(`select[name="add-source"]`, "plain"),
		chromedp.SendKeys(`input[name="add-identifier"]`, "manual-id", chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-hydrated="true"].mission-control`, chromedp.ByQuery),
	)

	got, err := f.Svc.GetGame(fixture.Slug)
	require.NoError(t, err)
	assert.Equal(t, fixture.Name, got.Name)
	assert.Equal(t, "manual-id", got.SourceIDs["plain"])
	// "plain" has no GameCatalog, so choosing it while a detected row is
	// active auto-runs (and gracefully swallows) a catalog search that 400s
	// - Chrome logs that network response as an error entry independently
	// of the SPA's own handling, the same accepted non-bug
	// TestE2E_FirstRunManualAdd_CatalogAuthRequiredNamesTheSourceNotADeadEnd
	// documents for its own rejected search.
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_ManualAdd_StaleDetectedAppIDOffersRescan pins the stale-scan
// error path: a row is picked, the game it names disappears from disk
// before the submit (the CLI's own detectedGameCandidate hits the same
// core.FindDetectedGame miss), and the form must say so and offer a
// Rescan rather than leaving a dead end.
func TestE2E_ManualAdd_StaleDetectedAppIDOffersRescan(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	fixture := writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())
	f.Svc.RegisterSource(newFakeSource("plain"))

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="add-with-details"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-detected"]`, chromedp.ByQuery),
	)

	// The uncurated game is uninstalled out from under the running server -
	// the NEXT scan (POST /api/v1/games re-scans rather than trusting the
	// browser's own row) will not find it.
	removeSteamAppManifest(t, fixture.SteamRoot, fixture.UnknownAppID)

	f.runInBrowser(t,
		retrySetValue(`select[name="add-source"]`, "plain"),
		chromedp.SendKeys(`input[name="add-identifier"]`, "whatever", chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-game"] .modal__error`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="rescan-detected"]`, chromedp.ByQuery),
	)

	var errorText string
	f.runInBrowser(t, chromedp.Text(`[data-testid="setup-add-game"] .modal__error`, &errorText, chromedp.ByQuery))
	assert.Contains(t, errorText, "no installed Steam game has that app id")

	var onMissionControl bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-hydrated="true"].mission-control') !== null`, &onMissionControl))
	assert.False(t, onMissionControl, "a stale scan must not navigate away")

	// Rescanning drops the stale row and reopens the picker against a
	// fresh scan - the uninstalled game is gone from it.
	f.runInBrowser(t,
		chromedp.Click(`[data-action="rescan-detected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-add-picker"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="pick-installed-row"]`, chromedp.ByQuery),
	)
	var pickCount int
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelectorAll('[data-action="pick-installed-row"]').length`, &pickCount))
	assert.Equal(t, 1, pickCount, "the uninstalled game must be gone from a fresh scan")

	_, err := f.Svc.GetGame(fixture.UnknownSlug)
	assert.Error(t, err, "the failed submit must not have written anything")
	// "plain"'s own auto-search 400 and the from_steam_app_id rejection are
	// both real, expected network responses this test asserts on directly -
	// see the identical rule this suite already applies to a rejected
	// catalog search or a rejected field submit.
	assertNoUncaughtErrors(t, f.BrowserErrors())
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

// TestE2E_ManualAdd_IdentifierHintsPerSourceType pins first-run readiness
// item 4: the "Identifier with that source" field had no placeholder, no
// example and no per-source hint, so a first-run user who cannot use
// detect had to guess what it even looks like. Nexus and CurseForge are
// both built-ins with no wire-level distinction (source.TypeLabelOf
// reports both "built-in"), so the id itself is what selects the hint.
func TestE2E_ManualAdd_IdentifierHintsPerSourceType(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	f.Svc.RegisterSource(newFakeSource("nexusmods"))
	f.Svc.RegisterSource(newFakeSource("unknown-source"))

	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		retrySetValue(`select[name="add-source"]`, "nexusmods"),
	)
	var placeholder, hint string
	f.runInBrowser(t,
		chromedp.AttributeValue(`input[name="add-identifier"]`, "placeholder", &placeholder, nil, chromedp.ByQuery),
		chromedp.Text(`.setup-add__identifier-hint`, &hint, chromedp.ByQuery),
	)
	assert.Equal(t, "skyrimspecialedition", placeholder)
	assert.Contains(t, hint, "NexusMods")

	f.runInBrowser(t, retrySetValue(`select[name="add-source"]`, "unknown-source"))
	var noHintCount int
	f.runInBrowser(t, chromedp.Evaluate(
		`document.querySelectorAll('.setup-add__identifier-hint').length`, &noHintCount))
	assert.Zero(t, noHintCount, "a source with no known hint must not show a stale one")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_ManualAdd_GameIDFieldErrorOpensAdvancedAndRendersInline is N-6 of
// the epic re-review, superseding the old Minor 2 regression (unit7-
// review.md): "game_id" used to be a GameSpecError field this form had no
// input for at all - only ever set from a catalog match's own game_id,
// never typed directly - so a rejection naming it fell back to the
// form-wide banner. N-6 gave it a real Advanced input, so KNOWN_FIELD_ERRORS
// now covers it like every other named field: the error renders directly
// under the Game id input, which must auto-open (it starts collapsed) so
// the user is not left staring at a busy-cleared button with the actual
// reason hidden inside a <details> they never opened.
//
// A catalog whose slug derives to a path-unsafe game id (DeriveGameID
// lower-cases and dashes spaces, but does not strip "/") reproduces it from
// the browser.
func TestE2E_ManualAdd_GameIDFieldErrorOpensAdvancedAndRendersInline(t *testing.T) {
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
		chromedp.WaitVisible(`[data-testid="setup-add-advanced"][open] .modal__error`, chromedp.ByQuery),
	)

	var errorText string
	f.runInBrowser(t, chromedp.Text(`[data-testid="setup-add-advanced"] .modal__error`, &errorText, chromedp.ByQuery))
	assert.Contains(t, errorText, "path separators",
		"the field-specific reason must render under the Game id input, not the form-wide banner")

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

// TestE2E_ManualAdd_AdvancedGameIDOverridesTheDerivedKey is N-6 of the epic
// re-review: `lmm game add --game-id` had no web input at all -
// gameadd.js:180's own doc comment used to document the absence - so a
// custom-source user's only way to choose the local games.yaml key by hand
// was the CLI. The manual identifier path derives one from the identifier
// when the field is left blank (DeriveGameID), so filling in a DIFFERENT
// id under Advanced and confirming it, not the derived one, is what wins is
// the only way to prove the override actually reaches the request.
func TestE2E_ManualAdd_AdvancedGameIDOverridesTheDerivedKey(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	f.Svc.RegisterSource(newFakeSource("plain")) // no GameCatalog - identifier only

	install := t.TempDir()
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
		retrySetValue(`select[name="add-source"]`, "plain"),
		chromedp.SendKeys(`input[name="add-identifier"]`, "acme", chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-name"]`, "Acme Game", chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-install-path"]`, install, chromedp.ByQuery),
		chromedp.Click(`[data-testid="setup-add-advanced"] summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`input[name="add-game-id"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="add-game-id"]`, "acme-custom", chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-hydrated="true"].mission-control`, chromedp.ByQuery),
	)

	var url string
	f.runInBrowser(t, chromedp.Location(&url))
	assert.Contains(t, url, "/g/acme-custom/", "the game must be addressed by the id typed under Advanced")

	got, err := f.Svc.GetGame("acme-custom")
	require.NoError(t, err, "the typed id must be the one actually saved")
	assert.Equal(t, "Acme Game", got.Name)

	_, err = f.Svc.GetGame("acme")
	assert.Error(t, err, "the derived id (from the identifier alone) must NOT have been used instead")
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
	//
	// It is TEXT beside the field rather than a placeholder inside it since
	// M-1/D-3 of the epic live review: as a placeholder it was visibly
	// truncated in a ~190px input and vanished on the first keystroke, in
	// the one place the UI names the variable at all.
	var envHint string
	f.runInBrowser(t, textContent(`[data-source="authy"] .setup-auth__env-var`, &envHint))
	assert.Equal(t, "or set LMM_AUTHY_API_KEY", envHint)

	f.runInBrowser(t,
		chromedp.SendKeys(`[data-source="authy"] input[type="password"]`, badKey, chromedp.ByQuery),
		chromedp.Click(`[data-source="authy"] button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-source="authy"] .modal__error`, chromedp.ByQuery),
	)
	assertKeyNeverRendered(t, badKey)
	// #333 Minor #11 + first-run readiness item 6: OuterHTML never
	// carries an input's live `value` property (markup serialisation does
	// not include it), so assertKeyNeverRendered above cannot prove
	// anything about it either way - read the DOM property directly. A
	// 400 (the validator's own verdict) now KEEPS the typed value rather
	// than clearing it, so a single-character typo in a long key does not
	// cost retyping the whole thing.
	var valueAfterRejection string
	f.runInBrowser(t, chromedp.Value(`[data-source="authy"] input[type="password"]`, &valueAfterRejection, chromedp.ByQuery))
	assert.Equal(t, badKey, valueAfterRejection, "a 400 (the validator's own verdict) must keep the typed value for an easy retry")

	f.runInBrowser(t,
		// Reset the retained bad key by hand before typing the good one -
		// SendKeys appends to whatever the field already holds, and this
		// is a controlled input (Preact tracks it via onInput), so the
		// native value setter plus a real "input" event is what actually
		// clears it rather than only the DOM attribute.
		chromedp.Evaluate(`(() => {
			const el = document.querySelector('[data-source="authy"] input[type="password"]');
			Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set.call(el, "");
			el.dispatchEvent(new Event("input", { bubbles: true }));
		})()`, nil),
		chromedp.SendKeys(`[data-source="authy"] input[type="password"]`, goodKey, chromedp.ByQuery),
		chromedp.Click(`[data-source="authy"] button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-source="authy"] .badge--good`, chromedp.ByQuery),
	)
	assertKeyNeverRendered(t, goodKey)

	var maskedVisible bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-source="authy"] .mono') !== null`, &maskedVisible))
	assert.True(t, maskedVisible, "the masked form must render once authenticated")

	// Issue 334 (Unit 7 review Minor #6): this fixture's source is
	// registered in memory with no definition file behind it, so
	// app.RekeySource cannot rebuild it and the live swap genuinely fails -
	// which is precisely the condition restart_required exists to report.
	// Before it, the row said "authenticated" and nothing else, while the
	// running server kept using the old credential.
	var restartNotice string
	f.runInBrowser(t, textContent(`[data-source="authy"] [data-testid="auth-restart-required"]`, &restartNotice))
	assert.Contains(t, restartNotice, "restart",
		"a login the running server did not pick up must SAY so, not claim success in silence")

	f.runInBrowser(t,
		chromedp.Click(`[data-source="authy"] button.button--small`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-source="authy"] input[type="password"]`, chromedp.ByQuery),
	)
	assertKeyNeverRendered(t, goodKey)
	var valueAfterLogout string
	f.runInBrowser(t, chromedp.Value(`[data-source="authy"] input[type="password"]`, &valueAfterLogout, chromedp.ByQuery))
	assert.Empty(t, valueAfterLogout, "a freshly re-rendered login form must start with an empty field")
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

	// First-run readiness item 10: a one-line "what is a custom source?"
	// with a link to the README's own section, shown above the editor -
	// the previous state dropped the user straight into a YAML textarea
	// with no explanation at all.
	var docsHref, docsText string
	f.runInBrowser(t,
		chromedp.AttributeValue(`[data-testid="setup-sources"] a[target="_blank"]`, "href", &docsHref, nil, chromedp.ByQuery),
		chromedp.Text(`[data-testid="setup-sources"] a[target="_blank"]`, &docsText, chromedp.ByQuery),
	)
	assert.Contains(t, docsHref, "custom-sources")
	assert.Contains(t, docsText, "custom source")

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

// TestE2E_SourcesTableActionsColumnIsTheSameWidthOnEveryRow is N-2 of the
// epic re-review (= epic M-4): the previous fix put `display: flex` on the
// actions <td> itself, which computes it out of the table's own row layout
// - so a built-in source's EMPTY actions cell (no Download/Edit/Delete)
// measured its own near-zero content width instead of the column's real
// width, leaving that row's bottom border a detached fragment instead of
// running the column's full span. "fake" (a built-in, non-custom type) has
// no actions; "my-mods" (a custom directory source) has all three - so
// their last cell's rendered widths, and the header's blank one above them,
// must all agree.
func TestE2E_SourcesTableActionsColumnIsTheSameWidthOnEveryRow(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))
	dir := t.TempDir()
	yaml := []byte("id: my-mods\nname: My Mods\ntype: directory\ndirectory:\n  path: " + dir + "\n")
	_, err := app.SaveSourceDefinition(f.Ctx, f.Svc, "", yaml)
	require.NoError(t, err)

	var widths []float64
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`tr[data-source="fake"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`tr[data-source="my-mods"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".setup-table__actions"))
				.map((el) => el.getBoundingClientRect().width)
		`, &widths),
	)

	require.Len(t, widths, 3, "the header th plus the two rows' td, all sharing the class")
	assert.InDelta(t, widths[0], widths[1], 1,
		"the built-in row's (empty) actions cell must be the header's width, not its own content's")
	assert.InDelta(t, widths[0], widths[2], 1,
		"the custom row's (populated) actions cell must be the same width too")
	assert.Empty(t, f.BrowserErrors())
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

// TestE2E_Setup_TabsDeepLinkAndAreKeyboardNavigable pins Minor #13(b) and
// #13(c): switching a Setup tab must write ?section= (a reload otherwise
// always lands back on Games), and the tablist must implement the WAI-ARIA
// tabs pattern completely - a roving tabindex (only the active tab is a
// Tab stop) and ArrowLeft/ArrowRight/Home/End - rather than the partial
// role="tablist"/role="tab" markup the review found worse for a screen
// reader than plain buttons would have been.
func TestE2E_Setup_TabsDeepLinkAndAreKeyboardNavigable(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("")),
		chromedp.WaitVisible(`[data-testid="setup-page"]`, chromedp.ByQuery),
		chromedp.Click(`[data-section="sources"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
	)

	var search string
	f.runInBrowser(t, chromedp.Evaluate(`window.location.search`, &search))
	assert.Equal(t, "?section=sources", search, "switching tabs must write ?section=")

	f.runInBrowser(t,
		chromedp.Reload(),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
	)
	var selectedAfterReload string
	f.runInBrowser(t, chromedp.AttributeValue(
		`[data-section="sources"]`, "aria-selected", &selectedAfterReload, nil, chromedp.ByQuery))
	assert.Equal(t, "true", selectedAfterReload, "a reload must keep the deep-linked tab active")

	// Roving tabindex: only the active tab ("sources") is a Tab stop.
	var activeTabIndex, inactiveTabIndex string
	f.runInBrowser(t,
		chromedp.AttributeValue(`[data-section="sources"]`, "tabindex", &activeTabIndex, nil, chromedp.ByQuery),
		chromedp.AttributeValue(`[data-section="games"]`, "tabindex", &inactiveTabIndex, nil, chromedp.ByQuery),
	)
	assert.Equal(t, "0", activeTabIndex)
	assert.Equal(t, "-1", inactiveTabIndex)

	// ArrowRight from "sources" moves to and activates "archive" (the next
	// tab in SECTIONS' own order: games, auth, sources, archive, adopt).
	f.runInBrowser(t,
		chromedp.Focus(`[data-section="sources"]`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.ArrowRight),
		chromedp.WaitVisible(`[data-testid="setup-import-archive"]`, chromedp.ByQuery),
	)
	var focused string
	f.runInBrowser(t, chromedp.Evaluate(`document.activeElement.dataset.section`, &focused))
	assert.Equal(t, "archive", focused, "ArrowRight must move focus to the next tab, not just activate it")

	// Home jumps to the first tab ("games") from wherever focus is.
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.Home),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
	)
	f.runInBrowser(t, chromedp.Evaluate(`document.activeElement.dataset.section`, &focused))
	assert.Equal(t, "games", focused)

	// End jumps to the last tab ("adopt").
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.End),
		chromedp.WaitVisible(`[data-testid="setup-adopt"]`, chromedp.ByQuery),
	)
	f.runInBrowser(t, chromedp.Evaluate(`document.activeElement.dataset.section`, &focused))
	assert.Equal(t, "adopt", focused)

	// The single tabpanel's aria-labelledby tracks whichever tab is active.
	var labelledBy string
	f.runInBrowser(t, chromedp.AttributeValue(
		`[role="tabpanel"]`, "aria-labelledby", &labelledBy, nil, chromedp.ByQuery))
	assert.Equal(t, "setup-tab-adopt", labelledBy)

	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_Sources_ProbeIDFieldReachesTheServer pins Minor #13(d): probing
// an `api` definition with no search endpoint requires an explicit mod id
// (app.ProbeSource's own "no search endpoint" guard), which the source
// editor had no field for at all.
func TestE2E_Sources_ProbeIDFieldReachesTheServer(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	yaml := `id: probe-api
name: Probe API
type: api
api:
  base_url: https://api.example.test
  endpoints:
    get_mod:
      path: /mods/{mod_id}
  mappings:
    mod:
      id: id
      name: name
`

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="new-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
		chromedp.SetValue(`.source-editor__textarea`, yaml, chromedp.ByQuery),
		chromedp.Click(`.plan__control--inline input[type="checkbox"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.source-editor input[type="text"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.source-editor input[type="text"]`, "some-mod-id", chromedp.ByQuery),
		chromedp.Click(`[data-action="validate-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.source-editor__report`, chromedp.ByQuery),
	)

	var reportText string
	f.runInBrowser(t, chromedp.Text(`.source-editor__report`, &reportText, chromedp.ByQuery))
	// The fake api.example.test base URL can never actually answer, so the
	// live probe fails - but reaching THAT failure (a network/transport
	// error) rather than ProbeSource's own "pass --id" refusal proves the
	// mod id this field supplies made it onto the wire.
	assert.NotContains(t, reportText, "--id", "the probe_id field must reach the server, not fall back to the CLI's own guard message")
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

// TestE2E_ArchiveImport_SuccessDropsTheStaleDiscardSentence pins Minor
// #13(a): the server already deletes a SUCCESSFUL import's staged upload
// (kind_import_archive.go's applyImportArchiveKind), but the panel kept
// telling the user it was "still staged - discarded automatically after
// 30 minutes" for a file that no longer existed. A successful import must
// drop the sentence (and the now-meaningless source/mod-id fields) while
// the job's own outcome stays visible until dismissed (N7's completion
// affordance).
func TestE2E_ArchiveImport_SuccessDropsTheStaleDiscardSentence(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	zipPath := filepath.Join(t.TempDir(), "GammaMod-1.0.zip")
	require.NoError(t, os.WriteFile(zipPath, e2eZipWith("GammaMod/data.txt", "gamma content"), 0o644))

	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("archive")),
		chromedp.WaitVisible(`[data-testid="setup-import-archive"]`, chromedp.ByQuery),
		chromedp.SetUploadFiles(`[data-testid="setup-import-archive"] input[type="file"]`, []string{zipPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="staged-upload"]`, chromedp.ByQuery),
	)

	var beforeText string
	f.runInBrowser(t, chromedp.Text(`[data-testid="staged-upload"]`, &beforeText, chromedp.ByQuery))
	assert.Contains(t, beforeText, "discarded", "sanity: the sentence renders before the import runs")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="import-archive"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="import_archive"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-import-archive"] .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	var afterText string
	f.runInBrowser(t, chromedp.Text(`[data-testid="staged-upload"]`, &afterText, chromedp.ByQuery))
	assert.NotContains(t, afterText, "discarded", "the stale sentence must not survive a successful import")
	assert.NotContains(t, afterText, "Link to a source", "the now-meaningless source/mod-id fields must go with it")
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
	)

	// #333 fix-wave N2: the same htm whitespace-dropping defect Minor 1 fixed
	// in the import-archive modal ("BetaMod-1.0.zip asBetaMod") also reached
	// this modal ("1 untracked modfound (0 already tracked)." and
	// "HandPlacedModno source match"), because htm drops a whitespace-only
	// text chunk sitting between two interpolations or adjacent elements.
	var summary, row string
	f.runInBrowser(t,
		chromedp.Text(`.modal .plan__summary`, &summary, chromedp.ByQuery),
		chromedp.Text(`.modal .plan__mods li`, &row, chromedp.ByQuery),
	)
	assert.Contains(t, summary, "mod found (", "the plural suffix and \"found\" must not fuse together")
	assert.NotContains(t, summary, "modfound", "the plural suffix and \"found\" must not fuse together")
	assert.NotContains(t, row, "HandPlacedModno", "the mod name and its match detail must not fuse together")
	assert.Contains(t, row, "HandPlacedMod no source match", "the mod name and its match detail must be space-separated")

	f.runInBrowser(t,
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

// TestE2E_FirstRunCustomSourceThroughToAMappedGame is C-4 of the epic live
// review, driven as the review's own new-user drive drove it.
//
// The dead end: the custom-source editor lived only at
// /g/{game}/{profile}/setup, which needs a game; the first-run game form
// offered only sources that already existed; and NEITHER frontend could add
// a source to an existing game. So the case the README leads with - a
// directory source over a local mod folder - could not be started at all
// without stopping the server and hand-editing games.yaml.
//
// This is the whole path in one go: define the source on first run, add a
// game against it, and confirm the mapping is real by reading the game back.
func TestE2E_FirstRunCustomSourceThroughToAMappedGame(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	modsDir := t.TempDir()
	installDir := t.TempDir()

	yaml := "id: my-mods\nname: My Mods\ntype: directory\ndirectory:\n  path: " + modsDir + "\n"
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="first-run-setup"]`, chromedp.ByQuery),
		// The section is a disclosure rather than always-open: a user who
		// already has a built-in source configured should not have to scroll
		// past a YAML editor to add their game.
		chromedp.Click(`[data-testid="first-run-sources"] summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="new-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
		chromedp.SetValue(`.source-editor__textarea`, yaml, chromedp.ByQuery),
		// Save stays disabled until Validate has reported the draft valid -
		// the editor's own rule, unchanged here.
		chromedp.Click(`[data-action="validate-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.source-editor__report .plan__note`, chromedp.ByQuery),
		chromedp.Click(`[data-action="save-source"]`, chromedp.ByQuery),
		// Hot-registered: the source is live in the running process, which
		// is what lets the add form below offer it with no restart.
		chromedp.WaitVisible(`tr[data-source="my-mods"]`, chromedp.ByQuery),
	)

	f.runInBrowser(t,
		chromedp.Poll(`Array.from(document.querySelectorAll('select[name="add-source"] option')).some((o) => o.value === "my-mods")`,
			nil, chromedp.WithPollingInterval(100*time.Millisecond)),
		chromedp.SetValue(`select[name="add-source"]`, "my-mods", chromedp.ByQuery),
		chromedp.SetValue(`input[name="add-name"]`, "My Game", chromedp.ByQuery),
		chromedp.SetValue(`input[name="add-install-path"]`, installDir, chromedp.ByQuery),
		// A directory source's identifier is legitimately empty, so the form
		// has to accept that - it is the shape the README's own example uses.
		chromedp.SetValue(`input[name="add-identifier"]`, "local", chromedp.ByQuery),
		chromedp.Click(`[data-action="add-game"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
	)

	games := f.Svc.ListGames()
	require.Len(t, games, 1)
	assert.Contains(t, games[0].SourceIDs, "my-mods",
		"the game added on first run must map the source the same flow just created")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupGamesEditSourcesMapsAnExistingGame is C-4's other half: a
// source created AFTER the game exists.
//
// This is the "In use: —" state the review's drive got stuck in - the source
// saves beautifully and then sits there, because neither frontend could
// attach it to a game. The Games table's own row now can, over
// PUT /api/v1/games/{id}.
func TestE2E_SetupGamesEditSourcesMapsAnExistingGame(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))
	dir := t.TempDir()

	yaml := "id: my-mods\nname: My Mods\ntype: directory\ndirectory:\n  path: " + dir + "\n"
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="new-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="source-editor"]`, chromedp.ByQuery),
		chromedp.SetValue(`.source-editor__textarea`, yaml, chromedp.ByQuery),
		chromedp.Click(`[data-action="validate-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.source-editor__report .plan__note`, chromedp.ByQuery),
		chromedp.Click(`[data-action="save-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`tr[data-source="my-mods"]`, chromedp.ByQuery),

		chromedp.Navigate(f.SetupPath("games")),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="edit-sources"][data-game="g1"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="sources-map"]`, chromedp.ByQuery),
		chromedp.Click(`input[name="source-my-mods"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="save-sources"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-testid="setup-games"]')?.textContent.includes("my-mods")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
	)

	games := f.Svc.ListGames()
	require.Len(t, games, 1)
	assert.Contains(t, games[0].SourceIDs, "my-mods",
		"the mapping the table wrote must be on disk, not just on screen")
	assert.Contains(t, games[0].SourceIDs, "fake",
		"and a replacement-shaped PUT must not have dropped the source it already had")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
