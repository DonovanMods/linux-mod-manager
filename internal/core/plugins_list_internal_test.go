package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/stretchr/testify/assert"
)

// skyrimSEHeader is the header Skyrim SE under Proton writes at the top of
// its own Plugins.txt, LF-terminated (observed on a real install, design
// §5 "Line endings").
const skyrimSEHeader = "# This file is used by Skyrim to keep track of your downloaded content.\n# Please do not modify this file.\n"

func TestParsePluginList_Asterisk(t *testing.T) {
	f := parsePluginList("# This file is used by Skyrim\r\n*A.esp\r\nB.esp\r\n\r\n# later comment\r\n* C.esp \r\n*a.esp\r\n", true)
	assert.Equal(t, []listEntry{{"A.esp", true}, {"B.esp", false}, {"C.esp", true}}, f.entries)
	assert.Equal(t, []string{"# This file is used by Skyrim"}, f.header())
	assert.Equal(t, []commentBlock{{after: "", lines: []string{"# This file is used by Skyrim"}}, {after: "b.esp", lines: []string{"# later comment"}}}, f.comments)
	assert.Equal(t, "\r\n", f.eol)
}

func TestParsePluginList_TextfileEveryLineActive(t *testing.T) {
	f := parsePluginList("A.esp\nB.esp\n", false)
	assert.Equal(t, []listEntry{{"A.esp", true}, {"B.esp", true}}, f.entries)
	assert.Equal(t, "\n", f.eol)
}

func TestLineEndingOf(t *testing.T) {
	assert.Equal(t, "\n", lineEndingOf(nil), "a new file is LF, as the game writes its own")
	assert.Equal(t, "\n", lineEndingOf([]byte("*A.esp")), "no break at all is LF")
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
	asterisk := adapter.PluginSpec{ListKind: adapter.ListAsterisk}
	got := renderPluginList(asterisk, entries, []string{"Skyrim.esm"}, parsePluginList("# head\r\n", true), pluginListFile{}, "")
	assert.Equal(t, "# head\r\n*A.esp\r\nCaf\xe9.esp\r\n", string(got.list))
	assert.Nil(t, got.order)

	got = renderPluginList(asterisk, entries, nil, pluginListFile{}, pluginListFile{}, "")
	assert.Equal(t, "*A.esp\nCaf\xe9.esp\n", string(got.list), "a new file: LF, and no header")

	got = renderPluginList(adapter.PluginSpec{ListKind: adapter.ListTextfile}, entries, []string{"Skyrim.esm"}, parsePluginList("x\n", false), pluginListFile{}, "")
	assert.Equal(t, "A.esp\n", string(got.list))
	assert.Equal(t, "Skyrim.esm\nA.esp\nCafé.esp\n", string(got.order), "loadorder.txt is UTF-8, lists every plugin, and follows the list's endings when new")
}

// The owner's ruling: a read-modify-write of the game's own file - exactly
// its header, then plugin lines - keeps the header at the top and the LF
// endings.
func TestRenderPluginList_KeepsTheGamesHeaderAndLF(t *testing.T) {
	current := parsePluginList(skyrimSEHeader+"*A.esp\n*B.esp\n", true)
	got := renderPluginList(adapter.PluginSpec{ListKind: adapter.ListAsterisk},
		[]listEntry{{"B.esp", true}, {"A.esp", true}, {"C.esp", false}}, nil, current, pluginListFile{}, "")
	assert.Equal(t, skyrimSEHeader+"*B.esp\n*A.esp\nC.esp\n", string(got.list))
}

// A comment further down stays after the plugin it followed, moving with
// it; one whose plugin is no longer written stays after the nearest earlier
// plugin that is, or at the top.
func TestRenderPluginList_KeepsCommentsWhereTheyWere(t *testing.T) {
	current := parsePluginList("# top\n*A.esp\n# about A\n*Gone.esp\n# after gone\n*B.esp\n# end\n", true)
	got := renderPluginList(adapter.PluginSpec{ListKind: adapter.ListAsterisk},
		[]listEntry{{"B.esp", true}, {"A.esp", true}}, nil, current, pluginListFile{}, "")
	assert.Equal(t, "# top\n*B.esp\n# end\n*A.esp\n# about A\n# after gone\n", string(got.list))

	got = renderPluginList(adapter.PluginSpec{ListKind: adapter.ListAsterisk},
		[]listEntry{{"B.esp", true}}, nil, current, pluginListFile{}, "")
	assert.Equal(t, "# top\n# about A\n# after gone\n*B.esp\n# end\n", string(got.list), "A gone: its comments go to the top")
}
