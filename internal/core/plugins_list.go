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

// pluginListFile is a list file as read: its plugins, the comment lines
// that open it (kept on a rewrite), and its line ending.
type pluginListFile struct {
	entries []listEntry
	header  []string
	eol     string
}

// eolCRLF is the line ending lmm writes in a new list file: the games' own
// launchers write CRLF (design §5, "Line endings"). An existing file keeps
// its own.
const eolCRLF = "\r\n"

// lineEndingOf is the line ending data uses: CRLF when its first line ends
// in one, LF when it ends in a bare LF, and CRLF for data with no line
// break at all.
func lineEndingOf(data []byte) string {
	i := bytes.IndexByte(data, '\n')
	switch {
	case i < 0:
		return eolCRLF
	case i > 0 && data[i-1] == '\r':
		return eolCRLF
	default:
		return "\n"
	}
}

// parsePluginList reads a list file's text. asterisk says whether a '*'
// marks an active plugin (the asterisk format) or every listed plugin is
// active (the textfile format's Plugins.txt, where a stray '*' is
// tolerated). A plugin listed twice keeps its first line.
func parsePluginList(text string, asterisk bool) pluginListFile {
	out := pluginListFile{eol: lineEndingOf([]byte(text))}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if len(out.entries) == 0 {
				out.header = append(out.header, line)
			}
			continue
		}
		active := !asterisk
		if strings.HasPrefix(trimmed, "*") {
			active = true
			trimmed = strings.TrimSpace(trimmed[1:])
		}
		if trimmed == "" || seen[pluginKey(trimmed)] {
			continue
		}
		seen[pluginKey(trimmed)] = true
		out.entries = append(out.entries, listEntry{name: trimmed, active: active})
	}
	return out
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
// present, first in the textfile format's loadorder.txt. header and eol
// come from the file being replaced.
func renderPluginList(spec adapter.PluginSpec, entries []listEntry, implicit []string, header []string, eol, orderEOL string) renderedList {
	var b strings.Builder
	for _, h := range header {
		b.WriteString(h)
		b.WriteString(eol)
	}
	switch spec.ListKind {
	case adapter.ListTextfile:
		for _, e := range entries {
			if e.active {
				b.WriteString(e.name)
				b.WriteString(eol)
			}
		}
		var o strings.Builder
		for _, name := range implicit {
			o.WriteString(name)
			o.WriteString(orderEOL)
		}
		for _, e := range entries {
			o.WriteString(e.name)
			o.WriteString(orderEOL)
		}
		list, _ := encodeWindows1252(b.String())
		return renderedList{list: list, order: []byte(o.String())}
	default:
		for _, e := range entries {
			if e.active {
				b.WriteByte('*')
			}
			b.WriteString(e.name)
			b.WriteString(eol)
		}
		list, _ := encodeWindows1252(b.String())
		return renderedList{list: list}
	}
}
