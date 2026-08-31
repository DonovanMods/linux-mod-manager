package serve

// Task 10: a11y landmark + JS-enhancement hook tests
// (docs/plans/2026-08-30-serve-impl.md Task 10; unit6-carry-list.md).
// Additions only - every Unit 4/5 no-JS assertion is untouched. Each test
// here proves a markup hook app.js depends on, or an a11y landmark the
// keyboard-only walkthrough in the Task 10 report relies on, is actually in
// the rendered HTML rather than only in the template source.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayoutPage_HasSkipLinkAndMainLandmark proves every page carries the
// keyboard-first entry point the a11y walkthrough depends on: a skip link
// that targets the <main> landmark by id, and app.js loading deferred so it
// never blocks first paint.
func TestLayoutPage_HasSkipLinkAndMainLandmark(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/", "")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `href="#main-content"`, "a skip link must target the main landmark")
	assert.Contains(t, body, `id="main-content"`, "the main landmark it targets must exist")
	assert.Contains(t, body, `<script src="/static/app.js" defer>`, "app.js loads on every page, deferred")
}

// TestConfirmPage_CarriesJSEnhancementHook proves the confirm form renders
// the data-js-enhance="confirm" attribute app.js's enhanceConfirmForm scans
// for - without it, no confirm submission on any page would ever be
// intercepted and swapped in place.
func TestConfirmPage_CarriesJSEnhancementHook(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	deployFixtureProfile(t, s, game)

	rec := postForm(s, "/mods/fake/m1/uninstall", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `data-js-enhance="confirm"`)
}
