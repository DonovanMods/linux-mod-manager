package unrealpak

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
)

// ErrUnsupportedFormat indicates the pak uses a feature this package
// deliberately does not support (compression, encryption, exotic FString
// encodings) rather than a genuine parse failure. Callers should fail loudly
// on this, not silently degrade (repo precedent: #95).
var ErrUnsupportedFormat = errors.New("unrealpak: unsupported pak feature")

const magic uint32 = 0x5A6F12E1

// footerSize is the only footer shape this package supports: the version>=8
// layout, EncryptionKeyGuid(16)+bEncryptedIndex(1)+Magic(4)+Version(4)+
// IndexOffset(8)+IndexSize(8)+IndexHash(20)+CompressionMethods(5x32) = 221.
// Note EncryptionKeyGuid and bEncryptedIndex precede Magic — not after
// IndexHash, as some public docs describe. Confirmed on all 34 paks in a real
// Icarus install (Task 1); see docs/plans/icarus-pak-format-findings.md.
const footerSize = 221

// minVersion is the oldest pak version this package reads. Version 10
// (PakFile_Version_PathHashIndex) introduced the three-part index — primary
// index + path-hash index + full directory index — that this package parses.
// Older paks use a flat index with a completely different shape; rather than
// carry a second parser for a layout Icarus does not ship, they are a hard
// ErrUnsupportedFormat (repo precedent #95: no silent fallbacks).
const minVersion int32 = 10

// writeVersion is what Writer emits: the same version Icarus's own paks use,
// so the engine loads our output through the exact code path it already uses.
const writeVersion int32 = 11

// storedHeaderSize is the on-disk size of the per-entry FPakEntry header that
// precedes each stored (uncompressed) file's payload:
// Offset(8)+Size(8)+UncompressedSize(8)+CompressionMethodIndex(4)+Hash(20)+
// Flags(1)+CompressionBlockSize(4) = 53. Compressed entries add
// BlockCount(4)+16*blocks between Hash and Flags; this package never writes
// those and refuses to read their payloads.
const storedHeaderSize = 53

// The footer's CompressionMethods table: 5 fixed-width, NUL-padded name slots
// starting at byte 61. An entry's CompressionMethodIndex is 1-based into it
// (0 means "stored", naming no slot).
const (
	maxCompressionMethods     = 5
	compressionMethodNameSize = 32
	compressionMethodsOffset  = 61
)

// zlibMethodName is the CompressionMethods entry this package can decompress.
// Matched case-insensitively: the name is free-form text written by whatever
// cooked the pak.
const zlibMethodName = "Zlib"

// maxUncompressedEntrySize caps a single entry's decompressed size.
//
// This deliberately does NOT reuse validateAllocSize's "cannot exceed the pak
// file's own size" rule, which holds for on-disk regions but is simply false
// for decompressed output: Icarus's Items/D_ItemsStatic.json expands to
// 7,304,687 bytes inside a 2,458,743-byte pak. A fixed ceiling is the right
// shape of bound here — it stops a malicious or corrupt UncompressedSize from
// driving an unbounded allocation without rejecting legitimate compression.
const maxUncompressedEntrySize = 512 << 20

// FileEntry describes one file inside a pak, as returned by Reader.Files.
type FileEntry struct {
	Path string // Mount-relative path, e.g. "Icarus/Content/Data/AI-D_AIGrowth.json"
	Size int64  // Uncompressed size in bytes
}

// hashPath computes a path's key in the pak's path-hash index: FNV-1a 64 over
// the UTF-16LE bytes of the lowercased mount-relative path (no NUL
// terminator), seeded by ADDING the pak's PathHashSeed to the FNV offset
// basis. Any leading "/" is stripped first — the full directory index stores
// root-level files under a "/" directory, and the hash is taken over the path
// without it.
//
// This recipe was not guessed: it was recovered by brute-forcing seed/
// encoding/case/prefix combinations until computed hashes matched stored keys,
// then verified against all 173,078 entries across all 34 paks in a real
// install. See docs/plans/icarus-pak-format-findings.md.
//
// strings.ToLower is full-Unicode where UE's FChar::ToLower is not, but no
// non-ASCII path exists in any shipped Icarus pak and this package controls
// the paths it writes, so the two agree for everything we handle.
func hashPath(mountRelative string, seed uint64) uint64 {
	const (
		offsetBasis uint64 = 0xCBF29CE484222325
		prime       uint64 = 0x00000100000001B3
	)
	h := offsetBasis + seed
	for _, u := range utf16.Encode([]rune(strings.ToLower(strings.TrimPrefix(mountRelative, "/")))) {
		h ^= uint64(byte(u))
		h *= prime
		h ^= uint64(byte(u >> 8))
		h *= prime
	}
	return h
}

// defaultMountPoint is the mount point Writer stamps into the primary index
// when the caller doesn't override it via WithMountPoint. It is the bare
// UE4 convention ("../../../", relative to <UProject>/Binaries/Win64/"),
// deliberately game-agnostic — this package has no opinion on any specific
// game's directory layout. A caller writing paks for a real game (see
// internal/source/icarus's Writer usage, #178) supplies the mount point its
// own game's mod loader actually expects.
const defaultMountPoint = "../../../"

// writeFString writes a length-prefixed ANSI Unreal FString (length includes
// the trailing NUL).
func writeFString(buf *bytes.Buffer, s string) {
	b := append([]byte(s), 0)
	binary.Write(buf, binary.LittleEndian, int32(len(b))) //nolint:errcheck // bytes.Buffer writes never fail
	buf.Write(b)
}

// splitMountPath splits a mount-relative path into the directory-index key
// (trailing "/", or exactly "/" for a root-level file) and the leaf name,
// matching how real paks key their directory indexes.
func splitMountPath(rel string) (dir, file string) {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i+1], rel[i+1:]
	}
	return "/", rel
}

// storedEntryHeader builds the 53-byte FPakEntry header that precedes a stored
// file's payload on disk. The Offset field is always 0 in this local copy —
// real paks write 0 there too, the authoritative offset lives in the index.
// Hash is the SHA1 of the on-disk payload bytes.
func storedEntryHeader(size int64, content []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, int64(0)) //nolint:errcheck // Offset
	binary.Write(&b, binary.LittleEndian, size)     //nolint:errcheck // Size
	binary.Write(&b, binary.LittleEndian, size)     //nolint:errcheck // UncompressedSize
	binary.Write(&b, binary.LittleEndian, int32(0)) //nolint:errcheck // CompressionMethodIndex: stored
	h := sha1.Sum(content)                          //nolint:gosec
	b.Write(h[:])
	b.WriteByte(0)                                   // Flags: not encrypted, not deleted
	binary.Write(&b, binary.LittleEndian, uint32(0)) //nolint:errcheck // CompressionBlockSize
	return b.Bytes()
}

// buildPrimaryIndex serializes the primary index. Callers build it twice: the
// sub-index offsets it records point past its own end, but its length does not
// depend on their values (they are fixed-width int64), so a first pass with
// zero offsets measures it and a second pass writes the real ones.
func buildPrimaryIndex(mountPoint string, numEntries int32, seed uint64,
	phiOffset, phiSize int64, phiHash [20]byte,
	fdiOffset, fdiSize int64, fdiHash [20]byte, encoded []byte) []byte {
	var b bytes.Buffer
	writeFString(&b, mountPoint)
	binary.Write(&b, binary.LittleEndian, numEntries) //nolint:errcheck
	binary.Write(&b, binary.LittleEndian, seed)       //nolint:errcheck // PathHashSeed
	binary.Write(&b, binary.LittleEndian, int32(1))   //nolint:errcheck // bHasPathHashIndex
	binary.Write(&b, binary.LittleEndian, phiOffset)  //nolint:errcheck
	binary.Write(&b, binary.LittleEndian, phiSize)    //nolint:errcheck
	b.Write(phiHash[:])
	binary.Write(&b, binary.LittleEndian, int32(1))  //nolint:errcheck // bHasFullDirectoryIndex
	binary.Write(&b, binary.LittleEndian, fdiOffset) //nolint:errcheck
	binary.Write(&b, binary.LittleEndian, fdiSize)   //nolint:errcheck
	b.Write(fdiHash[:])
	binary.Write(&b, binary.LittleEndian, int32(len(encoded))) //nolint:errcheck // EncodedPakEntriesSize
	b.Write(encoded)
	binary.Write(&b, binary.LittleEndian, int32(0)) //nolint:errcheck // NumNonEncodedFiles: none
	return b.Bytes()
}

// buildFooter serializes the 221-byte version>=8 footer.
func buildFooter(version int32, indexOffset, indexSize int64, indexHash [20]byte) []byte {
	var b bytes.Buffer
	b.Write(make([]byte, 16))                          // EncryptionKeyGuid: zero
	b.WriteByte(0)                                     // bEncryptedIndex: false
	binary.Write(&b, binary.LittleEndian, magic)       //nolint:errcheck
	binary.Write(&b, binary.LittleEndian, version)     //nolint:errcheck
	binary.Write(&b, binary.LittleEndian, indexOffset) //nolint:errcheck
	binary.Write(&b, binary.LittleEndian, indexSize)   //nolint:errcheck
	b.Write(indexHash[:])
	// CompressionMethods: 5 fixed-width 32-byte name slots, all empty since
	// this package only ever writes stored entries. Real paks name "Oodle"
	// and "Zlib" here; an all-zero table is the correct shape for method 0.
	b.Write(make([]byte, 160))
	return b.Bytes()
}
