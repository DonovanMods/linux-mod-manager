package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/stretchr/testify/assert"
)

func TestParsePluginList_Asterisk(t *testing.T) {
	f := parsePluginList("# This file is used by Skyrim\r\n*A.esp\r\nB.esp\r\n\r\n# later comment\r\n* C.esp \r\n*a.esp\r\n", true)
	assert.Equal(t, []listEntry{{"A.esp", true}, {"B.esp", false}, {"C.esp", true}}, f.entries)
	assert.Equal(t, []string{"# This file is used by Skyrim"}, f.header)
	assert.Equal(t, "\r\n", f.eol)
}

func TestParsePluginList_TextfileEveryLineActive(t *testing.T) {
	f := parsePluginList("A.esp\nB.esp\n", false)
	assert.Equal(t, []listEntry{{"A.esp", true}, {"B.esp", true}}, f.entries)
	assert.Equal(t, "\n", f.eol)
}

func TestLineEndingOf(t *testing.T) {
	assert.Equal(t, "\r\n", lineEndingOf(nil), "a new file is CRLF")
	assert.Equal(t, "\r\n", lineEndingOf([]byte("*A.esp")), "no break at all is CRLF")
	assert.Equal(t, "\n", lineEndingOf([]byte("*A.esp\n*B.esp\r\n")), "the first line decides")
	assert.Equal(t, "\r\n", lineEndingOf([]byte("*A.esp\r\n")))
}

func TestWindows1252RoundTrip(t *testing.T) {
	for _, s := range []string{"plain.esp", "Café.esp", "€uro – “quoted”.esp", "\u0081odd"} {
		b, ok := encodeWindows1252(s)
		assert.True(t, ok, s)
		assert.Equal(t, s, decodeWindows1252(b))
	}
	_, ok := encodeWindows1252("日本.esp")
	assert.False(t, ok)
}

func TestListableName(t *testing.T) {
	assert.True(t, listableName("Café.esp"))
	assert.False(t, listableName("日本.esp"), "no Windows-1252 byte")
	assert.False(t, listableName("evil\n*other.esp"), "a line break would add a line")
	assert.False(t, listableName("tab\t.esp"))
}

func TestDecodeLoadOrderTxt(t *testing.T) {
	assert.Equal(t, "Café.esp\n", decodeLoadOrderTxt([]byte("\xef\xbb\xbfCaf\xc3\xa9.esp\n")))
	assert.Equal(t, "Café.esp\n", decodeLoadOrderTxt([]byte("Caf\xe9.esp\n")), "not UTF-8: read as Windows-1252")
}

func TestRenderPluginList(t *testing.T) {
	entries := []listEntry{{"A.esp", true}, {"Café.esp", false}}
	got := renderPluginList(adapter.PluginSpec{ListKind: adapter.ListAsterisk}, entries, []string{"Skyrim.esm"}, []string{"# head"}, "\r\n", "")
	assert.Equal(t, "# head\r\n*A.esp\r\nCaf\xe9.esp\r\n", string(got.list))
	assert.Nil(t, got.order)

	got = renderPluginList(adapter.PluginSpec{ListKind: adapter.ListTextfile}, entries, []string{"Skyrim.esm"}, nil, "\n", "\r\n")
	assert.Equal(t, "A.esp\n", string(got.list))
	assert.Equal(t, "Skyrim.esm\r\nA.esp\r\nCafé.esp\r\n", string(got.order), "loadorder.txt is UTF-8 and lists every plugin")
}
