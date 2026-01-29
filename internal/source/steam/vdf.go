package steam

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// VDFMap is a parsed VDF key-value structure (nested maps and string values).
type VDFMap map[string]interface{}

// ParseVDF reads Valve Key-Value format from r and returns the root map.
func ParseVDF(r io.Reader) (VDFMap, error) {
	scanner := bufio.NewScanner(r)
	scanner.Split(scanVDFTokens)
	var tokens []string
	for scanner.Scan() {
		tokens = append(tokens, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading vdf: %w", err)
	}
	if len(tokens) == 0 {
		return VDFMap{}, nil
	}
	pos := 0
	root := make(VDFMap)
	key := tokens[pos]
	pos++
	if pos >= len(tokens) {
		return nil, fmt.Errorf("vdf: unexpected end after key %q", key)
	}
	if tokens[pos] == "{" {
		pos++
		inner, err := parseVDFObject(tokens, &pos)
		if err != nil {
			return nil, err
		}
		root[key] = inner
	}
	return root, nil
}

// parseVDFObject parses key-value pairs until "}", returns the map and advances pos past "}".
func parseVDFObject(tokens []string, pos *int) (VDFMap, error) {
	result := make(VDFMap)
	for *pos < len(tokens) && tokens[*pos] != "}" {
		key := tokens[*pos]
		*pos++
		if *pos >= len(tokens) {
			return nil, fmt.Errorf("vdf: unexpected end after key %q", key)
		}
		if tokens[*pos] == "{" {
			*pos++
			inner, err := parseVDFObject(tokens, pos)
			if err != nil {
				return nil, err
			}
			result[key] = inner
		} else {
			result[key] = tokens[*pos]
			*pos++
		}
	}
	if *pos < len(tokens) && tokens[*pos] == "}" {
		*pos++
	}
	return result, nil
}

// scanVDFTokens splits on quoted strings and single characters { }.
func scanVDFTokens(data []byte, atEOF bool) (advance int, token []byte, err error) {
	// Skip leading whitespace
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\t' || data[start] == '\n' || data[start] == '\r') {
		start++
	}
	if start >= len(data) {
		if atEOF {
			return start, nil, nil
		}
		return 0, nil, nil
	}
	data = data[start:]

	if data[0] == '"' {
		// Quoted string: find closing "
		for i := 1; i < len(data); i++ {
			if data[i] == '\\' && i+1 < len(data) {
				i++
				continue
			}
			if data[i] == '"' {
				return start + i + 1, data[1:i], nil
			}
		}
		if atEOF {
			return len(data) + start, nil, fmt.Errorf("vdf: unclosed quote")
		}
		return 0, nil, nil
	}
	if data[0] == '{' || data[0] == '}' {
		return start + 1, data[0:1], nil
	}
	// Skip to next whitespace or end
	i := 0
	for i < len(data) && !unicode.IsSpace(rune(data[i])) && data[i] != '"' {
		i++
	}
	if i > 0 {
		return start + i, data[:i], nil
	}
	if atEOF {
		return start + 1, data[0:1], nil
	}
	return 0, nil, nil
}

// getLibraryPaths extracts library paths from a parsed libraryfolders.vdf root.
// Expects structure: libraryfolders -> "0","1",... -> path
func getLibraryPaths(root VDFMap) []string {
	lf, ok := root["libraryfolders"].(VDFMap)
	if !ok {
		return nil
	}
	var paths []string
	for i := 0; ; i++ {
		key := fmt.Sprintf("%d", i)
		entry, ok := lf[key].(VDFMap)
		if !ok {
			break
		}
		if p, ok := entry["path"].(string); ok && p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// AppManifest holds parsed fields from an appmanifest_*.acf file.
type AppManifest struct {
	AppID      string
	Name       string
	InstallDir string
}

// ParseAppManifest parses appmanifest_*.acf content and returns AppManifest.
func ParseAppManifest(data string) (AppManifest, error) {
	root, err := ParseVDF(strings.NewReader(data))
	if err != nil {
		return AppManifest{}, err
	}
	state, ok := root["AppState"].(VDFMap)
	if !ok {
		return AppManifest{}, fmt.Errorf("vdf: missing AppState")
	}
	var m AppManifest
	if v, ok := state["appid"].(string); ok {
		m.AppID = v
	}
	if v, ok := state["name"].(string); ok {
		m.Name = v
	}
	if v, ok := state["installdir"].(string); ok {
		m.InstallDir = v
	}
	return m, nil
}
