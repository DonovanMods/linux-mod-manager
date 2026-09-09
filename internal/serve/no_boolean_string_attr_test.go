package serve_test

// The coercion ratchet for the SPA's own JavaScript, and IMP-3's own
// generalisation (the closing wave's gate review).
//
// setupsources.js wrote spellcheck="false" on the custom-source YAML
// editor and carried it, unnoticed, from #333 to the end of the epic. It
// reads correctly and it is wrong: htm hands Preact the STRING "false",
// Preact assigns spellcheck as a DOM PROPERTY, and a non-empty string is
// truthy - so the element rendered with spellcheck === true and the editor
// really did draw red squiggles under every YAML key. The epic reviewer saw
// them; a fix wave closed the finding as "does not reproduce" after reading
// the source rather than the DOM.
//
// The lesson generalises to every HTML boolean and enumerated attribute:
// in this codebase they must be written as JS booleans (spellcheck=${false})
// and never as string literals. data-* and aria-* attributes are exempt and
// deliberately so - they ARE strings by spec, aria-modal="true" is correct
// as written, and the SPA has a dozen of them.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// booleanStringAttr matches an HTML boolean/enumerated attribute written
// with a quoted "true"/"false" value. The list is the set this application
// could plausibly reach for; every one of them is a property Preact
// assigns, so every one of them would coerce the same way.
//
// The leading (^|[\s]) is load-bearing rather than a plain \b: it is what
// keeps aria-hidden="true" and data-hydrated="true" out, which are strings
// by spec and correct as written. A \b would match inside both.
var booleanStringAttr = regexp.MustCompile(
	`(^|\s)(spellcheck|draggable|contenteditable|disabled|checked|hidden|readonly|readOnly|required|open|inert|multiple|selected|autofocus|novalidate|noValidate)\s*=\s*"(true|false)"`)

// commentLine matches a JS or CSS comment line - skipped, because this
// ratchet's own subject gets DESCRIBED in prose (setupsources.js's own doc
// comment quotes the coercion it exists to explain) and a ratchet that
// cannot be written about is a ratchet nobody documents.
var commentLine = regexp.MustCompile(`^\s*(//|/\*|\*)`)

// TestNoStringLiteralBooleanAttributes walks internal/serve/spa - the SPA's
// own shell and modules - and fails on a boolean attribute written as a
// quoted string.
//
// internal/serve/vendor is not walked, for the same reason the unsafe-DOM
// ratchet skips it: those files are pinned, byte-exact third-party
// artifacts and are not ours to edit.
func TestNoStringLiteralBooleanAttributes(t *testing.T) {
	root := filepath.Join("spa")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".js", ".html":
		default:
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(body), "\n") {
			if commentLine.MatchString(line) {
				continue
			}
			if m := booleanStringAttr.FindString(line); m != "" {
				attr := strings.TrimSpace(strings.SplitN(m, "=", 2)[0])
				t.Errorf("%s:%d: %s - write it as a JS boolean (%s=${false}); "+
					"Preact assigns it as a DOM property and the string \"false\" is truthy",
					path, i+1, strings.TrimSpace(m), attr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}
