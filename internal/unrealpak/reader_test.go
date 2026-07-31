package unrealpak

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"errors"
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
	sizing := buildPrimaryIndex(1, fixtureSeed, 0, 0, phiHash, 0, 0, fdiHash, encoded.Bytes())
	phiOffset := indexOffset + int64(len(sizing))
	fdiOffset := phiOffset + int64(phi.Len())
	index := buildPrimaryIndex(1, fixtureSeed,
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
	defer r.Close()

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

// A root-level file is keyed under the "/" directory in the directory index;
// Files must report it without the leading slash, matching what hashPath uses.
func TestReader_Open_RootLevelFile(t *testing.T) {
	path := writeMinimalPak(t, "x.json", []byte("{}"))
	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

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
	defer r.Close()

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
	defer r.Close()

	// Enumeration must still work — the reader lists compressed entries.
	if files := r.Files(); len(files) != 1 || files[0].Path != name {
		t.Fatalf("Files() = %+v, want one entry named %q", files, name)
	}
	if _, err := r.ReadFile(name); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("ReadFile error = %v, want ErrUnsupportedFormat", err)
	}
}
