package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"gopkg.in/yaml.v3"
)

// ErrProfileLayoutUnsupported is returned by MarkModsDisabled when it cannot
// add the marker without changing anything else in the file - an alias
// standing in for the reference, a block scalar where the marker would go,
// or any other shape whose edited text does not decode to exactly the
// original document plus the marker. The file is left untouched.
var ErrProfileLayoutUnsupported = errors.New("the profile file's layout does not allow an in-place edit")

// ProfilePath returns the file gameID's profile profileName is read from by
// LoadProfile - the name is the FILE name, not the `name:` inside it.
func ProfilePath(configDir, gameID, profileName string) (string, error) {
	if err := validateProfilePath(gameID, profileName); err != nil {
		return "", err
	}
	return filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml"), nil
}

// MarkModsDisabled records #431's `disabled: true` marker on each reference
// in the profile file at path whose source_id/mod_id pair is in mods, and
// returns the references it newly marked, in file order. A reference that
// already carries the marker, and a mod the file does not list, are simply
// not returned; only the first reference to a mod is considered, the one
// every flow reads.
//
// Unlike SaveProfile it does not re-serialize the document: the marker is
// inserted into the file's own bytes, next to the reference it belongs to,
// so comments, key order, flow style, blank lines, indentation and an
// unexpanded `~/` hook path all stay exactly as the author wrote them. That
// is what makes it safe to run on a hand-edited file the user never asked
// lmm to rewrite. The edit is checked before anything is written - the new
// text must decode to the original document with only those markers set -
// and anything else is ErrProfileLayoutUnsupported with the file untouched.
//
// The write goes to path itself (through a symlink, to its target) by
// writing a temporary file beside it and renaming it into place, so a
// reader never sees a partial document and the file keeps its mode. A
// missing file is domain.ErrProfileNotFound.
func MarkModsDisabled(path string, mods []domain.ModReference) ([]domain.ModReference, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.ErrProfileNotFound
		}
		return nil, fmt.Errorf("reading profile: %w", err)
	}

	expected, edits, marked, err := planMarkers(path, data, mods)
	if err != nil || len(edits) == 0 {
		return nil, err
	}
	edited, err := applyEdits(data, edits)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrProfileLayoutUnsupported, path, err)
	}
	var after ProfileConfig
	if err := unmarshalYAML(edited, &after); err != nil || !reflect.DeepEqual(expected, after) {
		return nil, fmt.Errorf("%w: %s: the edited text would not read back as the same profile", ErrProfileLayoutUnsupported, path)
	}

	if err := writeFileAtomic(path, edited); err != nil {
		return nil, err
	}
	return marked, nil
}

// planMarkers works out, without checking the result, the edits that mark
// mods in data (the file at path): the document they should decode to, the
// edits, and the references they mark, in file order.
func planMarkers(path string, data []byte, mods []domain.ModReference) (ProfileConfig, []textEdit, []domain.ModReference, error) {
	var before ProfileConfig
	if err := unmarshalYAML(data, &before); err != nil {
		return ProfileConfig{}, nil, nil, fmt.Errorf("parsing profile: %w", err)
	}
	var doc yaml.Node
	if err := unmarshalYAML(data, &doc); err != nil {
		return ProfileConfig{}, nil, nil, fmt.Errorf("parsing profile: %w", err)
	}
	if len(before.Mods) == 0 {
		return before, nil, nil, nil
	}
	seq := modsSequence(&doc)
	if seq == nil || len(seq.Content) != len(before.Mods) {
		return ProfileConfig{}, nil, nil, fmt.Errorf("%w: %s: its mods list is not a plain sequence", ErrProfileLayoutUnsupported, path)
	}

	wanted := make(map[string]bool, len(mods))
	for _, m := range mods {
		wanted[domain.ModKey(m.SourceID, m.ModID)] = true
	}

	src := newSourceText(data)
	var (
		edits  []textEdit
		marked []domain.ModReference
	)
	expected := before
	expected.Mods = slices.Clone(before.Mods)
	seen := make(map[string]bool, len(before.Mods))
	for i, ref := range before.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] {
			continue
		}
		seen[key] = true
		if !wanted[key] || ref.Disabled {
			continue
		}
		edit, err := markerEdit(src, seq.Content[i])
		if err != nil {
			return ProfileConfig{}, nil, nil, fmt.Errorf("%w: %s (%s): %v", ErrProfileLayoutUnsupported, path, key, err)
		}
		edits = append(edits, edit)
		expected.Mods[i].Disabled = true
		marked = append(marked, domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID})
	}
	return expected, edits, marked, nil
}

// modsSequence returns the value node of the document's top-level `mods`
// key when it is a sequence, or nil.
func modsSequence(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == "mods" && root.Content[i+1].Kind == yaml.SequenceNode {
			return root.Content[i+1]
		}
	}
	return nil
}

// textEdit replaces length bytes at offset with text (length 0 inserts).
type textEdit struct {
	offset int
	length int
	text   string
}

// applyEdits applies edits back to front, so an earlier edit's offset is
// never moved by a later one. Edits that overlap, or reach outside data,
// are refused rather than applied.
func applyEdits(data []byte, edits []textEdit) ([]byte, error) {
	sorted := slices.Clone(edits)
	slices.SortFunc(sorted, func(a, b textEdit) int { return b.offset - a.offset })
	limit := len(data)
	for _, e := range sorted {
		if e.offset < 0 || e.length < 0 || e.offset+e.length > limit {
			return nil, fmt.Errorf("an edit at byte %d overlaps another or runs past the end", e.offset)
		}
		limit = e.offset
	}
	out := slices.Clone(data)
	for _, e := range sorted {
		out = slices.Concat(out[:e.offset], []byte(e.text), out[e.offset+e.length:])
	}
	return out, nil
}

// markerEdit is the one edit that sets `disabled: true` on item, a sequence
// entry of the mods list.
func markerEdit(src sourceText, item *yaml.Node) (textEdit, error) {
	if item.Kind != yaml.MappingNode {
		return textEdit{}, fmt.Errorf("the reference is not a plain mapping")
	}

	for i := 0; i+1 < len(item.Content); i += 2 {
		if key := item.Content[i]; key.Kind != yaml.ScalarNode || key.Value != "disabled" {
			continue
		}
		// An explicit `disabled: false`: its own text becomes `true`.
		value := item.Content[i+1]
		if value.Kind != yaml.ScalarNode || value.Style != 0 {
			return textEdit{}, fmt.Errorf("its disabled value is not a plain scalar")
		}
		if value.Value == "" {
			return textEdit{}, fmt.Errorf("its disabled value is empty")
		}
		offset, ok := src.offset(value.Line, value.Column)
		if !ok || !bytes.HasPrefix(src.data[offset:], []byte(value.Value)) {
			return textEdit{}, fmt.Errorf("its disabled value could not be located")
		}
		return textEdit{offset: offset, length: len(value.Value), text: "true"}, nil
	}

	if item.Style&yaml.FlowStyle != 0 {
		start, ok := src.offset(item.Line, item.Column)
		if !ok {
			return textEdit{}, fmt.Errorf("the reference could not be located")
		}
		end, ok := src.matchingClose(start)
		if !ok {
			return textEdit{}, fmt.Errorf("the reference's closing brace could not be located")
		}
		// After the last thing written inside the braces, so the marker
		// sits where the author's own next entry would, and whatever
		// spacing they left before `}` stays put.
		last := src.lastNonSpace(start, end)
		text := ", disabled: true"
		if src.data[last] == ',' {
			text = " disabled: true"
		}
		return textEdit{offset: last + 1, text: text}, nil
	}

	if len(item.Content) == 0 {
		return textEdit{}, fmt.Errorf("the reference is empty")
	}
	indent := item.Content[0].Column - 1
	endLine, ok := src.lastLine(item)
	if !ok {
		return textEdit{}, fmt.Errorf("the reference's last line could not be located")
	}
	offset, lineBreak, ok := src.lineEnd(endLine)
	if !ok {
		return textEdit{}, fmt.Errorf("the reference's last line does not end in a line break a marker line can repeat")
	}
	return textEdit{offset: offset, text: lineBreak + strings.Repeat(" ", indent) + "disabled: true"}, nil
}

// utf8BOM is the byte-order mark yaml.v3 drops from the start of a
// document before it counts a single line or column.
var utf8BOM = []byte("\xef\xbb\xbf")

// breakLen returns the length of the line break data[i:] starts with, or 0.
// It counts what yaml.v3 counts (libyaml, YAML 1.1): CRLF as one break, and
// a lone CR, LF, NEL (U+0085), LS (U+2028) and PS (U+2029) each as one. A
// line numbering that disagrees with yaml's by even one of these puts every
// later mark on the wrong line.
func breakLen(data []byte, i int) int {
	switch {
	case i >= len(data):
		return 0
	case data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n':
		return 2
	case data[i] == '\r' || data[i] == '\n':
		return 1
	case data[i] == 0xc2 && i+1 < len(data) && data[i+1] == 0x85:
		return 2
	case data[i] == 0xe2 && i+2 < len(data) && data[i+1] == 0x80 && (data[i+2] == 0xa8 || data[i+2] == 0xa9):
		return 3
	}
	return 0
}

// breakLenBefore returns the length of the line break data[:i] ends with,
// or 0.
func breakLenBefore(data []byte, i int) int {
	for _, n := range []int{3, 2, 1} {
		if i-n >= 0 && breakLen(data, i-n) == n {
			return n
		}
	}
	return 0
}

// sourceLine is one line of a document as yaml.v3 counts them: its text
// runs from start to end, and eol is the line break after it ("" for a
// last line that has none).
type sourceLine struct {
	start, end int
	eol        string
}

// sourceText is a document's bytes with its lines indexed, for turning
// yaml.v3's 1-based (line, column) marks - columns count characters, not
// bytes - into byte offsets. Every accessor is bounds-checked: a mark this
// cannot place is an answer of false, never an index panic.
type sourceText struct {
	data  []byte
	lines []sourceLine
}

func newSourceText(data []byte) sourceText {
	start := 0
	if bytes.HasPrefix(data, utf8BOM) {
		start = len(utf8BOM)
	}
	var lines []sourceLine
	for i := start; i < len(data); {
		n := breakLen(data, i)
		if n == 0 {
			// Byte by byte is safe: in valid UTF-8 (yaml.v3 accepts
			// nothing else) no continuation byte is a break's first byte.
			i++
			continue
		}
		lines = append(lines, sourceLine{start: start, end: i, eol: string(data[i : i+n])})
		i += n
		start = i
	}
	lines = append(lines, sourceLine{start: start, end: len(data)})
	return sourceText{data: data, lines: lines}
}

// offset returns the byte offset of 1-based line and column.
func (s sourceText) offset(line, column int) (int, bool) {
	if line < 1 || line > len(s.lines) || column < 1 {
		return 0, false
	}
	l := s.lines[line-1]
	offset := l.start
	for range column - 1 {
		if offset >= l.end {
			return 0, false
		}
		_, size := utf8.DecodeRune(s.data[offset:l.end])
		offset += size
	}
	return offset, true
}

// lineOf returns the 1-based line offset falls on, or 0.
func (s sourceText) lineOf(offset int) int {
	return sort.Search(len(s.lines), func(i int) bool { return s.lines[i].start > offset })
}

// lineEnd returns the offset just past line's last character - before its
// line break, if it has one - and the line break a line added after it
// should end with: its own, or, for a last line with none, the one before
// it. ok is false for a line yaml.v3 does not have, and for a NEL, LS or PS
// there: yaml.v3 reads those as line breaks, YAML 1.2 and most editors do
// not, so no marker line ending would read the same to every reader.
func (s sourceText) lineEnd(line int) (offset int, lineBreak string, ok bool) {
	if line < 1 || line > len(s.lines) {
		return 0, "", false
	}
	i := line - 1
	if s.lines[i].eol == "" {
		if i == 0 {
			return s.lines[i].end, "\n", true
		}
		i--
		if s.doubled(i - 1) {
			i--
		}
	}
	lineBreak = s.lines[i].eol
	if s.doubled(i) {
		lineBreak = "\r\r\n"
	}
	switch lineBreak {
	case "\n", "\r\n", "\r", "\r\r\n":
		return s.lines[line-1].end, lineBreak, true
	}
	return 0, "", false
}

// doubled reports whether line index i ends in a lone CR followed by an
// empty line ending in CRLF: `\r\r\n`, a CRLF file converted to CRLF a
// second time, which is how such a file's author sees one line break.
func (s sourceText) doubled(i int) bool {
	if i < 0 || i+1 >= len(s.lines) {
		return false
	}
	next := s.lines[i+1]
	return s.lines[i].eol == "\r" && next.start == next.end && next.eol == "\r\n"
}

// lastLine is the last line node's text occupies: the deepest, latest
// descendant's line, or a flow collection's closing bracket's. A block
// scalar's text runs past its own mark, so one there is refused rather than
// guessed at.
func (s sourceText) lastLine(node *yaml.Node) (int, bool) {
	switch {
	case node.Kind == yaml.AliasNode:
		return node.Line, true
	case node.Kind == yaml.ScalarNode:
		if node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return 0, false
		}
		return node.Line, true
	case node.Style&yaml.FlowStyle != 0:
		start, ok := s.offset(node.Line, node.Column)
		if !ok {
			return 0, false
		}
		end, ok := s.matchingClose(start)
		if !ok {
			return 0, false
		}
		line := s.lineOf(end)
		return line, line > 0
	}
	last := node.Line
	for _, child := range node.Content {
		line, ok := s.lastLine(child)
		if !ok {
			return 0, false
		}
		last = max(last, line)
	}
	return last, true
}

// matchingClose returns the offset of the bracket closing the flow
// collection that opens at start, skipping quoted text and comments. A quote
// only opens a quoted scalar where a scalar can begin - right after an
// indicator - so an apostrophe inside a plain word is just a character.
func (s sourceText) matchingClose(start int) (int, bool) {
	if start < 0 || start >= len(s.data) || (s.data[start] != '{' && s.data[start] != '[') {
		return 0, false
	}
	depth := 0
	var prev byte // the last significant character before i
	for i := start; i < len(s.data); {
		if n := breakLen(s.data, i); n > 0 {
			i += n
			continue
		}
		c := s.data[i]
		switch {
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return i, true
			}
		case (c == '"' || c == '\'') && scalarMayStart(prev):
			end, ok := quotedEnd(s.data, i)
			if !ok {
				return 0, false
			}
			i = end
		case c == '#' && s.spaceBefore(i):
			for i < len(s.data) && breakLen(s.data, i) == 0 {
				i++
			}
			continue
		}
		if c != ' ' && c != '\t' {
			prev = c
		}
		i++
	}
	return 0, false
}

// spaceBefore reports whether the character before offset i is white space
// or a line break - what makes a `#` there start a comment.
func (s sourceText) spaceBefore(i int) bool {
	return i > 0 && (s.data[i-1] == ' ' || s.data[i-1] == '\t' || breakLenBefore(s.data, i) > 0)
}

// lastNonSpace returns the offset of the last byte before end, and after
// start, that is neither white space nor part of a line break; start when
// there is none.
func (s sourceText) lastNonSpace(start, end int) int {
	i := end
	for i-1 > start {
		switch {
		case s.data[i-1] == ' ' || s.data[i-1] == '\t':
			i--
		case breakLenBefore(s.data, i) > 0:
			i -= breakLenBefore(s.data, i)
		default:
			return i - 1
		}
	}
	return start
}

func scalarMayStart(prev byte) bool {
	switch prev {
	case '{', '[', ',', ':', '?', '-':
		return true
	}
	return false
}

// quotedEnd returns the offset of the quote closing the scalar that opens at
// start.
func quotedEnd(data []byte, start int) (int, bool) {
	quote := data[start]
	for i := start + 1; i < len(data); i++ {
		switch {
		case quote == '"' && data[i] == '\\':
			i++
		case data[i] == quote && quote == '\'' && i+1 < len(data) && data[i+1] == '\'':
			i++
		case data[i] == quote:
			return i, true
		}
	}
	return 0, false
}

// writeFileAtomic replaces path's contents with data by renaming a sibling
// temporary file over it, so a reader never sees half a document. A symlink
// is followed, so the link itself - a dotfile manager's, typically - survives
// and its target is what changes; the temporary name does not end in .yaml,
// so ListProfiles never sees it.
//
// A file the user cannot write is refused (os.ErrPermission), just as every
// other lmm write refuses it: a rename only needs the directory to be
// writable, so without the check a read-only profile would be replaced
// anyway (fix round 3, F4).
//
// A file with more than one hard link is the exception: a rename would give
// this name a new inode and silently fork it from its other names (a
// dotfile manager that hard-links, rather than symlinks, into place), so it
// is rewritten in place instead, trading atomicity for keeping the link.
func writeFileAtomic(path string, data []byte) error {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolving profile path: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("reading profile file mode: %w", err)
	}
	if err := syscall.Access(target, accessWrite); err != nil {
		return fmt.Errorf("writing profile: %w", &os.PathError{Op: "access", Path: target, Err: err})
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return fmt.Errorf("writing profile: %w", err)
		}
		return nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	renamed := false
	defer func() {
		// Whatever stopped the write short - an error, or a panic on its
		// way to the backfill's recover - leaves no temporary file behind.
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	renamed = true
	return nil
}

// accessWrite is access(2)'s W_OK.
const accessWrite = 0x2
