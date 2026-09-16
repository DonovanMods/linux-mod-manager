package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

	var before ProfileConfig
	if err := yaml.Unmarshal(data, &before); err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}
	if len(before.Mods) == 0 {
		return nil, nil
	}
	seq := modsSequence(&doc)
	if seq == nil || len(seq.Content) != len(before.Mods) {
		return nil, fmt.Errorf("%w: %s: its mods list is not a plain sequence", ErrProfileLayoutUnsupported, path)
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
			return nil, fmt.Errorf("%w: %s (%s): %v", ErrProfileLayoutUnsupported, path, key, err)
		}
		edits = append(edits, edit)
		expected.Mods[i].Disabled = true
		marked = append(marked, domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID})
	}
	if len(edits) == 0 {
		return nil, nil
	}

	edited := applyEdits(data, edits)
	var after ProfileConfig
	if err := yaml.Unmarshal(edited, &after); err != nil || !reflect.DeepEqual(expected, after) {
		return nil, fmt.Errorf("%w: %s: the edited text would not read back as the same profile", ErrProfileLayoutUnsupported, path)
	}

	if err := writeFileAtomic(path, edited); err != nil {
		return nil, err
	}
	return marked, nil
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
		if root.Content[i].Value == "mods" && root.Content[i+1].Kind == yaml.SequenceNode {
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

// applyEdits applies non-overlapping edits back to front, so an earlier
// edit's offset is never moved by a later one.
func applyEdits(data []byte, edits []textEdit) []byte {
	sorted := slices.Clone(edits)
	slices.SortFunc(sorted, func(a, b textEdit) int { return b.offset - a.offset })
	out := slices.Clone(data)
	for _, e := range sorted {
		out = slices.Concat(out[:e.offset], []byte(e.text), out[e.offset+e.length:])
	}
	return out
}

// markerEdit is the one edit that sets `disabled: true` on item, a sequence
// entry of the mods list.
func markerEdit(src sourceText, item *yaml.Node) (textEdit, error) {
	if item.Kind != yaml.MappingNode {
		return textEdit{}, fmt.Errorf("the reference is not a plain mapping")
	}

	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value != "disabled" {
			continue
		}
		// An explicit `disabled: false`: its own text becomes `true`.
		value := item.Content[i+1]
		if value.Kind != yaml.ScalarNode || value.Style != 0 {
			return textEdit{}, fmt.Errorf("its disabled value is not a plain scalar")
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
		last := end - 1
		for last > start && isFlowSpace(src.data[last]) {
			last--
		}
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
	offset, eol := src.lineEnd(endLine)
	return textEdit{offset: offset, text: eol + string(bytes.Repeat([]byte{' '}, indent)) + "disabled: true"}, nil
}

func isFlowSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// sourceText is a document's bytes with its line starts indexed, for
// turning yaml.v3's 1-based (line, column) marks - columns count
// characters, not bytes - into byte offsets.
type sourceText struct {
	data       []byte
	lineStarts []int
}

func newSourceText(data []byte) sourceText {
	starts := []int{0}
	for i, b := range data {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return sourceText{data: data, lineStarts: starts}
}

// offset returns the byte offset of 1-based line and column.
func (s sourceText) offset(line, column int) (int, bool) {
	if line < 1 || line > len(s.lineStarts) || column < 1 {
		return 0, false
	}
	offset := s.lineStarts[line-1]
	for range column - 1 {
		if offset >= len(s.data) || s.data[offset] == '\n' {
			return 0, false
		}
		_, size := utf8.DecodeRune(s.data[offset:])
		offset += size
	}
	return offset, true
}

// lineEnd returns the offset just past line's last character - before its
// terminator, if it has one - and the terminator the file uses there.
func (s sourceText) lineEnd(line int) (int, string) {
	start := s.lineStarts[line-1]
	end := len(s.data)
	if line < len(s.lineStarts) {
		end = s.lineStarts[line] - 1
	}
	if end > start && s.data[end-1] == '\r' {
		return end - 1, "\r\n"
	}
	return end, "\n"
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
		line, _ := slices.BinarySearch(s.lineStarts, end+1)
		return line, true
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
	if start >= len(s.data) || (s.data[start] != '{' && s.data[start] != '[') {
		return 0, false
	}
	depth := 0
	var prev byte // the last significant character before i
	for i := start; i < len(s.data); i++ {
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
		case c == '#' && isFlowSpace(s.data[i-1]):
			for i+1 < len(s.data) && s.data[i+1] != '\n' {
				i++
			}
			continue
		}
		if !isFlowSpace(c) {
			prev = c
		}
	}
	return 0, false
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
// A file with more than one hard link is the exception: a rename would give
// this name a new inode and silently fork it from its other names (a
// dotfile manager that hard-links, rather than symlinks, into place), so it
// is rewritten in place instead, trading atomicity for keeping the link.
func writeFileAtomic(path string, data []byte) (err error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolving profile path: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("reading profile file mode: %w", err)
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
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err = tmp.Chmod(info.Mode().Perm()); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	return nil
}
