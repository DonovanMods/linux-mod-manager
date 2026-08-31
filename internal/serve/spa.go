// spa.go serves the single-page application: the embedded shell, its ES
// modules and stylesheet, the pinned third-party modules, and the 301s from
// the URLs the deleted server-rendered page layer owned
// (docs/plans/2026-08-31-serve-spa-design.md).
//
// The shell is one document served for every app route. The router runs in
// the browser, so /g/{game}/{profile}/mod/{source}/{id} is not a different
// page - it is this page, told where it is by its own URL. That is also why
// the shell is a TEMPLATE rather than a static file: it carries this
// server process's CSRF token, which is the only thing about it that is not
// constant.
package serve

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

//go:embed spa/index.html spa/app.css spa/app/*.js spa/app/components/*.js
var spaFS embed.FS

//go:embed vendor/*.js
var vendorFS embed.FS

// shellTemplate is the parsed shell. It is parsed once at startup for the
// same reason the page layer's templates were: a broken embedded template
// is a build defect, not a runtime condition to recover from.
var shellTemplate = template.Must(template.ParseFS(spaFS, "spa/index.html"))

// shellData is everything the shell template interpolates. Exactly one
// member, and it is the reason the shell cannot be a static asset.
type shellData struct {
	CSRFToken string
}

// inlineScriptPattern matches the shell's inline <script> block - the one
// sanctioned inline script (the pre-paint theme bootstrap; see
// spa/index.html). It deliberately does NOT match `<script type="module"
// src=...>`: those are external files the CSP already admits as 'self'.
var inlineScriptPattern = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// contentSecurityPolicy is the policy every response carries.
//
// It stays "everything from this origin only", with one addition: the
// SHA-256 of the shell's inline theme bootstrap. Hashing is what keeps the
// exception honest - 'unsafe-inline' would admit any inline script anyone
// ever adds, while a hash admits exactly the bytes that were measured, so
// an edited bootstrap stops working rather than silently widening the
// policy. It is computed from the EMBEDDED shell at startup, so the value
// can never drift from what is actually served.
//
// No 'unsafe-eval': htm parses tagged templates at runtime without eval or
// the Function constructor, and Preact needs neither.
var contentSecurityPolicy = buildContentSecurityPolicy()

// buildContentSecurityPolicy measures the shell's inline script and returns
// the policy admitting exactly it. A shell with no inline script (or more
// than one) is a build-time defect in this package's own asset, so it
// panics rather than shipping a policy that does not match the document.
//
// It measures the RENDERED shell, not the source file. html/template
// rewrites what it emits inside a <script> element - it strips JS comments,
// among other things - so hashing the file on disk would produce a policy
// that blocks the very script this package serves, and it would only fail
// in a browser. Rendering here with a placeholder token is safe because the
// token is not in the script: every render produces the same script bytes.
func buildContentSecurityPolicy() string {
	var rendered strings.Builder
	if err := shellTemplate.ExecuteTemplate(&rendered, "index.html", shellData{CSRFToken: "0"}); err != nil {
		panic(fmt.Errorf("serve: rendering the SPA shell to measure its CSP: %w", err))
	}
	matches := inlineScriptPattern.FindAllStringSubmatch(rendered.String(), -1)
	if len(matches) != 1 {
		panic(fmt.Sprintf("serve: the SPA shell must hold exactly one inline script, found %d", len(matches)))
	}
	sum := sha256.Sum256([]byte(matches[0][1]))
	return "default-src 'self'; script-src 'self' 'sha256-" +
		base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// handleShell serves the SPA shell. Cache-Control is no-store rather than
// merely revalidated: the document carries THIS process's CSRF token, and a
// copy served from cache after a restart would hand the SPA a token every
// mutation is then refused for.
func (s *Server) handleShell(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := shellTemplate.ExecuteTemplate(w, "index.html", shellData{CSRFToken: s.csrf.token}); err != nil {
		s.log.Error("rendering the SPA shell", "err", err)
	}
}

// assetHandler serves one embedded asset tree under prefix, with
// conservative headers. Cache-Control is no-cache (revalidate, don't
// re-download) rather than a long max-age: the assets only ever change with
// the binary, and a stale module paired with a fresh shell is the one
// failure mode worth spending a conditional request to avoid.
func assetHandler(embedded fs.FS, dir string) http.Handler {
	sub, err := fs.Sub(embedded, dir)
	if err != nil {
		// The tree is embedded at compile time; a broken embed is a
		// build-time defect, not a runtime condition to recover from.
		panic(fmt.Errorf("serve: opening embedded assets %q: %w", dir, err))
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The shell is a template with a per-process token in it, so it is
		// served by handleShell and nowhere else. Handing out a static copy
		// would be a cacheable, token-free duplicate of the application.
		if strings.TrimPrefix(r.URL.Path, "/") == "index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// legacyRedirect answers one of the deleted page layer's URLs with a 301
// into the SPA's scheme. build turns the resolved context path (the
// /g/{game}/{profile} prefix) and the original request into the new URL.
//
// A context that cannot be resolved has no scoped URL to point at, so the
// old link lands on "/" - the SPA's own entry point, which is where an
// unresolved context is answered (the game chooser). 301 rather than 302
// because the old scheme is not coming back.
func (s *Server) legacyRedirect(build func(base string, r *http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := "/"
		if base := s.spaContextPath(r); base != "" {
			target = build(base, r)
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}
}

// spaContextPath resolves the request's ?game/?profile the same way every
// scoped endpoint does and renders it as the SPA's path prefix, or "" when
// it does not resolve. This is the whole of the old query-param scheme's
// translation into the new path-based one.
func (s *Server) spaContextPath(r *http.Request) string {
	sel, err := s.resolveSelection(r)
	if err != nil || !sel.ready() {
		return ""
	}
	return "/g/" + url.PathEscape(sel.Game.ID) + "/" + url.PathEscape(sel.Profile)
}

// spaRoutes registers the shell, the asset trees and the legacy redirects.
//
// The shell is registered for the app's OWN routes ("/" exactly, and the
// /g/ subtree), never as a bare "/" catch-all: a typo must still be a 404
// rather than a 200 rendering an empty application.
func (s *Server) spaRoutes() {
	s.mux.Handle("GET /{$}", s.wrap(s.handleShell))
	s.mux.Handle("GET /g/", s.wrap(s.handleShell))

	s.mux.Handle("GET /static/", http.StripPrefix("/static/", assetHandler(spaFS, "spa")))
	s.mux.Handle("GET /vendor/", http.StripPrefix("/vendor/", assetHandler(vendorFS, "vendor")))

	// The six page routes, plus the job page. /mods, /updates, /profiles
	// and /health were separate pages of the same context and are now
	// regions of Mission Control, so all four land on it; /jobs/{id} is
	// there too, because a job's live state is the activity tray rather
	// than a page of its own.
	missionControl := func(base string, _ *http.Request) string { return base }
	s.mux.Handle("GET /mods", s.wrap(s.legacyRedirect(missionControl)))
	s.mux.Handle("GET /updates", s.wrap(s.legacyRedirect(missionControl)))
	s.mux.Handle("GET /profiles", s.wrap(s.legacyRedirect(missionControl)))
	s.mux.Handle("GET /health", s.wrap(s.legacyRedirect(missionControl)))
	s.mux.Handle("GET /jobs/{id}", s.wrap(s.legacyRedirect(missionControl)))

	s.mux.Handle("GET /mods/{source}/{id}", s.wrap(s.legacyRedirect(
		func(base string, r *http.Request) string {
			return base + "/mod/" + url.PathEscape(r.PathValue("source")) + "/" + url.PathEscape(r.PathValue("id"))
		})))

	// Search is the one old page whose own parameter has a home in the new
	// scheme, so ?q= comes along rather than being dropped on the way.
	s.mux.Handle("GET /search", s.wrap(s.legacyRedirect(
		func(base string, r *http.Request) string {
			target := base + "/search"
			if q := r.URL.Query().Get("q"); q != "" {
				target += "?q=" + url.QueryEscape(q)
			}
			return target
		})))
}
