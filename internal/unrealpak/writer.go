package unrealpak

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
)

// writerSeed is the PathHashSeed stamped into written paks. Any value works —
// readers take the seed from the index, and real paks use a different one per
// chunk — but a fixed one keeps output deterministic.
const writerSeed uint64 = 0x9E3779B97F4A7C15

// Writer produces a stored (uncompressed), unencrypted version-11 pak carrying
// the full three-part index: primary index, path-hash index and full directory
// index, then the 221-byte footer.
//
// AddFile buffers content in memory and Close emits everything sorted by path,
// so identical inputs produce byte-identical output regardless of AddFile call
// order. Mod paks are small — Icarus's entire base data.pak is 2.4 MB — so
// buffering costs little, and deterministic output is worth more: it makes the
// round-trip test able to assert on bytes and keeps compiled paks stable across
// recompiles.
type Writer struct {
	f      *os.File
	closed bool
	files  []writerFile
	seen   map[string]bool
}

type writerFile struct {
	path string
	data []byte
}

// Create opens path for writing. Call AddFile for each entry, then Close.
func Create(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("unrealpak: creating %s: %w", path, err)
	}
	return &Writer{f: f, seen: make(map[string]bool)}, nil
}

// AddFile records one entry. Nothing reaches disk until Close.
func (w *Writer) AddFile(mountPath string, data []byte) error {
	if w.closed {
		return fmt.Errorf("unrealpak: AddFile on closed writer")
	}
	// Root-level files are keyed under "/" in the directory index, but the
	// canonical path — and the one hashPath consumes — carries no leading slash.
	rel := strings.TrimPrefix(mountPath, "/")
	if rel == "" {
		return fmt.Errorf("unrealpak: AddFile: empty mount path")
	}
	if w.seen[rel] {
		return fmt.Errorf("unrealpak: AddFile: duplicate path %q", rel)
	}
	w.seen[rel] = true
	w.files = append(w.files, writerFile{path: rel, data: slices.Clone(data)})
	return nil
}

// checkEncodedLocationFits validates that encodedLen — the offset the next
// entry's record is about to be stored at within the encoded index blob —
// still fits the int32 location field the format stores in the path-hash and
// full directory indexes. Extracted from Close so the boundary can be tested
// without constructing a >2 GiB fixture: encoded.Len() growing past
// math.MaxInt32 would otherwise silently wrap (even go negative) when cast to
// int32, corrupting every location recorded from that point on.
func checkEncodedLocationFits(encodedLen int) error {
	if encodedLen > math.MaxInt32 {
		return fmt.Errorf("encoded index exceeds the 32-bit location field this writer emits (%d bytes)", encodedLen)
	}
	return nil
}

// Close assembles the data section and all three index structures, writes them
// with the footer, and closes the file.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true

	sort.Slice(w.files, func(i, j int) bool { return w.files[i].path < w.files[j].path })

	// Data section and encoded index records, in one pass. Each payload is
	// preceded by its 53-byte header; entries are packed with no padding.
	var data, encoded bytes.Buffer
	locations := make(map[string]int32, len(w.files))
	for _, file := range w.files {
		offset, size := int64(data.Len()), int64(len(file.data))
		if offset > math.MaxUint32 || size > math.MaxUint32 {
			w.f.Close() //nolint:errcheck
			return fmt.Errorf("unrealpak: %s: offset/size exceeds the 32-bit encoded-entry form this writer emits", file.path)
		}
		data.Write(storedEntryHeader(size, file.data))
		data.Write(file.data)

		if err := checkEncodedLocationFits(encoded.Len()); err != nil {
			w.f.Close() //nolint:errcheck
			return fmt.Errorf("unrealpak: %s: %w", file.path, err)
		}
		locations[file.path] = int32(encoded.Len())
		// The 12-byte stored record: offset/uncompressed-size/size all
		// 32-bit-safe, method 0, no compression blocks.
		binary.Write(&encoded, binary.LittleEndian, uint32(0xE0000000)) //nolint:errcheck
		binary.Write(&encoded, binary.LittleEndian, uint32(offset))     //nolint:errcheck
		binary.Write(&encoded, binary.LittleEndian, uint32(size))       //nolint:errcheck
	}

	// Full directory index: directory -> file -> encoded-record location.
	byDir := make(map[string][]string)
	for _, file := range w.files {
		dir, name := splitMountPath(file.path)
		byDir[dir] = append(byDir[dir], name)
	}
	dirNames := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirNames = append(dirNames, dir)
	}
	sort.Strings(dirNames)

	var fdi bytes.Buffer
	binary.Write(&fdi, binary.LittleEndian, int32(len(dirNames))) //nolint:errcheck
	for _, dir := range dirNames {
		writeFString(&fdi, dir)
		names := byDir[dir]
		sort.Strings(names)
		binary.Write(&fdi, binary.LittleEndian, int32(len(names))) //nolint:errcheck
		for _, name := range names {
			writeFString(&fdi, name)
			binary.Write(&fdi, binary.LittleEndian, locations[strings.TrimPrefix(dir+name, "/")]) //nolint:errcheck
		}
	}

	// Path-hash index, then an empty pruned directory index.
	var phi bytes.Buffer
	binary.Write(&phi, binary.LittleEndian, int32(len(w.files))) //nolint:errcheck
	for _, file := range w.files {
		binary.Write(&phi, binary.LittleEndian, hashPath(file.path, writerSeed)) //nolint:errcheck
		binary.Write(&phi, binary.LittleEndian, locations[file.path])            //nolint:errcheck
	}
	binary.Write(&phi, binary.LittleEndian, int32(0)) //nolint:errcheck // pruned index: 0 directories

	// The primary index records absolute offsets of the two sub-indexes that
	// follow it; its own length is independent of those values, so measure it
	// with zeros first, then rebuild with the real offsets.
	phiHash := sha1.Sum(phi.Bytes()) //nolint:gosec
	fdiHash := sha1.Sum(fdi.Bytes()) //nolint:gosec
	count := int32(len(w.files))
	indexOffset := int64(data.Len())
	sizing := buildPrimaryIndex(count, writerSeed, 0, 0, phiHash, 0, 0, fdiHash, encoded.Bytes())
	phiOffset := indexOffset + int64(len(sizing))
	fdiOffset := phiOffset + int64(phi.Len())
	index := buildPrimaryIndex(count, writerSeed,
		phiOffset, int64(phi.Len()), phiHash,
		fdiOffset, int64(fdi.Len()), fdiHash, encoded.Bytes())
	indexHash := sha1.Sum(index) //nolint:gosec

	// Regions tile the file exactly, as they do in every real pak:
	// data | primary index | path-hash index | full directory index | footer.
	for _, chunk := range [][]byte{
		data.Bytes(), index, phi.Bytes(), fdi.Bytes(),
		buildFooter(writeVersion, indexOffset, int64(len(index)), indexHash),
	} {
		if _, err := w.f.Write(chunk); err != nil {
			w.f.Close() //nolint:errcheck
			return fmt.Errorf("unrealpak: writing pak: %w", err)
		}
	}
	return w.f.Close()
}
