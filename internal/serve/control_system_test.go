package serve_test

// The control-system ratchets (#518). Every button in this UI is drawn by
// one system - app.css's control block: the .button variants and sizes, and
// .menu-item - so that controls in one row share a height and a look, and
// a new screen cannot reintroduce a bespoke one. Two halves:
//
//   - TestEveryButtonUsesTheControlSystem: every <button> the SPA renders
//     carries "button" (any variant) or "menu-item".
//   - TestNoControlStylingOutsideTheControlBlock: no rule outside the
//     control block re-defines what the block defines - height, padding,
//     border, radius, font size - on a selector that targets a button. An
//     element-specific class may still place a control (margin, position,
//     flex, colour); it may not reshape it.
//
// Text scanning rather than a parser, like no_hardcoded_color_test.go and
// no_unsafe_dom_test.go: the shapes it reads (an htm tag, a flat CSS rule)
// are regular enough, and the failure it prevents is someone styling one
// more button by hand under deadline.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// controlSystemClasses are the class names a <button> may carry to be drawn
// by the control system.
var controlSystemClasses = []string{"button", "menu-item"}

// buttonClassAllowList is every <button> that may stay outside the control
// system, keyed "file:line", with the reason. Empty, and meant to stay
// that way: an entry needs a reason a reviewer accepts.
var buttonClassAllowList = map[string]string{}

// controlBlockBegin and controlBlockEnd delimit app.css's control block,
// the one place the control-defining properties may be set on a button.
const (
	controlBlockBegin = "/* @control-system begin */"
	controlBlockEnd   = "/* @control-system end */"
)

// controlDefiningProperty matches a declaration's property that shapes a
// control rather than places it: its height, padding, border (width, style
// and colour - the variant's look), radius, and type size (font-size, and
// the font shorthand that sets it, and the line-height that sizes a box).
var controlDefiningProperty = regexp.MustCompile(
	`^(height|min-height|max-height|padding(-[a-z-]+)?|border(-[a-z-]+)?|font|font-size|line-height)$`)

// buttonTag is one <button ...> opening tag found in an SPA module.
type buttonTag struct {
	file string
	line int
	tag  string
}

// classAttr is a class attribute's value: a quoted string (which may
// interpolate ${...}) or a bare ${...} expression.
var classAttr = regexp.MustCompile(`\bclass=("|\$\{)`)

// quotedString is a JS string literal of any of the three quote kinds.
var quotedString = regexp.MustCompile("\"[^\"]*\"|'[^']*'|`[^`]*`")

// buttonTags returns every <button opening tag in the SPA's modules,
// skipping comment lines (prose that names a "<button>").
func buttonTags(t *testing.T) []buttonTag {
	t.Helper()
	var out []buttonTag
	err := filepath.Walk(filepath.Join("spa", "app"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".js" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out = append(out, buttonTagsIn(path, string(data))...)
		return nil
	})
	require.NoError(t, err)
	return out
}

// buttonTagsIn finds every <button opening tag in one module's source. A
// tag ends at the first ">" outside a ${...} interpolation, so a multi-line
// tag with arrow functions in its handlers is read whole.
func buttonTagsIn(file, src string) []buttonTag {
	var out []buttonTag
	for _, loc := range regexp.MustCompile(`<button\b`).FindAllStringIndex(src, -1) {
		lineStart := strings.LastIndex(src[:loc[0]], "\n") + 1
		prefix := strings.TrimSpace(src[lineStart:loc[0]])
		if strings.HasPrefix(prefix, "//") || strings.HasPrefix(prefix, "*") || strings.HasPrefix(prefix, "/*") {
			continue
		}
		end, depth := loc[1], 0
		for end < len(src) {
			switch {
			case strings.HasPrefix(src[end:], "${"):
				depth++
				end++
			case src[end] == '{' && depth > 0:
				depth++
			case src[end] == '}' && depth > 0:
				depth--
			case src[end] == '>' && depth == 0:
				goto done
			}
			end++
		}
	done:
		out = append(out, buttonTag{
			file: file,
			line: strings.Count(src[:loc[0]], "\n") + 1,
			tag:  src[loc[0]:min(end+1, len(src))],
		})
	}
	return out
}

// classValue returns the text of tag's class attribute and whether the
// attribute is a bare ${...} expression (the text is then the expression's)
// rather than a quoted value (the text is then the value, interpolations
// included). ok is false when the tag has no class attribute.
func classValue(tag string) (value string, isExpr, ok bool) {
	loc := classAttr.FindStringSubmatchIndex(tag)
	if loc == nil {
		return "", false, false
	}
	start := loc[1]
	isExpr = tag[loc[2]:loc[3]] != `"`
	depth := 0
	if isExpr {
		depth = 1
	}
	for i := start; i < len(tag); i++ {
		switch {
		case strings.HasPrefix(tag[i:], "${"):
			depth++
			i++
		case tag[i] == '{' && depth > 0:
			depth++
		case tag[i] == '}' && depth > 0:
			depth--
			if isExpr && depth == 0 {
				return tag[start:i], true, true
			}
		case tag[i] == '"' && depth == 0 && !isExpr:
			return tag[start:i], false, true
		}
	}
	return tag[start:], isExpr, true
}

// classNames returns the class names a class attribute can produce: the
// literal words of a quoted value outside its interpolations, plus the words
// of every string literal inside an expression. An identifier is not a class
// name, so a class spelled only through variables names nothing.
func classNames(value string, isExpr bool) []string {
	var names []string
	expr := value
	if !isExpr {
		var literal, inner strings.Builder
		depth := 0
		for i := 0; i < len(value); i++ {
			switch {
			case strings.HasPrefix(value[i:], "${"):
				depth++
				i++
				literal.WriteByte(' ')
				continue
			case value[i] == '{' && depth > 0:
				depth++
			case value[i] == '}' && depth > 0:
				depth--
				if depth == 0 {
					inner.WriteByte(' ')
					continue
				}
			}
			if depth == 0 {
				literal.WriteByte(value[i])
			} else {
				inner.WriteByte(value[i])
			}
		}
		names = strings.Fields(literal.String())
		expr = inner.String()
	}
	for _, s := range quotedString.FindAllString(expr, -1) {
		names = append(names, strings.Fields(strings.Trim(s, "\"'`"))...)
	}
	return names
}

// TestEveryButtonUsesTheControlSystem fails on any <button> in the SPA's
// modules whose class names include neither "button" nor "menu-item".
func TestEveryButtonUsesTheControlSystem(t *testing.T) {
	tags := buttonTags(t)
	require.NotEmpty(t, tags, "the scan found no <button> at all - it is reading the wrong place")
	for _, b := range tags {
		key := fmt.Sprintf("%s:%d", filepath.ToSlash(b.file), b.line)
		if _, allowed := buttonClassAllowList[key]; allowed {
			continue
		}
		value, isExpr, ok := classValue(b.tag)
		if !ok || !slices.ContainsFunc(classNames(value, isExpr), func(c string) bool {
			return slices.Contains(controlSystemClasses, c)
		}) {
			flat := strings.Join(strings.Fields(b.tag), " ")
			t.Errorf("%s: a <button> outside the control system - give it class \"button\" (with a variant) or \"menu-item\": %s",
				key, flat[:min(160, len(flat))])
		}
	}
}

// quietAffordanceClasses are the classes that give a quiet button an
// affordance of its own: a glyph-only control (✕, ?, ‹ ›, the bell) or a
// picker/menu trigger, which draws its ▾.
var quietAffordanceClasses = []string{"button--icon", "picker__trigger"}

// TestQuietButtonsCarryTheirOwnAffordance fails on a borderless
// (button--quiet) <button> that is neither glyph-only nor a menu trigger.
// Borderless at rest, a text-only control with no ▾ reads as a label - the
// theme toggle's "Theme: system" did, until it became a secondary button
// (#518) - so a text-only control keeps a visible boundary at rest.
func TestQuietButtonsCarryTheirOwnAffordance(t *testing.T) {
	for _, b := range buttonTags(t) {
		value, isExpr, ok := classValue(b.tag)
		if !ok {
			continue
		}
		names := classNames(value, isExpr)
		if !slices.Contains(names, "button--quiet") {
			continue
		}
		if !slices.ContainsFunc(names, func(c string) bool { return slices.Contains(quietAffordanceClasses, c) }) {
			t.Errorf("%s:%d: a quiet button with no affordance of its own - a text-only control is a secondary .button (a visible boundary at rest); .button--quiet is for glyph-only (.button--icon) controls and menu triggers (.picker__trigger, with their ▾): %s",
				filepath.ToSlash(b.file), b.line, strings.Join(names, " "))
		}
	}
}

// TestButtonScan_ReadsEveryClassShape pins the scanner's own reach: a
// multi-line tag with handlers, a class value with an interpolation, a
// bare-expression class, and prose in a comment that is not a tag.
func TestButtonScan_ReadsEveryClassShape(t *testing.T) {
	src := "// a <button> in prose\n" +
		"html`<button\n  type=\"button\"\n  onClick=${() => { go(); }}\n  class=\"button ${on ? \"button--primary\" : \"\"}\"\n>x</button>\n" +
		"<button class=${open ? \"menu-item menu-item--open\" : \"menu-item\"}>y</button>\n" +
		"<button class=\"picker__trigger\">z</button>\n" +
		"<button class=${cls}>w</button>`"
	tags := buttonTagsIn("x.js", src)
	require.Len(t, tags, 4, "the comment's <button> is prose, not a tag")
	want := [][]string{
		{"button", "button--primary"},
		{"menu-item", "menu-item--open", "menu-item"},
		{"picker__trigger"},
		nil,
	}
	for i, tag := range tags {
		value, isExpr, ok := classValue(tag.tag)
		require.True(t, ok, tag.tag)
		got := classNames(value, isExpr)
		if want[i] == nil {
			assert.Empty(t, got, "a class spelled only through a variable names nothing the scan can certify")
			continue
		}
		assert.Equal(t, want[i], got, tag.tag)
	}
	assert.Equal(t, 2, tags[0].line)
}

// buttonTargetClasses is every class name some <button> in the SPA carries,
// plus the control system's own - the classes a rule outside the control
// block may not reshape.
func buttonTargetClasses(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{"button": true, "menu-item": true}
	for _, b := range buttonTags(t) {
		if value, isExpr, ok := classValue(b.tag); ok {
			for _, c := range classNames(value, isExpr) {
				out[c] = true
			}
		}
	}
	return out
}

// TestNoControlStylingOutsideTheControlBlock fails on any app.css rule
// outside the control block that sets a control-defining property on a
// selector targeting a button - by a class some <button> carries, or by
// the button element itself.
func TestNoControlStylingOutsideTheControlBlock(t *testing.T) {
	css := readAppCSS(t)
	require.Equal(t, 1, strings.Count(css, controlBlockBegin), "app.css has exactly one control block")
	require.Equal(t, 1, strings.Count(css, controlBlockEnd), "app.css has exactly one control block")
	for _, v := range controlStylingViolations(css, buttonTargetClasses(t)) {
		t.Error(v)
	}
}

// buttonElement matches the button type selector (not a .button class, not
// a [type="button"] attribute, not a longer name).
var buttonElement = regexp.MustCompile(`(^|[\s>+~(,])button($|[^\w-"])`)

// controlStylingViolations is TestNoControlStylingOutsideTheControlBlock's
// checker over any stylesheet, so its own reach can be tested. Comments and
// the control block are blanked line-for-line first, so a violation's line
// number is the stylesheet's own.
func controlStylingViolations(css string, classes map[string]bool) []string {
	blank := func(s string) string { return regexp.MustCompile(`[^\n]`).ReplaceAllString(s, " ") }
	if begin := strings.Index(css, controlBlockBegin); begin >= 0 {
		if end := strings.Index(css[begin:], controlBlockEnd); end >= 0 {
			end += begin + len(controlBlockEnd)
			css = css[:begin] + blank(css[begin:end]) + css[end:]
		}
	}
	css = cssComment.ReplaceAllStringFunc(css, blank)

	var out []string
	for _, m := range cssRule.FindAllStringSubmatchIndex(css, -1) {
		selector := strings.TrimSpace(cssSpaceRun.ReplaceAllString(css[m[2]:m[3]], " "))
		if strings.HasPrefix(selector, "@") || !targetsButton(selector, classes) {
			continue
		}
		line := strings.Count(css[:m[4]], "\n") + 1
		for _, declaration := range strings.Split(css[m[4]:m[5]], ";") {
			property, _, ok := strings.Cut(declaration, ":")
			property = strings.ToLower(strings.TrimSpace(property))
			if ok && controlDefiningProperty.MatchString(property) {
				out = append(out, fmt.Sprintf(
					"app.css:%d: %q sets %s outside the control block - a button's shape comes from its .button variant/size or .menu-item; keep only placement (margin, position, flex, colour) here",
					line, selector, property))
			}
		}
	}
	return out
}

// targetsButton reports whether a selector list names the button element
// or any class in classes.
func targetsButton(selector string, classes map[string]bool) bool {
	if buttonElement.MatchString(selector) {
		return true
	}
	for _, m := range regexp.MustCompile(`\.([A-Za-z_][\w-]*)`).FindAllStringSubmatch(selector, -1) {
		if classes[m[1]] {
			return true
		}
	}
	return false
}

// TestControlStylingCheck_CatchesEveryForm pins the checker's reach: a
// button class, a modifier, a descendant, the element, a rule inside
// @media - and leaves the control block, placement-only rules and
// unrelated classes alone.
func TestControlStylingCheck_CatchesEveryForm(t *testing.T) {
	classes := map[string]bool{"button": true, "button--small": true, "picker__trigger": true}
	css := controlBlockBegin + "\n.button { padding: 0; height: 2rem; }\n" + controlBlockEnd + "\n" +
		"/* .picker__trigger { padding: 0 } */\n" +
		".picker__trigger { margin-left: auto; color: var(--x); }\n" +
		".picker__trigger:hover { border-color: red; }\n" +
		".bar .button--small { font-size: 2em; }\n" +
		".toolbar button { min-height: 0; }\n" +
		"@media (min-width: 1px) {\n  .button { border-radius: 0; }\n}\n" +
		".card { padding: 1rem; }\n" +
		".buttons { padding: 0; }\n" +
		"[type=\"button\"] { padding: 0; }\n"
	got := controlStylingViolations(css, classes)
	require.Len(t, got, 4, strings.Join(got, "\n"))
	assert.Contains(t, got[0], "app.css:6:")
	assert.Contains(t, got[0], "border-color")
	assert.Contains(t, got[1], "font-size")
	assert.Contains(t, got[2], "min-height")
	assert.Contains(t, got[3], "app.css:10:")
}
