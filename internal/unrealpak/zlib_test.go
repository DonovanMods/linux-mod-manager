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
	sizing := buildPrimaryIndex(count, seed, 0, 0, phiHash, 0, 0, fdiHash, encoded.Bytes())
	phiOffset := indexOffset + int64(len(sizing))
	fdiOffset := phiOffset + int64(phi.Len())
	index := buildPrimaryIndex(count, seed, phiOffset, int64(phi.Len()), phiHash,
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
