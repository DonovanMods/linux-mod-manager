package unrealpak

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Reader provides read access to an uncompressed, unencrypted UE4-range pak.
type Reader struct {
	f       *os.File
	entries []readerEntry
}

type readerEntry struct {
	FileEntry
	offset int64 // absolute offset of the entry's on-disk header
	method int32 // CompressionMethodIndex; 0 = stored. Non-zero entries are
	// enumerated but their payloads cannot be read (see ReadFile, Task 3).
}

// Open parses path's footer and index. It does not read file contents —
// call ReadFile for that (Task 3).
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unrealpak: opening %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close() //nolint:errcheck
		return nil, fmt.Errorf("unrealpak: stat %s: %w", path, err)
	}

	ft, err := readFooter(f, info.Size())
	if err != nil {
		f.Close() //nolint:errcheck
		return nil, err
	}
	if ft.encryptedIndex {
		f.Close() //nolint:errcheck
		return nil, fmt.Errorf("unrealpak: %s: %w: encrypted index", path, ErrUnsupportedFormat)
	}

	indexBuf, err := readRegion(f, ft.indexOffset, ft.indexSize, ft.indexHash)
	if err != nil {
		f.Close() //nolint:errcheck
		return nil, fmt.Errorf("unrealpak: %s: primary index: %w", path, err)
	}

	entries, err := parseIndex(f, indexBuf)
	if err != nil {
		f.Close() //nolint:errcheck
		return nil, fmt.Errorf("unrealpak: %s: parsing index: %w", path, err)
	}

	return &Reader{f: f, entries: entries}, nil
}

// readRegion reads size bytes at offset and verifies them against want. Every
// index region in a version-11 pak is SHA1-gated: the footer covers the
// primary index, and the primary index covers each sub-index. All three gates
// are enforced — a mismatch is corruption or an unrecognized layout, never
// something to parse through.
func readRegion(r io.ReaderAt, offset, size int64, want [20]byte) ([]byte, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("%w: negative region offset/size", ErrUnsupportedFormat)
	}
	buf := make([]byte, size)
	if _, err := r.ReadAt(buf, offset); err != nil {
		return nil, fmt.Errorf("reading region at %d: %w", offset, err)
	}
	if sum := sha1.Sum(buf); !bytes.Equal(sum[:], want[:]) { //nolint:gosec
		return nil, fmt.Errorf("hash mismatch (corrupt or unsupported format)")
	}
	return buf, nil
}

// Close releases the underlying file handle.
func (r *Reader) Close() error { return r.f.Close() }

// Files returns every file this pak's index describes.
func (r *Reader) Files() []FileEntry {
	out := make([]FileEntry, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.FileEntry
	}
	return out
}

type footer struct {
	version        int32
	indexOffset    int64
	indexSize      int64
	indexHash      [20]byte
	encryptedIndex bool
}

// readFooter parses the single 221-byte footer shape this package supports.
// The footer is fixed-size and sits flush against EOF, so there is nothing to
// search for and no alternate width to try: if Magic isn't where it must be,
// this is not a pak we handle.
func readFooter(r io.ReaderAt, fileSize int64) (footer, error) {
	if fileSize < footerSize {
		return footer{}, fmt.Errorf("%w: file of %d bytes is smaller than a %d-byte footer",
			ErrUnsupportedFormat, fileSize, footerSize)
	}
	buf := make([]byte, footerSize)
	if _, err := r.ReadAt(buf, fileSize-footerSize); err != nil {
		return footer{}, fmt.Errorf("reading footer: %w", err)
	}
	// Layout: EncryptionKeyGuid(0:16) bEncryptedIndex(16) Magic(17:21)
	// Version(21:25) IndexOffset(25:33) IndexSize(33:41) IndexHash(41:61)
	// CompressionMethods(61:221).
	if binary.LittleEndian.Uint32(buf[17:21]) != magic {
		return footer{}, fmt.Errorf("%w: no pak magic at the expected footer offset", ErrUnsupportedFormat)
	}
	ft := footer{
		encryptedIndex: buf[16] != 0,
		version:        int32(binary.LittleEndian.Uint32(buf[21:25])),
		indexOffset:    int64(binary.LittleEndian.Uint64(buf[25:33])),
		indexSize:      int64(binary.LittleEndian.Uint64(buf[33:41])),
	}
	copy(ft.indexHash[:], buf[41:61])
	if ft.version < minVersion {
		return footer{}, fmt.Errorf("%w: pak version %d (this package requires >= %d)",
			ErrUnsupportedFormat, ft.version, minVersion)
	}
	// The trailing CompressionMethods name table is intentionally left
	// unparsed: entries carry a method *index*, and this package only ever
	// reads payloads whose index is 0 (stored), which needs no name.
	return ft, nil
}

// parseIndex parses the primary index, then the full directory index it points
// at, resolving every path to its bit-packed entry record.
//
// Version-11 paks have no flat entry array. The primary index holds a blob of
// bit-packed records plus SHA1-gated offsets to two sub-indexes: a path-hash
// index (hash -> record offset) and a full directory index
// (directory -> file -> record offset). Enumeration uses the directory index,
// which is the only one that carries real path strings.
func parseIndex(f io.ReaderAt, index []byte) ([]readerEntry, error) {
	c := &cursor{b: index}
	c.fstring() // MountPoint: recorded for the engine's benefit, unused here
	numEntries := c.i32()
	seed := c.u64()
	_ = seed // only the writer needs the seed; enumeration goes via the directory index

	pathHash, err := readSubIndexRef(c, "path hash index")
	if err != nil {
		return nil, err
	}
	fullDir, err := readSubIndexRef(c, "full directory index")
	if err != nil {
		return nil, err
	}
	encoded := c.bytes(int(c.i32())) // EncodedPakEntriesSize, then the blob
	if nonEncoded := c.i32(); nonEncoded != 0 {
		return nil, fmt.Errorf("%w: %d non-encoded index entries", ErrUnsupportedFormat, nonEncoded)
	}
	if c.err != nil {
		return nil, fmt.Errorf("primary index: %w", c.err)
	}

	// Verify the path-hash index's hash even though enumeration does not use
	// it: it is part of the format's integrity chain, and a pak whose
	// sub-index hashes don't hold is not one to trust.
	if _, err := readRegion(f, pathHash.offset, pathHash.size, pathHash.hash); err != nil {
		return nil, fmt.Errorf("path hash index: %w", err)
	}
	dirBuf, err := readRegion(f, fullDir.offset, fullDir.size, fullDir.hash)
	if err != nil {
		return nil, fmt.Errorf("full directory index: %w", err)
	}

	entries, err := parseDirectoryIndex(dirBuf, encoded)
	if err != nil {
		return nil, err
	}
	if int32(len(entries)) != numEntries {
		return nil, fmt.Errorf("directory index lists %d files, index header says %d",
			len(entries), numEntries)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

type subIndexRef struct {
	offset, size int64
	hash         [20]byte
}

// readSubIndexRef reads a `bHas<X>Index` flag and, when set, the offset/size/
// hash triple that follows. Both sub-indexes are required: every pak version
// this package accepts writes both, and a reader that limped along without the
// directory index would have no paths to report.
func readSubIndexRef(c *cursor, name string) (subIndexRef, error) {
	if c.i32() == 0 {
		return subIndexRef{}, fmt.Errorf("%w: pak has no %s", ErrUnsupportedFormat, name)
	}
	ref := subIndexRef{offset: c.i64(), size: c.i64()}
	copy(ref.hash[:], c.bytes(20))
	return ref, c.err
}

// parseDirectoryIndex walks directory -> file -> entry-location and decodes the
// bit-packed record each location points at.
func parseDirectoryIndex(dir, encoded []byte) ([]readerEntry, error) {
	c := &cursor{b: dir}
	dirCount := c.i32()
	var entries []readerEntry
	for i := int32(0); i < dirCount && c.err == nil; i++ {
		dirName := c.fstring()
		fileCount := c.i32()
		for j := int32(0); j < fileCount && c.err == nil; j++ {
			fileName := c.fstring()
			loc := c.i32()
			// Root-level files live under a "/" directory key, so the naive
			// join yields a leading slash; the canonical mount-relative path
			// (and the one hashPath consumes) has none.
			full := strings.TrimPrefix(dirName+fileName, "/")
			if loc < 0 {
				// Negative locations index a non-encoded FPakEntry array. No
				// pak in a real Icarus install uses them.
				return nil, fmt.Errorf("entry %q: %w: non-encoded entry location", full, ErrUnsupportedFormat)
			}
			e, err := decodeEntry(encoded, int(loc))
			if err != nil {
				return nil, fmt.Errorf("entry %q: %w", full, err)
			}
			e.Path = full
			entries = append(entries, e)
		}
	}
	if c.err != nil {
		return nil, fmt.Errorf("directory index: %w", c.err)
	}
	return entries, nil
}

// decodeEntry decodes one bit-packed FPakEntry from the encoded blob.
//
// The leading uint32 packs: bit31 offset-is-32-bit, bit30 uncompressed-size-
// is-32-bit, bit29 size-is-32-bit, bits28-23 CompressionMethodIndex, bit22
// encrypted, bits21-6 compression block count, bits5-0 CompressionBlockSize>>11
// (0x3f = escape, an explicit uint32 follows). Fields then appear in this
// order: [CompressionBlockSize] Offset, UncompressedSize, [Size], [block
// sizes]. Size is omitted for stored entries (it equals UncompressedSize), and
// the per-block size table is omitted for a lone unencrypted block.
//
// The block-size-before-Offset ordering is easy to get wrong; it was pinned
// down empirically and this decoder reproduces all 173,078 records across a
// real install exactly. See docs/plans/icarus-pak-format-findings.md.
func decodeEntry(b []byte, at int) (readerEntry, error) {
	c := &cursor{b: b, pos: at}
	flags := c.u32()
	var (
		method     = int32((flags >> 23) & 0x3F)
		blockCount = int((flags >> 6) & 0xFFFF)
		encrypted  = flags&(1<<22) != 0
	)
	if flags&0x3F == 0x3F {
		c.u32() // explicit CompressionBlockSize
	}
	read := func(is32 bool) int64 {
		if is32 {
			return int64(c.u32())
		}
		return int64(c.u64())
	}
	offset := read(flags&(1<<31) != 0)
	uncompressed := read(flags&(1<<30) != 0)
	if method != 0 {
		read(flags&(1<<29) != 0) // Size on disk; unused, we refuse to read these payloads
	}
	if blockCount > 0 && (blockCount > 1 || encrypted) {
		c.bytes(4 * blockCount)
	}
	if c.err != nil {
		return readerEntry{}, fmt.Errorf("decoding entry at blob offset %d: %w", at, c.err)
	}
	if encrypted {
		return readerEntry{}, fmt.Errorf("%w: encrypted entry", ErrUnsupportedFormat)
	}
	return readerEntry{
		FileEntry: FileEntry{Size: uncompressed},
		offset:    offset,
		method:    method,
	}, nil
}

// cursor is a bounds-checked little-endian cursor over an in-memory index
// region. It latches the first error so parse code can read a whole structure
// and check once, rather than wrapping every field.
type cursor struct {
	b   []byte
	pos int
	err error
}

func (c *cursor) take(n int) []byte {
	if c.err != nil {
		return make([]byte, n)
	}
	if n < 0 || c.pos+n > len(c.b) {
		c.err = io.ErrUnexpectedEOF
		return make([]byte, max(n, 0))
	}
	v := c.b[c.pos : c.pos+n]
	c.pos += n
	return v
}

func (c *cursor) bytes(n int) []byte { return c.take(n) }
func (c *cursor) u32() uint32        { return binary.LittleEndian.Uint32(c.take(4)) }
func (c *cursor) i32() int32         { return int32(c.u32()) }
func (c *cursor) u64() uint64        { return binary.LittleEndian.Uint64(c.take(8)) }
func (c *cursor) i64() int64         { return int64(c.u64()) }

// fstring reads a length-prefixed Unreal FString. A negative length signals
// UTF-16, which no pak in a real Icarus install uses and this package does not
// decode.
func (c *cursor) fstring() string {
	n := c.i32()
	if n == 0 || c.err != nil {
		return ""
	}
	if n < 0 {
		c.err = fmt.Errorf("%w: UTF-16 FString", ErrUnsupportedFormat)
		return ""
	}
	return string(bytes.TrimRight(c.take(int(n)), "\x00"))
}
