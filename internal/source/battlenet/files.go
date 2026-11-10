package battlenet

import (
	"bufio"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// uninstallEntry is the part of one Uninstall key lmm reads.
type uninstallEntry struct {
	DisplayName     string
	InstallLocation string
}

// uninstallKeyMarker is what every Uninstall key's name contains, under
// both the 64-bit view and Wow6432Node - as system.reg spells it, with its
// backslashes doubled.
const uninstallKeyMarker = `\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\`

// readUninstallEntries reads the Uninstall keys out of a prefix's
// system.reg, in file order. Only DisplayName and InstallLocation are
// kept, and every other key is skipped without being decoded: the file is
// Wine's plain-text registry, routinely several megabytes, and nothing else
// in it is lmm's business.
func readUninstallEntries(path string) ([]uninstallEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only

	var out []uninstallEntry
	var cur *uninstallEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "[") {
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			if strings.Contains(line, uninstallKeyMarker) {
				cur = &uninstallEntry{}
			}
			continue
		}
		if cur == nil {
			continue
		}
		name, value, ok := regStringValue(line)
		if !ok {
			continue
		}
		switch name {
		case "DisplayName":
			cur.DisplayName = value
		case "InstallLocation":
			cur.InstallLocation = value
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out, scanner.Err()
}

// regStringValue parses one `"Name"="value"` (or `"Name"=str(2):"value"`)
// line, undoing Wine's escapes. Any other kind of value is not a string
// this reader wants.
func regStringValue(line string) (name, value string, ok bool) {
	if !strings.HasPrefix(line, `"`) {
		return "", "", false
	}
	name, rest, ok := regQuoted(line[1:])
	if !ok || !strings.HasPrefix(rest, "=") {
		return "", "", false
	}
	rest = strings.TrimPrefix(rest[1:], "str(2):")
	if !strings.HasPrefix(rest, `"`) {
		return "", "", false
	}
	value, _, ok = regQuoted(rest[1:])
	return name, value, ok
}

// regQuoted decodes a quoted string whose opening quote is already
// consumed, returning it and what follows the closing quote. Wine writes
// \\, \", \n, \r, \t, \0 and \xHHHH escapes - UTF-16 code
// units, so a character outside the BMP is a surrogate pair of them.
func regQuoted(s string) (string, string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), s[i+1:], true
		case c != '\\' || i+1 == len(s):
			b.WriteByte(c)
		default:
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '0':
				b.WriteByte(0)
			case 'x':
				unit, next, ok := regHexUnit(s, i+1)
				if !ok {
					b.WriteByte('x')
					break
				}
				i = next - 1
				r := rune(unit)
				if utf16.IsSurrogate(r) {
					// A pair is two escapes in a row; anything else
					// leaves a lone half, which names no character.
					r = unicode.ReplacementChar
					if strings.HasPrefix(s[next:], `\x`) {
						if low, after, ok := regHexUnit(s, next+2); ok {
							if pair := utf16.DecodeRune(rune(unit), rune(low)); pair != unicode.ReplacementChar {
								r, i = pair, after-1
							}
						}
					}
				}
				b.WriteRune(r)
			default:
				b.WriteByte(s[i])
			}
		}
	}
	return "", "", false
}

// regHexUnit reads the up to four hex digits of a \x escape starting at
// s[start], returning the UTF-16 code unit and the index after its last
// digit; ok is false when there are none.
func regHexUnit(s string, start int) (unit uint16, next int, ok bool) {
	next = start
	for next < len(s) && next < start+4 && strings.ContainsRune("0123456789abcdefABCDEF", rune(s[next])) {
		next++
	}
	if next == start {
		return 0, start, false
	}
	n, err := strconv.ParseUint(s[start:next], 16, 16)
	if err != nil {
		return 0, start, false
	}
	return uint16(n), next, true
}

// readBuildInfoProducts reads the product codes a game root's .build.info
// names: the Product column of every active row, lower-cased, each once,
// in file order. The file is pipe-delimited; its first line is a header of
// "Name!TYPE:size" columns. A file with no Product column - an older
// client's - confirms nothing and yields none.
func readBuildInfoProducts(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	product, active := -1, -1
	for i, col := range strings.Split(lines[0], "|") {
		name, _, _ := strings.Cut(col, "!")
		switch strings.TrimSpace(name) {
		case "Product":
			product = i
		case "Active":
			active = i
		}
	}
	if product < 0 {
		return nil, nil
	}
	var out []string
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if product >= len(fields) {
			continue
		}
		if active >= 0 && active < len(fields) && strings.TrimSpace(fields[active]) == "0" {
			continue
		}
		code := strings.ToLower(strings.TrimSpace(fields[product]))
		if code != "" && !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	return out, nil
}
