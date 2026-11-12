package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The project site (#625) is two parts published as one: the landing page,
// plain static HTML in docs/site at the site root, and the user guide in
// docs/guide, built by Jekyll into /guide/. The guide lived at the root
// until then, so every old guide URL has a redirect stub in docs/site.
const (
	// siteDir is the landing page and the redirect stubs.
	siteDir = "../../docs/site"
	// siteBaseURL is where the site is published.
	siteBaseURL = "https://donovanmods.github.io/linux-mod-manager/"
	// pagesWorkflow builds and deploys the site.
	pagesWorkflow = "../../.github/workflows/pages.yml"
	// guideConfig is the guide's Jekyll configuration.
	guideConfig = "../../docs/guide/_config.yml"
	// serveVendorDir holds the web UI's vendored fonts, which the landing
	// page reuses.
	serveVendorDir = "../../internal/serve/vendor"
)

// guidePageNames returns the base name, without ".md", of every guide page
// that had its own URL at the site root before #625: every page but the
// index, which was the root itself and is now the landing page.
func guidePageNames(t *testing.T) []string {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join(guideDir, "*.md"))
	require.NoError(t, err)
	require.NotEmpty(t, pages, "no guide pages under %s", guideDir)
	var names []string
	for _, page := range pages {
		name := strings.TrimSuffix(filepath.Base(page), ".md")
		if name == "index" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// TestSiteRedirectsEveryOldGuideURL is the ratchet behind #625's promise that
// old guide links keep working: <site>/<page>.html, the URL every guide page
// had before the guide moved under /guide/, must be a stub that sends the
// reader to <site>/guide/<page>.html. A new guide page cannot ship without
// one (it is cheap, and a link to the page's root URL is the first thing a
// reader who remembers the old layout will try).
func TestSiteRedirectsEveryOldGuideURL(t *testing.T) {
	for _, name := range guidePageNames(t) {
		t.Run(name, func(t *testing.T) {
			stub := filepath.Join(siteDir, name+".html")
			raw, err := os.ReadFile(stub)
			require.NoError(t, err, "docs/guide/%s.md has no redirect stub: add docs/site/%s.html (copy any other stub and change the page name)", name, name)
			body := string(raw)
			target := "guide/" + name + ".html"
			assert.Regexp(t, `<meta http-equiv="refresh" content="0; url=`+regexp.QuoteMeta(target)+`"`, body,
				"%s must meta-refresh to %s", stub, target)
			assert.Contains(t, body, `<link rel="canonical" href="`+siteBaseURL+target+`"`,
				"%s must name its new URL as canonical", stub)
			assert.Contains(t, body, `<a href="`+target+`">`,
				"%s must link to %s for a reader whose browser does not follow the refresh", stub, target)
			assert.Contains(t, body, `<meta name="robots" content="noindex">`,
				"%s must keep search engines indexing the real page, not the stub", stub)
		})
	}
}

// TestSiteHasNoStaleRedirects is the other direction: a stub whose guide page
// was deleted or renamed would send readers to a 404.
func TestSiteHasNoStaleRedirects(t *testing.T) {
	names := guidePageNames(t)
	stubs, err := filepath.Glob(filepath.Join(siteDir, "*.html"))
	require.NoError(t, err)
	for _, stub := range stubs {
		raw, err := os.ReadFile(stub)
		require.NoError(t, err)
		if !strings.Contains(string(raw), `http-equiv="refresh"`) {
			continue // the landing page or the 404 page, not a stub
		}
		name := strings.TrimSuffix(filepath.Base(stub), ".html")
		assert.True(t, slices.Contains(names, name),
			"%s redirects to a guide page that no longer exists (docs/guide/%s.md)", stub, name)
	}
}

// TestGuideIsPublishedUnderGuide pins the Jekyll baseurl to the path the
// workflow builds the guide into, so the theme's links and assets resolve
// there.
func TestGuideIsPublishedUnderGuide(t *testing.T) {
	raw, err := os.ReadFile(guideConfig)
	require.NoError(t, err)
	var cfg struct {
		URL     string `yaml:"url"`
		BaseURL string `yaml:"baseurl"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	assert.Equal(t, strings.TrimSuffix(siteBaseURL, "/"), cfg.URL+"/linux-mod-manager", "url")
	assert.Equal(t, "/linux-mod-manager/guide", cfg.BaseURL, "baseurl must match where pages.yml builds the guide")
}

// pagesWorkflowDoc is the subset of pages.yml the tests below read.
type pagesWorkflowDoc struct {
	On struct {
		Push struct {
			Branches []string `yaml:"branches"`
			Paths    []string `yaml:"paths"`
		} `yaml:"push"`
	} `yaml:"on"`
	Jobs map[string]struct {
		If    string `yaml:"if"`
		Needs any    `yaml:"needs"`
		Steps []struct {
			Name string            `yaml:"name"`
			If   string            `yaml:"if"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// TestPagesWorkflowBuildsTheSiteAndTheGuide holds pages.yml to the #625
// layout: it runs for a change to either part, builds on develop so a broken
// site is caught before a release, deploys from main only, and publishes
// exactly docs/site at the root and docs/guide under /guide/ - never docs/ as
// a whole, which would publish docs/plans.
func TestPagesWorkflowBuildsTheSiteAndTheGuide(t *testing.T) {
	raw, err := os.ReadFile(pagesWorkflow)
	require.NoError(t, err)
	var wf pagesWorkflowDoc
	require.NoError(t, yaml.Unmarshal(raw, &wf))

	assert.ElementsMatch(t, []string{"main", "develop"}, wf.On.Push.Branches, "push branches")
	for _, p := range []string{"docs/site/**", "docs/guide/**", ".github/workflows/pages.yml"} {
		assert.Contains(t, wf.On.Push.Paths, p, "paths filter")
	}

	build, ok := wf.Jobs["build"]
	require.True(t, ok, "pages.yml has no build job")
	var copiesSite, buildsGuide bool
	for _, step := range build.Steps {
		if strings.Contains(step.Run, "cp -R docs/site/. _site/") {
			copiesSite = true
		}
		if strings.Contains(step.Uses, "actions/jekyll-build-pages@") {
			buildsGuide = true
			assert.Equal(t, "./docs/guide", step.With["source"], "the guide's Jekyll source")
			assert.Equal(t, "./_site/guide", step.With["destination"], "the guide's Jekyll destination")
		}
		if strings.Contains(step.Uses, "actions/upload-pages-artifact@") || strings.Contains(step.Uses, "actions/configure-pages@") {
			assert.Contains(t, step.If, "refs/heads/main", "%q must run on main only", step.Name)
		}
	}
	assert.True(t, copiesSite, "the build job must copy docs/site to the root of _site")
	assert.True(t, buildsGuide, "the build job must build docs/guide with Jekyll")
	assert.NotRegexp(t, `source:\s*\./docs\s*$`, string(raw), "never publish docs/ as a whole")

	deploy, ok := wf.Jobs["deploy"]
	require.True(t, ok, "pages.yml has no deploy job")
	assert.Contains(t, deploy.If, "refs/heads/main", "only main deploys")
}

// siteReference matches the URLs a page or stylesheet loads or links to:
// src and href attributes, srcset candidates, CSS url()s, and the Open Graph
// and Twitter card images (a meta tag's content).
var siteReference = regexp.MustCompile(`(?:\b(?:src|href|srcset)="([^"]*)"|url\(["']?([^"')]+)["']?\)|<meta (?:property|name)="(?:og|twitter):image" content="([^"]*)")`)

// TestSiteIsSelfContained holds the landing page to #625's privacy rule -
// nothing is fetched from anywhere but the site itself: no CDN, no web font
// service, no analytics, no third-party embed - and to resolving every local
// reference to a file that is there, since there is no build step to catch a
// typo. Links to other sites (GitHub, the issue tracker) are fine; it is
// loading from them that is not, so only <a href> may point off the site.
func TestSiteIsSelfContained(t *testing.T) {
	var files []string
	require.NoError(t, filepath.WalkDir(siteDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".css")) {
			files = append(files, path)
		}
		return nil
	}))
	require.NotEmpty(t, files, "no pages under %s", siteDir)
	require.FileExists(t, filepath.Join(siteDir, "index.html"), "the landing page")

	anchor := regexp.MustCompile(`<a\b[^>]*\bhref="([^"]*)"`)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		body := string(raw)
		assert.NotRegexp(t, `<script[^>]+src=`, body, "%s loads a script file", file)
		assert.NotRegexp(t, `<iframe|<embed|<object`, body, "%s embeds third-party content", file)

		links := map[string]bool{}
		for _, m := range anchor.FindAllStringSubmatch(body, -1) {
			links[m[1]] = true
		}
		for _, m := range siteReference.FindAllStringSubmatch(body, -1) {
			for _, ref := range strings.Split(m[1]+m[2]+m[3], ",") {
				ref = strings.Fields(strings.TrimSpace(ref) + " ")[0]
				switch {
				case ref == "" || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "mailto:") || strings.HasPrefix(ref, "data:"):
					continue
				case strings.HasPrefix(ref, siteBaseURL):
					// An absolute URL on the site itself: the canonical
					// link, the Open Graph image. Check it resolves.
					checkSiteRef(t, file, siteDir, strings.TrimPrefix(ref, siteBaseURL))
				case strings.Contains(ref, "://") || strings.HasPrefix(ref, "//"):
					assert.True(t, links[ref], "%s loads %s from another site; self-host it under docs/site", file, ref)
				case strings.Contains(ref, ";"):
					// a meta refresh's "0; url=..." is checked by the
					// redirect test
				default:
					checkSiteRef(t, file, filepath.Dir(file), ref)
				}
			}
		}
	}
}

// checkSiteRef asserts that ref, a reference in file relative to base,
// resolves: to a file under docs/site, or, under guide/, to the guide page
// Jekyll builds it from.
func checkSiteRef(t *testing.T, file, base, ref string) {
	t.Helper()
	ref, _, _ = strings.Cut(ref, "#")
	ref, _, _ = strings.Cut(ref, "?")
	if ref == "" {
		return
	}
	path := filepath.Clean(filepath.Join(base, ref))
	rel, err := filepath.Rel(siteDir, path)
	require.NoError(t, err)
	if rel == "guide" || strings.HasPrefix(rel, "guide/") {
		page := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(rel, "guide"), "/"), ".html")
		if page == "" {
			page = "index"
		}
		assert.FileExists(t, filepath.Join(guideDir, page+".md"), "%s links to %s, which no guide page builds", file, ref)
		return
	}
	if strings.HasSuffix(ref, "/") {
		path = filepath.Join(path, "index.html")
	}
	assert.FileExists(t, path, "%s references %s, which is not in docs/site", file, ref)
}

// TestSiteFontsAreTheVendoredFonts holds the landing page's font copies to
// the web UI's, byte for byte, with their license texts beside them: the
// provenance and the OFL-1.1 notice recorded for internal/serve/vendor (its
// FONTS.md, THIRD_PARTY_NOTICES) then cover the copies too, and an upgrade
// of one cannot leave the other behind unnoticed.
func TestSiteFontsAreTheVendoredFonts(t *testing.T) {
	fonts, err := filepath.Glob(filepath.Join(siteDir, "fonts", "*"))
	require.NoError(t, err)
	require.NotEmpty(t, fonts, "the landing page ships its fonts under docs/site/fonts")
	var woff2 int
	for _, font := range fonts {
		name := filepath.Base(font)
		if name == "README.md" {
			continue
		}
		if strings.HasSuffix(name, ".woff2") {
			woff2++
		}
		site, err := os.ReadFile(font)
		require.NoError(t, err)
		vendored, err := os.ReadFile(filepath.Join(serveVendorDir, name))
		require.NoError(t, err, "docs/site/fonts/%s has no counterpart in internal/serve/vendor", name)
		assert.True(t, bytes.Equal(site, vendored), "docs/site/fonts/%s differs from internal/serve/vendor/%s", name, name)
	}
	require.NotZero(t, woff2, "no fonts under docs/site/fonts")
	for _, license := range []string{"LICENSE.inter", "LICENSE.jetbrains-mono"} {
		assert.FileExists(t, filepath.Join(siteDir, "fonts", license))
	}
}

// TestWebUILinksIntoTheGuideUseItsNewURL keeps the web UI's links into the
// guide on /guide/ (#625). The old root URLs redirect, but a redirect costs
// the reader a round trip and would hide a page that later goes missing.
// (The systemd units' Documentation= lines still name the old URLs: their
// goldens pin the unit text, and the redirect stubs keep them working.)
func TestWebUILinksIntoTheGuideUseItsNewURL(t *testing.T) {
	oldGuideURL := regexp.MustCompile(regexp.QuoteMeta(siteBaseURL) + `([a-z0-9-]+)\.html`)
	require.NoError(t, filepath.WalkDir("../../internal/serve/spa", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range oldGuideURL.FindAllStringSubmatch(string(raw), -1) {
			t.Errorf("%s links to %s; use %sguide/%s.html", path, m[0], siteBaseURL, m[1])
		}
		return nil
	}))
}
