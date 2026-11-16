package larian

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"
)

const testUUID = "26922ba9-6018-5252-075d-7ff2ba6ed879"

func TestLZ4_RoundTripsThroughTheTestCompressor(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte("a"),
		[]byte("hello hello hello hello hello hello hello"),
		bytes.Repeat([]byte("abcdefgh"), 4000),
		[]byte(strings.Repeat("x", 300) + "tail-literals-not-matched"),
	}
	for _, in := range inputs {
		out := make([]byte, len(in))
		n, err := decodeLZ4Block(lz4(in), out)
		if err != nil {
			t.Fatalf("decode %q...: %v", truncate(in), err)
		}
		if n != len(in) || !bytes.Equal(out[:n], in) {
			t.Fatalf("round trip of %q... gave %q...", truncate(in), truncate(out[:n]))
		}
	}
}

func truncate(b []byte) []byte {
	if len(b) > 20 {
		return b[:20]
	}
	return b
}

func TestLZ4_RefusesMalformedBlocks(t *testing.T) {
	cases := map[string][]byte{
		"offset zero":           {0x14, 'a', 0x00, 0x00},
		"offset before start":   {0x10, 'a', 0x05, 0x00},
		"literals past input":   {0x50, 'a'},
		"length run past input": {0xF0, 0xFF},
		"offset past input":     {0x10, 'a', 0x01},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeLZ4Block(src, make([]byte, 64)); err == nil {
				t.Fatal("a malformed block decoded")
			}
		})
	}
	// Output larger than the room given is refused, never grown.
	if _, err := decodeLZ4Block(lz4(bytes.Repeat([]byte("z"), 100)), make([]byte, 10)); err == nil {
		t.Fatal("a block larger than its destination decoded")
	}
}

// Every BG3 package version (15: early access, 16: early access patch 4,
// 18: release) and every compression lmm decodes: the file list, and the
// members, read back exactly.
func TestOpenPak_ReadsEveryVersionAndCompression(t *testing.T) {
	for _, version := range []uint32{15, 16, 18} {
		for _, method := range []uint32{methodNone, methodZlib, methodLZ4} {
			payload := bytes.Repeat([]byte("Mod content, repeated. "), 50)
			pak := buildPak(t, pakOptions{version: version},
				testMember{name: "Public/Mod/a.txt", data: payload, method: method},
				testMember{name: "Mods/MyMod/meta.lsx", data: metaLSX("MyMod", "My Mod", testUUID, "36028797018963968"), method: method},
			)
			p, err := OpenPak(bytes.NewReader(pak), int64(len(pak)))
			if err != nil {
				t.Fatalf("v%d method %d: %v", version, method, err)
			}
			if p.Version != version || len(p.Files) != 2 || p.Files[0].Name != "Public/Mod/a.txt" {
				t.Fatalf("v%d method %d: got %+v", version, method, p)
			}
			got, err := p.ReadFile(p.Files[0], 1<<20)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("v%d method %d: read %d bytes, %v", version, method, len(got), err)
			}
		}
	}
}

func TestOpenPak_RefusesWhatIsNotABG3Package(t *testing.T) {
	good := buildPak(t, pakOptions{version: 18}, testMember{name: "a", data: []byte("x")})
	v13 := bytes.Clone(good)
	binary.LittleEndian.PutUint32(v13[4:], 13)
	listPastEnd := bytes.Clone(good)
	binary.LittleEndian.PutUint64(listPastEnd[8:], uint64(len(good)))
	hugeCount := bytes.Clone(good)
	listOff := binary.LittleEndian.Uint64(good[8:])
	binary.LittleEndian.PutUint32(hugeCount[listOff:], 0x7fffffff)
	negCount := bytes.Clone(good)
	binary.LittleEndian.PutUint32(negCount[listOff:], 0xffffffff)
	cases := map[string]struct {
		data []byte
		want error
	}{
		"empty":                {nil, ErrNotPak},
		"not LSPK":             {[]byte("PK\x03\x04 a zip file, not a pak at all"), ErrNotPak},
		"truncated header":     {good[:20], ErrNotPak},
		"DOS2 version":         {v13, ErrUnsupportedVersion},
		"list past the end":    {listPastEnd, ErrCorrupt},
		"file count too large": {hugeCount, ErrCorrupt},
		"negative file count":  {negCount, ErrCorrupt},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := OpenPak(bytes.NewReader(c.data), int64(len(c.data)))
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestReadFile_RefusesZstdSolidMultipartAndOversize(t *testing.T) {
	pak := buildPak(t, pakOptions{version: 18},
		testMember{name: "zstd", data: []byte("data"), method: methodZstd},
		testMember{name: "part", data: []byte("data"), part: 1},
		testMember{name: "big", data: bytes.Repeat([]byte("b"), 4096), method: methodLZ4},
	)
	p, err := OpenPak(bytes.NewReader(pak), int64(len(pak)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadFile(p.Files[0], 1<<20); !errors.Is(err, ErrZstd) {
		t.Fatalf("zstd: %v", err)
	}
	if _, err := p.ReadFile(p.Files[1], 1<<20); !errors.Is(err, ErrMultiPart) {
		t.Fatalf("part: %v", err)
	}
	if _, err := p.ReadFile(p.Files[2], 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}

	solid := buildPak(t, pakOptions{version: 18, flags: flagSolid}, testMember{name: "a", data: []byte("x"), method: methodLZ4})
	p, err = OpenPak(bytes.NewReader(solid), int64(len(solid)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ReadFile(p.Files[0], 1<<20); !errors.Is(err, ErrSolid) {
		t.Fatalf("solid: %v", err)
	}
}

func TestReadFile_RefusesAMemberOutsideThePak(t *testing.T) {
	pak := buildPak(t, pakOptions{version: 15}, testMember{name: "a", data: []byte("xyz")})
	p, err := OpenPak(bytes.NewReader(pak), int64(len(pak)))
	if err != nil {
		t.Fatal(err)
	}
	f := p.Files[0]
	f.Offset = uint64(len(pak)) - 1
	if _, err := p.ReadFile(f, 1<<20); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	f = p.Files[0]
	f.Offset = ^uint64(0) - 1 // offset + size overflows
	if _, err := p.ReadFile(f, 1<<20); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("overflow: %v", err)
	}
}

// A stored size that disagrees with what the member decodes to is corrupt,
// whichever codec stored it.
func TestReadFile_RefusesASizeMismatch(t *testing.T) {
	for _, method := range []uint32{methodZlib, methodLZ4} {
		pak := buildPak(t, pakOptions{version: 16}, testMember{name: "a", data: bytes.Repeat([]byte("q"), 200), method: method})
		p, err := OpenPak(bytes.NewReader(pak), int64(len(pak)))
		if err != nil {
			t.Fatal(err)
		}
		f := p.Files[0]
		f.Size = 150
		if _, err := p.ReadFile(f, 1<<20); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("method %d: got %v", method, err)
		}
	}
}

// An LZ4 block cannot expand more than ~255 times, so a file list that
// claims ~50 MiB of entries from a few compressed bytes is refused before
// lmm allocates room for it.
func TestOpenPak_RefusesAListLargerThanItsBlockCanHold(t *testing.T) {
	pak := buildPak(t, pakOptions{version: 18}, testMember{name: "a", data: []byte("x")})
	listOff := binary.LittleEndian.Uint64(pak[8:])
	binary.LittleEndian.PutUint32(pak[listOff:], 200_000) // 200,000 × 272 bytes: under the 64 MiB cap
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := OpenPak(bytes.NewReader(pak), int64(len(pak)))
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("allocated %d bytes for a list its block cannot hold", grew)
	}
}
