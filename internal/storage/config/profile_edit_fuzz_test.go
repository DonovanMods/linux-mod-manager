package config

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// markerFuzzSeeds is the final review's editor corpus (fix round 3), every
// shape the tests above name, and the line breaks behind F1.
var markerFuzzSeeds = []string{
	"# top\nname: p # n\ngame_id: g\nmods:\n  # before a\n  - source_id: s # sc\n    mod_id: a # mc\n    # trailing a comment\n\n  # before b\n  - source_id: s\n    mod_id: b\n# end\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: a}\n  - {source_id: s, mod_id: b,   }\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s,\n     mod_id: a # c\n    }\n",
	"name: p\ngame_id: g\nmods: [{source_id: s, mod_id: a}, {source_id: s, mod_id: b}]\n",
	"name: p\ngame_id: g\nmods: [source_id: s]\n",
	"name: p\ngame_id: g\nmods:\n  - &base\n    source_id: s\n    mod_id: a\n  - <<: *base\n    mod_id: b\n",
	"name: p\ngame_id: g\nx: &r {source_id: s, mod_id: a}\nmods:\n  - *r\n",
	"name: p\ngame_id: g\nx: &r {source_id: s}\nmods:\n  - <<: *r\n    mod_id: a\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n  - source_id: s\n    mod_id: a\n",
	"name: p\r\ngame_id: g\r\nmods:\r\n  - source_id: s\r\n    mod_id: a\r\n  - source_id: s\r\n    mod_id: b\r\n",
	"\ufeffname: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n",
	"\ufeff{name: p, game_id: g, mods: [{source_id: s, mod_id: a}]}\n",
	"\ufeffname: p\r\ngame_id: g\r\nmods:\r\n- source_id: s\r\n  mod_id: a\r\n",
	"name: p\ngame_id: g\nmods:\n  -\tsource_id: s\n    mod_id: a\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\t\n    mod_id: a\t\t\n",
	"name: p\ngame_id: g\nmods:\n- source_id: s\n  mod_id: a\n- source_id: s\n  mod_id: b\n",
	"name: p\ngame_id: g\nmods:\n    -   source_id: s\n        mod_id: a\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a # c",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    disabled: false # keep\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    disabled: no\n    mod_id: a\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    disabled:\n    mod_id: a\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    disabled:\n  - source_id: s\n    mod_id: b\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    disabled:\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    disabled: ~\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    disabled: \"false\"\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    disabled: &f false\n  - source_id: s\n    mod_id: b\n    disabled: *f\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    note:\n  - source_id: s\n    mod_id: b\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    note:\nis_default: true\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    meta:\n      k: v\n      # deep comment\n  - source_id: s\n    mod_id: b\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    note: |\n      line1\n      line2\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    note: first\n      second\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    note: \"first\n      second\"\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    file_ids:\n      - \"1\"\n      - \"2\"\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    file_ids: [\"1\",\n      \"2\"]\n  - source_id: s\n    mod_id: b\n",
	"---\nname: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n...\n---\nother: doc\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\nhooks:\n  install:\n    after_all: ~/bin/x.sh\noverrides:\n  a.ini: |\n    k=v\n",
	"name: p\ngame_id: g\nmods:\n  - mod_id: a\n    source_id: s\n    version: \"1.0\"   \n",
	"name: p\ngame_id: g\n\"mods\":\n  - \"source_id\": 's'\n    'mod_id': \"a\"\n",
	"name: pé\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    note: \"日本語 ✓\"\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: a, note: it's}\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s, note: \"}{\", mod_id: a}\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s, note: 'a #b', mod_id: a}\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s, file_ids: [\"1\", \"2\"], mod_id: a}\n",
	"name: p\rgame_id: g\rmods:\r  - source_id: s\r    mod_id: a\r",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\nis_default: true\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n        # deeper comment\n  - source_id: s\n    mod_id: b\n",
	"name: p\ngame_id: g\nmods:\n  -\n    source_id: s\n    mod_id: a\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n# note\u0085more\n# x\u2028y\u2029z\n",
	"name: p # a\u2028b\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n  - source_id: s\n    mod_id: b\n",
	"name: \"p\u0085q\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    version: \"1\"\n",
	"name: p\r\r\ngame_id: g\r\r\nmods:\r\r\n  - source_id: s\r\r\n    mod_id: a\r\r\n",
	"name: \"p\u0085q\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a",
	"name: \"p\u0085q\u2028r\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n",
	"# exported\r from windows\r\nname: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n# end\r\n",
	"name: p\ngame_id: g\nmods:\n\t- source_id: s\n\t  mod_id: a\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\u0085",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\u2028  - source_id: s\n    mod_id: b\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\u2029    mod_id: a",
	"name: p\rgame_id: g\rmods:\r  - {source_id: s,\r     mod_id: a\r    }\r",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: a\u0085  }\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s,\u2028 mod_id: a # c\u2028 }\n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    disabled: \n",
	"name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: a\n    file_ids: [a,\n      b\n    ]\n  - source_id: s\n    mod_id: b\n",
	"name: p\ngame_id: g\nmods: [ {source_id: 'it''s}', mod_id: don't , } ]\n",
	"name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: a, disabled: yes}\n  - {source_id: s, mod_id: b, disabled: true}\n",
	"name: p\ngame_id: g\nmods: []\n",
	"name: p\ngame_id: g\nbase: &base {source_id: s, mod_id: a}\nmods:\n  - *base\n",
	"name: default\ngame_id: g1\nmods:\n    - source_id: nexusmods\n      mod_id: \"1\"\n      version: \"1.0\"\n      file_ids:\n        - \"10\"\n        - \"11\"\n    - source_id: nexusmods\n      mod_id: \"2\"\n      version: \"2.0\"\n      locked: true\nis_default: true\n",
}

// FuzzMarkModsDisabled is F1's property test over the in-place editor. For
// any input, MarkModsDisabled never panics, and either
//
//   - it declines: an error, and the file is byte-identical; or
//   - it succeeds: the file decodes to exactly the original document with the
//     marker set on every reference to a requested mod not already marked
//     (and nothing else), and its bytes differ from the input only by
//     marker text - a `true` over a plain `disabled:` value, `, disabled:
//     true` before a flow reference's closing brace, or a whole marker line
//     at the end of a block reference's line, ending the way that line does.
//
// pick chooses the requested mods: bit i asks for the file's i-th distinct
// mod, and bit 63 also asks for one the file does not list.
func FuzzMarkModsDisabled(f *testing.F) {
	for _, seed := range markerFuzzSeeds {
		f.Add([]byte(seed), ^uint64(0))
		f.Add([]byte(seed), uint64(1))
		f.Add([]byte(seed), uint64(2))
	}
	f.Fuzz(func(t *testing.T, data []byte, pick uint64) {
		path := filepath.Join(t.TempDir(), "p.yaml")
		require.NoError(t, os.WriteFile(path, data, 0o644))

		var before ProfileConfig
		parsed := safeyaml.Unmarshal(data, &before) == nil
		var mods []domain.ModReference
		var keys []string
		for _, ref := range before.Mods {
			key := domain.ModKey(ref.SourceID, ref.ModID)
			if slices.Contains(keys, key) {
				continue
			}
			if bit := len(keys); bit < 63 && pick&(1<<bit) != 0 {
				mods = append(mods, domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID})
			}
			keys = append(keys, key)
		}
		if pick&(1<<63) != 0 || !parsed {
			mods = append(mods, domain.ModReference{SourceID: "not", ModID: "listed"})
		}

		marked, err := MarkModsDisabled(path, mods)
		after, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		if err != nil {
			require.Equal(t, data, after, "a declined edit leaves the file byte-identical")
			require.Nil(t, marked)
			return
		}
		require.True(t, parsed, "an input yaml.v3 cannot decode is never edited")

		// Every unmarked reference to a requested mod; each such mod reported
		// once, in the file order of its first newly marked reference.
		requested := make(map[string]bool, len(mods))
		for _, m := range mods {
			requested[domain.ModKey(m.SourceID, m.ModID)] = true
		}
		want := before
		want.Mods = slices.Clone(before.Mods)
		var wantMarked []domain.ModReference
		wantEdits := 0
		reported := make(map[string]bool)
		for i, ref := range before.Mods {
			key := domain.ModKey(ref.SourceID, ref.ModID)
			if !requested[key] || ref.Disabled {
				continue
			}
			want.Mods[i].Disabled = true
			wantEdits++
			if !reported[key] {
				reported[key] = true
				wantMarked = append(wantMarked, domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID})
			}
		}
		require.Equal(t, wantMarked, marked)
		if len(marked) == 0 {
			require.Equal(t, data, after, "nothing to mark writes nothing")
			return
		}

		var got ProfileConfig
		require.NoError(t, safeyaml.Unmarshal(after, &got))
		require.Equal(t, want, got)

		// Byte level: the file is the input with the planned edits applied,
		// and every one of them is marker text in a marker's place.
		_, edits, _, err := planMarkers(path, data, mods)
		require.NoError(t, err)
		require.Len(t, edits, wantEdits)
		applied, err := applyEdits(data, edits)
		require.NoError(t, err)
		require.Equal(t, string(applied), string(after))
		for _, e := range edits {
			assertMarkerEdit(t, data, e)
		}
	})
}

// assertMarkerEdit checks that e, an edit of data, is one of the three
// shapes a marker takes.
func assertMarkerEdit(t *testing.T, data []byte, e textEdit) {
	t.Helper()
	before, rest := data[:e.offset], data[e.offset:]
	switch {
	case e.length > 0:
		// A plain `disabled:` value, rewritten in place.
		assert.Equal(t, "true", e.text)
		old := string(rest[:e.length])
		assert.NotContains(t, old, "#")
		assert.False(t, strings.ContainsAny(old, " \t\r\n\u0085\u2028\u2029"), "replaced %q", old)
		assert.True(t, valueFollowsKey(before), "replaced text is not a value right after its key: %q", before)

	case e.text == ", disabled: true" || e.text == " disabled: true":
		// Inside a flow mapping, right before its closing brace.
		next := bytes.TrimLeft(rest, " \t\r\n\u0085\u2028\u2029")
		assert.True(t, bytes.HasPrefix(next, []byte("}")), "flow marker not before a brace: %q", rest)
		if e.text == " disabled: true" {
			assert.True(t, bytes.HasSuffix(before, []byte(",")), "a marker without its comma must follow one: %q", before)
		}

	default:
		// A whole line after a block reference's last line.
		trimmed := strings.TrimLeft(e.text, "\r\n")
		brk := e.text[:len(e.text)-len(trimmed)]
		assert.Contains(t, []string{"\n", "\r\n", "\r", "\r\r\n"}, brk, "marker line break %q", brk)
		assert.Equal(t, "disabled: true", strings.TrimLeft(trimmed, " "), "marker line %q", e.text)
		assert.False(t, endsInBreak(before), "a marker line follows content, not a line break: %q", before)
		if len(rest) > 0 {
			assert.Equal(t, brk, breakAtStart(rest), "the marker line ends the way its line does")
		} else {
			assert.Equal(t, brk, lastBreak(before), "at the end of the file, the way the last line break does")
		}
	}
}

// valueFollowsKey reports whether a value may start right after before: its
// key's colon, then white space - on the same line, or on later lines with
// only blank lines and comments in between (third fuzzing run: `{disabled:`
// with its value on the next line).
func valueFollowsKey(before []byte) bool {
	text := string(before)
	if text != "" && !endsInBreak(before) && !strings.ContainsAny(text[len(text)-1:], " \t") {
		return false // the value would touch whatever precedes it on its line
	}
	lines := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\u0085' || r == '\u2028' || r == '\u2029'
	})
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue // blank, or a comment, between the key and its value
		}
		if at := strings.Index(line, " #"); at >= 0 {
			line = strings.TrimSpace(line[:at])
		}
		return strings.HasSuffix(line, ":")
	}
	return false
}

func endsInBreak(b []byte) bool {
	for _, brk := range []string{"\n", "\r", "\u0085", "\u2028", "\u2029"} {
		if bytes.HasSuffix(b, []byte(brk)) {
			return true
		}
	}
	return false
}

// breakAtStart is the line break b starts with, reading the doubled
// `\r\r\n` as one.
func breakAtStart(b []byte) string {
	for _, brk := range []string{"\r\r\n", "\r\n", "\r", "\n", "\u0085", "\u2028", "\u2029"} {
		if bytes.HasPrefix(b, []byte(brk)) {
			return brk
		}
	}
	return ""
}

// lastBreak is the last line break in b, or "\n" when it has none.
func lastBreak(b []byte) string {
	for i := len(b) - 1; i >= 0; i-- {
		for _, brk := range []string{"\r\r\n", "\r\n", "\r", "\n", "\u0085", "\u2028", "\u2029"} {
			if bytes.HasSuffix(b[:i+1], []byte(brk)) {
				return brk
			}
		}
	}
	return "\n"
}

func TestValueFollowsKey(t *testing.T) {
	for before, want := range map[string]bool{
		"  - source_id: s\n    disabled: ":          true,
		"  - {disabled: ":                           true,
		"mods:\n  - {disabled:\n":                   true,
		"    disabled:\n      ":                     true,
		"    disabled: # why\n\n    # more\n      ": true,
		"    disabled:":                             false,
		"    source_id: s\n    ":                    false,
		"    note: x\n":                             false,
		"":                                          false,
	} {
		assert.Equal(t, want, valueFollowsKey([]byte(before)), "%q", before)
	}
}
