package core

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readFixture reads one testing-plugins fixture's header
// (testdata/plugins/README.md has their provenance).
func readFixture(t *testing.T, rel string) (PluginHeader, error) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "plugins", filepath.FromSlash(rel)))
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // read-only
	return ReadPluginHeader(f)
}

// The real plugins: their flags and masters, as xEdit and the Creation Kit
// wrote them.
func TestReadPluginHeader_Fixtures(t *testing.T) {
	for rel, want := range map[string]PluginHeader{
		"SkyrimSE/Blank.esm":                              {Flags: 0x1},
		"SkyrimSE/Blank.esp":                              {},
		"SkyrimSE/Blank.esl":                              {Flags: 0x200},
		"SkyrimSE/Blank - Master Dependent.esp":           {Masters: []string{"Blank.esm"}},
		"SkyrimSE/Blank - Different Master Dependent.esm": {Flags: 0x1, Masters: []string{"Blank - Different.esm"}},
		"Fallout4/Blank.esp":                              {Masters: []string{"Fallout4.esm"}},
		"Starfield/Blank.full.esm":                        {Flags: 0x1},
		"Starfield/Blank.medium.esm":                      {Flags: 0x401},
		"Starfield/Blank.small.esm":                       {Flags: 0x501},
		"Starfield/Blank - Override.esp":                  {Flags: 0x200, Masters: []string{"Blank.full.esm"}},
	} {
		t.Run(rel, func(t *testing.T) {
			got, err := readFixture(t, rel)
			require.NoError(t, err)
			assert.Equal(t, want.Flags, got.Flags)
			assert.Equal(t, want.Masters, got.Masters)
		})
	}
}

// Morrowind's TES3 format is not a TES4 header, and saying so beats
// misreading it.
func TestReadPluginHeader_RefusesTES3(t *testing.T) {
	_, err := readFixture(t, "Morrowind/Blank.esm")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPluginHeader))
	assert.Contains(t, err.Error(), "TES3")
}

// tes4 builds a synthetic TES4 header record: flags and raw subrecords.
func tes4(flags uint32, subs ...[]byte) []byte {
	var body []byte
	for _, s := range subs {
		body = append(body, s...)
	}
	var b bytes.Buffer
	b.WriteString("TES4")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(body)))
	_ = binary.Write(&b, binary.LittleEndian, flags)
	b.Write(make([]byte, 12)) // form id, version control, version + unknown
	b.Write(body)
	return b.Bytes()
}

// sub builds one subrecord with a u16 size.
func sub(typ string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString(typ)
	_ = binary.Write(&b, binary.LittleEndian, uint16(len(data)))
	b.Write(data)
	return b.Bytes()
}

// A master name is Windows-1252, the encoding the games use for every
// plugin name; it comes back as UTF-8.
func TestReadPluginHeader_Windows1252Master(t *testing.T) {
	raw := append([]byte("Caf\xe9 \x80\x9f\x81.esm"), 0)
	got, err := ReadPluginHeader(bytes.NewReader(tes4(0, sub("HEDR", make([]byte, 12)), sub("MAST", raw), sub("DATA", make([]byte, 8)))))
	require.NoError(t, err)
	assert.Equal(t, []string{"Café €Ÿ\u0081.esm"}, got.Masters)
}

// Several masters, in file order, each followed by its DATA subrecord.
func TestReadPluginHeader_MastersInOrder(t *testing.T) {
	data := tes4(0x1,
		sub("HEDR", make([]byte, 12)),
		sub("MAST", []byte("Skyrim.esm\x00")), sub("DATA", make([]byte, 8)),
		sub("MAST", []byte("Update.esm\x00")), sub("DATA", make([]byte, 8)),
		sub("MAST", []byte("No Terminator.esp")),
	)
	got, err := ReadPluginHeader(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, uint32(0x1), got.Flags)
	assert.Equal(t, []string{"Skyrim.esm", "Update.esm", "No Terminator.esp"}, got.Masters)
}

// The reader stops at the end of the header record: what follows (the
// plugin's real records) is never read.
func TestReadPluginHeader_StopsAtTheRecordEnd(t *testing.T) {
	data := append(tes4(0, sub("MAST", []byte("A.esm\x00"))), []byte("GRUP garbage that is never read")...)
	got, err := ReadPluginHeader(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, []string{"A.esm"}, got.Masters)
}

// Every malformed header is a typed refusal, never a panic and never a
// partial answer.
func TestReadPluginHeader_Malformed(t *testing.T) {
	good := tes4(0, sub("MAST", []byte("A.esm\x00")))
	oversize := func() []byte {
		var size [4]byte
		binary.LittleEndian.PutUint32(size[:], 1<<20)
		return tes4(0, sub("XXXX", size[:]), sub("ONAM", nil))
	}
	for name, data := range map[string][]byte{
		"empty":                       nil,
		"short record header":         []byte("TES4\x10\x00"),
		"wrong record type":           append([]byte("GRUP"), good[4:]...),
		"truncated subrecord header":  good[:len(good)-8],
		"truncated subrecord data":    good[:len(good)-2],
		"subrecord past the record":   tes4(0, []byte("MAST\xff\x00abc")),
		"compressed header":           tes4(0x00040000, sub("MAST", []byte("A.esm\x00"))),
		"XXXX with a short size":      tes4(0, sub("XXXX", []byte{1, 2})),
		"XXXX size past the record":   oversize(),
		"XXXX as the last subrecord":  tes4(0, sub("XXXX", []byte{4, 0, 0, 0})),
		"empty master name":           tes4(0, sub("MAST", []byte{0})),
		"master name past the cap":    tes4(0, sub("MAST", []byte(strings.Repeat("a", maxMasterName+1)))),
		"record size past the cap":    append([]byte("TES4\xff\xff\xff\x7f"), make([]byte, 16)...),
		"subrecord header is cut off": tes4(0, []byte("MAS")),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadPluginHeader(bytes.NewReader(data))
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrPluginHeader), "%v", err)
		})
	}
}

// ClassifyPlugin applies a game's PluginSpec to a header: the esplugin
// rules the spec's fields encode.
func TestClassifyPlugin(t *testing.T) {
	sse := adapter.PluginSpec{
		MasterFlag: 0x1, LightFlag: 0x200,
		ExtImpliesMaster: []string{".esm", ".esl"}, ExtImpliesLight: []string{".esl"},
	}
	starfield := adapter.PluginSpec{
		MasterFlag: 0x1, LightFlag: 0x100, MediumFlag: 0x400, UpdateFlag: 0x200, BlueprintFlag: 0x800,
		ExtImpliesMaster: []string{".esm", ".esl"}, ExtImpliesLight: []string{".esl"},
	}
	le := adapter.PluginSpec{MasterFlag: 0x1}

	for name, tt := range map[string]struct {
		spec  adapter.PluginSpec
		file  string
		flags uint32
		want  PluginClass
	}{
		"sse esp":                         {sse, "A.esp", 0, PluginClass{}},
		"sse esp with master flag":        {sse, "A.esp", 0x1, PluginClass{Master: true}},
		"sse esm by extension":            {sse, "A.esm", 0, PluginClass{Master: true}},
		"sse esl is a light master":       {sse, "A.ESL", 0, PluginClass{Master: true, Light: true}},
		"sse esp with light flag":         {sse, "A.esp", 0x200, PluginClass{Light: true}},
		"le esm without the flag":         {le, "A.esm", 0, PluginClass{}},
		"le light bit means nothing":      {le, "A.esp", 0x200, PluginClass{}},
		"starfield medium":                {starfield, "A.esm", 0x401, PluginClass{Master: true, Medium: true}},
		"starfield small beats medium":    {starfield, "A.esm", 0x501, PluginClass{Master: true, Light: true}},
		"starfield esl light":             {starfield, "A.esl", 0, PluginClass{Master: true, Light: true}},
		"starfield update stops esl":      {starfield, "A.esl", 0x200, PluginClass{Master: true}},
		"starfield update is not light":   {starfield, "A.esp", 0x200, PluginClass{}},
		"starfield blueprint":             {starfield, "A.esm", 0x801, PluginClass{Master: true, Blueprint: true}},
		"sse has no blueprint bit":        {sse, "A.esm", 0x800, PluginClass{Master: true}},
		"no extension still reads flags":  {sse, "A", 0x1, PluginClass{Master: true}},
		"starfield light flag on its own": {starfield, "A.esp", 0x100, PluginClass{Light: true}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyPlugin(tt.file, PluginHeader{Flags: tt.flags}, tt.spec))
		})
	}
}

// FuzzReadPluginHeader holds the reader to its contract on any input: it
// never panics, and an accepted header's masters are non-empty, NUL-free
// and within the cap.
func FuzzReadPluginHeader(f *testing.F) {
	entries, err := filepath.Glob(filepath.Join("testdata", "plugins", "*", "*.es[mpl]"))
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range entries {
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add(tes4(0x1, sub("MAST", []byte("A.esm\x00")), sub("DATA", make([]byte, 8))))
	f.Add([]byte("TES4"))
	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := ReadPluginHeader(bytes.NewReader(data))
		if err != nil {
			if !errors.Is(err, ErrPluginHeader) {
				t.Fatalf("untyped error: %v", err)
			}
			return
		}
		for _, m := range h.Masters {
			if m == "" || strings.ContainsRune(m, 0) || len(m) > 3*maxMasterName {
				t.Fatalf("bad master %q", m)
			}
		}
	})
}
