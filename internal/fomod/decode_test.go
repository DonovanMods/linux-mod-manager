package fomod

import (
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// utf16Bytes encodes s as UTF-16 in the given byte order, with or without
// a byte-order mark.
func utf16Bytes(s string, bigEndian, bom bool) []byte {
	units := utf16.Encode([]rune(s))
	if bom {
		units = append([]uint16{0xFEFF}, units...)
	}
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		if bigEndian {
			out = append(out, byte(u>>8), byte(u))
		} else {
			out = append(out, byte(u), byte(u>>8))
		}
	}
	return out
}

func TestDecodeText(t *testing.T) {
	const doc = `<config><moduleName>Café “Quoted” €</moduleName></config>`
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"utf-8 without BOM", []byte(doc), doc},
		{"utf-8 with BOM", append([]byte{0xEF, 0xBB, 0xBF}, doc...), doc},
		{"utf-16 LE with BOM", utf16Bytes(doc, false, true), doc},
		{"utf-16 BE with BOM", utf16Bytes(doc, true, true), doc},
		{"utf-16 LE without BOM", utf16Bytes(doc, false, false), doc},
		{"utf-16 BE without BOM", utf16Bytes(doc, true, false), doc},
		{
			// 0xE9 é, 0x93/0x94 curly quotes, 0x80 euro: the 0x80-0x9F range
			// is where Windows-1252 and Latin-1 differ.
			"windows-1252",
			[]byte("<config><moduleName>Caf\xE9 \x93Quoted\x94 \x80</moduleName></config>"),
			doc,
		},
		{
			// An undefined Windows-1252 byte maps to its C1 control, as WHATWG does.
			"windows-1252 undefined byte",
			[]byte("a\x81b\xFF"),
			"a\u0081bÿ",
		},
		{
			"utf-16 LE with a stray odd byte",
			append(utf16Bytes("<a/>", false, true), 0x00),
			"<a/>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeText(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeText_SizeCap(t *testing.T) {
	_, err := decodeText(make([]byte, MaxConfigSize+1))
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestStripDeclaration(t *testing.T) {
	tests := []struct{ in, want string }{
		{`<?xml version="1.0" encoding="UTF-16"?><config/>`, `<config/>`},
		{"  \r\n<?xml version='1.0' encoding='windows-1252' ?>\n<config/>", "\n<config/>"},
		{`<config/>`, `<config/>`},
		{`<?xml unterminated`, `<?xml unterminated`},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, stripDeclaration(tt.in))
	}
}
