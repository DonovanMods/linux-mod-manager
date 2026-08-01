package unrealpak

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zlibFixture describes one compressed entry to place in a synthetic pak.
type zlibFixture struct {
	path   string
	blocks [][]byte // each block's PLAINTEXT; each is deflated independently
	method int32    // 1-based index into the methods table below
	// declaredUncompressed, when non-zero, overrides the UncompressedSize
	// this fixture declares in both its on-disk header and its encoded
	// index record, independent of the real sum of block plaintext lengths
	// — for fixtures that need to lie about their size (decompression-bomb
	// and size-cap regression tests) without needing real gigabytes of
	// content. Zero means "use the real sum," which every genuine fixture
	// in this file has anyway (none declares a real zero-length entry).
	declaredUncompressed int64
}

// writeMethodPak hand-builds a version-11 pak whose footer declares methods and
// which holds one compressed entry per fixture.
//
// The Writer only ever emits stored entries, so a compressed fixture has to be
// assembled here. The layout mirrors what a real cooked pak contains: a
// per-entry header carrying the block table, then the deflated blocks, then the
// three index structures and the 221-byte footer.
func writeMethodPak(t *testing.T, methods []string, fixtures []zlibFixture) string {
	t.Helper()
	const seed uint64 = 0x0123456789ABCDEF

	var data bytes.Buffer
	var encoded bytes.Buffer
	locations := make(map[string]int32, len(fixtures))

	for _, fx := range fixtures {
		var payload bytes.Buffer
		type span struct{ start, end int64 }
		hdrSize := compressedHeaderSize(len(fx.blocks))
		var spans []span
		var uncompressed int64
		for _, plain := range fx.blocks {
			var zbuf bytes.Buffer
			zw := zlib.NewWriter(&zbuf)
			if _, err := zw.Write(plain); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			start := hdrSize + int64(payload.Len())
			payload.Write(zbuf.Bytes())
			spans = append(spans, span{start, hdrSize + int64(payload.Len())})
			uncompressed += int64(len(plain))
		}
		if fx.declaredUncompressed != 0 {
			uncompressed = fx.declaredUncompressed
		}
		size := int64(payload.Len())
		sum := sha1.Sum(payload.Bytes()) //nolint:gosec

		entryOffset := int64(data.Len())
		var hdr bytes.Buffer
		binary.Write(&hdr, binary.LittleEndian, int64(0))     //nolint:errcheck // Offset
		binary.Write(&hdr, binary.LittleEndian, size)         //nolint:errcheck
		binary.Write(&hdr, binary.LittleEndian, uncompressed) //nolint:errcheck
		binary.Write(&hdr, binary.LittleEndian, fx.method)    //nolint:errcheck
		hdr.Write(sum[:])
		binary.Write(&hdr, binary.LittleEndian, int32(len(fx.blocks))) //nolint:errcheck
		for _, sp := range spans {
			binary.Write(&hdr, binary.LittleEndian, sp.start) //nolint:errcheck
			binary.Write(&hdr, binary.LittleEndian, sp.end)   //nolint:errcheck
		}
		hdr.WriteByte(0)                                       // Flags
		binary.Write(&hdr, binary.LittleEndian, uint32(65536)) //nolint:errcheck // CompressionBlockSize
		if int64(hdr.Len()) != hdrSize {
			t.Fatalf("fixture header is %d bytes, compressedHeaderSize says %d", hdr.Len(), hdrSize)
		}
		data.Write(hdr.Bytes())
		data.Write(payload.Bytes())

		locations[fx.path] = int32(encoded.Len())
		flags := uint32(1<<31) | uint32(1<<30) | uint32(1<<29) |
			uint32(fx.method)<<23 | uint32(len(fx.blocks))<<6 | uint32(65536>>11)
		binary.Write(&encoded, binary.LittleEndian, flags)                //nolint:errcheck
		binary.Write(&encoded, binary.LittleEndian, uint32(entryOffset))  //nolint:errcheck
		binary.Write(&encoded, binary.LittleEndian, uint32(uncompressed)) //nolint:errcheck
		binary.Write(&encoded, binary.LittleEndian, uint32(size))         //nolint:errcheck
		if len(fx.blocks) > 1 {
			for _, sp := range spans {
				binary.Write(&encoded, binary.LittleEndian, uint32(sp.end-sp.start)) //nolint:errcheck
			}
		}
	}

	// Full directory index: one directory per fixture path.
	var fdi bytes.Buffer
	binary.Write(&fdi, binary.LittleEndian, int32(len(fixtures))) //nolint:errcheck
	for _, fx := range fixtures {
		dir, file := splitMountPath(fx.path)
		writeFString(&fdi, dir)
		binary.Write(&fdi, binary.LittleEndian, int32(1)) //nolint:errcheck
		writeFString(&fdi, file)
		binary.Write(&fdi, binary.LittleEndian, locations[fx.path]) //nolint:errcheck
	}
	// Path-hash index, then an empty pruned directory index.
	var phi bytes.Buffer
	binary.Write(&phi, binary.LittleEndian, int32(len(fixtures))) //nolint:errcheck
	for _, fx := range fixtures {
		binary.Write(&phi, binary.LittleEndian, hashPath(fx.path, seed)) //nolint:errcheck
		binary.Write(&phi, binary.LittleEndian, locations[fx.path])      //nolint:errcheck
	}
	binary.Write(&phi, binary.LittleEndian, int32(0)) //nolint:errcheck

	phiHash := sha1.Sum(phi.Bytes()) //nolint:gosec
	fdiHash := sha1.Sum(fdi.Bytes()) //nolint:gosec
	count := int32(len(fixtures))
	indexOffset := int64(data.Len())
	sizing := buildPrimaryIndex(defaultMountPoint, count, seed, 0, 0, phiHash, 0, 0, fdiHash, encoded.Bytes())
	phiOffset := indexOffset + int64(len(sizing))
	fdiOffset := phiOffset + int64(phi.Len())
	index := buildPrimaryIndex(defaultMountPoint, count, seed, phiOffset, int64(phi.Len()), phiHash,
		fdiOffset, int64(fdi.Len()), fdiHash, encoded.Bytes())
	indexHash := sha1.Sum(index) //nolint:gosec

	footer := buildFooter(writeVersion, indexOffset, int64(len(index)), indexHash)
	for i, name := range methods {
		copy(footer[compressionMethodsOffset+i*compressionMethodNameSize:], name)
	}

	var out bytes.Buffer
	out.Write(data.Bytes())
	out.Write(index)
	out.Write(phi.Bytes())
	out.Write(fdi.Bytes())
	out.Write(footer)

	p := filepath.Join(t.TempDir(), "compressed.pak")
	if err := os.WriteFile(p, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The method table is read from the pak's own footer: the SAME index means
// different things in different paks, so an index naming Oodle must be refused
// even though index 1 means Zlib elsewhere.
func TestReadFile_MethodIndexResolvedAgainstThisPaksTable(t *testing.T) {
	body := []byte("{}")
	p := writeMethodPak(t, []string{"Oodle", "Zlib"}, []zlibFixture{
		{path: "a/Oodled.json", blocks: [][]byte{body}, method: 1},
		{path: "b/Zlibbed.json", blocks: [][]byte{body}, method: 2},
	})

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	_, err = r.ReadFile("a/Oodled.json")
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Oodle entry: err = %v, want ErrUnsupportedFormat", err)
	}
	if !strings.Contains(err.Error(), "Oodle") {
		t.Errorf("Oodle refusal %q should name the method", err)
	}
	got, err := r.ReadFile("b/Zlibbed.json")
	if err != nil {
		t.Fatalf("Zlib entry at index 2: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("ReadFile = %q, want %q", got, body)
	}
}

func TestReadFile_ZlibSingleBlock(t *testing.T) {
	body := []byte("{\r\n    \"Rows\": [1,2,3]\r\n}")
	p := writeMethodPak(t, []string{"Zlib"}, []zlibFixture{
		{path: "Factions/D_Factions.json", blocks: [][]byte{body}, method: 1},
	})

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	got, err := r.ReadFile("Factions/D_Factions.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("ReadFile = %q, want %q", got, body)
	}
	if files := r.Files(); len(files) != 1 || files[0].Size != int64(len(body)) {
		t.Errorf("Files() = %+v, want one entry sized %d", files, len(body))
	}
}

// Multi-block reassembly is the case the block table exists for: the blocks
// must be concatenated in order.
func TestReadFile_ZlibMultiBlock(t *testing.T) {
	b1 := bytes.Repeat([]byte("alpha "), 400)
	b2 := bytes.Repeat([]byte("beta "), 400)
	b3 := []byte("tail")
	want := append(append(append([]byte{}, b1...), b2...), b3...)
	p := writeMethodPak(t, []string{"Zlib"}, []zlibFixture{
		{path: "Items/D_ItemsStatic.json", blocks: [][]byte{b1, b2, b3}, method: 1},
	})

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	got, err := r.ReadFile("Items/D_ItemsStatic.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ReadFile returned %d bytes, want %d (block reassembly)", len(got), len(want))
	}
}

// An index with no name in the table is unsupported, never silently stored.
func TestReadFile_UnnamedMethodIndex_IsUnsupported(t *testing.T) {
	p := writeMethodPak(t, []string{"Zlib"}, []zlibFixture{
		{path: "x/Y.json", blocks: [][]byte{[]byte("{}")}, method: 3},
	})

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	if _, err := r.ReadFile("x/Y.json"); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}

// A corrupted compressed payload must fail the entry's SHA1 gate.
func TestReadFile_ZlibCorruptPayload_FailsHashGate(t *testing.T) {
	p := writeMethodPak(t, []string{"Zlib"}, []zlibFixture{
		{path: "c/D.json", blocks: [][]byte{bytes.Repeat([]byte("x"), 200)}, method: 1},
	})
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the first entry's compressed payload (just past its
	// single-block header).
	raw[compressedHeaderSize(1)+2] ^= 0xFF
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	if _, err := r.ReadFile("c/D.json"); err == nil {
		t.Fatal("expected an error for a corrupted compressed payload, got nil")
	}
}

// A block that decompresses to more bytes than the entry's own declared
// UncompressedSize (a lying header, or an over-inflating compression bomb)
// must be caught mid-decompress, not silently truncated or allowed to
// over-allocate — #175 final review ledger item 1. The real block content
// is genuinely 1000 bytes; the fixture just declares (and the index/header
// therefore both agree on) a 5-byte size, which is what readZlib actually
// checks against as it reads.
func TestReadFile_ZlibBlockExceedsDeclaredSize_Errors(t *testing.T) {
	huge := bytes.Repeat([]byte("A"), 1000)
	p := writeMethodPak(t, []string{"Zlib"}, []zlibFixture{
		{path: "bomb/D.json", blocks: [][]byte{huge}, method: 1, declaredUncompressed: 5},
	})

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	_, err = r.ReadFile("bomb/D.json")
	if err == nil {
		t.Fatal("expected an error for a block decompressing past the declared uncompressed size, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds the declared uncompressed size") {
		t.Errorf("error %q should name the size mismatch", err)
	}
}

// An entry declaring an UncompressedSize over maxUncompressedEntrySize must
// be refused before any decompression is attempted — #175 final review
// ledger item 2. The real block content is tiny; only the declared size
// needs to be oversized; the cap check runs before the block loop ever
// touches the payload, so no gigabytes of real content are needed.
func TestReadFile_ZlibUncompressedSizeExceedsCap_IsUnsupported(t *testing.T) {
	body := []byte("tiny")
	p := writeMethodPak(t, []string{"Zlib"}, []zlibFixture{
		{path: "big/Huge.json", blocks: [][]byte{body}, method: 1, declaredUncompressed: maxUncompressedEntrySize + 1},
	})

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	_, err = r.ReadFile("big/Huge.json")
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
	if !strings.Contains(err.Error(), "exceeds the") {
		t.Errorf("error %q should name the cap", err)
	}
}

// A block whose (start, end) span falls outside the entry's own payload
// region must be refused, never sliced out of range — #175 final review
// ledger item 3. Corrupts the single block's "end" field (at header byte
// offset 60, per readZlib's hdr[60+i*16:68+i*16] for i=0) to 0, which is
// less than "start" (== hdrSize == compressedHeaderSize(1), unaffected by
// this edit), tripping the end < start bounds check — independent of the
// SHA1 hash gate, since only the header's block-span table is touched, not
// the actual compressed payload bytes the hash covers.
func TestReadFile_ZlibBlockSpanOutsidePayload_Errors(t *testing.T) {
	p := writeMethodPak(t, []string{"Zlib"}, []zlibFixture{
		{path: "c/D.json", blocks: [][]byte{[]byte("hello world")}, method: 1},
	})
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 60; i < 68; i++ {
		raw[i] = 0
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	_, err = r.ReadFile("c/D.json")
	if err == nil {
		t.Fatal("expected an error for a block span outside the entry's payload, got nil")
	}
	if !strings.Contains(err.Error(), "outside the entry's payload") {
		t.Errorf("error %q should name the bounds violation", err)
	}
}
