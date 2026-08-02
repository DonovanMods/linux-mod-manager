package unrealpak

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSeed is an arbitrary PathHashSeed for fixture paks. Readers take the
// seed from the index, so any value works — real paks use a different one per
// chunk.
const fixtureSeed uint64 = 0x0123456789ABCDEF

// writeMinimalPak builds a hand-crafted but fully valid version-11 pak holding
// a single stored entry: data section, primary index, path-hash index, full
// directory index, then the 221-byte footer. It deliberately does not use the
// Task 4 Writer — the reader's tests must be able to fail independently of the
// writer, and vice versa.
func writeMinimalPak(t *testing.T, mountPath string, content []byte) string {
	t.Helper()
	return writeMinimalPakMethod(t, mountPath, content, 0)
}

// writeMinimalPakMethod builds a fixture whose entry claims CompressionMethodIndex
// method. Only method 0 produces a genuinely readable pak; non-zero values exist
// to exercise the reader's refusal path (Task 3), which is the case that matters
// in practice — 74% of real Icarus entries are Oodle-compressed.
func writeMinimalPakMethod(t *testing.T, mountPath string, content []byte, method int32) string {
	t.Helper()
	pakPath := filepath.Join(t.TempDir(), "test.pak")
	if err := os.WriteFile(pakPath, buildFixturePak(mountPath, content, method), 0o644); err != nil {
		t.Fatalf("writing test pak: %v", err)
	}
	return pakPath
}

func buildFixturePak(mountPath string, content []byte, method int32) []byte {
	rel := strings.TrimPrefix(mountPath, "/")

	// Data section: the 53-byte per-entry header, then the payload, at offset 0.
	var data bytes.Buffer
	hdr := storedEntryHeader(int64(len(content)), content)
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(method)) // CompressionMethodIndex
	data.Write(hdr)
	data.Write(content)

	// One encoded index record. For method 0 that is the 12-byte stored shape:
	// flags 0xE0000000 (offset/uncompressed-size/size all 32-bit-safe, no
	// blocks), then uint32 Offset and uint32 UncompressedSize — Size is not
	// serialized for method 0, it equals UncompressedSize. A non-zero method
	// adds the uint32 Size field, per the encoded-record layout.
	var encoded bytes.Buffer
	binary.Write(&encoded, binary.LittleEndian, uint32(0xE0000000)|uint32(method)<<23) //nolint:errcheck
	binary.Write(&encoded, binary.LittleEndian, uint32(0))                             //nolint:errcheck
	binary.Write(&encoded, binary.LittleEndian, uint32(len(content)))                  //nolint:errcheck
	if method != 0 {
		binary.Write(&encoded, binary.LittleEndian, uint32(len(content))) //nolint:errcheck // Size
	}

	// Full directory index: one directory, one file, pointing at blob offset 0.
	dirName, fileName := splitMountPath(rel)
	var fdi bytes.Buffer
	binary.Write(&fdi, binary.LittleEndian, int32(1)) //nolint:errcheck // DirCount
	writeFString(&fdi, dirName)
	binary.Write(&fdi, binary.LittleEndian, int32(1)) //nolint:errcheck // FileCount
	writeFString(&fdi, fileName)
	binary.Write(&fdi, binary.LittleEndian, int32(0)) //nolint:errcheck // PakEntryLocation

	// Path-hash index: the hash->location map, then an EMPTY pruned directory
	// index. 33 of the 34 paks in a real install ship it empty, so a bare
	// int32(0) is a shape the engine demonstrably accepts.
	var phi bytes.Buffer
	binary.Write(&phi, binary.LittleEndian, int32(1))                   //nolint:errcheck // Count
	binary.Write(&phi, binary.LittleEndian, hashPath(rel, fixtureSeed)) //nolint:errcheck
	binary.Write(&phi, binary.LittleEndian, int32(0))                   //nolint:errcheck // location
	binary.Write(&phi, binary.LittleEndian, int32(0))                   //nolint:errcheck // pruned index: 0 dirs

	phiHash := sha1.Sum(phi.Bytes()) //nolint:gosec
	fdiHash := sha1.Sum(fdi.Bytes()) //nolint:gosec

	indexOffset := int64(data.Len())
	sizing := buildPrimaryIndex(defaultMountPoint, 1, fixtureSeed, 0, 0, phiHash, 0, 0, fdiHash, encoded.Bytes())
	phiOffset := indexOffset + int64(len(sizing))
	fdiOffset := phiOffset + int64(phi.Len())
	index := buildPrimaryIndex(defaultMountPoint, 1, fixtureSeed,
		phiOffset, int64(phi.Len()), phiHash,
		fdiOffset, int64(fdi.Len()), fdiHash, encoded.Bytes())
	indexHash := sha1.Sum(index) //nolint:gosec

	var out bytes.Buffer
	out.Write(data.Bytes())
	out.Write(index)
	out.Write(phi.Bytes())
	out.Write(fdi.Bytes())
	out.Write(buildFooter(writeVersion, indexOffset, int64(len(index)), indexHash))
	return out.Bytes()
}

func TestReader_Open_ListsFiles(t *testing.T) {
	content := []byte(`{"hello":"world"}`)
	path := writeMinimalPak(t, "Icarus/Content/Data/Test.json", content)

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	files := r.Files()
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Path != "Icarus/Content/Data/Test.json" {
		t.Errorf("Path = %q, want Icarus/Content/Data/Test.json", files[0].Path)
	}
	if files[0].Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", files[0].Size, len(content))
	}
}

// IndexHash must be deterministic (same pak layout -> same hash) and must
// change when the pak's layout changes (#196: it's the staleness signal a
// base data.pak refresh is detected by). It must also never be all-zero -
// that would mean the footer's indexHash field was never actually captured.
//
// The primary index records each entry's path/offset/size, not its payload
// bytes - a same-length content edit that happens to leave every offset and
// size unchanged would NOT move IndexHash (this is the footer's own
// documented shape, not a gap in this method: see #196's design note that
// this is "cheap" specifically because it never reads payload data). B's
// content is a different LENGTH than A's so the size field actually differs,
// which is what a real pak rebuild's added/changed/removed rows always do.
func TestReader_IndexHash(t *testing.T) {
	pathA := writeMinimalPak(t, "Icarus/Content/Data/Test.json", []byte(`{"a":1}`))
	pathA2 := writeMinimalPak(t, "Icarus/Content/Data/Test.json", []byte(`{"a":1}`))
	pathB := writeMinimalPak(t, "Icarus/Content/Data/Test.json", []byte(`{"a":22222}`))

	rA, err := Open(pathA)
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	defer rA.Close() //nolint:errcheck
	rA2, err := Open(pathA2)
	if err != nil {
		t.Fatalf("Open A2: %v", err)
	}
	defer rA2.Close() //nolint:errcheck
	rB, err := Open(pathB)
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	defer rB.Close() //nolint:errcheck

	hashA := rA.IndexHash()
	hashA2 := rA2.IndexHash()
	hashB := rB.IndexHash()

	if len(hashA) != 40 {
		t.Fatalf("IndexHash length = %d, want 40 (20-byte SHA1 hex)", len(hashA))
	}
	if hashA == strings.Repeat("0", 40) {
		t.Fatal("IndexHash is all-zero - footer indexHash was never captured")
	}
	if hashA != hashA2 {
		t.Errorf("identical pak content produced different IndexHash: %q vs %q", hashA, hashA2)
	}
	if hashA == hashB {
		t.Errorf("different pak content produced the same IndexHash: %q", hashA)
	}
}

// A root-level file is keyed under the "/" directory in the directory index;
// Files must report it without the leading slash, matching what hashPath uses.
func TestReader_Open_RootLevelFile(t *testing.T) {
	path := writeMinimalPak(t, "x.json", []byte("{}"))
	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	files := r.Files()
	if len(files) != 1 || files[0].Path != "x.json" {
		t.Fatalf("Files() = %+v, want one entry with Path %q", files, "x.json")
	}
}

func TestReader_Open_RejectsEncryptedIndex(t *testing.T) {
	path := writeMinimalPak(t, "x.json", []byte("{}"))
	data, _ := os.ReadFile(path)
	// bEncryptedIndex sits at offset 16 from footer start — right after the
	// 16-byte EncryptionKeyGuid, immediately before Magic.
	data[len(data)-footerSize+16] = 1
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Open(path)
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Open error = %v, want ErrUnsupportedFormat", err)
	}
}

// Versions below 10 use a flat index this package deliberately does not parse:
// a hard error, never a fallback.
func TestReader_Open_RejectsPreVersion10(t *testing.T) {
	path := writeMinimalPak(t, "x.json", []byte("{}"))
	data, _ := os.ReadFile(path)
	// Version is the int32 at footer offset 21 (after Guid+flag+Magic).
	binary.LittleEndian.PutUint32(data[len(data)-footerSize+21:], uint32(9))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Open(path)
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Open error = %v, want ErrUnsupportedFormat", err)
	}
}

// Corruption anywhere in the index must trip a SHA1 gate rather than be
// parsed. The full directory index is the last region before the footer, so
// flipping the byte just before it exercises the primary->sub-index gate.
func TestReader_Open_RejectsCorruptedDirectoryIndex(t *testing.T) {
	path := writeMinimalPak(t, "x.json", []byte("{}"))
	data, _ := os.ReadFile(path)
	data[len(data)-footerSize-1] ^= 0xFF
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("expected error for corrupted directory index, got nil")
	}
}

func TestReader_ReadFile(t *testing.T) {
	content := []byte(`{"hello":"world"}`)
	path := writeMinimalPak(t, "Icarus/Content/Data/Test.json", content)

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	got, err := r.ReadFile("Icarus/Content/Data/Test.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("got %q, want %q", got, content)
	}

	if _, err := r.ReadFile("does/not/exist.json"); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestReader_ReadFile_RejectsCompressedEntry(t *testing.T) {
	const name = "Items/D_ItemsStatic.json"
	path := writeMinimalPakMethod(t, name, []byte(`{"a":1}`), 1) // 1 = Oodle

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close() //nolint:errcheck

	// Enumeration must still work — the reader lists compressed entries.
	if files := r.Files(); len(files) != 1 || files[0].Path != name {
		t.Fatalf("Files() = %+v, want one entry named %q", files, name)
	}
	if _, err := r.ReadFile(name); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("ReadFile error = %v, want ErrUnsupportedFormat", err)
	}
}

func TestValidateAllocSize(t *testing.T) {
	tests := []struct {
		name     string
		size     int64
		fileSize int64
		wantErr  bool
		want     int
	}{
		{"valid, well within file", 10, 1000, false, 10},
		{"valid, exactly file size", 1000, 1000, false, 1000},
		{"negative (e.g. a 64-bit field with its top bit set, cast to int64)", -1, 1000, true, 0},
		{"exceeds file size", 1001, 1000, true, 0},
		// math.MaxInt itself must still be accepted (inclusive boundary, not
		// an off-by-one) -- guards the int(size) cast that follows against
		// overflow on a 32-bit build, where int is narrower than int64. On a
		// 64-bit build (this test's normal target) math.MaxInt == MaxInt64,
		// so size can never actually exceed it: size is itself int64, and
		// there is no larger int64 value to construct a "just past the
		// boundary" case with. The check is a genuine no-op here and only
		// load-bearing on 32-bit -- see validateAllocSize's doc comment.
		{"exactly math.MaxInt is still valid (boundary, not exceeded)", math.MaxInt, math.MaxInt, false, math.MaxInt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateAllocSize(tt.size, tt.fileSize)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateAllocSize(%d, %d) = %d, nil; want error", tt.size, tt.fileSize, got)
				}
				if !errors.Is(err, ErrUnsupportedFormat) {
					t.Errorf("error = %v, want ErrUnsupportedFormat", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateAllocSize(%d, %d): %v", tt.size, tt.fileSize, err)
			}
			if got != tt.want {
				t.Errorf("validateAllocSize(%d, %d) = %d, want %d", tt.size, tt.fileSize, got, tt.want)
			}
		})
	}
}

// readRegion must reject an invalid size (or offset) before ever attempting
// to read — a corrupted size field must not drive an unbounded allocation or
// even a spurious I/O attempt. panicReaderAt fails the test if readRegion
// reaches the ReadAt call at all.
type readerAtFunc func([]byte, int64) (int, error)

func (f readerAtFunc) ReadAt(p []byte, off int64) (int, error) { return f(p, off) }

func TestReadRegion_RejectsInvalidSizeOrOffsetBeforeReading(t *testing.T) {
	panicReader := readerAtFunc(func(p []byte, off int64) (int, error) {
		panic("readRegion must not read when offset/size is invalid")
	})
	var want [20]byte

	if _, err := readRegion(panicReader, 0, -1, 1000, want); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("negative size: error = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := readRegion(panicReader, 0, 2000, 1000, want); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("oversized size: error = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := readRegion(panicReader, -1, 10, 1000, want); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("negative offset: error = %v, want ErrUnsupportedFormat", err)
	}
}

// ReadFile must reject a corrupted entry-size field before allocating the
// payload buffer, whether the corruption makes the field negative (a 64-bit
// UncompressedSize with its top bit set, cast to int64) or merely larger
// than the file that supposedly contains it. Constructed directly against a
// Reader/readerEntry rather than through a full on-disk fixture: the
// interesting case is an internally-consistent header+index pair that still
// disagrees with reality, not a hash-gated corruption (which a different,
// already-tested path already catches).
func TestReader_ReadFile_RejectsInvalidSizeField(t *testing.T) {
	tests := []struct {
		name     string
		size     int64
		fileSize int64
	}{
		{"negative size", -1, 1000},
		{"size exceeding the file it's claimed to live in", 1 << 40, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdr := storedEntryHeader(tt.size, []byte("irrelevant")) // payload is never read: the guard fires first
			pakPath := filepath.Join(t.TempDir(), "test.pak")
			if err := os.WriteFile(pakPath, hdr, 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(pakPath)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close() //nolint:errcheck

			r := &Reader{
				f:        f,
				fileSize: tt.fileSize,
				entries: []readerEntry{
					{FileEntry: FileEntry{Path: "x.json", Size: tt.size}, offset: 0, method: 0},
				},
			}

			if _, err := r.ReadFile("x.json"); !errors.Is(err, ErrUnsupportedFormat) {
				t.Fatalf("ReadFile error = %v, want ErrUnsupportedFormat", err)
			}
		})
	}
}

// A cursor that has already latched an error must not panic when asked to
// take a negative length (reachable via a corrupted length field parsed
// earlier in the same structure, e.g. fstring's negative-length check
// latches c.err and later cursor calls in the same parse can still run).
func TestCursor_Take_ClampsNegativeLengthOnErrLatchedPath(t *testing.T) {
	c := &cursor{b: []byte{1, 2, 3}, err: errors.New("boom")}
	got := c.take(-5) // must not panic
	if len(got) != 0 {
		t.Errorf("take(-5) on an err-latched cursor = %v, want empty", got)
	}
}
