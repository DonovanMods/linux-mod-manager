package larian

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// modsettings.lsx is Baldur's Gate 3's mod list, PlayerProfiles/Public/
// modsettings.lsx beside the Mods folder [NMA "Load Order"; VXG util.ts
// profilesPath, getActivePlayerProfile]. Since Patch 7 (game 4.58.49) its
// shape is [VXG common.ts DEFAULT_MOD_SETTINGS_V7/V8; NMA]:
//
//	region ModuleSettings > node root > children > node Mods > children >
//	  node ModuleShortDesc, one per enabled module, in load order (the last
//	  loaded wins a conflict), each with the attributes
//	  Folder LSString, MD5 LSString, Name LSString, PublishHandle uint64,
//	  UUID guid, Version64 int64
//
// The first entry is the game's own: GustavDev (Patch 7) or GustavX (Patch
// 8, game 4.67.58), which the file always starts with [bg3.wiki
// Modding:Installing_mods: "Gustav should always be the first item"]. A .pak
// in the Mods folder that the list does not name is disabled.
//
// Before Patch 7 the order was a separate ModOrder node of Module/UUID
// entries [VXG common.ts DEFAULT_MOD_SETTINGS_V6]. lmm does not write that
// format: ModSettings.HasModOrder reports it, and the caller refuses.
//
// ParseModSettings keeps the file's bytes; Render rewrites only the Mods
// node, copying every entry it keeps byte for byte, so whatever else the
// file holds - its header, its version, other regions, an entry's own
// formatting - is left exactly as it was.

// ErrNoModList: the file is LSX, but has no ModuleSettings mod list.
var ErrNoModList = errors.New("no ModuleSettings mod list")

// ModSettings is a parsed modsettings.lsx.
type ModSettings struct {
	// Entries are the Mods node's entries, in the file's order.
	Entries []ModEntry
	// HasModOrder reports a pre-Patch 7 file, with a ModOrder node.
	HasModOrder bool

	data []byte
	mods *lsxNode
}

// ModEntry is one entry of the mod list.
type ModEntry struct {
	UUID, Folder, Name, Version64, MD5 string

	// doc and index are the parsed file the entry is from and its place
	// there; nil and -1 for a new entry (NewModEntry). An entry from a
	// file - this one, or another, such as a kept original - is written
	// back as its own bytes.
	doc   *ModSettings
	index int
	raw   []byte
}

// FromFile reports whether e was read from a file, and is written back as
// it was there.
func (e ModEntry) FromFile() bool { return e.doc != nil && e.raw != nil }

// NewModEntry is the entry for m, as Vortex writes one for a Patch 7 or 8
// list [VXG loadOrder.ts exportTo: Folder, MD5, Name, PublishHandle 0,
// UUID, Version64, in that order].
func NewModEntry(m Module) ModEntry {
	return ModEntry{UUID: m.UUID, Folder: m.Folder, Name: m.Name, Version64: m.Version64, MD5: m.MD5, index: -1}
}

// Matches reports whether e lists m as m's meta.lsx describes it now: the
// same UUID, folder, name, version and checksum.
func (e ModEntry) Matches(m Module) bool {
	return strings.EqualFold(e.UUID, m.UUID) && e.Folder == m.Folder && e.Name == m.Name && e.Version64 == m.Version64 && e.MD5 == m.MD5
}

// ParseModSettings parses a modsettings.lsx.
func ParseModSettings(data []byte) (*ModSettings, error) {
	doc, err := parseLSX(data)
	if err != nil {
		return nil, err
	}
	region := doc.region("ModuleSettings")
	if region == nil {
		return nil, fmt.Errorf("%w: no ModuleSettings region", ErrNoModList)
	}
	root := region.node("root")
	if root == nil {
		return nil, fmt.Errorf("%w: no root node", ErrNoModList)
	}
	mods := root.child("Mods")
	if mods == nil {
		return nil, fmt.Errorf("%w: no Mods node", ErrNoModList)
	}
	s := &ModSettings{data: data, mods: mods, HasModOrder: root.child("ModOrder") != nil}
	for _, n := range mods.Children {
		e := ModEntry{doc: s, index: len(s.Entries), raw: data[n.Start:n.End]}
		e.UUID, _ = n.attr("UUID")
		e.Folder, _ = n.attr("Folder")
		e.Name, _ = n.attr("Name")
		e.Version64, _ = n.attr("Version64")
		e.MD5, _ = n.attr("MD5")
		s.Entries = append(s.Entries, e)
	}
	return s, nil
}

// Render is the file with its mod list replaced by entries: the parsed
// file's own entries copied as they were, new ones written as Vortex
// writes them, indented as the file indents its list. Entries that are
// exactly the file's own, in its order, give back the file's own bytes.
func (s *ModSettings) Render(entries []ModEntry) []byte {
	if s.unchanged(entries) {
		return bytes.Clone(s.data)
	}
	eol := "\n"
	if bytes.Contains(s.data, []byte("\r\n")) {
		eol = "\r\n"
	}
	ind0 := indentBefore(s.data, s.mods.Start)
	// The list's own entries say how deep a level is; failing them, the
	// Mods node's indentation does (the game's Patch 8 file indents by
	// four spaces, Patch 7's by two [VXG common.ts]).
	unit := "    "
	switch {
	case strings.Contains(ind0, "\t"):
		unit = "\t"
	case len(ind0)%4 != 0:
		unit = "  "
	}
	if len(s.mods.Children) > 0 {
		if in := indentBefore(s.data, s.mods.Children[0].Start); strings.HasPrefix(in, ind0) && len(in) > len(ind0) && (len(in)-len(ind0))%2 == 0 {
			unit = in[len(ind0) : len(ind0)+(len(in)-len(ind0))/2]
		}
	}
	ind1, ind2, ind3 := ind0+unit, ind0+unit+unit, ind0+unit+unit+unit

	var b bytes.Buffer
	b.WriteString(`<node id="Mods">`)
	b.WriteString(eol + ind1 + "<children>")
	for _, e := range entries {
		b.WriteString(eol + ind2)
		if e.FromFile() {
			b.Write(e.raw)
			continue
		}
		b.WriteString(`<node id="ModuleShortDesc">`)
		for _, a := range []struct{ id, typ, value string }{
			{"Folder", "LSString", e.Folder},
			{"MD5", "LSString", e.MD5},
			{"Name", "LSString", e.Name},
			{"PublishHandle", "uint64", "0"},
			{"UUID", "guid", e.UUID},
			{"Version64", "int64", e.Version64},
		} {
			fmt.Fprintf(&b, `%s%s<attribute id="%s" type="%s" value="%s"/>`, eol, ind3, a.id, a.typ, escapeAttr(a.value))
		}
		b.WriteString(eol + ind2 + "</node>")
	}
	b.WriteString(eol + ind1 + "</children>")
	b.WriteString(eol + ind0 + "</node>")

	out := make([]byte, 0, len(s.data)+b.Len())
	out = append(out, s.data[:s.mods.Start]...)
	out = append(out, b.Bytes()...)
	return append(out, s.data[s.mods.End:]...)
}

// unchanged reports whether entries are exactly this parsed file's own, in
// its order.
func (s *ModSettings) unchanged(entries []ModEntry) bool {
	if len(entries) != len(s.Entries) {
		return false
	}
	for i, e := range entries {
		if e.doc != s || e.index != i {
			return false
		}
	}
	return true
}

// indentBefore is the whitespace between the start of the line holding
// data[at] and at, or "" when anything else comes first on that line.
func indentBefore(data []byte, at int) string {
	start := at
	for start > 0 && (data[start-1] == ' ' || data[start-1] == '\t') {
		start--
	}
	if start > 0 && data[start-1] != '\n' {
		return ""
	}
	return string(data[start:at])
}

// escapeAttr escapes v for a double-quoted attribute value.
func escapeAttr(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, "&#x%X;", r)
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
