package serve_test

// The text-field ratchet (#652). app.css draws every text-entry field one
// way - the control block's height, border, fill and type size - but it
// does so by input TYPE, so a field of a type the block does not list
// falls back to the browser's own small grey box. The collection import's
// URL field did (type="url"), beside full-size text fields.
//
// TestEveryInputTypeIsStyled holds every <input> the SPA renders to a type
// the control block sizes, or to a type that is deliberately not a text
// field (nonTextInputTypes) and that the block still names (issue 653), or
// to a listed exception (unstyledInputTypes). An <input> must name its type: the browser's
// default is text, but app.css's input[type="text"] does not match an
// input with no type attribute at all.

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

// nonTextInputTypes are the input types that are not text fields, so they
// need no control height - but the control block still draws each one
// (issue 653): a box to tick or a radio, one rem square in the accent; a
// file input, never drawn, behind a visible .button. Each must be named by
// a rule in the block (blockInputTypes), or it is drawn by the browser.
var nonTextInputTypes = []string{"checkbox", "radio", "file"}

// unstyledInputTypes are the input types the control block deliberately
// does not draw, with the reason. A new entry needs a reason a reviewer
// accepts.
var unstyledInputTypes = map[string]string{
	"hidden": "carries a value with the form; never drawn, so there is nothing to style",
}

// typeAttr matches an input's own type attribute (not data-type=).
var typeAttr = regexp.MustCompile(`(?:^|\s)type=("|\$\{)`)

// inputTypeSelector matches an attribute selector on an input's type.
var inputTypeSelector = regexp.MustCompile(`input\[type="([a-z-]+)"\]`)

// styledInputTypes returns the input types the control block gives the
// control height: the types named in a rule inside the block that sets
// height.
func styledInputTypes(css string) []string {
	begin := strings.Index(css, controlBlockBegin)
	end := strings.Index(css, controlBlockEnd)
	if begin < 0 || end < begin {
		return nil
	}
	block := cssComment.ReplaceAllString(css[begin:end], "")
	var out []string
	for _, m := range cssRule.FindAllStringSubmatch(block, -1) {
		if !regexp.MustCompile(`(^|;)\s*height\s*:`).MatchString(m[2]) {
			continue
		}
		for _, t := range inputTypeSelector.FindAllStringSubmatch(m[1], -1) {
			if !slices.Contains(out, t[1]) {
				out = append(out, t[1])
			}
		}
	}
	return out
}

// blockInputTypes returns every input type a rule inside the control block
// names in its selector, whatever the rule sets.
func blockInputTypes(css string) []string {
	begin := strings.Index(css, controlBlockBegin)
	end := strings.Index(css, controlBlockEnd)
	if begin < 0 || end < begin {
		return nil
	}
	block := cssComment.ReplaceAllString(css[begin:end], "")
	var out []string
	for _, m := range cssRule.FindAllStringSubmatch(block, -1) {
		for _, t := range inputTypeSelector.FindAllStringSubmatch(m[1], -1) {
			if !slices.Contains(out, t[1]) {
				out = append(out, t[1])
			}
		}
	}
	return out
}

// drawnInputTypes is every type an <input> in the SPA may have: the text
// types the block gives the control height, the non-text types the block
// names, and the deliberate exceptions.
func drawnInputTypes(css string) []string {
	out := styledInputTypes(css)
	for _, ty := range blockInputTypes(css) {
		if slices.Contains(nonTextInputTypes, ty) && !slices.Contains(out, ty) {
			out = append(out, ty)
		}
	}
	for ty := range unstyledInputTypes {
		out = append(out, ty)
	}
	return out
}

// inputTypes returns the types an <input> tag can have: its quoted type,
// or every string literal in a ${...} type expression. ok is false when
// the tag has no type attribute.
func inputTypes(tag string) (types []string, ok bool) {
	value, isExpr, ok := attrValue(tag, typeAttr)
	if !ok {
		return nil, false
	}
	if !isExpr {
		return []string{value}, true
	}
	for _, s := range quotedString.FindAllString(value, -1) {
		types = append(types, strings.Trim(s, "\"'`"))
	}
	return types, true
}

// inputTypeViolations is TestEveryInputTypeIsStyled's checker over one
// module's source, so its own reach can be tested.
func inputTypeViolations(file, src string, drawn []string) []string {
	var out []string
	for _, in := range openingTagsIn(file, src, "input") {
		key := fmt.Sprintf("%s:%d", filepath.ToSlash(in.file), in.line)
		types, ok := inputTypes(in.tag)
		if !ok || len(types) == 0 {
			out = append(out, key+`: an <input> with no type - name it (type="text" for a text field), since app.css styles fields by type`)
			continue
		}
		for _, ty := range types {
			if !slices.Contains(drawn, ty) {
				out = append(out, fmt.Sprintf(
					"%s: an <input type=%q> that app.css's control block does not style - add input[type=%q] to the block's field rules (beside text and search, or beside checkbox and radio), so it is drawn like every other control",
					key, ty, ty))
			}
		}
	}
	return out
}

// TestEveryInputTypeIsStyled fails on any <input> in the SPA whose type
// the control block does not size and that is not a deliberate non-text
// input.
func TestEveryInputTypeIsStyled(t *testing.T) {
	css := readAppCSS(t)
	require.Contains(t, styledInputTypes(css), "text", "the scan found no styled input types - it is reading the wrong place")
	styled := drawnInputTypes(css)

	var found int
	err := filepath.Walk("spa", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if ext := filepath.Ext(path); ext != ".js" && ext != ".html" {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/vendor/") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found += len(openingTagsIn(path, string(data), "input"))
		for _, v := range inputTypeViolations(path, string(data), styled) {
			t.Error(v)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, found, "the scan found no <input> at all - it is reading the wrong place")
}

// TestInputTypeCheck_CatchesEveryForm pins the checker's reach: an
// unstyled literal type, a multi-line tag, a missing type, a type chosen
// by an expression, and data-type= not mistaken for type=; the styled and
// non-text types pass.
func TestInputTypeCheck_CatchesEveryForm(t *testing.T) {
	css := "a { color: red; }\n" + controlBlockBegin + "\n" +
		"/* input[type=\"week\"] { height: 1px; } */\n" +
		"select,\ninput[type=\"text\"],\ninput[type=\"url\"] {\n  height: var(--control-height);\n}\n" +
		"input[type=\"color\"] { border-radius: 0; }\n" +
		"input[type=\"checkbox\"],\ninput[type=\"radio\"] { accent-color: var(--accent); }\n" +
		controlBlockEnd + "\ninput[type=\"date\"] { height: 2rem; }\ninput[type=\"file\"] { display: none; }\n"
	assert.Equal(t, []string{"text", "url"}, styledInputTypes(css),
		"only a rule inside the block that sets height counts; a comment, a height-less rule and a rule outside the block do not")
	styled := drawnInputTypes(css)
	assert.ElementsMatch(t, []string{"text", "url", "checkbox", "radio", "hidden"}, styled,
		"a non-text type counts once the block names it (color is not a non-text type; file is named only outside the block)")

	src := "// an <input> in prose\n" +
		"html`<input type=\"text\" />\n" +
		"<input\n  data-type=\"text\"\n  type=\"number\"\n/>\n" +
		"<input value=${v} />\n" +
		"<input type=${single ? \"radio\" : \"checkbox\"} />\n" +
		"<input type=${on ? \"url\" : \"week\"} />\n" +
		"<input type=\"file\" hidden />`"
	got := inputTypeViolations("x.js", src, styled)
	require.Len(t, got, 4, strings.Join(got, "\n"))
	assert.Contains(t, got[0], "x.js:3:")
	assert.Contains(t, got[0], `"number"`)
	assert.Contains(t, got[1], "x.js:7:")
	assert.Contains(t, got[1], "no type")
	assert.Contains(t, got[2], "x.js:9:")
	assert.Contains(t, got[2], `"week"`)
	assert.Contains(t, got[3], "x.js:10:")
	assert.Contains(t, got[3], `"file"`, "a file input the block does not name is drawn by the browser")
}

// TestSelectAndTextareaAreStyled holds the two field elements that are not
// <input>s to the block: a select at the control height, a textarea with a
// floor of its own.
func TestSelectAndTextareaAreStyled(t *testing.T) {
	css := readAppCSS(t)
	begin := strings.Index(css, controlBlockBegin)
	end := strings.Index(css, controlBlockEnd)
	require.True(t, begin >= 0 && end > begin)
	block := cssComment.ReplaceAllString(css[begin:end], "")
	sets := func(element, property string) bool {
		for _, m := range cssRule.FindAllStringSubmatch(block, -1) {
			if fieldElement.MatchString(m[1]) && regexp.MustCompile(`(^|[\s,])`+element+`($|[\s,{])`).MatchString(m[1]) &&
				regexp.MustCompile(`(^|;)\s*`+property+`\s*:`).MatchString(m[2]) {
				return true
			}
		}
		return false
	}
	assert.True(t, sets("select", "height"), "the control block gives select the control height")
	assert.True(t, sets("textarea", "min-height"), "the control block gives textarea a minimum height")
	assert.True(t, sets("textarea", "border"), "the control block draws textarea's border")
}
