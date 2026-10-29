package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"go.yaml.in/yaml/v3"
)

// patchGames returns data - games.yaml as it was read - with game id's
// entry set to cfg, or removed for a nil cfg, and every other byte left as
// it was (#362). ok is false when there is nothing to keep (no game in the
// file) or the file's layout defeats both ways of editing it; the caller
// then writes the file whole.
//
// The first way edits the text: id's lines are replaced, removed, or - for a
// new game - added after the last game's, in the file's own indentation and
// line breaks, so every other game's lines stay byte-identical, comments and
// all. A layout that cannot be edited by whole lines (a `games: {...}` flow
// mapping, a block scalar in the entry being replaced) is edited as a
// yaml.Node document instead, which reformats the file but still keeps every
// other game's values as written - a "~/" path stays "~/".
//
// Whichever way produced it, a result is used only if it decodes to exactly
// the file it was made from with that one entry changed. The entry is
// rendered by marshalling GameConfig, so a field added to GameConfig needs
// nothing here.
func patchGames(data []byte, id string, cfg *GameConfig) (out []byte, ok bool) {
	var before GamesFile
	if err := safeyaml.Unmarshal(data, &before); err != nil || len(before.Games) == 0 {
		return nil, false
	}
	expected := maps.Clone(before.Games)
	delete(expected, id)
	if cfg != nil {
		block, err := renderGame(id, *cfg, 4)
		if err != nil {
			return nil, false
		}
		var rendered map[string]GameConfig
		if err := safeyaml.Unmarshal([]byte(block), &rendered); err != nil {
			return nil, false
		}
		expected[id] = rendered[id]
	}
	readsBack := func(out []byte) bool {
		var after GamesFile
		if err := safeyaml.Unmarshal(out, &after); err != nil {
			return false
		}
		return len(after.Games) == 0 && len(expected) == 0 || reflect.DeepEqual(after.Games, expected)
	}

	var doc yaml.Node
	if err := safeyaml.Unmarshal(data, &doc); err != nil {
		return nil, false
	}
	if out, err := spliceGame(data, &doc, id, cfg); err == nil && readsBack(out) {
		return out, true
	}
	if out, err := patchGameNode(&doc, id, cfg); err == nil && readsBack(out) {
		return out, true
	}
	return nil, false
}

// gamesMapping returns the value of the document's top-level `games:` key,
// or nil.
func gamesMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if key := root.Content[i]; key.Kind == yaml.ScalarNode && key.Value == "games" {
			return root.Content[i+1]
		}
	}
	return nil
}

// renderGame is id's entry in a games mapping, as `id:` followed by cfg's
// keys indented by indent, without a final line break.
func renderGame(id string, cfg GameConfig, indent int) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)
	if err := enc.Encode(map[string]GameConfig{id: cfg}); err != nil {
		return "", fmt.Errorf("marshaling game %q: %w", id, err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("marshaling game %q: %w", id, err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// errNoSplice is spliceGame declining a layout it cannot edit by lines.
var errNoSplice = errors.New("games.yaml cannot be edited line by line")

// spliceGame is patchGames' text edit: id's entry in the block mapping
// under `games:` is replaced, removed or added by whole lines.
func spliceGame(data []byte, doc *yaml.Node, id string, cfg *GameConfig) ([]byte, error) {
	games := gamesMapping(doc)
	if games == nil || games.Kind != yaml.MappingNode || games.Style&yaml.FlowStyle != 0 || len(games.Content) < 2 {
		return nil, errNoSplice
	}
	src := newSourceText(data)
	indent := games.Content[0].Column - 1
	step := indent
	for i := 0; i+1 < len(games.Content); i += 2 {
		if v := games.Content[i+1]; v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 {
			step = v.Content[0].Column - games.Content[i].Column
			break
		}
	}
	if step < 2 {
		step = 4
	}

	// lines returns entry i's first byte (its line's start) and last line.
	lines := func(i int) (start, last int, err error) {
		key, value := games.Content[i], games.Content[i+1]
		start, ok := src.offset(key.Line, 1)
		keyAt, ok2 := src.offset(key.Line, key.Column)
		if !ok || !ok2 || key.Column != indent+1 || len(bytes.TrimLeft(data[start:keyAt], " ")) != 0 {
			return 0, 0, errNoSplice
		}
		last = key.Line
		if !isEmptyScalar(value) {
			valueLast, ok := src.lastLine(value)
			if !ok {
				return 0, 0, errNoSplice
			}
			last = max(last, valueLast)
		}
		return start, last, nil
	}

	render := func(lineBreak string) (string, error) {
		block, err := renderGame(id, *cfg, step)
		if err != nil {
			return "", err
		}
		rows := strings.Split(block, "\n")
		for i, row := range rows {
			if row != "" {
				rows[i] = strings.Repeat(" ", indent) + row
			}
		}
		return strings.Join(rows, lineBreak), nil
	}

	target := -1
	for i := 0; i+1 < len(games.Content); i += 2 {
		if key := games.Content[i]; key.Kind == yaml.ScalarNode && key.Value == id {
			target = i
			break
		}
	}

	if target < 0 {
		if cfg == nil {
			return nil, errNoSplice
		}
		_, last, err := lines(len(games.Content) - 2)
		if err != nil {
			return nil, err
		}
		end, lineBreak, ok := src.lineEnd(last)
		if !ok {
			return nil, errNoSplice
		}
		block, err := render(lineBreak)
		if err != nil {
			return nil, err
		}
		return applyEdits(data, []textEdit{{offset: end, text: lineBreak + block}})
	}

	start, last, err := lines(target)
	if err != nil {
		return nil, err
	}
	end, lineBreak, ok := src.lineEnd(last)
	if !ok {
		return nil, errNoSplice
	}
	if cfg != nil {
		block, err := render(lineBreak)
		if err != nil {
			return nil, err
		}
		return applyEdits(data, []textEdit{{offset: start, length: end - start, text: block}})
	}
	// Removed with its line break; the file's last line has none, so the
	// one before the entry goes instead.
	if eol := src.lines[last-1].eol; eol != "" {
		end += len(eol)
	} else {
		start -= breakLenBefore(data, start)
	}
	return applyEdits(data, []textEdit{{offset: start, length: end - start}})
}

// patchGameNode is patchGames' document edit: id's entry is replaced,
// removed or appended in the yaml.Node tree, which is then encoded whole.
func patchGameNode(doc *yaml.Node, id string, cfg *GameConfig) ([]byte, error) {
	games := gamesMapping(doc)
	if games == nil || games.Kind != yaml.MappingNode {
		return nil, errors.New("games.yaml has no games mapping to edit")
	}
	var key, value yaml.Node
	if cfg != nil {
		if err := key.Encode(id); err != nil {
			return nil, err
		}
		if err := value.Encode(cfg); err != nil {
			return nil, err
		}
	}
	found := false
	for i := 0; i+1 < len(games.Content); i += 2 {
		if k := games.Content[i]; k.Kind != yaml.ScalarNode || k.Value != id {
			continue
		}
		found = true
		if cfg == nil {
			games.Content = slices.Delete(games.Content, i, i+2)
		} else {
			games.Content[i+1] = &value
		}
		break
	}
	if !found && cfg != nil {
		games.Content = append(games.Content, &key, &value)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("marshaling games: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("marshaling games: %w", err)
	}
	return buf.Bytes(), nil
}
