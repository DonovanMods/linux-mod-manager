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
	`^(height|min-height|max-height|block-size|min-block-size|max-block-size|padding(-[a-z-]+)?|border(-[a-z-]+)?|font|font-size|line-height)$`)

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

// buttonTagsIn finds every <button opening tag in one module's source.
func buttonTagsIn(file, src string) []buttonTag {
	return openingTagsIn(file, src, "button")
}

// openingTagsIn finds every <name opening tag in one module's source,
// skipping comment lines. A tag ends at the first ">" outside a ${...}
// interpolation, so a multi-line tag with arrow functions in its handlers
// is read whole.
func openingTagsIn(file, src, name string) []buttonTag {
	var out []buttonTag
	for _, loc := range regexp.MustCompile(`<`+name+`\b`).FindAllStringIndex(src, -1) {
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
	return attrValue(tag, classAttr)
}

// attrValue is classValue for any attribute: attr matches the attribute's
// name and "=", then its opening quote or "${" as group 1.
func attrValue(tag string, attr *regexp.Regexp) (value string, isExpr, ok bool) {
	loc := attr.FindStringSubmatchIndex(tag)
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
var buttonElement = regexp.MustCompile(`(?i)(^|[\s>+~(,])button($|[^\w-"])`)

// controlStylingViolations is TestNoControlStylingOutsideTheControlBlock's
// checker over any stylesheet, so its own reach can be tested.
func controlStylingViolations(css string, classes map[string]bool) []string {
	return reshapeViolations(css, func(selector string) bool { return targetsButton(selector, classes) },
		"a button's shape comes from its .button variant/size or .menu-item; keep only placement (margin, position, flex, colour) here")
}

// reshapeViolations reports every rule outside the control block whose
// selector targets says it aims at a control and that sets a
// control-defining property, with why in each message. Comments and the
// control block are blanked line-for-line first, so a violation's line
// number is the stylesheet's own.
func reshapeViolations(css string, targets func(selector string) bool, why string) []string {
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
		if strings.HasPrefix(selector, "@") || !targets(selector) {
			continue
		}
		line := strings.Count(css[:m[4]], "\n") + 1
		for _, declaration := range strings.Split(css[m[4]:m[5]], ";") {
			property, _, ok := strings.Cut(declaration, ":")
			property = strings.ToLower(strings.TrimSpace(property))
			if ok && controlDefiningProperty.MatchString(property) {
				out = append(out, fmt.Sprintf("app.css:%d: %q sets %s outside the control block - %s",
					line, selector, property, why))
			}
		}
	}
	return out
}

// targetsButton reports whether a selector list names the button element
// or any class in classes.
func targetsButton(selector string, classes map[string]bool) bool {
	if buttonElement.MatchString(selector) || universalSelector.MatchString(selector) {
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

// ---------------------------------------------------------------------------
// Phase 2 (#653): fields and forms. The same two halves for every other
// control - no rule outside the control block reshapes a field - and a
// third for layout: every form, fieldset and label is laid out by the
// form-layout classes, not a layout of its own.
// ---------------------------------------------------------------------------

// fieldTags returns every <input>, <select> and <textarea> opening tag in
// the SPA's modules.
func fieldTags(t *testing.T) []buttonTag {
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
		for _, name := range []string{"input", "select", "textarea"} {
			out = append(out, openingTagsIn(path, string(data), name)...)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

// fieldTargetClasses is every class name some field in the SPA carries.
func fieldTargetClasses(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range fieldTags(t) {
		if value, isExpr, ok := classValue(f.tag); ok {
			for _, c := range classNames(value, isExpr) {
				out[c] = true
			}
		}
	}
	return out
}

// fieldElement matches the input, select and textarea type selectors (not
// a class or a longer name that starts with one of them).
var fieldElement = regexp.MustCompile(`(?i)(^|[\s>+~(,])(input|select|textarea)($|[^\w-"])`)

// universalSelector matches a selector component that is the universal
// selector, alone or qualified only by pseudo-classes (".x > *",
// "*:hover"): it targets every control there is.
var universalSelector = regexp.MustCompile(`(^|[\s>+~(,])\*($|[\s>+~),:\[])`)

// targetsField reports whether a selector list names a field element or
// any class in classes.
func targetsField(selector string, classes map[string]bool) bool {
	if fieldElement.MatchString(selector) || universalSelector.MatchString(selector) {
		return true
	}
	for _, m := range regexp.MustCompile(`\.([A-Za-z_][\w-]*)`).FindAllStringSubmatch(selector, -1) {
		if classes[m[1]] {
			return true
		}
	}
	return false
}

// fieldStylingViolations is TestNoFieldStylingOutsideTheControlBlock's
// checker over any stylesheet.
func fieldStylingViolations(css string, classes map[string]bool) []string {
	return reshapeViolations(css, func(selector string) bool { return targetsField(selector, classes) },
		"a field's shape comes from the control block (a .field--mono or .field--code variant, if it needs one); keep only placement (width, margin, flex) here")
}

// TestNoFieldStylingOutsideTheControlBlock fails on any app.css rule
// outside the control block that sets a control-defining property - height,
// padding, border, radius, type size - on a selector targeting a field: an
// input, select or textarea element, or a class some field carries.
func TestNoFieldStylingOutsideTheControlBlock(t *testing.T) {
	classes := fieldTargetClasses(t)
	for _, v := range fieldStylingViolations(readAppCSS(t), classes) {
		t.Error(v)
	}
}

// TestFieldStylingCheck_CatchesEveryForm pins the field checker's reach: an
// element selector, an attribute-qualified one, a class a field carries, a
// rule in @media - and leaves the block, placement and look-alike names
// (an .input-group class, a [type="text"] attribute alone) alone.
func TestFieldStylingCheck_CatchesEveryForm(t *testing.T) {
	classes := map[string]bool{"omnibar": true}
	css := controlBlockBegin + "\ninput[type=\"text\"] { height: 2rem; }\n" + controlBlockEnd + "\n" +
		".editor textarea { padding: 0; }\n" +
		".bar > input[type=\"search\"] { flex: 1; width: 100%; }\n" +
		".omnibar { border-radius: 0; }\n" +
		"@media (min-width: 1px) {\n  select { font-size: 2em; }\n}\n" +
		".input-group { padding: 1rem; }\n" +
		"[type=\"text\"] { padding: 0; }\n" +
		".form :is(input, select) { line-height: 2; }\n" +
		".row > * { padding: 0; }\n" +
		".bar INPUT { block-size: 3rem; }\n" +
		"*, *::before { box-sizing: border-box; }\n"
	got := fieldStylingViolations(css, classes)
	require.Len(t, got, 6, strings.Join(got, "\n"))
	assert.Contains(t, got[0], "app.css:4:")
	assert.Contains(t, got[0], "padding")
	assert.Contains(t, got[1], "border-radius")
	assert.Contains(t, got[2], "font-size")
	assert.Contains(t, got[3], "line-height")
	assert.Contains(t, got[4], `".row > *"`, "a universal selector reaches every field")
	assert.Contains(t, got[5], "block-size", "an uppercase element and the logical height both count")
}

// formLayoutClasses are the form-layout classes each container element
// must carry one of: a form is a section (or, inline, a row); a fieldset is
// a section; a label is a field (its text over its control), a check (a
// box beside its text), or the label of a .form-field <div> whose control
// cannot sit inside a <label> (a field with a Browse… button beside it).
var formLayoutClasses = map[string][]string{
	"form":     {"form-section", "form-row"},
	"fieldset": {"form-section"},
	"label":    {"form-field", "form-check", "form-field__label"},
}

// formLayoutAllowList is every form, fieldset or label that may stay
// outside the form layout, keyed "file:line", with the reason. Empty, and
// meant to stay that way.
var formLayoutAllowList = map[string]string{}

// formLayoutViolations is TestEveryFormUsesTheFormLayout's checker over one
// module's source.
func formLayoutViolations(file, src string) []string {
	var out []string
	for _, element := range []string{"form", "fieldset", "label"} {
		want := formLayoutClasses[element]
		for _, tag := range openingTagsIn(file, src, element) {
			key := fmt.Sprintf("%s:%d", filepath.ToSlash(tag.file), tag.line)
			if _, allowed := formLayoutAllowList[key]; allowed {
				continue
			}
			value, isExpr, ok := classValue(tag.tag)
			if ok && slices.ContainsFunc(classNames(value, isExpr), func(c string) bool { return slices.Contains(want, c) }) {
				continue
			}
			flat := strings.Join(strings.Fields(tag.tag), " ")
			out = append(out, fmt.Sprintf("%s: a <%s> outside the form layout - give it one of %s (docs/control-system.md): %s",
				key, element, strings.Join(want, ", "), flat[:min(160, len(flat))]))
		}
	}
	return out
}

// TestEveryFormUsesTheFormLayout fails on any <form>, <fieldset> or <label>
// in the SPA's modules that carries none of the form-layout classes for its
// element: every form, dialog section and Setup panel is laid out one way
// (issue 653), so a new one cannot build a layout of its own.
func TestEveryFormUsesTheFormLayout(t *testing.T) {
	var found int
	err := filepath.Walk(filepath.Join("spa", "app"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".js" {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, element := range []string{"form", "label"} {
			found += len(openingTagsIn(path, string(data), element))
		}
		for _, v := range formLayoutViolations(path, string(data)) {
			t.Error(v)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, found, "the scan found no <form> or <label> at all - it is reading the wrong place")
}

// TestFormLayoutCheck_CatchesEveryForm pins the layout checker's reach: a
// bare form, a fieldset with a class of its own, a label carrying a
// layout class only through an expression it cannot read - and passes the
// layout classes, in a literal or an expression, and <legend>/<labelled>
// look-alikes.
func TestFormLayoutCheck_CatchesEveryForm(t *testing.T) {
	src := "// a <form> in prose\n" +
		"html`<form onSubmit=${(e) => { e.preventDefault(); }}>\n" +
		"<fieldset class=\"game-editor__group\">\n" +
		"<label class=${cls}>x</label>\n" +
		"<form class=\"form-section profiles-import__url\">\n" +
		"<label class=${wide ? \"form-field\" : \"form-check\"}>y</label>\n" +
		"<legend class=\"x\">z</legend><labelled-thing />\n" +
		"<fieldset class=\"form-section\"><label class=\"form-check\"><input type=\"checkbox\" /></label></fieldset>`"
	got := formLayoutViolations("x.js", src)
	require.Len(t, got, 3, strings.Join(got, "\n"))
	assert.Contains(t, got[0], "x.js:2:")
	assert.Contains(t, got[0], "<form>")
	assert.Contains(t, got[1], "x.js:3:")
	assert.Contains(t, got[1], "<fieldset>")
	assert.Contains(t, got[2], "x.js:4:")
	assert.Contains(t, got[2], "<label>")
}
