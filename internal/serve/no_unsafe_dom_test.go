package serve_test

// The rendering-safety ratchet for the SPA's own JavaScript, the sibling of
// no_unsafe_html_test.go's ratchet for this package's Go
// (docs/plans/2026-08-31-webui-impl.md §Global Constraints: "all DOM
// through Preact (auto-escaping); dangerouslySetInnerHTML and
// template.HTML-class casts FORBIDDEN").
//
// Every string this UI puts on screen comes from a mod's metadata, a
// source's search results or a core error - none of it authored by us, all
// of it going through Preact, which escapes text by construction. The
// writes below are the ways out of that guarantee, and there is no reason
// this application needs any of them. A grep ratchet is the right shape
// here for the same reason the Go one is: the failure it prevents is
// someone reaching for the escape hatch under deadline, and it costs
// nothing to make that fail the build instead.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// unsafeDOMWrite matches the raw-markup escape hatches. dangerouslySetInner
// HTML is Preact's own; the rest are the DOM's, and eval/Function are here
// because the page's Content-Security-Policy carries no 'unsafe-eval' - a
// use would fail at runtime in a browser and nowhere else.
var unsafeDOMWrite = regexp.MustCompile(
	`dangerouslySetInnerHTML|\.innerHTML|\.outerHTML|insertAdjacentHTML|document\.write|\beval\s*\(|new\s+Function\s*\(`)

// TestNoUnsafeDOMWrites walks internal/serve/spa - the SPA's own shell and
// modules - and fails on any raw-markup or dynamic-code write.
//
// internal/serve/vendor is deliberately NOT walked: Preact's own
// implementation of dangerouslySetInnerHTML lives there, which is exactly
// the mechanism this test forbids US from reaching. Those files are pinned,
// byte-exact third-party artifacts (see their headers) and are not ours to
// edit.
func TestNoUnsafeDOMWrites(t *testing.T) {
	root := filepath.Join(".", "spa")

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".js" && ext != ".html" {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				// A doc comment naming the forbidden thing (this rule is
				// documented in render.js) is not a use of it.
				continue
			}
			if unsafeDOMWrite.MatchString(line) {
				t.Errorf("%s:%d: raw-markup or dynamic-code write in SPA source: %s",
					path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

// TestSPAModulesLoadVendorByPinnedPath keeps the vendored libraries a
// closed set: an ES module can import anything a URL reaches, so a
// same-origin /vendor/ path is the only import this application may make
// outside its own tree. A CDN import would put a network fetch back into
// the product the whole "no npm, no bundler, users install nothing"
// decision exists to keep it out of.
func TestSPAModulesLoadVendorByPinnedPath(t *testing.T) {
	remoteImport := regexp.MustCompile(`(?:from|import)\s*\(?\s*["']https?://`)

	err := filepath.Walk(filepath.Join(".", "spa"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".js" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if remoteImport.Match(data) {
			t.Errorf("%s imports over the network; every dependency is vendored under internal/serve/vendor", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking spa: %v", err)
	}
}
