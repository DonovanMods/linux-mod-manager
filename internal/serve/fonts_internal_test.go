package serve

// The bundled UI fonts (#523): Inter and JetBrains Mono, embedded beside the
// vendored JavaScript so every machine draws the page in the same face
// instead of whatever its system-ui resolves to.
//
// These pin the server half - the bytes, the content type, the caching, and
// the stylesheet that declares them. Whether a browser actually admits and
// uses them is e2e_bundled_font_test.go's question.

import (
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vendoredFonts lists the .woff2 files embedded in vendorFS.
func vendoredFonts(t *testing.T) []string {
	t.Helper()
	fonts, err := fs.Glob(vendorFS, "vendor/*.woff2")
	require.NoError(t, err)
	require.NotEmpty(t, fonts, "the UI's fonts are embedded")
	for i, f := range fonts {
		fonts[i] = path.Base(f)
	}
	return fonts
}

// TestFonts_ServedVerbatimAsWoff2 serves every embedded font with its exact
// bytes, font/woff2 - which Go's mime table has no entry for, so without an
// explicit type the header would depend on the host's /etc/mime.types - and
// the same revalidated caching every other asset gets.
func TestFonts_ServedVerbatimAsWoff2(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	for _, name := range vendoredFonts(t) {
		t.Run(name, func(t *testing.T) {
			want, err := vendorFS.ReadFile("vendor/" + name)
			require.NoError(t, err)

			rec := doAPI(s, http.MethodGet, "/vendor/"+name, "")

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "font/woff2", rec.Header().Get("Content-Type"))
			assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
			assert.NotEmpty(t, rec.Header().Get("ETag"))
			assert.Equal(t, want, rec.Body.Bytes(), "served byte for byte, never re-encoded")
		})
	}
}

// fontFaceBlock matches one @font-face rule in app.css.
var fontFaceBlock = regexp.MustCompile(`(?s)@font-face\s*\{(.*?)\}`)

// fontFaceURL matches a rule's same-origin /vendor/ source.
var fontFaceURL = regexp.MustCompile(`url\("/vendor/([^"]+\.woff2)"\)\s*format\("woff2"\)`)

// TestFonts_StylesheetDeclaresEveryVendoredFont holds app.css and the
// embedded fonts in step: every @font-face names a font that is embedded,
// every embedded font is declared (an undeclared one is dead weight in the
// binary), each swaps rather than hiding text while it loads, and the two
// type tokens lead with the bundled families while keeping the system
// faces behind them as fallbacks.
func TestFonts_StylesheetDeclaresEveryVendoredFont(t *testing.T) {
	raw, err := spaFS.ReadFile("spa/app.css")
	require.NoError(t, err)
	css := string(raw)

	declared := map[string]bool{}
	blocks := fontFaceBlock.FindAllStringSubmatch(css, -1)
	require.NotEmpty(t, blocks, "app.css declares the bundled fonts")
	for _, b := range blocks {
		src := fontFaceURL.FindAllStringSubmatch(b[1], -1)
		require.Len(t, src, 1, "one same-origin woff2 source per @font-face:\n%s", b[1])
		declared[src[0][1]] = true
		assert.Contains(t, b[1], "font-display: swap;", src[0][1])
	}
	for _, name := range vendoredFonts(t) {
		assert.True(t, declared[name], "%s is embedded but no @font-face declares it", name)
		delete(declared, name)
	}
	assert.Empty(t, declared, "an @font-face names a font that is not embedded")

	for token, want := range map[string][]string{
		"--font-sans": {`"Inter"`, "system-ui", "sans-serif"},
		"--font-mono": {`"JetBrains Mono"`, "ui-monospace", "monospace"},
	} {
		m := regexp.MustCompile(`(?s)` + token + `:\s*(.*?);`).FindStringSubmatch(css)
		require.NotNil(t, m, token)
		value := strings.Join(strings.Fields(m[1]), " ")
		assert.True(t, strings.HasPrefix(value, want[0]+","), "%s leads with the bundled face: %s", token, value)
		for _, fallback := range want[1:] {
			assert.Contains(t, value, fallback, "%s keeps the system fallback", token)
		}
	}
}

// TestFonts_ShellPreloadsTheTextFace: the shell asks for the upright Inter
// before the stylesheet is even parsed, so on a local server it is in hand
// by first paint and the page does not visibly swap faces. crossorigin is
// required even same-origin - a font is always fetched in CORS mode, and a
// preload without it is a second, unused download.
func TestFonts_ShellPreloadsTheTextFace(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/", "")

	require.Equal(t, http.StatusOK, rec.Code)
	shell := strings.Join(strings.Fields(rec.Body.String()), " ")
	assert.Contains(t, shell,
		`<link rel="preload" href="/vendor/InterVariable.woff2" as="font" type="font/woff2" crossorigin />`)
}
