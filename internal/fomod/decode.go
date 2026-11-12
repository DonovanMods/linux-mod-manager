package fomod

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// cp1252High maps Windows-1252's 0x80-0x9F range, the only place it
// differs from Latin-1. Bytes Windows-1252 leaves undefined (0x81, 0x8D,
// 0x8F, 0x90, 0x9D) map to the C1 control of the same value, as the WHATWG
// encoding standard does.
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// decodeText turns a ModuleConfig.xml's bytes into text, sniffing the
// encoding from the bytes alone: a UTF-8 or UTF-16 byte-order mark first,
// then UTF-16 without one (a NUL in the first code unit), then UTF-8 if the
// bytes are valid UTF-8, and Windows-1252 otherwise. The XML declaration's
// encoding is never consulted: in real installers it is usually wrong.
func decodeText(data []byte) (string, error) {
	if len(data) > MaxConfigSize {
		return "", ErrTooLarge
	}
	switch {
	case len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF:
		return decodeUTF8OrCP1252(data[3:]), nil
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		return decodeUTF16(data[2:], false), nil
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		return decodeUTF16(data[2:], true), nil
	case len(data) >= 2 && data[0] != 0 && data[1] == 0:
		return decodeUTF16(data, false), nil
	case len(data) >= 2 && data[0] == 0 && data[1] != 0:
		return decodeUTF16(data, true), nil
	}
	return decodeUTF8OrCP1252(data), nil
}

func decodeUTF8OrCP1252(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	b.Grow(len(data) + len(data)/4)
	for _, c := range data {
		switch {
		case c >= 0x80 && c <= 0x9F:
			b.WriteRune(cp1252High[c-0x80])
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

// decodeUTF16 decodes UTF-16 in the given byte order. A trailing odd byte
// is dropped; unpaired surrogates become U+FFFD.
func decodeUTF16(data []byte, bigEndian bool) string {
	units := make([]uint16, len(data)/2)
	for i := range units {
		hi, lo := data[2*i], data[2*i+1]
		if !bigEndian {
			hi, lo = lo, hi
		}
		units[i] = uint16(hi)<<8 | uint16(lo)
	}
	return string(utf16.Decode(units))
}

// stripDeclaration removes a leading XML declaration (and the whitespace
// before it). encoding/xml would otherwise act on its encoding and version,
// both of which are routinely wrong in real installers.
func stripDeclaration(text string) string {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "<?xml") {
		return text
	}
	end := strings.Index(trimmed, "?>")
	if end < 0 {
		return text
	}
	return trimmed[end+2:]
}
