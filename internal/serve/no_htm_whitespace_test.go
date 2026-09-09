package serve_test

// The htm-whitespace-drop ratchet (#333 fix-wave N2, rereview-u7.md "New
// issues #2"): htm (spa/vendor/htm.module.js, the tagged-template renderer
// every component here calls through) does not just collapse a text chunk
// between two template-literal boundaries to a single space the way a
// browser collapses ordinary HTML whitespace - it drops a WHITESPACE-ONLY
// chunk that contains a newline ENTIRELY, so no space renders at all.
// Two interpolations, a closing tag followed by an interpolation, an
// interpolation followed by an opening tag, or two tags, laid out on
// separate source lines with nothing between them but the line break and
// its indent, therefore fuse together with ZERO space where the source
// reads like there should be one. Minor 1 hit this in
// plan_import_archive.js ("BetaMod-1.0.zip asBetaMod-1.01.0"); the fix
// wave that answered it missed the identical shape one file over, in
// plan_adopt.js's confirm modal ("UntrackedModno source match"). An
// explicit ${" "} at the boundary is the fix in both files - it is a real
// string VALUE inside an interpolation, not template text, so htm never
// gets the chance to drop it.
//
// Scope: this ratchet walks only the plan_*.js family - the confirm-modal
// renderers for a core Plan (plan_install.go, plan_deploy.go, ...:
// architecture.md's "planrenderers, plan_deploy, plan_install, ..." list)
// - because that is exactly the surface both known defects are about: "a
// headline sentence at the moment a user is being asked to approve a
// mutation" (rereview-u7.md's own words for why the Adopt modal's fused
// headline mattered). The identical tag-adjacency SHAPE this regex looks
// for is extremely common elsewhere in spa/app as ordinary block layout -
// a table's <th>/<td> cells, a stack of independent <option>s, a row of
// nav <button>s - where nothing is meant to read as continuous prose and
// CSS, not a text node, does the layout. Widening the walk to all of
// spa/app pulls in dozens of those with no way to tell them apart from a
// real defect by syntax alone; scoping to the confirm-modal family covers
// the risk class the finding named without that noise.
//
// Matched shapes, all requiring the two sides to be separated by NOTHING
// but a bare newline and its leading indentation (any other character -
// including an explicit ${" "}, which is exactly the fix - breaks the
// match, since it is no longer "nothing but whitespace"):
//   - </span> immediately followed by <span (two adjacent inline runs)
//   - </span> immediately followed by ${   (a run, then a value)
//   - }       immediately followed by <span (a value, then a run)
//
// All three are unambiguous template-literal syntax: none can occur in
// ordinary JS control flow the way a bare "} \n someIdentifier" could, so
// there is no ordinary-code false-positive class to guard against the way
// there would be for a plain "closing brace, next statement" pattern.
//
// gameadd.js gets one further, narrower shape: plain prose text ending in
// a word character, immediately followed - again across a bare newline
// and its indentation only - by an interpolation's opening ${. This is
// exactly what the unit9 review's Important 2 found in the prefill
// banner: "(Steam app526870)" (the wire's actual "700003" reproduced from
// three separate rows) - "app" fused directly onto the interpolated app
// id with no space at all, because nothing but line-wrap separated them.
// This shape is NOT widened to the whole plan_*.js family: ordinary prose
// wrapped across lines for readability is common style throughout this
// codebase, and (per the note above) flagging it everywhere would be
// noise with no way to tell a real defect from ordinary line-wrapping by
// syntax alone. gameadd.js is scanned for it by name because that is
// exactly the file the defect lived in and the file this ratchet exists
// to keep it from recurring in.
//
// Two kinds of match are still safe on this tree and are excluded rather
// than flagged:
//   - A } that closes an explicit ${" "} (or ${' '}) - the fix this
//     ratchet exists to require - already supplies the space; matching it
//     anyway would fail the very code that answers the finding.
//   - A boundary marked on an adjacent line with the literal comment
//     "htm-ws-ok", for the rare case where what follows already supplies
//     its own leading space (a nested template starting " (...)") or is
//     itself a block element (a <ul> list) with no text-flow relationship
//     to what came before. The comment sits in the JS expression at the
//     matched boundary (e.g. right after a ${), where it costs nothing
//     rendered - see plan_install.js and plan_deploy.js for the pattern.
import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// htmWhitespaceBoundary matches a confirm-modal template-literal boundary
// where htm drops the intervening text node outright rather than
// collapsing it to one space: an inline run's close, or an interpolation's
// close, immediately followed - across a bare newline and its indentation
// only - by another inline run's open or another interpolation's open.
var htmWhitespaceBoundary = regexp.MustCompile(`</span>[ \t]*\n[ \t]*(?:<span|\$\{)|\}[ \t]*\n[ \t]*<span`)

// htmWhitespaceGuarded matches a } that closes an explicit space literal -
// ${" "} or ${' '} - immediately before the boundary in question. That }
// is the fix this ratchet requires, not a defect: flagging it would fail
// the very code that answers the finding.
var htmWhitespaceGuarded = regexp.MustCompile(`\$\{\s*["'][ \t]+["']\s*\}$`)

// htmWordBeforeInterpBoundary matches plain prose text ending in a word
// character, immediately followed - across a bare newline and its
// indentation only - by an interpolation's opening ${. See the file-level
// comment above for why this shape is scoped to gameadd.js alone rather
// than the whole plan_*.js family.
var htmWordBeforeInterpBoundary = regexp.MustCompile(`\w[ \t]*\n[ \t]*\$\{`)

// TestNoAdjacentHTMWhitespaceDrops walks the plan_*.js confirm-modal
// renderers (plus gameadd.js, for its own narrower shape) for a template
// literal that joins two inline runs, a run and an interpolation, or an
// interpolation and a run, across a bare newline - the shape that lets htm
// drop the joining whitespace and fuse the rendered text together in front
// of a user approving a mutation (or, in gameadd.js's case, reading a
// prefill banner).
func TestNoAdjacentHTMWhitespaceDrops(t *testing.T) {
	dir := filepath.Join(".", "spa", "app")
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		base := filepath.Base(path)
		isPlanFile := strings.HasPrefix(base, "plan_")
		isGameAdd := base == "gameadd.js"
		if filepath.Ext(path) != ".js" || !(isPlanFile || isGameAdd) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lines := strings.Split(string(data), "\n")

		// Each file gets only its own shape: plan_*.js keeps the original
		// inline-run/interpolation boundary unchanged, and gameadd.js gets
		// only the narrower word-before-interpolation shape its own defect
		// was - widening the ORIGINAL shape to gameadd.js too would also
		// flag unrelated pre-existing block-element adjacencies there
		// (e.g. a name next to a badge <span>) that are out of scope here.
		var locs [][]int
		if isPlanFile {
			locs = append(locs, htmWhitespaceBoundary.FindAllStringIndex(string(data), -1)...)
		}
		if isGameAdd {
			locs = append(locs, htmWordBeforeInterpBoundary.FindAllStringIndex(string(data), -1)...)
		}
		for _, loc := range locs {
			start, end := loc[0], loc[1]
			if data[start] == '}' && htmWhitespaceGuarded.Match(data[:start+1]) {
				continue // an explicit ${" "} already supplies the space
			}
			startLine := strings.Count(string(data[:start]), "\n")
			endLine := strings.Count(string(data[:end]), "\n")
			if htmWhitespaceMarkedSafe(lines, startLine, endLine) {
				continue
			}
			t.Errorf("%s:%d: %q joins two template-literal boundaries across a bare newline with no ${\" \"} - htm drops that whitespace entirely, fusing the rendered text; add ${\" \"} at the boundary (or note it htm-ws-ok if the gap is genuinely already covered)",
				path, startLine+1, data[start:end])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
}

// htmWhitespaceMarkedSafe reports whether the literal escape comment
// "htm-ws-ok" appears anywhere from the line before the match through a
// few lines after it - enough room for the comment to sit inside the JS
// expression the boundary opens into, which is where it costs nothing
// rendered (see plan_install.js/plan_deploy.js/plan_verify_fix.js).
func htmWhitespaceMarkedSafe(lines []string, startLine, endLine int) bool {
	from := startLine - 1
	if from < 0 {
		from = 0
	}
	to := endLine + 5
	if to >= len(lines) {
		to = len(lines) - 1
	}
	for _, l := range lines[from : to+1] {
		if strings.Contains(l, "htm-ws-ok") {
			return true
		}
	}
	return false
}
