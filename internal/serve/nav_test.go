package serve_test

// I1 (epic live review): before this, layout.gohtml carried exactly one
// nav link ("/"), so a user landing on any page but the first had no way to
// reach any of the other five without hand-typing a URL. These tests pin
// that every one of the six top-level pages renders links to all six
// (itself included), and marks its own with aria-current="page" - the
// accessible way to indicate the current page (WEBUI.md: semantic markup,
// no JS required for core navigation).

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// navPages is every top-level page the nav must link to, alongside the
// path a test actually requests to land on it.
var navPages = []struct {
	Label string
	Path  string
}{
	{"Status", "/"},
	{"Mods", "/mods"},
	{"Search", "/search"},
	{"Updates", "/updates"},
	{"Profiles", "/profiles"},
	{"Health", "/health"},
}

// navCurrentPattern builds the regexp asserting that label's link is the
// one carrying aria-current="page" on a given render.
func navCurrentPattern(label string) *regexp.Regexp {
	return regexp.MustCompile(`<a href="[^"]*" aria-current="page"[^>]*>` + regexp.QuoteMeta(label) + `</a>`)
}

// TestServer_Nav_LinksAllSixPagesFromEveryPage is I1's headline test: every
// one of the six top-level pages must render a link to every other one, and
// mark its own with aria-current.
func TestServer_Nav_LinksAllSixPagesFromEveryPage(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)
	seedInstalledMod(t, svc, game, domain.Mod{ID: "x", SourceID: "fake", Name: "Mod X", Version: "1.0", GameID: game.ID}, true, nil)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	for _, page := range navPages {
		t.Run(page.Label, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+page.Path, nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()

			for _, target := range navPages {
				assert.Contains(t, body, `>`+target.Label+`</a>`,
					"page %q must link to %q", page.Path, target.Label)
			}

			assert.Regexp(t, navCurrentPattern(page.Label), body,
				"page %q must mark its own nav entry (%q) with aria-current", page.Path, page.Label)

			for _, other := range navPages {
				if other.Label == page.Label {
					continue
				}
				assert.NotRegexp(t, navCurrentPattern(other.Label), body,
					"page %q must not mark %q as the current page too", page.Path, other.Label)
			}
		})
	}
}

// TestServer_Nav_CarriesResolvedGameAndProfile pins that a nav link
// preserves the game/profile the user is already looking at, rather than
// silently resetting to whatever the default happens to be on arrival.
func TestServer_Nav_CarriesResolvedGameAndProfile(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Contains(t, rec.Body.String(), fmt.Sprintf(`href="/health?game=%s&amp;profile=default"`, game.ID))
}

// TestServer_Mods_EmptyProfile_LinksToSearch is I1's other named fix: an
// empty profile's /mods page must offer a real way to find something to
// install, not just a bare sentence.
func TestServer_Mods_EmptyProfile_LinksToSearch(t *testing.T) {
	src := newFakeSource("fake")
	svc, game := newFixtureServiceWithSource(t, src)

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/mods", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "No mods installed in this profile.")
	assert.Contains(t, body, fmt.Sprintf(`href="/search?game=%s"`, game.ID))
}
