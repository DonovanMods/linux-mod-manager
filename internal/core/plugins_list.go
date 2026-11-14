package core

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
)

// The active-plugin list files (#616): reading and rendering a game's
// Plugins.txt (and Skyrim LE's loadorder.txt). Facts from libloadorder's
// docs (doc/src/active_plugins.md, textfile_standard.md; GPL, read for
// behaviour, no code taken): Plugins.txt is Windows-1252, one plugin per
// line, '#' starting a comment; in the asterisk format (Skyrim SE and
// later) every plugin is listed in load order and an active one's line
// starts with '*'; in the textfile format (Skyrim LE) Plugins.txt lists
// only the active plugins and loadorder.txt, UTF-8 without a BOM, the full
// load order.

// listEntry is one plugin a list file names.
type listEntry struct {
	name   string
	active bool
}

// pluginListFile is a list file as read: its plugins, its comment lines
// (kept on a rewrite, where they were), and its line ending.
type pluginListFile struct {
	entries []listEntry
	// comments are the file's '#' lines, verbatim, in file order, each
	// block under the plugin line it followed ("" for the lines before the
	// first plugin - the game's own header).
	comments []commentBlock
	// prev maps a plugin's key to the key of the plugin line before it
	// ("" for the first), so a comment whose plugin is gone stays after
	// the nearest plugin that is not.
	prev map[string]string
	eol  string
}

// commentBlock is a run of comment lines and the plugin they follow.
type commentBlock struct {
	after string
	lines []string
}

// eolNew is the line ending lmm writes in a new list file: LF, which is
// what Skyrim SE under Proton writes in its own Plugins.txt (observed on a
// real install, compatdata 489830, 2026-11-13: zero CRs, while the same
// game's Skyrim.ini is CRLF; design §5, "Line endings"). An existing file
// keeps its own.
const eolNew = "\n"

// lineEndingOf is the line ending data uses: CRLF when its first line ends
// in one, LF when it ends in a bare LF, and eolNew for data with no line
// break at all.
func lineEndingOf(data []byte) string {
	i := bytes.IndexByte(data, '\n')
	switch {
	case i < 0:
		return eolNew
	case i > 0 && data[i-1] == '\r':
		return "\r\n"
	default:
		return "\n"
	}
}

// parsePluginList reads a list file's text. asterisk says whether a '*'
// marks an active plugin (the asterisk format) or every listed plugin is
// active (the textfile format's Plugins.txt, where a stray '*' is
// tolerated). A plugin listed twice keeps its first line.
func parsePluginList(text string, asterisk bool) pluginListFile {
	out := pluginListFile{eol: lineEndingOf([]byte(text)), prev: map[string]string{}}
	seen := map[string]bool{}
	last := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if n := len(out.comments); n > 0 && out.comments[n-1].after == last {
				out.comments[n-1].lines = append(out.comments[n-1].lines, line)
			} else {
				out.comments = append(out.comments, commentBlock{after: last, lines: []string{line}})
			}
			continue
		}
		active := !asterisk
		if strings.HasPrefix(trimmed, "*") {
			active = true
			trimmed = strings.TrimSpace(trimmed[1:])
		}
		if trimmed == "" {
			continue
		}
		key := pluginKey(trimmed)
		if !seen[key] {
			seen[key] = true
			out.prev[key] = last
			out.entries = append(out.entries, listEntry{name: trimmed, active: active})
		}
		last = key
	}
	return out
}

// header is the comment lines before the file's first plugin.
func (f pluginListFile) header() []string {
	if len(f.comments) > 0 && f.comments[0].after == "" {
		return f.comments[0].lines
	}
	return nil
}

// placeComments puts f's comment blocks against the plugins written: each
// after the plugin it followed, or - when that plugin is not written - the
// nearest earlier one that is, else at the top. written holds the keys of
// the plugin lines being written.
func (f pluginListFile) placeComments(written map[string]bool) (top []string, after map[string][]string) {
	after = map[string][]string{}
	for _, c := range f.comments {
		k := c.after
		for seen := map[string]bool{}; k != "" && !written[k] && !seen[k]; {
			seen[k] = true
			k = f.prev[k]
		}
		if k == "" || !written[k] {
			top = append(top, c.lines...)
			continue
		}
		after[k] = append(after[k], c.lines...)
	}
	return top, after
}

// decodePluginsTxt decodes Plugins.txt's Windows-1252 bytes.
func decodePluginsTxt(data []byte) string { return decodeWindows1252(data) }

// decodeLoadOrderTxt decodes loadorder.txt: UTF-8, a BOM tolerated, and
// Windows-1252 when the bytes are not valid UTF-8 (libloadorder's
// fallback).
func decodeLoadOrderTxt(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) {
		return string(data)
	}
	return decodeWindows1252(data)
}

// encodeWindows1252 encodes s in Windows-1252, reporting false when a rune
// has no Windows-1252 byte.
func encodeWindows1252(s string) ([]byte, bool) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			b, ok := windows1252Byte(r)
			if !ok {
				return nil, false
			}
			out = append(out, b)
		}
	}
	return out, true
}

// windows1252Byte is the byte in 0x80-0x9F that decodes to r, if any
// (decodeWindows1252's table, inverted: the five undefined bytes map to
// their C1 controls both ways, so a file read is written back unchanged).
func windows1252Byte(r rune) (byte, bool) {
	for i, c := range windows1252High {
		if c == r {
			return byte(0x80 + i), true
		}
	}
	return 0, false
}

// listableName reports whether name can be written to a list file: it
// encodes in Windows-1252 and holds no control character - a file name can
// carry a line break, which would add a line to the list.
func listableName(name string) bool {
	for _, r := range name {
		if r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F) {
			return false
		}
	}
	_, ok := encodeWindows1252(name)
	return ok
}

// renderedList is what a sync writes: the list file's bytes, and for the
// textfile format the order file's.
type renderedList struct {
	list  []byte
	order []byte
}

// renderPluginList renders entries - the full load order, implicit plugins
// excluded - for spec's list kind. implicit are the implicit plugins
// present, first in the textfile format's loadorder.txt. file is the list
// file being replaced and orderFile the order file (Skyrim LE): their
// comment lines are kept where they were, and their line endings used.
func renderPluginList(spec adapter.PluginSpec, entries []listEntry, implicit []string, file, orderFile pluginListFile, orderEOL string) renderedList {
	if spec.ListKind != adapter.ListTextfile {
		return renderedList{list: renderLines(file, entries, true, false)}
	}
	var active []listEntry
	for _, e := range entries {
		if e.active {
			active = append(active, e)
		}
	}
	order := make([]listEntry, 0, len(implicit)+len(entries))
	for _, name := range implicit {
		order = append(order, listEntry{name: name, active: true})
	}
	order = append(order, entries...)
	if orderEOL != "" {
		orderFile.eol = orderEOL
	} else if orderFile.eol == "" {
		orderFile.eol = file.eol
	}
	return renderedList{list: renderLines(file, active, false, false), order: renderLines(orderFile, order, false, true)}
}

// renderLines writes entries, one per line, in file's line ending, with
// file's comment lines where they were (placeComments): with star, an
// active entry's line starts with '*'. utf8 writes UTF-8 (loadorder.txt),
// else Windows-1252 - and an entry with no Windows-1252 spelling is left
// out of that file alone, so one name can never cost the whole list (#616
// review 8: a non-1252 external stays in loadorder.txt).
func renderLines(file pluginListFile, entries []listEntry, star, utf8 bool) []byte {
	eol := file.eol
	if eol == "" {
		eol = eolNew
	}
	encode := func(text string) ([]byte, bool) {
		if utf8 {
			return []byte(text), true
		}
		return encodeWindows1252(text)
	}
	written := map[string]bool{}
	for _, e := range entries {
		if _, ok := encode(e.name); ok {
			written[pluginKey(e.name)] = true
		}
	}
	top, after := file.placeComments(written)
	var b []byte
	line := func(text string) {
		if enc, ok := encode(text); ok {
			b = append(append(b, enc...), eol...)
		}
	}
	for _, c := range top {
		line(c)
	}
	for _, e := range entries {
		key := pluginKey(e.name)
		if !written[key] {
			continue
		}
		text := e.name
		if star && e.active {
			text = "*" + text
		}
		line(text)
		for _, c := range after[key] {
			line(c)
		}
	}
	return b
}
