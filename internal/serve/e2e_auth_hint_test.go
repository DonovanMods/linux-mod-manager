package serve_test

// #582: a source with no usable key - most often one set only in a login
// shell, which `lmm serve` under its systemd unit never sees - refuses the
// health check, the update check and searches. Wherever the web UI says so
// it links to where that source's key is set: Setup > Authentication,
// scrolled to and focused on that source's row.

import (
	"context"
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// e2eKeylessSource is an auth-capable source with no key: every call that
// needs one answers domain.ErrAuthRequired, the way CurseForge does when
// CURSEFORGE_API_KEY is not in lmm-serve's environment.
type e2eKeylessSource struct{ *e2eAuthSource }

func (s *e2eKeylessSource) Name() string { return "Keyless Source" }

func (s *e2eKeylessSource) refusal() error {
	return fmt.Errorf("getting mod files: %w: Keyless Source API key required", domain.ErrAuthRequired)
}

func (s *e2eKeylessSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, s.refusal()
}

func (s *e2eKeylessSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, s.refusal()
}

func (s *e2eKeylessSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, s.refusal()
}

// IsAuthenticated reports a key the source holds but that was refused, so
// a search warns about it rather than skipping it as never signed in.
func (s *e2eKeylessSource) IsAuthenticated() bool { return true }

var _ source.ModSource = (*e2eKeylessSource)(nil)

// newE2EFixtureWithAKeylessSource is newE2EFixture plus the keyless source
// mapped to the game and one installed mod from it with a recorded file,
// so verify's version pass asks the source - and is refused.
func newE2EFixtureWithAKeylessSource(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixture(t)
	f.Svc.RegisterSource(&e2eKeylessSource{newE2EAuthSource("keyless", "never")})
	f.Game.SourceIDs["keyless"] = ""
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	gameCache := f.Svc.GetGameCache(f.Game)
	require.NoError(t, gameCache.Store(f.Game.ID, "keyless", "km", "1.0", "f1", []byte("content")))
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "km", SourceID: "keyless", Name: "Keyed Mod", Version: "1.0", GameID: f.Game.ID},
		ProfileName:  f.Profile,
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	return f
}

// focusedKeylessKeyInput is true when keyboard focus is in the keyless
// source's API key field on Setup > Authentication.
const focusedKeylessKeyInput = `document.activeElement === document.querySelector('.setup-auth__row[data-source="keyless"] input[type="password"]')`

// TestE2E_AuthHint_HealthRowLinksToTheSourcesKey: the health card's row
// for a version check the source refused names the missing key and offers
// "Add API key…" in place of the dead "Not fixable", and the link lands on
// that source's key field.
func TestE2E_AuthHint_HealthRowLinksToTheSourcesKey(t *testing.T) {
	f := newE2EFixtureWithAKeylessSource(t)

	const row = `.card--health [data-status="skipped"]`
	var rowText, path, search string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(fmt.Sprintf(`document.querySelector(%q) !== null`, row+` [data-action="add-api-key"]`)),
		textContent(row, &rowText),
		clickWhenSettled(row+` [data-action="add-api-key"]`),
		pollUntil(focusedKeylessKeyInput),
		chromedp.Evaluate(`location.pathname`, &path),
		chromedp.Evaluate(`location.search`, &search),
	)
	assert.Contains(t, rowText, "Keyed Mod")
	// The fixture holds a key the source refuses, so it was rejected.
	assert.Contains(t, rowText, "Keyless Source rejected its API key")
	assert.Contains(t, rowText, "Add API key…")
	assert.NotContains(t, rowText, "Not fixable")
	assert.Equal(t, f.ContextPath()+"/setup", path)
	assert.Contains(t, search, "section=auth")
	assert.Contains(t, search, "source=keyless")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AuthHint_UpdatesCardLinksToTheSourcesKey: an update check the
// source refused used to leave the card silent - the report's error was
// never rendered. It now names the source and links to its key.
func TestE2E_AuthHint_UpdatesCardLinksToTheSourcesKey(t *testing.T) {
	f := newE2EFixtureWithAKeylessSource(t)

	const note = `.card--updates [data-testid="updates-auth-required"]`
	var noteText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(fmt.Sprintf(`document.querySelector(%q) !== null`, note+` [data-action="add-api-key"]`)),
		textContent(note, &noteText),
		clickWhenSettled(note+` [data-action="add-api-key"]`),
		pollUntil(focusedKeylessKeyInput),
	)
	assert.Contains(t, noteText, "Keyless Source")
	assert.Contains(t, noteText, "API key")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AuthHint_SearchWarningLinksToTheSourcesKey: a search the source
// refused for its key keeps its warning row, which now offers the link.
func TestE2E_AuthHint_SearchWarningLinksToTheSourcesKey(t *testing.T) {
	f := newE2EFixtureWithAKeylessSource(t)

	const warning = `.search-result--warning`
	var warningText string
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+f.ContextPath()+"/search?q=anything"),
		pollUntil(fmt.Sprintf(`document.querySelector(%q) !== null`, warning+` [data-action="add-api-key"]`)),
		textContent(warning, &warningText),
		clickWhenSettled(warning+` [data-action="add-api-key"]`),
		pollUntil(focusedKeylessKeyInput),
	)
	assert.Contains(t, warningText, "keyless")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AuthHint_AnAuthenticatedRowIsFocusedNotItsLogOut: a key the
// source REJECTED still shows its row as authenticated, whose only control
// is "Log out" - focusing the row's first control would put one Enter
// between the user and deleting the key. The deep link focuses the row
// itself instead.
func TestE2E_AuthHint_AnAuthenticatedRowIsFocusedNotItsLogOut(t *testing.T) {
	f := newE2EFixtureWithAKeylessSource(t)
	require.NoError(t, f.Svc.SaveSourceToken(t.Context(), "keyless", "rejected-key-0123456789"))

	const rowSel = `.setup-auth__row[data-source="keyless"]`
	var focusedTag string
	var hasInput bool
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+f.ContextPath()+"/setup?section=auth&source=keyless"),
		pollUntil(fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, rowSel)),
		chromedp.Evaluate(`document.activeElement.tagName`, &focusedTag),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q + ' input') !== null`, rowSel), &hasInput),
	)
	assert.Equal(t, "LI", focusedTag)
	assert.False(t, hasInput, "the row is the authenticated one, with Log out and no key field")
	assert.Empty(t, f.BrowserErrors())
}
