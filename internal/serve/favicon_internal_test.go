package serve

// The tab icon (issue 435): one embedded asset, two paths to it, and a shell
// that names it.
//
// Before this the shell declared no icon at all, so a `lmm serve` tab showed
// the browser's generic page glyph and every load fired a GET /favicon.ico
// that spaRoutes - which registers no bare "/" catch-all on purpose - had
// nothing to answer with.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFavicon_ServedAtBothPathsFromOneAsset pins the route, its content type
// and the identity of the two copies. The identity is the point: a second,
// separately-maintained raster icon is exactly the drift this avoids.
func TestFavicon_ServedAtBothPathsFromOneAsset(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	for _, path := range []string{"/favicon.ico", "/static/favicon.svg"} {
		t.Run(path, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, path, "")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Header().Get("Content-Type"), "image/svg+xml")
			assert.Equal(t, string(faviconBytes), rec.Body.String(),
				"both paths answer with the one embedded mark")
		})
	}
}

// TestFavicon_ShellDeclaresIt is the other half: a route nothing references
// is a route no browser ever asks for on a page that has a <link>.
func TestFavicon_ShellDeclaresIt(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `<link rel="icon" type="image/svg+xml" href="/static/favicon.svg" />`)
}

// TestFavicon_IsAnSVGWithNoScriptOrExternalReference keeps the mark to what
// a favicon has any business being. An SVG is a document, not a bitmap: it
// can carry script and can fetch, and this one is served same-origin from
// the same binary as the application it decorates.
//
// The Content-Security-Policy does not reach inside an image the browser
// chrome paints outside the page, so the guarantee has to be a property of
// the asset itself rather than of a header.
func TestFavicon_IsAnSVGWithNoScriptOrExternalReference(t *testing.T) {
	mark := string(faviconBytes)

	assert.Contains(t, mark, "<svg")
	for _, forbidden := range []string{"<script", "<foreignObject", "<image", "<use", "href", "url("} {
		assert.NotContains(t, mark, forbidden,
			"a tab icon neither runs anything nor fetches anything")
	}
	// The SVG namespace is the one URL a standalone SVG must carry, and it is
	// a NAME rather than a fetch - so it is excluded by hand here instead of
	// weakening the rule above into something that would miss a real one.
	assert.NotContains(t,
		strings.Replace(mark, `xmlns="http://www.w3.org/2000/svg"`, "", 1),
		"http",
		"nothing in this document points anywhere")
	assert.True(t, strings.Contains(mark, `viewBox="0 0 32 32"`),
		"and scales from one square drawing, rather than shipping a size per platform")
}
