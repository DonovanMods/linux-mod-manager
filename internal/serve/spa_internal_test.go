package serve

// The SPA shell and its routing (docs/plans/2026-08-31-serve-spa-design.md
// §Information architecture, §Architecture): one embedded HTML shell served
// for every app route, the ES modules and vendored libraries it loads, and
// the 301s from the URLs the deleted page layer owned.

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSPAShell_SameBytesAtEveryAppRoute is the shell's defining property:
// "/" and any /g/{game}/{profile} route are the SAME document. The router
// runs in the browser, so a deep link is not a different page - it is this
// page, told where it is by its own URL.
func TestSPAShell_SameBytesAtEveryAppRoute(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	root := doAPI(s, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, root.Code)
	assert.Contains(t, root.Header().Get("Content-Type"), "text/html")

	for _, path := range []string{
		"/g/g1/default",
		"/g/g1/default/search",
		"/g/g1/default/mod/fake/m1",
		"/g/some-unconfigured-game/whatever",
	} {
		t.Run(path, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, path, "")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
			assert.Equal(t, root.Body.String(), rec.Body.String(),
				"every app route must serve the identical shell")
		})
	}
}

// TestSPAShell_CarriesTheCSRFTokenInAMetaTag pins the design's CSRF
// delivery: the token reaches the SPA in the shell, and every mutation
// sends it back as the existing header.
func TestSPAShell_CarriesTheCSRFTokenInAMetaTag(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, rec.Code)

	match := regexp.MustCompile(`<meta name="csrf-token" content="([0-9a-f]{64})"`).
		FindStringSubmatch(rec.Body.String())
	require.Len(t, match, 2, "the shell must carry the CSRF token in a meta tag")
	assert.Equal(t, s.csrf.token, match[1])
}

// TestSPAShell_IsNeverCached is why the shell is not a plain static file:
// it carries this PROCESS's CSRF token. A cached copy from a previous run
// would hand the SPA a token every mutation is then refused for.
func TestSPAShell_IsNeverCached(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Cache-Control"), "no-store")
}

// TestSPAShell_HasExactlyOneInlineScript pins the one sanctioned exception
// (docs/plans/2026-08-31-webui-impl.md §Pre-flight): the pre-paint theme
// bootstrap. It has to be inline and synchronous - reading localStorage
// after first paint is what a theme flash IS - and everything else in this
// UI is an ES module.
func TestSPAShell_HasExactlyOneInlineScript(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	inline := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(body, -1)
	require.Len(t, inline, 1, "exactly one inline script is allowed, and it is the theme bootstrap")
	assert.Contains(t, inline[0][1], "localStorage", "the one inline script is the theme bootstrap")
	assert.Contains(t, inline[0][1], "data-theme")

	// It must run BEFORE the body, or it is not a pre-paint bootstrap.
	assert.Less(t, strings.Index(body, "<script>"), strings.Index(body, "<body"),
		"the theme bootstrap must run before the body renders")
}

// TestSPAShell_CSPAdmitsTheInlineScriptByHash proves the exception is paid
// for rather than waived: the Content-Security-Policy still forbids inline
// script in general and admits this one by the hash of its exact bytes.
//
// It hashes what was actually SERVED, which is the assertion that matters:
// html/template rewrites what it emits inside a <script>, so a policy built
// from the source file would block the script this server sends, and would
// do it only in a browser.
func TestSPAShell_CSPAdmitsTheInlineScriptByHash(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, rec.Code)
	csp := rec.Header().Get("Content-Security-Policy")

	inline := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(rec.Body.String())
	require.Len(t, inline, 2)
	sum := sha256.Sum256([]byte(inline[1]))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	assert.Contains(t, csp, "default-src 'self'")
	assert.Contains(t, csp, want, "the CSP must admit the inline script by its own hash")
	assert.NotContains(t, csp, "unsafe-inline")
	assert.NotContains(t, csp, "unsafe-eval")
}

// TestSPAAssets_Served covers the two asset trees the shell pulls: the
// SPA's own files under /static/, and the pinned third-party modules under
// /vendor/. Both are embedded in the binary, never read from disk.
func TestSPAAssets_Served(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	for _, tc := range []struct{ path, contentType string }{
		{"/static/app.css", "css"},
		{"/static/app/main.js", "javascript"},
		{"/static/app/theme.js", "javascript"},
		{"/static/app/router.js", "javascript"},
		{"/static/app/store.js", "javascript"},
		{"/static/app/api.js", "javascript"},
		{"/static/app/sse.js", "javascript"},
		{"/vendor/preact.module.js", "javascript"},
		{"/vendor/hooks.module.js", "javascript"},
		{"/vendor/htm.module.js", "javascript"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, tc.path, "")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Header().Get("Content-Type"), tc.contentType)
			assert.NotEmpty(t, rec.Body.String())
		})
	}
}

// TestSPAAssets_VendorIsServedVerbatim proves the vendored file the browser
// gets is the committed one, header and all - no build step rewrites it on
// the way out.
func TestSPAAssets_VendorIsServedVerbatim(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	want, err := vendorFS.ReadFile("vendor/htm.module.js")
	require.NoError(t, err)

	rec := doAPI(s, http.MethodGet, "/vendor/htm.module.js", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, string(want), rec.Body.String())
	assert.Contains(t, rec.Body.String(), "SHA-256:", "the provenance header ships with it")
}

// TestSPAAssets_ShellIsNotServedAsAnAsset keeps the shell to its one entry
// point: it is a TEMPLATE (it carries the CSRF token), so a copy handed out
// as a static file would be a token-free, cacheable duplicate of it. This
// covers not just the literal "index.html" path but every way
// http.FileServerFS would otherwise resolve a directory: the directory
// index (/static/ itself) and a bare directory listing when there is no
// index (/static/app/, /vendor/) - both would leak either the shell or the
// asset inventory.
func TestSPAAssets_ShellIsNotServedAsAnAsset(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	for _, path := range []string{"/static/index.html", "/static/", "/static/app/", "/vendor/"} {
		t.Run(path, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, path, "")
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

// TestSPAAssets_RawShellTemplateIsUnreachable walks the whole embedded SPA
// tree looking for the raw "{{.CSRFToken}}" template placeholder - the
// literal string a browser would boot the SPA with if it ever reached the
// unrendered template instead of handleShell's output. It must not be
// reachable at any route this server registers, not just the ones this
// package's other tests happen to probe.
func TestSPAAssets_RawShellTemplateIsUnreachable(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	// dirs collects every directory the embedded SPA tree contains - the
	// root ("") and each file's parent - so the walk below can probe them
	// too. A walk that only ever requests FILE paths (as this test once
	// did) cannot see the directory-index/listing guard assetHandler
	// applies at all (task-2-review.md Minor 4): reverting that guard left
	// this test green while TestSPAAssets_ShellIsNotServedAsAnAsset went
	// red on exactly the paths below.
	dirs := map[string]bool{"": true}
	err := fs.WalkDir(spaFS, ".", func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		rel := strings.TrimPrefix(p, "spa/")
		if rel == "spa" || rel == "." {
			rel = ""
		}
		if d.IsDir() {
			dirs[rel] = true
			return nil
		}
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		dirs[parent] = true
		rec := doAPI(s, http.MethodGet, "/static/"+rel, "")
		if rec.Code == http.StatusOK {
			assert.NotContains(t, rec.Body.String(), "{{.CSRFToken}}",
				"the raw shell template must never be reachable as a static asset (%s)", p)
		}
		return nil
	})
	require.NoError(t, err)

	// Every directory - with a trailing slash, assetHandler's own guard
	// must refuse it outright (404); without one, http.FileServerFS's
	// directory redirect fires before the guard is ever consulted, so the
	// most it can leak is a Location header, never a 200 body. Both are
	// asserted so a regression in either layer is caught here rather than
	// only in the file-path table test.
	for dir := range dirs {
		target := "/static/"
		if dir != "" {
			target += dir + "/"
		}
		t.Run(target, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, target, "")
			assert.Equal(t, http.StatusNotFound, rec.Code,
				"a directory path must never resolve to an asset or a listing")
		})

		if dir == "" {
			continue // "/static" bare is the mux's own subtree redirect, not this handler's guard
		}
		noSlash := "/static/" + dir
		t.Run(noSlash, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, noSlash, "")
			assert.NotEqual(t, http.StatusOK, rec.Code,
				"a directory path without a trailing slash must never resolve to a 200")
		})
	}

	// vendorFS is a separate embed with no subdirectories of its own, so
	// the walk above never sees it - probe its root directly.
	for _, target := range []string{"/vendor/", "/vendor"} {
		t.Run(target, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, target, "")
			assert.NotEqual(t, http.StatusOK, rec.Code)
		})
	}

	// And the rendered shell itself, at every route it is actually served
	// on, must carry a real token rather than the placeholder.
	for _, route := range []string{"/", "/g/g1/default"} {
		rec := doAPI(s, http.MethodGet, route, "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, rec.Body.String(), "{{.CSRFToken}}")
		assert.Contains(t, rec.Body.String(), s.csrf.token)
		assert.Contains(t, rec.Header().Get("Cache-Control"), "no-store",
			"the rendered shell carries this process's token, so it must never be cached")
	}
}

// TestLegacyPageRoutes_301IntoTheSPAScheme covers the deleted page layer's
// URLs. Each carries whatever the old ?game/?profile pair resolved to into
// the SPA's path-based context (the audit's B1 wrong-game bug class becomes
// structurally impossible once the context is in the path), and a
// page-specific parameter that still has a home comes along.
func TestLegacyPageRoutes_301IntoTheSPAScheme(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	for _, tc := range []struct{ name, from, want string }{
		{"mods", "/mods", "/g/g1/default"},
		{"mods with explicit scope", "/mods?game=g1&profile=default", "/g/g1/default"},
		{"mod detail", "/mods/fake/m1", "/g/g1/default/mod/fake/m1"},
		{"search", "/search", "/g/g1/default/search"},
		{"search with query", "/search?q=boots", "/g/g1/default/search?q=boots"},
		{"updates", "/updates", "/g/g1/default"},
		{"profiles", "/profiles", "/g/g1/default"},
		{"health", "/health", "/g/g1/default"},
		{"job page", "/jobs/abc123", "/g/g1/default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, tc.from, "")
			require.Equal(t, http.StatusMovedPermanently, rec.Code)
			assert.Equal(t, tc.want, rec.Header().Get("Location"))
		})
	}
}

// TestLegacyPageRoutes_301ToRootWhenTheContextCannotResolve is the other
// half: with no game/profile to put in the path there is no scoped URL to
// send anyone to, so the old URL lands on the SPA's own entry point, which
// is where an unresolved context is answered (the game chooser).
func TestLegacyPageRoutes_301ToRootWhenTheContextCannotResolve(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	for _, from := range []string{"/mods?game=nope", "/search?q=boots&game=nope", "/health?game=nope"} {
		t.Run(from, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, from, "")
			require.Equal(t, http.StatusMovedPermanently, rec.Code)
			assert.Equal(t, "/", rec.Header().Get("Location"))
		})
	}
}

// TestSPARouting_UnknownPathIsStill404 pins the boundary of the catch-all:
// the shell is served for the app's OWN routes, not for everything. A typo
// must still be a 404 rather than a 200 that renders an empty app.
func TestSPARouting_UnknownPathIsStill404(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	for _, path := range []string{"/nope", "/mods/", "/static/app/nope.js", "/vendor/nope.js"} {
		t.Run(path, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, path, "")
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

// importSpecifier matches an ES module import's specifier, in both the
// `from "x"` and bare `import "x"` forms.
var importSpecifier = regexp.MustCompile(`(?:from|import)\s*\(?\s*["']([^"']+)["']`)

// TestSPAModuleGraphResolvesOverHTTP walks the whole module graph from the
// shell's entry point and asserts every import actually resolves to
// something this server serves.
//
// It exists because an import path is the one thing about this build that
// nothing else checks: there is no bundler to fail on a bad specifier
// (deliberately - `go build` is the whole build), so a mistyped or
// unrewritten path is a blank page in a browser and a green test suite
// everywhere else. That is not hypothetical: the vendored hooks module
// ships with a BARE `from"preact"` specifier, which a browser cannot
// resolve without an import map, and rewriting it is one of the two
// documented edits to that file.
func TestSPAModuleGraphResolvesOverHTTP(t *testing.T) {
	s, _, _ := newFlowFixtureServer(t)

	seen := map[string]bool{}
	queue := []string{"/static/app/main.js"}

	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true

		rec := doAPI(s, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code, "module %q does not resolve", path)

		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // a provenance header naming a specifier is not an import
			}
			for _, match := range importSpecifier.FindAllStringSubmatch(line, -1) {
				queue = append(queue, resolveModulePath(path, match[1]))
			}
		}
	}

	// A graph that silently collapsed to the entry point alone would pass
	// every assertion above.
	assert.Contains(t, seen, "/vendor/preact.module.js")
	assert.Contains(t, seen, "/vendor/hooks.module.js")
	assert.Contains(t, seen, "/vendor/htm.module.js")
	assert.GreaterOrEqual(t, len(seen), 8, "the module graph collapsed to almost nothing")
}

// resolveModulePath resolves one import specifier against the importing
// module's own URL path, the way a browser's module loader does.
func resolveModulePath(from, specifier string) string {
	if strings.HasPrefix(specifier, "/") {
		return specifier
	}
	base := from[:strings.LastIndex(from, "/")+1]
	return path.Clean(base + specifier)
}
