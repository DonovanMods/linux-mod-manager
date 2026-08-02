# Icarus `.exmod`/`.exmodz` PAK Compilation Implementation Plan

> **SUPERSEDED IN PART (2026-08-01, #175).** Everywhere this plan states that `data.pak`'s
> tables are Oodle-compressed and therefore unreadable — the Global Constraints' network
> bullet, Task 12's base-table note, Task 12a (the dump fetcher) and Task 13's `data_dump_path`
> wiring — the premise is false: those tables are **Zlib**, which the standard library reads.
> Tasks 1–11 (the pak format work, the Firestore source, `.EXMOD`/`.EXMODZ` handling) are
> unaffected and shipped as written. The dump subsystem those later tasks built has been
> removed by [`2026-08-01-icarus-zlib-pivot.md`](2026-08-01-icarus-zlib-pivot.md); read that
> for the current design. This document is kept unedited below as the record of how the epic
> was actually built.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let LMM install Icarus mods distributed as `.exmodz` by compiling their JSON diff into a working `_P.pak` on Linux, end to end from browsing the Firestore-backed catalog through deployed pak.

**Architecture:** A game-agnostic `internal/unrealpak` package (UE 4.25–4.27-range PAK index reader/writer, uncompressed+unencrypted only) underpins an Icarus-specific `internal/source/icarus` package (Firestore REST `ModSource` + `.EXMOD` diff engine + `.EXMODZ` unpacking + compile orchestration). A new `domain.DeployCompile` mode and a `source.Compiler` optional capability wire the compile step into `Service`'s existing cache-population path, so the linker's deploy step needs no changes at all.

**Tech Stack:** Go 1.25 (this repo's existing version), stdlib only (`net/http`, `encoding/json`, `encoding/binary`, `archive/zip`, `crypto/sha1`) — no new third-party dependencies, matching this repo's existing NexusMods/CurseForge clients and its `modernc.org/sqlite`-style no-CGO/no-external-binary convention.

## Global Constraints

- No new third-party Go dependencies (`go.mod` stays free of a Firestore SDK, a PAK library, etc.) — plain `net/http` and stdlib only, per repo convention and `~/.claude/GO.md`.
- No silent fallbacks: unexpected PAK format, encryption, or compression fails loudly with a clear error (repo precedent: #95).
- v1 PAK reader/writer scope is **uncompressed, unencrypted only** — anything else is a hard error, not a degraded path.
- **The compile path requires network access** (user decision, rev3): base data tables come
  from the community's hosted per-week JSON dumps, not from decompressing the local
  `data.pak`. This is a deliberate exception to compiling from local data only, forced by
  Oodle — 258 of `data.pak`'s 298 tables are Oodle-compressed and no stdlib decoder exists.
  Stdlib-only still holds (`net/http` is fine; no new dependencies). Compiling offline is
  therefore not supported; that must surface as a clear error, never a silent skip.
- Design source of truth: [`docs/plans/2026-07-29-icarus-exmod-pak-research.md`](../../plans/2026-07-29-icarus-exmod-pak-research.md) on branch `docs/icarus-exmod-pak-research`. This plan builds on that branch.
- Tracked by [#136](https://github.com/DonovanMods/linux-mod-manager/issues/136) — reference it in commit messages/PR per repo workflow.
- Follow this repo's TDD/table-driven Go conventions (`~/.claude/GO.md`, `~/.claude/DEV.md`) throughout.

---

## Task 1: Validate the real PAK footer/index format against a local install

This is an empirical spike, not code — everything downstream depends on its findings. The plan assumes the documented classic UE4 `FPakInfo` footer layout (magic `0x5A6F12E1`, version-gated fields), which is well established publicly (`repak`, `u4pak`) but has **not** been confirmed against Icarus's actual bytes.

**Files:**

- Create: `docs/plans/icarus-pak-format-findings.md` (scratch findings doc, committed alongside Task 2 once confirmed — not shipped as product docs)

**Interfaces:**

- Produces: confirmed values for `footerSize` (221 or 61 bytes — see Task 2), `Version` (int32), `bEncryptedIndex` (expected `false`), that later tasks' hard-coded assumptions in `internal/unrealpak` must match.

> **STATUS: DONE — and it falsified this plan's index assumption.** Both spike rounds are
> complete; findings are in [`docs/plans/icarus-pak-format-findings.md`](icarus-pak-format-findings.md).
> Steps 1–6 below are kept for provenance but need not be re-run. Three results reshaped
> Tasks 2–5, 12 and 13:
>
> 1. **The footer is confirmed** (version 11, 221 bytes, `bEncryptedIndex == 0`, SHA1 match)
>    — verified on all 34 paks in the install.
> 2. **The index is NOT the classic flat `MountPoint`+`NumEntries`+N×`FPakEntry` layout this
>    plan assumed.** Version 11 uses the UE 4.25+ three-part index: a primary index carrying
>    _bit-packed_ entry records plus SHA1-gated offsets to a path-hash index and a full
>    directory index. Every structure is now decoded at byte level in the findings doc
>    (Part 2), verified against 173,078 real entries.
> 3. **The `.EXMOD` base pak is `Icarus/Content/Data/data.pak`, not a pakchunk** — 298 files,
>    all `.json`. The `Content/Paks/pakchunk0*` chunks contain zero `.json`. See the Oodle
>    blocker note in Task 12.

- [ ] **Step 1: Locate the local `data.pak`**

```bash
find ~/.steam ~/.local/share/Steam -ipath "*Icarus*Content/Paks/*.pak" 2>/dev/null
```

Expected: a path like `.../steamapps/common/Icarus/Icarus/Content/Paks/pakchunk0-WindowsNoEditor.pak` (or similarly named — Icarus ships its base data under one or more numbered pakchunks, not necessarily literally named `data.pak`; note the exact filename(s) found).

- [ ] **Step 2: Dump the last 256 bytes and locate the magic**

```bash
PAK=/path/found/above
tail -c 256 "$PAK" | xxd | tail -20
python3 -c "
import struct
data = open('$PAK','rb').read()[-256:]
magic = struct.pack('<I', 0x5A6F12E1)
idx = data.rfind(magic)
print('magic found at offset from end of last-256 block:', idx, '(if -1, try a larger tail)')
"
```

If `-1`, retry with `tail -c 512` — some UE versions carry a larger footer than the 221-byte upper bound this plan assumes.

- [ ] **Step 3: Parse the footer fields at the found offset**

```bash
python3 -c "
import struct
path = '$PAK'
data = open(path, 'rb').read()
magic = struct.pack('<I', 0x5A6F12E1)
# search whole tail window found in Step 2; adjust slice size to match
tail = data[-256:]
i = tail.rfind(magic)
off = len(data) - 256 + i
version, index_offset, index_size = struct.unpack_from('<iqq', data, off+4)
index_hash = data[off+4+4+8+8: off+4+4+8+8+20]
print('version:', version)
print('index_offset:', index_offset, 'index_size:', index_size)
print('index_hash (hex):', index_hash.hex())
print('footer start offset:', off, '-> footer size:', len(data)-off)
# bEncryptedIndex is the byte immediately after IndexHash, present for version>=4
enc_off = off+4+4+8+8+20
print('bEncryptedIndex byte:', data[enc_off])
"
```

Expected (per Task 2's assumptions): `bEncryptedIndex == 0`. Record the actual `version` and footer size (221 vs 61 vs other) in `docs/plans/icarus-pak-format-findings.md`.

**Note (added post-spike):** the `bEncryptedIndex` byte offset in this script's naive parse (`off+4+4+8+8+20`, i.e. immediately after `IndexHash`) is **wrong** for Icarus's real version-11 paks — it lands inside the trailing `CompressionMethods` table instead of the actual flag byte. The real layout has `EncryptionKeyGuid`(16 bytes) + `bEncryptedIndex`(1 byte) immediately **before** `Magic`, and (for version≥8) a `CompressionMethods` table **after** `IndexHash`. The corrected byte layout is documented in `docs/plans/icarus-pak-format-findings.md`, which supersedes this script's field-offset assumptions — Task 2's implementation uses the corrected layout, not this script's.

- [ ] **Step 4: Cross-check IndexHash against the actual index bytes**

```bash
python3 -c "
import hashlib
data = open('$PAK', 'rb').read()
index_offset = <paste from Step 3>
index_size = <paste from Step 3>
index_bytes = data[index_offset:index_offset+index_size]
print('sha1 matches footer IndexHash:', hashlib.sha1(index_bytes).hexdigest())
"
```

Compare against the `index_hash` hex from Step 3. A match is strong confirmation the offset/size/version parsing above is correct — this is the acceptance gate for Task 2's format assumptions, since a wrong version/field-width would make this hash disagree.

- [ ] **Step 5: Write up findings**

Create `docs/plans/icarus-pak-format-findings.md` with: exact pak filename(s) found, `version`, footer size, confirmed `bEncryptedIndex == false`, and the SHA1 cross-check result from Step 4. If any assumption in Task 2 turns out wrong (different footer size, encrypted index, unexpected version), stop and revise Task 2's `footerSizes`/version-gate constants before proceeding — do not implement Task 2 against unconfirmed values.

- [ ] **Step 6: Commit**

```bash
git add docs/plans/icarus-pak-format-findings.md
git commit -m "docs: confirm Icarus data.pak footer format (#136)"
```

---

## Task 2: `internal/unrealpak` — footer + index reader

**Files:**

- Create: `internal/unrealpak/pak.go` (shared types/errors)
- Create: `internal/unrealpak/reader.go`
- Create: `internal/unrealpak/reader_test.go`

**Interfaces:**

- Consumes: footer format confirmed in Task 1.
- Produces: `type Reader struct{...}`, `func Open(path string) (*Reader, error)`, `func (r *Reader) Close() error`, `func (r *Reader) Files() []FileEntry`, `type FileEntry struct { Path string; Size int64 }` — Task 3 and Task 12 depend on these exact names.

- [ ] **Step 1: Write `pak.go` shared types, constants and format primitives**

```go
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
```

Then the shared **format primitives** — the byte-emitters for the four structures a
version-11 pak is made of. They live here rather than in `writer.go` because Task 2's
reader test builds its own fixture pak with them and must not depend on Task 4:

```go
// defaultMountPoint is the mount point Writer stamps into the primary index.
// Icarus's own data.pak uses an absolute cook-machine path
// ("C:/BA/work/.../Temp/Data/"); "../../../" is the conventional relative form
// used by its pakchunks. Confirming which one a _P.pak needs to override
// Content/Data/data.pak in-game is a post-plan validation item.
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
func buildPrimaryIndex(numEntries int32, seed uint64,
	phiOffset, phiSize int64, phiHash [20]byte,
	fdiOffset, fdiSize int64, fdiHash [20]byte, encoded []byte) []byte {
	var b bytes.Buffer
	writeFString(&b, defaultMountPoint)
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
```

(Both blocks above are one file: `pak.go`'s import list at the top covers them.)

- [ ] **Step 2: Write the failing test for footer parsing**

```go
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
```

- [ ] **Step 3: Run to verify it fails**

```bash
go test ./internal/unrealpak/... -run TestReader_Open -v
```

Expected: FAIL (`Open` undefined).

- [ ] **Step 4: Implement `reader.go`**

```go
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
```

`reader.go` needs `bytes`, `crypto/sha1`, `encoding/binary`, `fmt`, `io`, `os`, `sort`
and `strings`.

**Design note — enumeration vs. reading compressed entries.** `Open`/`Files` enumerate
_every_ entry regardless of compression method, and only `ReadFile` (Task 3) refuses a
non-stored payload. This is deliberate, and is what makes the "open the real pakchunk0 and
enumerate 9295 entries" acceptance step possible at all: 74% of the entries in a real
install are Oodle-compressed, so rejecting them at index-parse time would make the reader
unable to open any real pak. It does not weaken the Global Constraints — no caller can
ever obtain wrong bytes, because the refusal happens at exactly the point where wrong
bytes would otherwise be produced.

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/unrealpak/... -v
```

Expected: PASS for all five — `TestReader_Open_ListsFiles`, `TestReader_Open_RootLevelFile`,
`TestReader_Open_RejectsEncryptedIndex`, `TestReader_Open_RejectsPreVersion10` and
`TestReader_Open_RejectsCorruptedDirectoryIndex`.

- [ ] **Step 5b: Sanity-check against the real install (manual, not committed)**

The fixture only proves the reader agrees with itself. Point it at the real thing once:

```bash
go run ./internal/unrealpak/... 2>/dev/null # or a throwaway main/test that calls Open+Files
```

Expected: `Open` succeeds on
`/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Paks/pakchunk0-WindowsNoEditor.pak`
and `Files()` returns **9295** entries, and on
`Icarus/Content/Data/data.pak` returning **298** entries, all `.json`. If either count is
off, the index parser is wrong — fix it before Task 3. (Also listed as a post-plan
validation step.)

- [ ] **Step 6: Commit**

```bash
git add internal/unrealpak/pak.go internal/unrealpak/reader.go internal/unrealpak/reader_test.go
git commit -m "feat: add unrealpak footer+index reader (#136)"
```

---

## Task 3: `internal/unrealpak` — file content reader

**Files:**

- Modify: `internal/unrealpak/reader.go`
- Modify: `internal/unrealpak/reader_test.go`

**Interfaces:**

- Consumes: `Reader.entries []readerEntry` from Task 2.
- Produces: `func (r *Reader) ReadFile(path string) ([]byte, error)` — Task 12 depends on this exact signature.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/unrealpak/... -run TestReader_ReadFile -v
```

Expected: FAIL (`ReadFile` undefined).

- [ ] **Step 3: Implement `ReadFile`**

Add to `reader.go`:

```go
// ReadFile returns the bytes of the entry at mount-relative path.
//
// On-disk entry data is preceded by a full FPakEntry header — 53 bytes for a
// stored entry (Offset, Size, UncompressedSize, CompressionMethodIndex, Hash,
// Flags, CompressionBlockSize) — and the index's offset points at that header,
// not the payload. The header is re-read and cross-checked rather than trusted:
// its method and size must agree with the index, and its Hash must match the
// payload's SHA1. Real paks satisfy all three (verified across a whole install),
// so a disagreement means corruption or a layout this package misread.
func (r *Reader) ReadFile(path string) ([]byte, error) {
	for _, e := range r.entries {
		if e.Path != path {
			continue
		}
		// Compression is refused here rather than at index-parse time so that
		// Files() can still enumerate real paks, most of whose entries are
		// Oodle-compressed. No caller can obtain wrong bytes either way.
		if e.method != 0 {
			return nil, fmt.Errorf("unrealpak: %s: %w: compressed entry (method %d)",
				path, ErrUnsupportedFormat, e.method)
		}
		hdr := make([]byte, storedHeaderSize)
		if _, err := r.f.ReadAt(hdr, e.offset); err != nil {
			return nil, fmt.Errorf("unrealpak: %s: reading entry header: %w", path, err)
		}
		if m := int32(binary.LittleEndian.Uint32(hdr[24:28])); m != 0 {
			return nil, fmt.Errorf("unrealpak: %s: %w: compressed entry data (method %d)",
				path, ErrUnsupportedFormat, m)
		}
		if size := int64(binary.LittleEndian.Uint64(hdr[8:16])); size != e.Size {
			return nil, fmt.Errorf("unrealpak: %s: entry header size %d disagrees with index size %d",
				path, size, e.Size)
		}
		buf := make([]byte, e.Size)
		if _, err := r.f.ReadAt(buf, e.offset+storedHeaderSize); err != nil {
			return nil, fmt.Errorf("unrealpak: reading %s: %w", path, err)
		}
		if sum := sha1.Sum(buf); !bytes.Equal(sum[:], hdr[28:48]) { //nolint:gosec
			return nil, fmt.Errorf("unrealpak: %s: content hash mismatch", path)
		}
		return buf, nil
	}
	return nil, fmt.Errorf("unrealpak: %s: %w", path, os.ErrNotExist)
}
```

The Task 2 fixture already writes the 53-byte header ahead of each payload (a pak without
it would not be loadable), so no fixture surgery is needed here.

Add one more test — the case that dominates in practice, since 258 of the real
`data.pak`'s 298 JSON tables are Oodle-compressed:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/unrealpak/... -v
```

Expected: PASS, including the earlier `TestReader_Open_ListsFiles`.

- [ ] **Step 5: Commit**

```bash
git add internal/unrealpak/reader.go internal/unrealpak/reader_test.go
git commit -m "feat: add unrealpak file content reading (#136)"
```

---

## Task 4: `internal/unrealpak` — writer

**Files:**

- Create: `internal/unrealpak/writer.go`
- Create: `internal/unrealpak/writer_test.go`

**Interfaces:**

- Consumes: `entryHeaderBytes` helper pattern from Task 3 (reimplemented inline, not exported — writer and reader tests each own their fixture code per repo convention of small focused files).
- Produces: `func Create(path string) (*Writer, error)`, `func (w *Writer) AddFile(mountPath string, data []byte) error`, `func (w *Writer) Close() error` — Task 5 and Task 12 depend on these exact names.

- [ ] **Step 1: Write the failing test**

```go
package unrealpak

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestWriter_CreateAndClose_ProducesValidFooter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.pak")
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.AddFile("Icarus/Content/Data/Test.json", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if len(data) <= footerSize {
		t.Fatalf("output is %d bytes, want more than a bare %d-byte footer", len(data), footerSize)
	}
	ft := data[len(data)-footerSize:]
	if got := binary.LittleEndian.Uint32(ft[17:21]); got != magic {
		t.Errorf("footer magic = %#x, want %#x", got, magic)
	}
	if got := int32(binary.LittleEndian.Uint32(ft[21:25])); got != writeVersion {
		t.Errorf("footer version = %d, want %d", got, writeVersion)
	}
	if ft[16] != 0 {
		t.Errorf("bEncryptedIndex = %d, want 0", ft[16])
	}
}

// Output must not depend on AddFile ordering — Close sorts by path.
func TestWriter_Close_IsDeterministic(t *testing.T) {
	build := func(order []string) []byte {
		t.Helper()
		path := filepath.Join(t.TempDir(), "out.pak")
		w, err := Create(path)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		for _, name := range order {
			if err := w.AddFile(name, []byte(name)); err != nil {
				t.Fatalf("AddFile(%s): %v", name, err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading output: %v", err)
		}
		return data
	}

	a := build([]string{"a/one.json", "b/two.json", "root.json"})
	b := build([]string{"root.json", "b/two.json", "a/one.json"})
	if !bytes.Equal(a, b) {
		t.Error("output differs with AddFile order; Close must be deterministic")
	}
}

func TestWriter_AddFile_RejectsDuplicatePath(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "out.pak"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer w.Close() //nolint:errcheck
	if err := w.AddFile("x.json", []byte("{}")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.AddFile("x.json", []byte("{}")); err == nil {
		t.Error("expected error adding a duplicate path, got nil")
	}
}

func TestWriter_AddFile_AfterClose_Errors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.pak")
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.AddFile("x.json", []byte("{}")); err == nil {
		t.Error("expected error adding file after Close, got nil")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/unrealpak/... -run TestWriter -v
```

Expected: FAIL (`Create` undefined).

- [ ] **Step 3: Implement `writer.go`**

Emits a **faithful version-11 pak**: the 221-byte footer, the primary index, the path-hash
index and the full directory index — the same four structures, in the same byte shapes, that
Icarus's own paks use and that its engine demonstrably loads. Matching the base game's own
version rather than an earlier "simplest version 7" choice is deliberate: these paks are
loaded by Icarus's actual UE runtime, not just this package's Reader, so staying
byte-shape-identical to what the engine already loads beats hoping a simpler layout is also
accepted.

Concretely, per the verified format (see `docs/plans/icarus-pak-format-findings.md`):

- Each entry's payload is preceded by the 53-byte stored `FPakEntry` header, `Offset` field
  zeroed (real paks do the same) and `Hash` set to the payload's SHA1.
- Each index record is the 12-byte stored encoded shape — flags `0xE0000000`, `uint32`
  offset, `uint32` size — byte-identical to the 4089 stored records in the real pakchunk0.
- The path-hash index uses the verified FNV-1a-64 recipe via `hashPath`, followed by an
  **empty** pruned directory index (`int32(0)`), the shape 33 of 34 real paks ship.
- Entries are packed contiguously with no alignment padding, which real paks do for 8385 of
  their 9294 adjacent pairs.

```go
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
```

`writeFString`, `splitMountPath`, `storedEntryHeader`, `buildPrimaryIndex`, `buildFooter`
and `hashPath` all already exist in `pak.go` (Task 2, Step 1) — no reimplementation needed.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/unrealpak/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/unrealpak/writer.go internal/unrealpak/writer_test.go
git commit -m "feat: add unrealpak writer (#136)"
```

---

## Task 5: `internal/unrealpak` — round-trip integration test

**Files:**

- Create: `internal/unrealpak/roundtrip_test.go`

**Interfaces:**

- Consumes: `Create`/`AddFile`/`Close` (Task 4), `Open`/`Files`/`ReadFile` (Tasks 2–3).
- Produces: nothing new — this is the acceptance gate proving the two halves agree on format, independent of any real game file.

- [ ] **Step 1: Write the round-trip test**

```go
package unrealpak

import (
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip_WriteThenRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.pak")
	files := map[string][]byte{
		"Icarus/Content/Data/AI-D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":235}}`),
		"Icarus/Content/Data/Other.json":         []byte(`{"foo":"bar"}`),
		"DataTableMetadata.json":                 []byte(`{"root":true}`), // root-level: "/" directory key
	}

	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for name, data := range files {
		if err := w.AddFile(name, data); err != nil {
			t.Fatalf("AddFile(%s): %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	got := r.Files()
	if len(got) != len(files) {
		t.Fatalf("got %d files, want %d", len(got), len(files))
	}
	for name, want := range files {
		data, err := r.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if string(data) != string(want) {
			t.Errorf("ReadFile(%s) = %q, want %q", name, data, want)
		}
	}
}

// Structural assertions on the bytes themselves. Open() proves the Reader
// accepts what the Writer emits, but the Reader is not the audience that
// matters most — Icarus's engine is. These check the properties every real pak
// exhibits, so a drift away from the engine-proven shape fails here rather than
// silently in-game.
func TestRoundTrip_StructuralShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shape.pak")
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	content := []byte(`{"a":1}`)
	if err := w.AddFile("Icarus/Content/Data/Test.json", content); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	ft := data[len(data)-footerSize:]
	indexOffset := int64(binary.LittleEndian.Uint64(ft[25:33]))
	indexSize := int64(binary.LittleEndian.Uint64(ft[33:41]))

	// The footer's SHA1 must cover the primary index exactly.
	indexSum := sha1.Sum(data[indexOffset : indexOffset+indexSize]) //nolint:gosec
	if string(indexSum[:]) != string(ft[41:61]) {
		t.Error("footer IndexHash does not match the primary index bytes")
	}

	// The data section holds one 53-byte header plus the payload, and the
	// index starts immediately after it — regions tile with no gap.
	if want := int64(storedHeaderSize + len(content)); indexOffset != want {
		t.Errorf("index starts at %d, want %d (53-byte header + %d-byte payload)",
			indexOffset, want, len(content))
	}
	// The per-entry header's Hash field must be the payload's SHA1.
	if sum := sha1.Sum(content); string(sum[:]) != string(data[28:48]) { //nolint:gosec
		t.Error("per-entry header Hash does not match the payload SHA1")
	}
	// Its Offset field is zero, as in every real pak.
	if got := binary.LittleEndian.Uint64(data[0:8]); got != 0 {
		t.Errorf("per-entry header Offset = %d, want 0", got)
	}
}
```

- [ ] **Step 2: Run to verify it passes**

```bash
go test ./internal/unrealpak/... -v
```

Expected: PASS. If it fails, the Writer and Reader disagree on layout — fix before proceeding to any Icarus-specific code, since everything downstream depends on this package being internally consistent.

- [ ] **Step 3: Commit**

```bash
git add internal/unrealpak/roundtrip_test.go
git commit -m "test: add unrealpak writer/reader round-trip coverage (#136)"
```

---

## Task 6: Icarus source — Firestore typed-value decoder

**Files:**

- Create: `internal/source/icarus/firestore_value.go`
- Create: `internal/source/icarus/firestore_value_test.go`

**Interfaces:**

- Produces: `func decodeFields(fields map[string]any) map[string]any` — Task 8's mapping code depends on this exact name/signature.

- [ ] **Step 1: Write the failing test**

```go
package icarus

import (
	"reflect"
	"testing"
)

func TestDecodeFields(t *testing.T) {
	// Shape of a real Firestore REST document's "fields" object.
	raw := map[string]any{
		"name":    map[string]any{"stringValue": "Bear Mount"},
		"version": map[string]any{"stringValue": "3.3"},
		"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{
			"pak":    map[string]any{"stringValue": "https://example.com/mod.pak"},
			"exmodz": map[string]any{"stringValue": "https://example.com/mod.exmodz"},
		}}},
		"missing": map[string]any{"nullValue": nil},
	}

	got := decodeFields(raw)

	want := map[string]any{
		"name":    "Bear Mount",
		"version": "3.3",
		"files": map[string]any{
			"pak":    "https://example.com/mod.pak",
			"exmodz": "https://example.com/mod.exmodz",
		},
		"missing": nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeFields() = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/source/icarus/... -run TestDecodeFields -v
```

Expected: FAIL (`decodeFields` undefined, package may not exist yet — create the directory as part of this step).

- [ ] **Step 3: Implement**

```go
package icarus

// decodeFields unwraps a Firestore REST document's typed-value "fields"
// object (each value wrapped as {"stringValue": ...} / {"mapValue": {...}} /
// etc.) into plain Go values. Only the value kinds this catalog's schema
// actually uses are handled; anything else decodes to nil rather than
// panicking, since an unrecognized field should be ignorable, not fatal.
func decodeFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = decodeValue(v)
	}
	return out
}

func decodeValue(v any) any {
	wrapped, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	if s, ok := wrapped["stringValue"]; ok {
		return s
	}
	if b, ok := wrapped["booleanValue"]; ok {
		return b
	}
	if i, ok := wrapped["integerValue"]; ok {
		return i
	}
	if d, ok := wrapped["doubleValue"]; ok {
		return d
	}
	if m, ok := wrapped["mapValue"]; ok {
		mv, _ := m.(map[string]any)
		inner, _ := mv["fields"].(map[string]any)
		return decodeFields(inner)
	}
	if a, ok := wrapped["arrayValue"]; ok {
		av, _ := a.(map[string]any)
		values, _ := av["values"].([]any)
		out := make([]any, len(values))
		for i, item := range values {
			out[i] = decodeValue(item)
		}
		return out
	}
	if _, ok := wrapped["nullValue"]; ok {
		return nil
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/firestore_value.go internal/source/icarus/firestore_value_test.go
git commit -m "feat: add Firestore typed-value decoder for Icarus source (#136)"
```

---

## Task 7: Icarus source — Firestore REST client

**Files:**

- Create: `internal/source/icarus/firestore_client.go`
- Create: `internal/source/icarus/firestore_client_test.go`

**Interfaces:**

- Consumes: `decodeFields` (Task 6).
- Produces: `type firestoreDoc struct { ID string; Fields map[string]any }`, `func (c *firestoreClient) listCollection(ctx context.Context, collection string) ([]firestoreDoc, error)`, `func (c *firestoreClient) getDocument(ctx context.Context, collection, docID string) (*firestoreDoc, error)`, `func newFirestoreClient(projectID string, httpClient *http.Client) *firestoreClient` — Task 8 depends on all four names.

- [ ] **Step 1: Write the failing test**

```go
package icarus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFirestoreClient_ListCollection_Paginates(t *testing.T) {
	pages := []map[string]any{
		{
			"documents": []map[string]any{
				{"name": "projects/p/databases/(default)/documents/mods/abc", "fields": map[string]any{"name": map[string]any{"stringValue": "Bear Mount"}}},
			},
			"nextPageToken": "page2",
		},
		{
			"documents": []map[string]any{
				{"name": "projects/p/databases/(default)/documents/mods/def", "fields": map[string]any{"name": map[string]any{"stringValue": "Wolf Mount"}}},
			},
		},
	}
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := pages[callCount]
		callCount++
		json.NewEncoder(w).Encode(page) //nolint:errcheck
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	c.baseURL = srv.URL // test seam, see Step 3

	docs, err := c.listCollection(context.Background(), "mods")
	if err != nil {
		t.Fatalf("listCollection: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2 (pagination should have followed nextPageToken)", len(docs))
	}
	if docs[0].ID != "abc" || docs[1].ID != "def" {
		t.Errorf("doc IDs = %q, %q, want abc, def", docs[0].ID, docs[1].ID)
	}
	if docs[0].Fields["name"] != "Bear Mount" {
		t.Errorf("docs[0].Fields[name] = %v, want Bear Mount", docs[0].Fields["name"])
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (one per page)", callCount)
	}
}

func TestFirestoreClient_GetDocument_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newFirestoreClient("test-project", srv.Client())
	c.baseURL = srv.URL

	_, err := c.getDocument(context.Background(), "mods", "missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/source/icarus/... -run TestFirestoreClient -v
```

Expected: FAIL (`newFirestoreClient` undefined).

- [ ] **Step 3: Implement**

```go
package icarus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const defaultFirestoreBaseURL = "https://firestore.googleapis.com/v1"

// firestoreDoc is a decoded Firestore document: ID is the last path segment
// of its resource name, Fields is already unwrapped via decodeFields.
type firestoreDoc struct {
	ID     string
	Fields map[string]any
}

type firestoreClient struct {
	projectID  string
	httpClient *http.Client
	baseURL    string // overridable in tests; defaults to defaultFirestoreBaseURL
}

func newFirestoreClient(projectID string, httpClient *http.Client) *firestoreClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &firestoreClient{projectID: projectID, httpClient: httpClient, baseURL: defaultFirestoreBaseURL}
}

func (c *firestoreClient) documentsURL() string {
	return fmt.Sprintf("%s/projects/%s/databases/(default)/documents", c.baseURL, c.projectID)
}

// listCollection fetches every document in collection, following
// nextPageToken until exhausted (the catalog reads Firestore unauthenticated
// and public, with no server-side query support in play — see the design
// doc's "fetch-all + filter client-side" decision).
func (c *firestoreClient) listCollection(ctx context.Context, collection string) ([]firestoreDoc, error) {
	var all []firestoreDoc
	pageToken := ""
	for {
		url := fmt.Sprintf("%s/%s?pageSize=200", c.documentsURL(), collection)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}
		var page struct {
			Documents []struct {
				Name   string         `json:"name"`
				Fields map[string]any `json:"fields"`
			} `json:"documents"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, url, &page); err != nil {
			return nil, fmt.Errorf("listing %s: %w", collection, err)
		}
		for _, d := range page.Documents {
			all = append(all, firestoreDoc{ID: lastPathSegment(d.Name), Fields: decodeFields(d.Fields)})
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return all, nil
}

// getDocument fetches a single document by ID.
func (c *firestoreClient) getDocument(ctx context.Context, collection, docID string) (*firestoreDoc, error) {
	url := fmt.Sprintf("%s/%s/%s", c.documentsURL(), collection, docID)
	var doc struct {
		Name   string         `json:"name"`
		Fields map[string]any `json:"fields"`
	}
	if err := c.getJSON(ctx, url, &doc); err != nil {
		return nil, fmt.Errorf("fetching %s/%s: %w", collection, docID, err)
	}
	return &firestoreDoc{ID: lastPathSegment(doc.Name), Fields: decodeFields(doc.Fields)}, nil
}

func (c *firestoreClient) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func lastPathSegment(resourceName string) string {
	parts := strings.Split(resourceName, "/")
	return parts[len(parts)-1]
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/firestore_client.go internal/source/icarus/firestore_client_test.go
git commit -m "feat: add Firestore REST client for Icarus source (#136)"
```

---

## Task 8: Icarus source — `ModSource` implementation

**Files:**

- Create: `internal/source/icarus/icarus.go`
- Create: `internal/source/icarus/icarus_test.go`

**Interfaces:**

- Consumes: `newFirestoreClient`, `listCollection`, `getDocument` (Task 7); `source.ModSource`, `source.SearchQuery`, `source.SearchResult`, `source.ErrNotSupported`, `domain.Mod`, `domain.DownloadableFile`, `domain.ModReference`, `domain.InstalledMod`, `domain.Update` (existing repo types, confirmed above).
- Produces: `func New(httpClient *http.Client, projectID string) *Icarus`, satisfying `source.ModSource` and `source.CapabilityReporter` — Task 9 depends on this constructor signature.

- [ ] **Step 1: Write the failing test**

```go
package icarus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

func modsListHandler(mods []map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docs := make([]map[string]any, len(mods))
		for i, m := range mods {
			docs[i] = map[string]any{
				"name":   "projects/p/databases/(default)/documents/mods/" + m["id"].(string),
				"fields": m["fields"],
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"documents": docs}) //nolint:errcheck
	}
}

func TestIcarus_Search_FiltersClientSide(t *testing.T) {
	srv := httptest.NewServer(modsListHandler([]map[string]any{
		{"id": "abc", "fields": map[string]any{
			"name": map[string]any{"stringValue": "Bear Mount"}, "author": map[string]any{"stringValue": "Jimk72"},
			"description": map[string]any{"stringValue": "Ride a bear"}, "version": map[string]any{"stringValue": "3.3"},
			"compatibility": map[string]any{"stringValue": "w57"},
			"files":         map[string]any{"mapValue": map[string]any{"fields": map[string]any{"exmodz": map[string]any{"stringValue": "https://x/bear.exmodz"}}}},
		}},
		{"id": "def", "fields": map[string]any{
			"name": map[string]any{"stringValue": "Wolf Pack"}, "author": map[string]any{"stringValue": "Someone"},
			"description": map[string]any{"stringValue": "Tame wolves"}, "version": map[string]any{"stringValue": "1.0"},
			"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{"pak": map[string]any{"stringValue": "https://x/wolf.pak"}}}},
		}},
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	result, err := src.Search(context.Background(), source.SearchQuery{Query: "bear"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Mods) != 1 || result.Mods[0].Name != "Bear Mount" {
		t.Fatalf("Search(%q) = %+v, want exactly Bear Mount", "bear", result.Mods)
	}
	if result.Mods[0].GameID != "icarus" {
		t.Errorf("GameID = %q, want icarus", result.Mods[0].GameID)
	}
}

func TestIcarus_GetModFiles_ReturnsExmodzAndPak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"name": "projects/p/databases/(default)/documents/mods/abc",
			"fields": map[string]any{
				"name": map[string]any{"stringValue": "Bear Mount"},
				"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{
					"exmodz": map[string]any{"stringValue": "https://x/bear.exmodz"},
				}}},
			},
		})
	}))
	defer srv.Close()

	src := New(srv.Client(), "test-project")
	src.firestore.baseURL = srv.URL

	files, err := src.GetModFiles(context.Background(), &domain.Mod{ID: "abc", GameID: "icarus"})
	if err != nil {
		t.Fatalf("GetModFiles: %v", err)
	}
	if len(files) != 1 || files[0].FileName != "bear.exmodz" {
		t.Fatalf("files = %+v, want one bear.exmodz entry", files)
	}
	if !files[0].IsPrimary {
		t.Error("single file should be marked primary")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/source/icarus/... -run TestIcarus -v
```

Expected: FAIL (`New` undefined).

- [ ] **Step 3: Implement `icarus.go`**

```go
package icarus

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// gameID is fixed: the Firestore database this source reads is Icarus-only.
const gameID = "icarus"

// Icarus is a ModSource backed by the public, unauthenticated Firestore REST
// API described in docs/plans/2026-07-29-icarus-exmod-pak-research.md.
type Icarus struct {
	firestore *firestoreClient
}

// New constructs an Icarus source. projectID is the Firestore project ID
// (from the Firebase console) — passed explicitly rather than hard-coded so
// tests can point at an httptest server and so the real value lives in one
// place at the call site (Task 9), not buried in this package.
func New(httpClient *http.Client, projectID string) *Icarus {
	return &Icarus{firestore: newFirestoreClient(projectID, httpClient)}
}

var (
	_ source.ModSource          = (*Icarus)(nil)
	_ source.CapabilityReporter = (*Icarus)(nil)
)

func (s *Icarus) ID() string   { return "icarus" }
func (s *Icarus) Name() string { return "Icarus (Project Daedalus)" }

// AuthURL/ExchangeToken: unsupported — Firestore reads here are public.
func (s *Icarus) AuthURL() string { return "" }
func (s *Icarus) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", s.ID(), source.ErrNotSupported)
}

// GetDependencies: the modinfo.json v2 schema has no dependency field.
func (s *Icarus) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", s.ID(), source.ErrNotSupported)
}

func (s *Icarus) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Dependencies: false, Updates: true, Auth: false}
}

func (s *Icarus) TypeLabel() string { return "built-in" }

// Search fetches the whole mods collection and filters client-side — this
// catalog has no server-side query support to speak of, matching
// project_daedalus's own ModsController#find_mods approach.
func (s *Icarus) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	docs, err := s.firestore.listCollection(ctx, "mods")
	if err != nil {
		return source.SearchResult{}, fmt.Errorf("source %q: searching: %w", s.ID(), err)
	}

	var mods []domain.Mod
	q := strings.ToLower(query.Query)
	for _, d := range docs {
		m := mapDoc(d)
		if q == "" || strings.Contains(strings.ToLower(m.Name), q) ||
			strings.Contains(strings.ToLower(m.Author), q) ||
			strings.Contains(strings.ToLower(m.Description), q) {
			mods = append(mods, m)
		}
	}

	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	page := query.Page
	if page < 0 {
		page = 0
	}
	start := page * pageSize
	if start > len(mods) {
		start = len(mods)
	}
	end := start + pageSize
	if end > len(mods) {
		end = len(mods)
	}

	return source.SearchResult{Mods: mods[start:end], TotalCount: len(mods), Page: page, PageSize: pageSize}, nil
}

func (s *Icarus) GetMod(ctx context.Context, queryGameID, modID string) (*domain.Mod, error) {
	doc, err := s.firestore.getDocument(ctx, "mods", modID)
	if err != nil {
		return nil, fmt.Errorf("source %q: fetching mod %s: %w", s.ID(), modID, err)
	}
	m := mapDoc(*doc)
	return &m, nil
}

// GetModFiles returns the mod's downloadable files (pak and/or exmodz — see
// modinfo.json v2 schema). A single file is marked primary, matching the
// existing custom.API convention.
func (s *Icarus) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	doc, err := s.firestore.getDocument(ctx, "mods", mod.ID)
	if err != nil {
		return nil, fmt.Errorf("source %q: listing files for %s: %w", s.ID(), mod.ID, err)
	}
	filesField, _ := doc.Fields["files"].(map[string]any)
	var out []domain.DownloadableFile
	for _, kind := range []string{"pak", "exmodz"} {
		rawURL, ok := filesField[kind].(string)
		if !ok || rawURL == "" {
			continue
		}
		out = append(out, domain.DownloadableFile{
			ID:       kind,
			Name:     kind,
			FileName: fileNameFromURL(rawURL, kind),
			Category: strings.ToUpper(kind),
		})
	}
	if len(out) == 1 {
		out[0].IsPrimary = true
	}
	return out, nil
}

// GetDownloadURL re-fetches the mod document and returns the stored URL for
// fileID ("pak" or "exmodz") directly — no signing, matching a static-URL
// catalog rather than an OAuth-gated one.
func (s *Icarus) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	doc, err := s.firestore.getDocument(ctx, "mods", mod.ID)
	if err != nil {
		return "", fmt.Errorf("source %q: download URL for %s: %w", s.ID(), fileID, err)
	}
	filesField, _ := doc.Fields["files"].(map[string]any)
	rawURL, ok := filesField[fileID].(string)
	if !ok || rawURL == "" {
		return "", fmt.Errorf("source %q: file %s: no download URL", s.ID(), fileID)
	}
	return rawURL, nil
}

// CheckUpdates compares each installed mod's stored version against the
// catalog's current version string (semantic-ish, per modinfo.json's
// "recommended" versioning note — not guaranteed strictly semver, so this
// uses domain.IsNewerVersion the same way custom.API does).
func (s *Icarus) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var updates []domain.Update
	var errs []error
	for _, inst := range installed {
		select {
		case <-ctx.Done():
			return updates, ctx.Err()
		default:
		}
		current, err := s.GetMod(ctx, gameID, inst.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if domain.IsNewerVersion(inst.Version, current.Version) {
			updates = append(updates, domain.Update{InstalledMod: inst, NewVersion: current.Version})
		}
	}
	if len(errs) > 0 {
		return updates, fmt.Errorf("source %q: %d update check(s) failed: %v", s.ID(), len(errs), errs[0])
	}
	return updates, nil
}

// mapDoc converts a decoded Firestore document into domain.Mod per the
// modinfo.json v2 schema (docs/plans/2026-07-29-icarus-exmod-pak-research.md).
func mapDoc(d firestoreDoc) domain.Mod {
	str := func(key string) string {
		s, _ := d.Fields[key].(string)
		return s
	}
	return domain.Mod{
		ID:          d.ID,
		SourceID:    "icarus",
		GameID:      gameID,
		Name:        str("name"),
		Author:      str("author"),
		Version:     str("version"),
		Category:    str("compatibility"), // Icarus week-build string, e.g. "w57"
		Description: str("description"),
		PictureURL:  str("imageURL"),
		SourceURL:   str("readmeURL"),
	}
}

func fileNameFromURL(rawURL, fallbackExt string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" {
		return fallbackExt
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" {
		return fallbackExt
	}
	return base
}

var _ = strconv.Itoa // silence unused import if strconv ends up unused after edits; remove if genuinely unused
```

(Drop the trailing `var _ = strconv.Itoa` line and the `strconv` import if `go vet`/`goimports` flags it as unused once the real file is assembled — included here only as a reminder to check, not to ship.)

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/icarus.go internal/source/icarus/icarus_test.go
git commit -m "feat: implement Icarus ModSource over Firestore REST (#136)"
```

---

## Task 9: Register Icarus as a built-in source

**Files:**

- Modify: `cmd/lmm/root.go:213-216` (`builtinSourceFactories`)
- Modify: `README.md` (document the new source + required manual `games.yaml` entry, per this repo's "keep README updated when functional components change" convention)

**Interfaces:**

- Consumes: `icarus.New(httpClient *http.Client, projectID string) *Icarus` (Task 8).
- Produces: nothing new — this is wiring only.

- [ ] **Step 1: Add the factory**

In `cmd/lmm/root.go`, add the import and extend `builtinSourceFactories`:

```go
import (
	// ...existing imports...
	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
)

// icarusFirestoreProjectID is Project Daedalus's Firebase project ID, from
// the Firebase console. It is public information (Firestore reads are
// unauthenticated by design, per the research spike) — this constant is the
// one place it needs to be substituted with the real value (see "Post-plan
// manual validation" item 2 at the end of this plan).
const icarusFirestoreProjectID = "project-daedalus"

var builtinSourceFactories = []func() source.ModSource{
	func() source.ModSource { return nexusmods.New(nil, "") },
	func() source.ModSource { return curseforge.New(nil, "") },
	func() source.ModSource { return icarus.New(nil, icarusFirestoreProjectID) },
}
```

- [ ] **Step 2: Write a registration smoke test**

Add to `cmd/lmm/root_test.go` (or wherever `builtinSourceFactories` is already exercised — check for an existing test asserting NexusMods/CurseForge register cleanly, and follow its shape):

```go
func TestBuiltinSourceFactories_IncludesIcarus(t *testing.T) {
	found := false
	for _, factory := range builtinSourceFactories {
		if factory().ID() == "icarus" {
			found = true
		}
	}
	if !found {
		t.Error("builtinSourceFactories should include the icarus source")
	}
}
```

- [ ] **Step 3: Run to verify it passes**

```bash
go build ./... && go test ./cmd/lmm/... -run TestBuiltinSourceFactories -v
```

Expected: PASS.

- [ ] **Step 4: Document the manual game config in README.md**

Add an entry alongside this repo's existing per-game setup examples (find the section documenting `games.yaml` entries for existing sources and follow its exact format) showing:

```yaml
games:
  icarus:
    name: Icarus
    install_path: /path/to/Steam/steamapps/common/Icarus
    mod_path: /path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods
    deploy_mode: compile # added in Task 13
    source_ids:
      icarus: icarus
```

Note in prose: Steam auto-detection (`lmm game detect`) does not yet know about Icarus — this is a manual `games.yaml` entry for now (App ID 1149460 confirmed during the research spike; auto-detection is a separate, smaller follow-up not covered by this plan).

- [ ] **Step 5: Commit**

```bash
git add cmd/lmm/root.go cmd/lmm/root_test.go README.md
git commit -m "feat: register Icarus as a built-in mod source (#136)"
```

---

## Task 10: `.EXMOD` diff schema + row-patch application

**Files:**

- Create: `internal/source/icarus/exmod.go`
- Create: `internal/source/icarus/exmod_test.go`

**Interfaces:**

- Produces: `type ExmodDiff struct { Name, Author, Version, Description string; Rows []ExmodRow }`, `type ExmodRow struct { CurrentFile string; FileItems []ExmodFileItem }`, `type ExmodFileItem struct { Name string; Fields map[string]any }`, `func ParseExmod(data []byte) (*ExmodDiff, error)`, `func ApplyRowPatch(baseJSON []byte, row ExmodRow) ([]byte, error)` — Task 12 depends on all of these.

- [ ] **Step 1: Write the failing test**

```go
package icarus

import (
	"encoding/json"
	"testing"
)

const sampleExmod = `{
  "name": "Bear Mount",
  "author": "Jimk72",
  "version": "3.3",
  "description": "Allows raising cubs",
  "Rows": [
    {
      "CurrentFile": "AI-D_AIGrowth.json",
      "File_Items": [
        {"Name": "Mount_Bear", "BaseMovementSpeed": 235, "BaseSwimSpeed": 300}
      ]
    }
  ]
}`

func TestParseExmod(t *testing.T) {
	diff, err := ParseExmod([]byte(sampleExmod))
	if err != nil {
		t.Fatalf("ParseExmod: %v", err)
	}
	if diff.Name != "Bear Mount" || diff.Version != "3.3" {
		t.Errorf("Name/Version = %q/%q, want Bear Mount/3.3", diff.Name, diff.Version)
	}
	if len(diff.Rows) != 1 || diff.Rows[0].CurrentFile != "AI-D_AIGrowth.json" {
		t.Fatalf("Rows = %+v", diff.Rows)
	}
	if len(diff.Rows[0].FileItems) != 1 || diff.Rows[0].FileItems[0].Name != "Mount_Bear" {
		t.Fatalf("FileItems = %+v", diff.Rows[0].FileItems)
	}
	if diff.Rows[0].FileItems[0].Fields["BaseMovementSpeed"] != float64(235) {
		t.Errorf("BaseMovementSpeed = %v, want 235", diff.Rows[0].FileItems[0].Fields["BaseMovementSpeed"])
	}
}

func TestApplyRowPatch_OverwritesNamedRowFieldsOnly(t *testing.T) {
	base := []byte(`{
		"Mount_Bear": {"BaseMovementSpeed": 200, "BaseSwimSpeed": 150, "Untouched": "keep-me"},
		"Other_Row": {"BaseMovementSpeed": 999}
	}`)
	row := ExmodRow{
		CurrentFile: "AI-D_AIGrowth.json",
		FileItems: []ExmodFileItem{
			{Name: "Mount_Bear", Fields: map[string]any{"BaseMovementSpeed": float64(235)}},
		},
	}

	got, err := ApplyRowPatch(base, row)
	if err != nil {
		t.Fatalf("ApplyRowPatch: %v", err)
	}

	var result map[string]map[string]any
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if result["Mount_Bear"]["BaseMovementSpeed"] != float64(235) {
		t.Errorf("BaseMovementSpeed not patched: %v", result["Mount_Bear"]["BaseMovementSpeed"])
	}
	if result["Mount_Bear"]["Untouched"] != "keep-me" {
		t.Errorf("unrelated field was clobbered: %v", result["Mount_Bear"]["Untouched"])
	}
	if result["Other_Row"]["BaseMovementSpeed"] != float64(999) {
		t.Errorf("unrelated row was modified: %v", result["Other_Row"])
	}
}

func TestApplyRowPatch_UnknownRowName_Errors(t *testing.T) {
	base := []byte(`{"Mount_Bear": {}}`)
	row := ExmodRow{FileItems: []ExmodFileItem{{Name: "Does_Not_Exist", Fields: map[string]any{"X": 1}}}}

	if _, err := ApplyRowPatch(base, row); err == nil {
		t.Error("expected error for unknown row name (no silent fallback), got nil")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/source/icarus/... -run "TestParseExmod|TestApplyRowPatch" -v
```

Expected: FAIL (`ParseExmod`/`ApplyRowPatch` undefined).

- [ ] **Step 3: Implement**

The real sample's `File_Items` entries mix a fixed `Name` key with arbitrary game-specific override keys in the same object (see `Bear_Mount.EXMOD`, where some entries additionally nest a `Base` sub-object of `(Value="...")`-keyed stats — this plan's `ExmodFileItem.Fields` deliberately captures "everything except Name" generically, so both shapes round-trip through `json.RawMessage`/`any` without this package needing to special-case every game-data shape it might see):

```go
package icarus

import (
	"encoding/json"
	"fmt"
)

// ExmodDiff is the parsed .EXMOD manifest — a diff against the base game's
// JSON data tables, not a binary/compiled-asset diff (confirmed against a
// real sample; see docs/plans/2026-07-29-icarus-exmod-pak-research.md).
type ExmodDiff struct {
	Name        string
	Author      string
	Version     string
	Description string
	Rows        []ExmodRow
}

// ExmodRow targets one base data-table file (e.g. "AI-D_AIGrowth.json").
type ExmodRow struct {
	CurrentFile string
	FileItems   []ExmodFileItem
}

// ExmodFileItem overrides fields on the base row named Name. Fields holds
// every key from the source JSON except "Name" itself, generically — the
// real schema nests arbitrary game-data shapes here (see package doc
// comment), so this deliberately does not enumerate them.
type ExmodFileItem struct {
	Name   string
	Fields map[string]any
}

func ParseExmod(data []byte) (*ExmodDiff, error) {
	var raw struct {
		Name        string `json:"name"`
		Author      string `json:"author"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Rows        []struct {
			CurrentFile string                   `json:"CurrentFile"`
			FileItems   []map[string]any         `json:"File_Items"`
		} `json:"Rows"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("icarus: parsing .EXMOD: %w", err)
	}

	diff := &ExmodDiff{Name: raw.Name, Author: raw.Author, Version: raw.Version, Description: raw.Description}
	for _, r := range raw.Rows {
		row := ExmodRow{CurrentFile: r.CurrentFile}
		for _, item := range r.FileItems {
			name, _ := item["Name"].(string)
			if name == "" {
				return nil, fmt.Errorf("icarus: .EXMOD row in %s: File_Items entry missing Name", r.CurrentFile)
			}
			fields := make(map[string]any, len(item)-1)
			for k, v := range item {
				if k == "Name" {
					continue
				}
				fields[k] = v
			}
			row.FileItems = append(row.FileItems, ExmodFileItem{Name: name, Fields: fields})
		}
		diff.Rows = append(diff.Rows, row)
	}
	return diff, nil
}

// ApplyRowPatch merges row's named-row field overrides into baseJSON (a base
// game data-table file keyed by row name, e.g. {"Mount_Bear": {...}, ...})
// and returns the patched document. Fails loudly (no silent fallback, repo
// precedent #95) if a targeted row name doesn't exist in the base — that
// means either the base version is stale relative to the mod, or the exmod
// targets a file this function was called with by mistake.
func ApplyRowPatch(baseJSON []byte, row ExmodRow) ([]byte, error) {
	var doc map[string]map[string]any
	if err := json.Unmarshal(baseJSON, &doc); err != nil {
		return nil, fmt.Errorf("icarus: parsing base data table %s: %w", row.CurrentFile, err)
	}
	for _, item := range row.FileItems {
		target, ok := doc[item.Name]
		if !ok {
			return nil, fmt.Errorf("icarus: %s: row %q not found in base data table", row.CurrentFile, item.Name)
		}
		for k, v := range item.Fields {
			target[k] = v
		}
		doc[item.Name] = target
	}
	return json.Marshal(doc)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/exmod.go internal/source/icarus/exmod_test.go
git commit -m "feat: add .EXMOD parsing and row-patch application (#136)"
```

---

## Task 11: `.EXMODZ` archive unpacking

**Files:**

- Create: `internal/source/icarus/exmodz.go`
- Create: `internal/source/icarus/exmodz_test.go`

**Interfaces:**

- Consumes: `ParseExmod` (Task 10).
- Produces: `type ExmodzBundle struct { Diff *ExmodDiff; Assets map[string][]byte }`, `func ParseExmodz(zipData []byte) (*ExmodzBundle, error)` — Task 12 depends on this.

- [ ] **Step 1: Write the failing test**

```go
package icarus

import (
	"archive/zip"
	"bytes"
	"testing"
)

// buildTestExmodz mirrors the real Bear_Mount.EXMODZ layout: a manifest
// under "Extracted Mods/<name>.EXMOD" plus loose asset files at paths that
// mirror in-game mount structure.
func buildTestExmodz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifest := `{"name":"Bear Mount","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	w, err := zw.Create("Extracted Mods/Bear_Mount.EXMOD")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(manifest)) //nolint:errcheck

	assetW, err := zw.Create("Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset")
	if err != nil {
		t.Fatal(err)
	}
	assetW.Write([]byte("fake-uasset-bytes")) //nolint:errcheck

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseExmodz(t *testing.T) {
	bundle, err := ParseExmodz(buildTestExmodz(t))
	if err != nil {
		t.Fatalf("ParseExmodz: %v", err)
	}
	if bundle.Diff == nil || bundle.Diff.Name != "Bear Mount" {
		t.Fatalf("Diff = %+v", bundle.Diff)
	}
	asset, ok := bundle.Assets["Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset"]
	if !ok {
		t.Fatalf("Assets missing expected key; got keys: %v", mapKeys(bundle.Assets))
	}
	if string(asset) != "fake-uasset-bytes" {
		t.Errorf("asset content = %q", asset)
	}
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestParseExmodz_NoManifest_Errors(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	w.Write([]byte("no manifest here")) //nolint:errcheck
	zw.Close()                          //nolint:errcheck

	if _, err := ParseExmodz(buf.Bytes()); err == nil {
		t.Error("expected error when no .EXMOD manifest is present, got nil")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/source/icarus/... -run TestParseExmodz -v
```

Expected: FAIL (`ParseExmodz` undefined).

- [ ] **Step 3: Implement**

```go
package icarus

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// ExmodzBundle is a parsed .EXMODZ: the diff manifest plus any pre-built
// asset files the mod author already compiled (placed as-is into the output
// pak — never recompiled by LMM).
type ExmodzBundle struct {
	Diff   *ExmodDiff
	Assets map[string][]byte // zip-internal path -> raw content, manifest/readme/image excluded
}

// ParseExmodz unpacks zipData (an in-memory .EXMODZ) into its manifest and
// bundled assets. The manifest lives at "Extracted Mods/<name>.EXMOD" in
// every sample seen so far; this looks for any "*.EXMOD" file under an
// "Extracted Mods/" prefix rather than hard-coding the mod name, since that
// varies per mod.
func ParseExmodz(zipData []byte) (*ExmodzBundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("icarus: opening .EXMODZ: %w", err)
	}

	bundle := &ExmodzBundle{Assets: make(map[string][]byte)}
	var manifestPath string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "Extracted Mods/") && strings.HasSuffix(f.Name, ".EXMOD") {
			manifestPath = f.Name
			data, err := readZipFile(f)
			if err != nil {
				return nil, fmt.Errorf("icarus: reading %s: %w", f.Name, err)
			}
			bundle.Diff, err = ParseExmod(data)
			if err != nil {
				return nil, err
			}
			continue
		}
	}
	if manifestPath == "" {
		return nil, fmt.Errorf("icarus: .EXMODZ has no Extracted Mods/*.EXMOD manifest")
	}

	for _, f := range zr.File {
		if f.Name == manifestPath || f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name, ".uasset") && !strings.HasSuffix(f.Name, ".uexp") {
			continue // skip readme/image/other non-asset files — never placed into the output pak
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("icarus: reading asset %s: %w", f.Name, err)
		}
		bundle.Assets[f.Name] = data
	}

	return bundle, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck
	return io.ReadAll(rc)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/exmodz.go internal/source/icarus/exmodz_test.go
git commit -m "feat: add .EXMODZ archive unpacking (#136)"
```

---

## Task 12a: Icarus source — base-table dump fetcher + build detection (do BEFORE Task 12)

Added in rev3. Numbered `12a` so existing task numbers keep their meaning; it is a
prerequisite of Task 12, not a follow-up.

This is the component that resolves the Oodle blocker: it fetches the base data tables from
the community's per-week dump, works out which build is installed, and refuses to hand
Task 12 a dump that does not match that build. All of it is grounded in spike 3 — see
`docs/plans/icarus-pak-format-findings.md` Part 3 for the fetched URLs and measurements.

**Files:**

- Create: `internal/source/icarus/datadump.go`
- Create: `internal/source/icarus/datadump_test.go`

**Interfaces:**

- Consumes: `unrealpak.Open`/`Reader.Files`/`Reader.ReadFile` (Tasks 2–3) for validation;
  the repo's shared `httpclient` conventions, same as Task 7's Firestore client.
- Produces: `type Build`, `type Dump`, `type DumpStore`,
  `func newDumpStore(cacheDir string, httpClient *http.Client) *DumpStore`,
  `func (s *DumpStore) DumpForBuild(ctx context.Context, basePakPath, localDumpDir string) (*Dump, error)`,
  `func detectBuild(installRoot string) (Build, error)` — Task 12 depends on these names.

**Design notes (why it looks like this):**

- **Week resolution is content-based, not name-based.** Nothing in the install records a
  week number: `Icarus/Config/version.json` gives `3.0.21.155335`, and Steam's appmanifest
  gives a `buildid`, but neither says "Week 243". Steam's news feed does map versions to
  weeks, but only in prose titles that will drift. So the store fetches a candidate dump and
  _proves_ it matches by byte-comparing the tables `data.pak` stores uncompressed — the 40
  entries readable without Oodle. Exact, offline, and no prose parsing.
- **One dump-tree download, not 298 file fetches.** The tarball is 36 MB and lands in ~4 s;
  per-file fetching would be hundreds of round trips.
- **LF → CRLF on ingest.** Dump blobs are LF (committed with autocrlf); shipped paks are
  CRLF. Restoring CRLF reproduces shipped bytes exactly. Doing it once at ingest keeps the
  rest of the pipeline free of encoding special cases. A local dump directory may already
  hold CRLF (QuickBMS writes what the pak stored), so the conversion is idempotent —
  CRLF is normalized to LF first, then back — and both sources land in the same shape.
- **Hosted dump primary, local directory override (rev4 — USER DECISION).** A user may point
  the pipeline at a directory holding their own unpacked `data.pak` JSON tree (QuickBMS
  output, IMM's extracted `data` folder, anything with the same layout). When set, that
  directory is used **instead of** the network fetch — same tables, different transport.
  This exists because the hosted dump can lag the installed game (it was 7 weeks behind at
  spike time), and a user who can unpack their own pak should not be blocked on a third
  party. It also makes offline compiles possible.
- **Validation is identical for both sources.** `validateDump` runs on whatever was loaded,
  hosted or local. A local directory from the wrong week fails exactly as loudly as a stale
  hosted dump, naming the disagreeing tables. The override changes _where tables come from_,
  never _whether they are checked_ — silently trusting a user-supplied directory is the
  precise failure this gate exists to prevent.

- [ ] **Step 1: Write the failing tests**

Serve a synthetic dump tarball and a synthetic pak from `httptest`/`t.TempDir()` so the
tests never touch the network or a real install.

```go
package icarus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGz builds a dump-shaped tarball: a single top-level directory, then the
// table tree beneath it, LF-terminated exactly as the real repo stores it.
func tarGz(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		hdr := &tar.Header{Name: root + "/" + name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetectBuild_ReadsVersionJSON(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "Icarus", "Config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	const vjson = `{"Name":"Icarus","Version":{"Major":3,"Minor":0,"Patch":21,` +
		`"Changelist":155335,"BuildType":"Shipping","FeatureLevel":"DangerousHorizons"},` +
		`"Data":{"Changelist":155151}}`
	if err := os.WriteFile(filepath.Join(cfg, "version.json"), []byte(vjson), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := detectBuild(root)
	if err != nil {
		t.Fatalf("detectBuild: %v", err)
	}
	if got := b.String(); got != "3.0.21.155335" {
		t.Errorf("Build.String() = %q, want 3.0.21.155335", got)
	}
	if b.DataChangelist != 155151 {
		t.Errorf("DataChangelist = %d, want 155151", b.DataChangelist)
	}
}

func TestDetectBuild_MissingVersionFile_Errors(t *testing.T) {
	if _, err := detectBuild(t.TempDir()); err == nil {
		t.Fatal("expected error when version.json is absent, got nil")
	}
}

// A dump whose stored tables match the local pak byte-for-byte (after CRLF
// restoration) is accepted, and its tables are exposed with shipped bytes.
func TestDumpStore_DumpForBuild_AcceptsMatchingDump(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	shipped := []byte("{\r\n    \"Rows\": []\r\n}") // CRLF, as the pak stores it
	dumped := "{\n    \"Rows\": []\n}"              // LF, as the repo stores it

	pak := writeTestBasePak(t, map[string][]byte{rel: shipped}) // Task 12's helper
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-abc123", map[string]string{rel: dumped}))
	}))
	defer srv.Close()

	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL // test seam

	dump, err := store.DumpForBuild(context.Background(), pak, "")
	if err != nil {
		t.Fatalf("DumpForBuild: %v", err)
	}
	got, ok := dump.Table(rel)
	if !ok {
		t.Fatalf("dump has no table %q", rel)
	}
	if !bytes.Equal(got, shipped) {
		t.Errorf("table bytes = %q, want the shipped CRLF form %q", got, shipped)
	}
}

// The case that is live today: the newest dump is an older week than the
// install. Must fail loudly and name what disagreed.
func TestDumpStore_DumpForBuild_RejectsWrongWeek(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	pak := writeTestBasePak(t, map[string][]byte{rel: []byte("{\r\n    \"Rows\": [1]\r\n}")})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-old", map[string]string{rel: "{\n    \"Rows\": []\n}"}))
	}))
	defer srv.Close()

	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL

	_, err := store.DumpForBuild(context.Background(), pak, "")
	if err == nil {
		t.Fatal("expected an error for a dump that does not match the install, got nil")
	}
	if !strings.Contains(err.Error(), rel) {
		t.Errorf("error %q should name the table that disagreed (%s)", err, rel)
	}
}

// writeLocalDump lays out an unpacked-data.pak-shaped directory on disk.
func writeLocalDump(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// With a local dump directory configured, the network is never touched.
func TestDumpStore_DumpForBuild_LocalDirOverridesFetch(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	shipped := []byte("{\r\n    \"Rows\": []\r\n}")
	pak := writeTestBasePak(t, map[string][]byte{rel: shipped})
	local := writeLocalDump(t, map[string]string{rel: "{\n    \"Rows\": []\n}"})

	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL

	dump, err := store.DumpForBuild(context.Background(), pak, local)
	if err != nil {
		t.Fatalf("DumpForBuild with a local dump dir: %v", err)
	}
	if fetched {
		t.Error("the hosted dump was fetched even though a local dump dir was configured")
	}
	got, ok := dump.Table(rel)
	if !ok || !bytes.Equal(got, shipped) {
		t.Errorf("table bytes = %q (found=%v), want the shipped CRLF form %q", got, ok, shipped)
	}
}

// A local directory already storing CRLF must load unchanged — QuickBMS writes
// whatever the pak stored, so the conversion has to be idempotent.
func TestDumpStore_DumpForBuild_LocalDirAlreadyCRLF(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	shipped := "{\r\n    \"Rows\": []\r\n}"
	pak := writeTestBasePak(t, map[string][]byte{rel: []byte(shipped)})
	local := writeLocalDump(t, map[string]string{rel: shipped})

	store := newDumpStore(t.TempDir(), http.DefaultClient)
	store.treeURL = "http://127.0.0.1:0/never-used"

	if _, err := store.DumpForBuild(context.Background(), pak, local); err != nil {
		t.Fatalf("DumpForBuild with a CRLF local dump dir: %v", err)
	}
}

// A local dir from the wrong week is rejected exactly like a stale hosted
// dump, and the error points at the configured path.
func TestDumpStore_DumpForBuild_LocalDirWrongWeek_Rejected(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	pak := writeTestBasePak(t, map[string][]byte{rel: []byte("{\r\n    \"Rows\": [1]\r\n}")})
	local := writeLocalDump(t, map[string]string{rel: "{\n    \"Rows\": []\n}"})

	store := newDumpStore(t.TempDir(), http.DefaultClient)
	store.treeURL = "http://127.0.0.1:0/never-used"

	_, err := store.DumpForBuild(context.Background(), pak, local)
	if err == nil {
		t.Fatal("expected an error for a local dump dir from a different week, got nil")
	}
	if !strings.Contains(err.Error(), rel) {
		t.Errorf("error %q should name the disagreeing table (%s)", err, rel)
	}
	if !strings.Contains(err.Error(), local) {
		t.Errorf("error %q should name the configured data_dump_path (%s)", err, local)
	}
}

func TestDumpStore_DumpForBuild_LocalDirEmpty_IsActionable(t *testing.T) {
	pak := writeTestBasePak(t, map[string][]byte{"a/B.json": []byte("{}")})
	store := newDumpStore(t.TempDir(), http.DefaultClient)

	_, err := store.DumpForBuild(context.Background(), pak, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a data_dump_path holding no JSON tables, got nil")
	}
}

func TestDumpStore_DumpForBuild_NetworkFailure_IsActionable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL

	pak := writeTestBasePak(t, map[string][]byte{"a/B.json": []byte("{}")})
	_, err := store.DumpForBuild(context.Background(), pak, "")
	if err == nil {
		t.Fatal("expected an error when the dump host fails, got nil")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./internal/source/icarus/... -run 'TestDetectBuild|TestDumpStore' -v
```

Expected: FAIL (`detectBuild`, `newDumpStore` undefined).

- [ ] **Step 3: Implement `datadump.go`**

```go
package icarus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// defaultDumpTreeURL is the community per-week unpack of Icarus's data.pak:
// https://github.com/GODOFMINECRAFT4/IcarusData. The tree is committed as
// loose JSON at the repo root, one commit per game week, with the week
// recorded only in the commit message — there are no tags or releases. This
// URL is HEAD; a specific week is addressed by substituting its commit SHA.
const defaultDumpTreeURL = "https://codeload.github.com/GODOFMINECRAFT4/IcarusData/tar.gz/refs/heads/master"

// maxDumpBytes caps the download. The real tree is ~36 MB; this leaves room to
// grow while refusing to stream an unbounded body into memory.
const maxDumpBytes = 256 << 20

// Build identifies the installed game, read from Icarus/Config/version.json.
// Note this carries no week number — nothing in the install does. Week
// agreement is established by content comparison, not by this value.
type Build struct {
	Major, Minor, Patch int
	Changelist          int
	DataChangelist      int
	FeatureLevel        string
}

func (b Build) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", b.Major, b.Minor, b.Patch, b.Changelist)
}

// detectBuild reads <installRoot>/Icarus/Config/version.json.
func detectBuild(installRoot string) (Build, error) {
	p := filepath.Join(installRoot, "Icarus", "Config", "version.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return Build{}, fmt.Errorf("icarus: reading game version from %s: %w", p, err)
	}
	var doc struct {
		Version struct {
			Major, Minor, Patch int
			Changelist          int
			FeatureLevel        string
		}
		Data struct{ Changelist int }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Build{}, fmt.Errorf("icarus: parsing %s: %w", p, err)
	}
	return Build{
		Major: doc.Version.Major, Minor: doc.Version.Minor, Patch: doc.Version.Patch,
		Changelist:     doc.Version.Changelist,
		DataChangelist: doc.Data.Changelist,
		FeatureLevel:   doc.Version.FeatureLevel,
	}, nil
}

// Dump is a fetched set of base data tables, keyed by mount-relative path
// (e.g. "Factions/D_Factions.json") with values already converted back to the
// game's CRLF line endings.
type Dump struct {
	tables map[string][]byte
}

// Table returns one table's shipped bytes.
func (d *Dump) Table(rel string) ([]byte, bool) {
	b, ok := d.tables[rel]
	return b, ok
}

// DumpStore fetches and caches base-table dumps.
type DumpStore struct {
	cacheDir   string
	httpClient *http.Client
	treeURL    string // overridable in tests
}

func newDumpStore(cacheDir string, httpClient *http.Client) *DumpStore {
	return &DumpStore{cacheDir: cacheDir, httpClient: httpClient, treeURL: defaultDumpTreeURL}
}

// DumpForBuild loads the base data tables and returns them only if they match
// the installed game, proven by byte-comparing every table basePakPath stores
// uncompressed. A mismatch means the tables are for a different game week:
// that is a hard error naming the offending tables, never a silent
// best-effort.
//
// localDumpDir, when non-empty, is a user-supplied directory holding an
// unpacked data.pak JSON tree (QuickBMS output and the like); it replaces the
// network fetch entirely. Validation is the same either way — a local
// directory from the wrong week is rejected exactly like a stale hosted dump.
func (s *DumpStore) DumpForBuild(ctx context.Context, basePakPath, localDumpDir string) (*Dump, error) {
	var (
		dump *Dump
		err  error
	)
	if localDumpDir != "" {
		dump, err = loadLocalDump(localDumpDir)
	} else {
		dump, err = s.fetchTree(ctx, s.treeURL)
	}
	if err != nil {
		return nil, err
	}
	if err := validateDump(dump, basePakPath); err != nil {
		if localDumpDir != "" {
			return nil, fmt.Errorf("%w (tables were read from the configured data_dump_path %s)", err, localDumpDir)
		}
		return nil, err
	}
	return dump, nil
}

// loadLocalDump reads an unpacked data.pak JSON tree from disk. The layout is
// the same one the hosted dump ships — table paths relative to the directory
// root, e.g. "Factions/D_Factions.json" — so a user can point this at QuickBMS
// output without rearranging anything.
func loadLocalDump(dir string) (*Dump, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("icarus: reading the configured data_dump_path %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("icarus: the configured data_dump_path %s is not a directory", dir)
	}

	dump := &Dump{tables: make(map[string][]byte)}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dump.tables[filepath.ToSlash(rel)] = toCRLF(body)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("icarus: scanning the configured data_dump_path %s: %w", dir, err)
	}
	if len(dump.tables) == 0 {
		return nil, fmt.Errorf("icarus: the configured data_dump_path %s contains no JSON tables "+
			"(expected an unpacked data.pak tree, e.g. Factions/D_Factions.json)", dir)
	}
	return dump, nil
}

// fetchTree downloads a dump tarball and ingests its JSON tables, restoring
// the CRLF line endings the game ships (the repo stores LF).
func (s *DumpStore) fetchTree(ctx context.Context, url string) (*Dump, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("icarus: building dump request: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("icarus: fetching base-table dump: %w "+
			"(compiling Icarus mods requires network access — see the plan's Global Constraints)", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("icarus: fetching base-table dump from %s: HTTP %d", url, resp.StatusCode)
	}

	zr, err := gzip.NewReader(io.LimitReader(resp.Body, maxDumpBytes))
	if err != nil {
		return nil, fmt.Errorf("icarus: base-table dump is not valid gzip: %w", err)
	}
	defer zr.Close() //nolint:errcheck

	dump := &Dump{tables: make(map[string][]byte)}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("icarus: reading base-table dump: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".json") {
			continue
		}
		// Strip the archive's single top-level directory (e.g.
		// "IcarusData-<sha>/") to get the mount-relative table path.
		rel := hdr.Name
		if i := strings.Index(rel, "/"); i >= 0 {
			rel = rel[i+1:]
		}
		// The repo also carries a stale "data/" copy of the tree; the
		// authoritative tables are the root-level ones.
		if rel == "" || strings.HasPrefix(rel, "data/") {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("icarus: reading %s from base-table dump: %w", rel, err)
		}
		dump.tables[path.Clean(rel)] = toCRLF(body)
	}
	if len(dump.tables) == 0 {
		return nil, fmt.Errorf("icarus: base-table dump from %s contained no JSON tables", url)
	}
	return dump, nil
}

// toCRLF restores the game's line endings. The dump repo stores LF (committed
// with autocrlf); the shipped pak stores CRLF, and the two are otherwise
// byte-identical. Existing CRLFs are left alone so the conversion is
// idempotent.
func toCRLF(b []byte) []byte {
	return []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n"))
}

// validateDump proves a dump belongs to the installed game.
//
// Only the tables data.pak stores *uncompressed* can be checked — the rest are
// Oodle-compressed and unreadable here, which is the whole reason the dump
// exists. That is enough: a dump built from a different week's data.pak
// disagrees on some of them, and in practice it disagrees loudly (the spike saw
// 3 differing stored tables and 6 missing tables across a 7-week gap).
func validateDump(dump *Dump, basePakPath string) error {
	pak, err := unrealpak.Open(basePakPath)
	if err != nil {
		return fmt.Errorf("icarus: opening base pak %s for dump validation: %w", basePakPath, err)
	}
	defer pak.Close() //nolint:errcheck

	var missing, differing []string
	checked := 0
	for _, f := range pak.Files() {
		shipped, err := pak.ReadFile(f.Path)
		if err != nil {
			if errors.Is(err, unrealpak.ErrUnsupportedFormat) {
				continue // Oodle-compressed (or similar): not readable here, and not our gate
			}
			// Any other ReadFile failure — corruption, a truncated payload, an
			// I/O error — is not an expected skip. Silently excluding it here
			// would quietly narrow what this gate actually verified, exactly
			// the "no silent fallbacks" failure this function exists to prevent.
			return fmt.Errorf("icarus: validating base pak %s: reading %s: %w", basePakPath, f.Path, err)
		}
		checked++
		got, ok := dump.Table(f.Path)
		if !ok {
			missing = append(missing, f.Path)
			continue
		}
		if !bytes.Equal(got, shipped) {
			differing = append(differing, f.Path)
		}
	}
	if checked == 0 {
		return fmt.Errorf("icarus: %s exposed no uncompressed tables to validate the dump against", basePakPath)
	}
	if len(missing) == 0 && len(differing) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(differing)
	return fmt.Errorf(
		"icarus: the available base-table dump does not match the installed game "+
			"(%d/%d uncompressed tables disagree: %s). The dump is for a different game week. "+
			"Wait for the dump to be updated for your game version, or roll the game back to a "+
			"matching week; compiling against a mismatched week would silently corrupt mod data",
		len(missing)+len(differing), checked, summarize(append(differing, missing...)))
}

func summarize(paths []string) string {
	const max = 3
	if len(paths) <= max {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:max], ", "), len(paths)-max)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/source/icarus/... -run 'TestDetectBuild|TestDumpStore' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/datadump.go internal/source/icarus/datadump_test.go
git commit -m "feat: fetch Icarus base data tables from the per-week community dump (#136)"
```

---

## Task 12: Compile orchestration

**Files:**

- Create: `internal/source/icarus/compile.go`
- Create: `internal/source/icarus/compile_test.go`

**Interfaces:**

- Consumes: `unrealpak.Open`, `Reader.Files`, `unrealpak.Create`, `Writer.AddFile`/`Close` (Tasks 2–4); `ParseExmodz` (Task 11); `ApplyRowPatch` (Task 10); `DumpStore.DumpForBuild` (Task 12a).
- Produces: `func Compile(ctx context.Context, dumps *DumpStore, basePakPath, localDumpDir, exmodzPath, outputPakPath string) error` — Task 13 depends on this exact signature. This is also the function `source.Compiler` (Task 13) wraps; `localDumpDir` is the game's optional `data_dump_path` and is `""` when unset. (Implemented with a named return, `func Compile(...) (err error)`, used internally by a deferred cleanup on failure — see "Partial-output cleanup on failure" below; the call-site type is unchanged, so this is invisible to Task 13.)

> **Base tables come from a hosted per-week dump (rev3 — USER DECISION; resolves the Oodle
> blocker).** The base pak is `Icarus/Content/Data/data.pak` (298 files, all `.json`), and
> **258 of them are Oodle-compressed** — including every table a mod would plausibly patch
> (`D_ItemsStatic` 7.3 MB, `D_Talents` 2.6 MB, `D_Quests` 1.2 MB). Only 40 tiny stubs are
> stored uncompressed. Oodle has no Go stdlib decoder, so the base tables **cannot** be read
> out of the local pak.
>
> Resolution: fetch the base tables from the community's per-week JSON dump instead
> (Task 12a). The local `data.pak` is still opened, but for **validation only** — its 40
> stored tables are readable without Oodle and are byte-compared against the dump to prove
> the dump is the right week. That check is not speculative complexity: it is exactly how
> the spike detected that the newest dump is 7 weeks behind a current install.
>
> Two facts from the spike that shape this task's error handling — see
> `docs/plans/icarus-pak-format-findings.md` Part 3:
>
> - Dump blobs are **LF**; the shipped pak is **CRLF**. The fetcher restores `LF -> CRLF`,
>   which reproduces shipped bytes exactly (37/40 stored tables byte-identical).
> - **A matching dump may simply not exist yet.** At spike time the install was Week 243 and
>   the freshest dump was Week 236. "No dump for the installed build" is a normal outcome
>   that must fail loudly with an actionable message — not an edge case, and never a silent
>   fall back to a mismatched week.

**Note on the base data-table's mount path (corrected against real data — see task-12-report.md "plan delta 1")**: the real sample's `Rows[].CurrentFile` is the mount-relative directory path with every `/` flattened to `-` (e.g. base pak path `AI/D_AIGrowth.json` is recorded as `AI-D_AIGrowth.json`; a deeper path like `Audio/MusicConditions/D_MusicLocationConditions.json` is recorded as `Audio-MusicConditions-D_MusicLocationConditions.json`) — not a suffix-matchable bare filename as originally assumed here. A literal `/<CurrentFile>` suffix match, verified against a real install and a real `.EXMODZ`, matches **0 of 14** real rows. `resolveCurrentFile`/`matchMountPath` instead reconstruct the mount path by reversing that substitution (`strings.ReplaceAll(currentFile, "-", "/")`) and doing an **exact** match against the base pak's file listing; this is unambiguous in practice (verified: none of Icarus's 298 real base-table paths contain a literal hyphen). Zero matches or more than one match is still a loud, named error — the ambiguous-match case is now structurally unreachable through `unrealpak.Writer`'s public API (it rejects duplicate mount paths), so it is exercised as a `matchMountPath`-level unit test with a hand-built duplicate-path slice rather than a full `Compile()` integration test.

**`EndOfMod` sentinel**: real `.EXMOD` manifests terminate their `Rows` array with `{"CurrentFile":"EndOfMod"}` and no `File_Items` key at all — a known ecosystem terminator, not a data-table row. `Compile`'s row loop skips it explicitly (`row.CurrentFile == "EndOfMod"`, checked before `resolveCurrentFile` is ever called); any _other_ row with zero `File_Items` is treated as a malformed manifest and fails loudly, naming the row.

**Asset-path sanitation (controller-flagged security item, resolved — see task-12-report.md "plan delta 2")**: `ParseExmodz` (Task 11) carries each bundled asset's raw zip entry name through unchanged as the `ExmodzBundle.Assets` map key, and neither it nor `unrealpak.Writer.AddFile` sanitizes it. Before writing any asset, `Compile` calls `sanitizeAssetPath`, which normalizes backslashes to `/`, rejects a NUL byte, rejects absolute paths (a leading `/` or a Windows drive form like `C:/...`), and `path.Clean`s the result, rejecting anything that is `.`, `..`, or escapes with a leading `../`. This closes a pak-slip vector where a crafted `.EXMODZ` asset entry (`../evil.uasset`, `/evil`, `C:\evil`) could escape the mod's own namespace once the pak is deployed or unpacked elsewhere.

**Partial-output cleanup on failure (fix round 1 — see task-12-report.md)**: `unrealpak.Create` opens `outputPakPath` eagerly, so any error after that point (in either loop, or in `out.Close()`) originally left a partial/incomplete pak on disk — a hazard, since it could be picked up and deployed. `Compile` uses a named return, `func Compile(...) (err error)`, with a `defer` that removes `outputPakPath` on any non-nil error, joining a removal failure into the returned error rather than masking it; the success path is untouched. `unrealpak.Writer` has no way to abort without finalizing (`Close` always serializes and writes whatever was buffered, producing a semantically-incomplete-but-valid pak — worse than an empty file), so `os.Remove` on the error path is the fix, not a `unrealpak` API change; the underlying file descriptor is only reclaimed on GC in this path, a known, accepted limitation.

- [ ] **Step 1: Write the failing test**

```go
package icarus

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// testDumpStore serves a dump containing exactly files, so validateDump agrees
// it matches the base pak built from the same map. Reuses tarGz from
// datadump_test.go (same package).
func testDumpStore(t *testing.T, files map[string][]byte) *DumpStore {
	t.Helper()
	entries := make(map[string]string, len(files))
	for name, data := range files {
		entries[name] = string(data)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-test", entries))
	}))
	t.Cleanup(srv.Close)
	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL
	return store
}

// writeTestBasePak is defined in datadump_test.go (same package) — Task 12a
// needed it first for its own tests, so this file reuses it rather than
// redeclaring it.

func writeTestExmodzFile(t *testing.T, manifestJSON string, assets map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mod.exmodz")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Extracted Mods/Test.EXMOD")
	w.Write([]byte(manifestJSON)) //nolint:errcheck
	for name, data := range assets {
		aw, _ := zw.Create(name)
		aw.Write(data) //nolint:errcheck
	}
	zw.Close() //nolint:errcheck
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Base table paths and CurrentFile values below mirror the real shape found
// against a live install + a real Bear_Mount.EXMODZ during Step 5b
// verification: CurrentFile flattens the mount-relative directory path with
// "-" in place of "/" (e.g. "AI-D_AIGrowth.json" for base pak path
// "AI/D_AIGrowth.json"), not a bare filename living at a hyphenated leaf as
// originally assumed. See task-12-report.md "plan delta".
func TestCompile_AppliesDiffAndBundlesAssets(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"Bear Mount","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, map[string][]byte{
		"Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset": []byte("fake-asset"),
	})
	outputPath := filepath.Join(t.TempDir(), "Bear_Mount_P.pak")

	if err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening compiled output: %v", err)
	}
	defer r.Close()

	patched, err := r.ReadFile("AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile patched data table: %v", err)
	}
	if !bytes.Contains(patched, []byte(`"BaseMovementSpeed":235`)) {
		t.Errorf("patched data table = %s, want BaseMovementSpeed 235", patched)
	}

	asset, err := r.ReadFile("Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset")
	if err != nil {
		t.Fatalf("ReadFile bundled asset: %v", err)
	}
	if string(asset) != "fake-asset" {
		t.Errorf("bundled asset content = %q", asset)
	}
}

// The real .EXMOD ecosystem terminates Rows with {"CurrentFile":"EndOfMod"}
// and no File_Items key — Compile must skip it, not try to resolve it as a
// data table (it has none).
func TestCompile_SkipsEndOfModSentinelRow(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"X","Rows":[` +
		`{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]},` +
		`{"CurrentFile":"EndOfMod"}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	if err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening compiled output: %v", err)
	}
	defer r.Close()
	patched, err := r.ReadFile("AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile patched data table: %v", err)
	}
	if !bytes.Contains(patched, []byte(`"BaseMovementSpeed":235`)) {
		t.Errorf("patched data table = %s, want BaseMovementSpeed 235", patched)
	}
}

// A real (non-sentinel) row with no File_Items is a malformed manifest, not
// something to silently skip — only the EndOfMod sentinel gets that pass.
func TestCompile_RowWithoutFileItems_Errors(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_AIGrowth.json"}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath)
	if err == nil {
		t.Fatal("expected an error for a non-sentinel row with no File_Items, got nil")
	}
	if !strings.Contains(err.Error(), "AI-D_AIGrowth.json") {
		t.Errorf("error %q should name the offending row", err)
	}
}

// A stale dump must stop the compile before any output pak is written — this
// is the live case today, where the newest dump lags the installed game.
func TestCompile_DumpWeekMismatch_FailsBeforeWriting(t *testing.T) {
	basePak := writeTestBasePak(t, map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	})
	dumps := testDumpStore(t, map[string][]byte{ // different week's content
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":150}}`),
	})
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath)
	if err == nil {
		t.Fatal("expected an error when the dump is for a different game week, got nil")
	}
	if _, statErr := os.Stat(outputPath); statErr == nil {
		t.Error("no output pak should exist after a week-mismatch failure")
	}
}

// A malicious .EXMODZ whose bundled asset entry escapes the mod's own path
// must fail loudly rather than write outside the pak's intended namespace —
// see task-12-report.md "plan delta" for the exact semantics.
func TestCompile_UnsafeAssetPath_Errors(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"X","Rows":[]}`
	exmodzPath := writeTestExmodzFile(t, manifest, map[string][]byte{
		"../evil.uasset": []byte("payload"),
	})
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath)
	if err == nil {
		t.Fatal("expected an error for an asset path escaping the mod's own namespace, got nil")
	}
	if !strings.Contains(err.Error(), "../evil.uasset") {
		t.Errorf("error %q should name the offending asset path", err)
	}
	if _, statErr := os.Stat(outputPath); statErr == nil {
		t.Error("no partial output pak should exist after an unsafe-asset-path failure")
	}
}

// A failure that happens after unrealpak.Create(outputPakPath) has already
// created the file on disk (here: an unresolvable row, mid row-loop) must
// not leave a partial/incomplete pak behind — a stray partial _P.pak is a
// hazard (it could be picked up and deployed) and contradicts the
// fail-loud-and-clean philosophy. See task-12-report.md "plan delta" (fix
// round 1).
func TestCompile_MidCompileFailure_LeavesNoOutputFile(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	// CurrentFile has no matching base-pak file: resolveCurrentFile fails
	// inside the row loop, after out has already been created.
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_Nonexistent.json","File_Items":[{"Name":"Mount_Bear","X":1}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath)
	if err == nil {
		t.Fatal("expected an error for an unresolvable row, got nil")
	}
	if _, statErr := os.Stat(outputPath); statErr == nil {
		t.Error("no partial output pak should exist after a mid-compile failure")
	} else if !os.IsNotExist(statErr) {
		t.Errorf("unexpected error stat-ing output path: %v", statErr)
	}
}

func TestMatchMountPath(t *testing.T) {
	paths := []string{
		"AI/D_AIGrowth.json",
		"Audio/MusicConditions/D_MusicLocationConditions.json",
		"D_Factions.json",
	}

	t.Run("single-level directory", func(t *testing.T) {
		got, err := matchMountPath(paths, "AI-D_AIGrowth.json")
		if err != nil {
			t.Fatalf("matchMountPath: %v", err)
		}
		if got != "AI/D_AIGrowth.json" {
			t.Errorf("matchMountPath = %q, want AI/D_AIGrowth.json", got)
		}
	})

	t.Run("multi-level directory", func(t *testing.T) {
		got, err := matchMountPath(paths, "Audio-MusicConditions-D_MusicLocationConditions.json")
		if err != nil {
			t.Fatalf("matchMountPath: %v", err)
		}
		if got != "Audio/MusicConditions/D_MusicLocationConditions.json" {
			t.Errorf("matchMountPath = %q, want Audio/MusicConditions/D_MusicLocationConditions.json", got)
		}
	})

	t.Run("root-level file, no hyphen to convert", func(t *testing.T) {
		got, err := matchMountPath(paths, "D_Factions.json")
		if err != nil {
			t.Fatalf("matchMountPath: %v", err)
		}
		if got != "D_Factions.json" {
			t.Errorf("matchMountPath = %q, want D_Factions.json", got)
		}
	})

	t.Run("no match is a loud, actionable error", func(t *testing.T) {
		_, err := matchMountPath(paths, "AI-D_Nonexistent.json")
		if err == nil {
			t.Fatal("expected an error for a CurrentFile with no matching base pak file, got nil")
		}
		if !strings.Contains(err.Error(), "AI-D_Nonexistent.json") || !strings.Contains(err.Error(), "AI/D_Nonexistent.json") {
			t.Errorf("error %q should name both the CurrentFile and the expected mount path", err)
		}
	})

	t.Run("ambiguous match is a loud error", func(t *testing.T) {
		dup := []string{"AI/D_AIGrowth.json", "AI/D_AIGrowth.json"}
		_, err := matchMountPath(dup, "AI-D_AIGrowth.json")
		if err == nil {
			t.Fatal("expected an error for an ambiguous match, got nil")
		}
	})
}

func TestSanitizeAssetPath(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "parent traversal", raw: "../evil.json", wantErr: true},
		{name: "absolute unix path", raw: "/evil", wantErr: true},
		{name: "windows drive absolute", raw: `C:\evil`, wantErr: true},
		{name: "backslash-normalized nested path", raw: `Good\Nested\file.uasset`, want: "Good/Nested/file.uasset"},
		{name: "benign nested path", raw: "Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset", want: "Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizeAssetPath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("sanitizeAssetPath(%q) = %q, nil; want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("sanitizeAssetPath(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("sanitizeAssetPath(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/source/icarus/... -run TestCompile -v
```

Expected: FAIL (`Compile` undefined).

- [ ] **Step 3: Implement**

```go
package icarus

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// Compile reads exmodzPath's .EXMOD diff, applies it to the game's base data
// tables, bundles in any pre-built assets the .EXMODZ carries, and writes the
// result as a new pak at outputPakPath ready to deploy as-is.
//
// The base tables come from the community per-week dump (Task 12a), not from
// basePakPath: 258 of the 298 tables in a real data.pak are Oodle-compressed
// and cannot be read with the stdlib. basePakPath is still opened, for two
// things it alone can answer — which tables the installed game actually has
// (so a bare, hyphen-flattened CurrentFile resolves to a real mount path),
// and whether the dump
// is for the installed week (DumpForBuild byte-checks it against the tables
// the pak stores uncompressed). A dump that does not match fails the whole
// compile; see Task 12a.
//
// localDumpDir is the game's optional data_dump_path: when set, base tables
// are read from that directory instead of being fetched. It is validated
// identically, so a stale local directory fails just as loudly.
func Compile(ctx context.Context, dumps *DumpStore, basePakPath, localDumpDir, exmodzPath, outputPakPath string) (err error) {
	exmodzData, err := os.ReadFile(exmodzPath)
	if err != nil {
		return fmt.Errorf("icarus: reading %s: %w", exmodzPath, err)
	}
	bundle, err := ParseExmodz(exmodzData)
	if err != nil {
		return fmt.Errorf("icarus: %s: %w", exmodzPath, err)
	}

	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return fmt.Errorf("icarus: opening base pak %s: %w", basePakPath, err)
	}
	defer base.Close()

	// Loaded and validated before anything is written, so a week mismatch or
	// an offline machine fails before a half-built pak exists on disk.
	dump, err := dumps.DumpForBuild(ctx, basePakPath, localDumpDir)
	if err != nil {
		return err
	}

	out, err := unrealpak.Create(outputPakPath)
	if err != nil {
		return fmt.Errorf("icarus: creating %s: %w", outputPakPath, err)
	}
	// unrealpak.Create opens the file eagerly, so any error from here on
	// leaves a partial/incomplete pak at outputPakPath unless removed — a
	// hazard, since it could be picked up and deployed. unrealpak.Writer has
	// no way to abort without finalizing (Close always serializes and writes
	// whatever was buffered), so removing the file is the only way to keep
	// the fail-loud-and-clean contract on this path; the success path
	// (err == nil here) is untouched.
	defer func() {
		if err == nil {
			return
		}
		if rmErr := os.Remove(outputPakPath); rmErr != nil && !os.IsNotExist(rmErr) {
			err = fmt.Errorf("%w (additionally, removing partial output %s failed: %v)", err, outputPakPath, rmErr)
		}
	}()

	for _, row := range bundle.Diff.Rows {
		if row.CurrentFile == endOfModSentinel {
			// A known .EXMOD ecosystem terminator row: no File_Items, no
			// corresponding data table. Not a row to resolve or patch.
			continue
		}
		if len(row.FileItems) == 0 {
			return fmt.Errorf("icarus: %s: row has no File_Items to apply (malformed .EXMOD manifest)", row.CurrentFile)
		}
		mountPath, err := resolveCurrentFile(base, row.CurrentFile)
		if err != nil {
			return err
		}
		baseData, ok := dump.Table(mountPath)
		if !ok {
			return fmt.Errorf("icarus: base data table %s is present in the installed game "+
				"but missing from the base-table dump", mountPath)
		}
		patched, err := ApplyRowPatch(baseData, row)
		if err != nil {
			return err
		}
		if err := out.AddFile(mountPath, patched); err != nil {
			return fmt.Errorf("icarus: writing patched %s: %w", mountPath, err)
		}
	}

	for assetPath, data := range bundle.Assets {
		safePath, err := sanitizeAssetPath(assetPath)
		if err != nil {
			return err
		}
		if err := out.AddFile(safePath, data); err != nil {
			return fmt.Errorf("icarus: writing bundled asset %s: %w", safePath, err)
		}
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("icarus: finalizing %s: %w", outputPakPath, err)
	}
	return nil
}

// endOfModSentinel is a known .EXMOD ecosystem terminator row: real-world
// manifests end their Rows array with {"CurrentFile":"EndOfMod"} and no
// File_Items key at all. It targets no data table and carries no patch, so
// Compile skips it rather than trying (and failing) to resolve it.
const endOfModSentinel = "EndOfMod"

// resolveCurrentFile finds the base-pak file a row's bare CurrentFile refers
// to. The .EXMOD schema flattens the mount-relative directory path into
// CurrentFile by replacing every "/" with "-" (e.g. the real base pak path
// "Audio/MusicConditions/D_MusicLocationConditions.json" is recorded as
// "Audio-MusicConditions-D_MusicLocationConditions.json"); reversing that
// substitution reconstructs the mount path exactly. This was verified
// against a real install and a real .EXMODZ: none of Icarus's 298 real base
// table paths contain a literal hyphen, so the reverse mapping is
// unambiguous. Fails loudly on zero or multiple matches — see this task's
// header note; guessing which one is correct is exactly the kind of silent
// fallback repo precedent #95 forbids.
func resolveCurrentFile(base *unrealpak.Reader, currentFile string) (string, error) {
	files := base.Files()
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return matchMountPath(paths, currentFile)
}

// matchMountPath resolves currentFile against paths, isolated from
// *unrealpak.Reader so the zero/ambiguous-match error paths can be tested
// directly without needing a base pak with (unreachable in valid data)
// duplicate mount entries.
func matchMountPath(paths []string, currentFile string) (string, error) {
	candidate := strings.ReplaceAll(currentFile, "-", "/")
	var matches []string
	for _, p := range paths {
		if p == candidate {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("icarus: %s: no matching file in base pak "+
			"(expected mount path %s, from CurrentFile with '-' converted to '/')", currentFile, candidate)
	default:
		return "", fmt.Errorf("icarus: %s: ambiguous, matches %v", currentFile, matches)
	}
}

// sanitizeAssetPath validates a bundled asset's mount path before it is
// written into the output pak. .EXMODZ archives are third-party zip files,
// and ParseExmodz (Task 11) carries each entry's raw zip name through
// unchanged as the Assets map key. Without this gate, a crafted entry name
// (a "../" parent traversal, an absolute path, or a Windows drive path) could
// escape the mod's own namespace once the pak is deployed or unpacked
// elsewhere — the pak equivalent of a zip-slip. Rejecting it here, before
// AddFile, keeps that malformed-archive class of input a loud compile
// failure rather than a written-then-discovered problem.
func sanitizeAssetPath(rawZipName string) (string, error) {
	normalized := strings.ReplaceAll(rawZipName, `\`, "/")
	if strings.Contains(normalized, "\x00") {
		return "", fmt.Errorf("icarus: bundled asset %q: contains a NUL byte", rawZipName)
	}
	if strings.HasPrefix(normalized, "/") || isWindowsDriveAbsolute(normalized) {
		return "", fmt.Errorf("icarus: bundled asset %q: absolute paths are not allowed", rawZipName)
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("icarus: bundled asset %q: escapes the mod's own path", rawZipName)
	}
	return cleaned, nil
}

// isWindowsDriveAbsolute reports whether p starts with a Windows drive letter
// (e.g. "C:/evil"). Checked on the slash-normalized form, since a zip entry
// written by a Windows tool may carry "C:\evil" — backslashes normalize to
// forward slashes before this check runs.
func isWindowsDriveAbsolute(p string) bool {
	return len(p) >= 2 && p[1] == ':' &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/source/icarus/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/icarus/compile.go internal/source/icarus/compile_test.go
git commit -m "feat: implement exmod compile orchestration (#136)"
```

---

## Task 13: Wire the compile step into `Service`'s cache-population path

**Files:**

- Modify: `internal/domain/game.go:51-78` (`DeployMode`) and `Game` (new `BaseDataPath` field, Step 6b)
- Modify: `internal/storage/config/games.go` (new `data_dump_path` YAML key, Step 6b)
- Modify: `internal/source/source.go` (new `Compiler` optional interface, mirroring `DownloadHeaderProvider`'s type-assertion pattern)
- Modify: `internal/source/icarus/icarus.go` (implement `Compiler`)
- Modify: `internal/core/service.go:455-495` (`DownloadModToCache`'s extract/copy branch)
- Create: `internal/core/service_icarus_compile_test.go`

**Interfaces:**

- Consumes: `icarus.Compile` (Task 12), `domain.Game.DeployMode` (existing).
- Produces: `domain.DeployCompile` (new `DeployMode` value), `domain.Game.BaseDataPath` (new optional field), `source.Compiler` interface with `Compile(ctx context.Context, basePakPath, baseDataPath, sourceFilePath, outputPath string) error`.

- [ ] **Step 1: Add `DeployCompile` to the `DeployMode` enum**

In `internal/domain/game.go`:

```go
const (
	DeployExtract DeployMode = iota // Default: extract archives to mod path
	DeployCopy                      // Copy files as-is (for games like Hytale where .zip IS the mod)
	DeployCompile                   // Compile downloaded file into a new artifact before caching (Icarus .exmodz -> .pak)
)

func (m DeployMode) String() string {
	switch m {
	case DeployExtract:
		return "extract"
	case DeployCopy:
		return "copy"
	case DeployCompile:
		return "compile"
	default:
		return "extract"
	}
}

func ParseDeployMode(s string) DeployMode {
	switch s {
	case "copy":
		return DeployCopy
	case "compile":
		return DeployCompile
	default:
		return DeployExtract
	}
}
```

- [ ] **Step 2: Add the `Compiler` interface to `internal/source/source.go`**

```go
// Compiler is implemented by sources whose downloaded files need
// transforming into a different artifact before deployment (Icarus's
// .exmodz -> .pak). Service consults it, when DeployMode is DeployCompile,
// after downloading but before committing the file to cache — the result
// replaces the downloaded file in cache, so everything downstream (Install,
// the linker) treats it exactly like a DeployCopy file.
//
// basePakPath and baseDataPath are both resolved by the caller from the game's
// config: basePakPath from game.InstallPath, baseDataPath from the game's
// optional data_dump_path ("" when unset — see Step 6b). sourceFilePath is the
// just-downloaded file; outputPath is where the compiled result must be
// written.
type Compiler interface {
	Compile(ctx context.Context, basePakPath, baseDataPath, sourceFilePath, outputPath string) error
}
```

- [ ] **Step 3: Implement `Compiler` on `Icarus`** ✅ shipped as `SetDataDir` optional
      setter, not a `New` parameter — see below.

In `internal/source/icarus/icarus.go`, add:

```go
var _ source.Compiler = (*Icarus)(nil)

// Compile implements source.Compiler by delegating to the package-level
// Compile function (Task 12) — basePakPath/baseDataPath/sourceFilePath/
// outputPath map directly onto Compile's basePakPath/localDumpDir/exmodzPath/
// outputPakPath parameters. The base-table dump store (Task 12a) is supplied
// from the source itself; the per-game dump-directory override arrives as
// baseDataPath, since only the caller has the game's config.
func (s *Icarus) Compile(ctx context.Context, basePakPath, baseDataPath, sourceFilePath, outputPath string) error {
	if s.dumps == nil {
		return fmt.Errorf("source %q: not initialized with a data directory (SetDataDir was never called)", s.ID())
	}
	return Compile(ctx, s.dumps, basePakPath, baseDataPath, sourceFilePath, outputPath)
}
```

`Icarus` gains a `dumps *DumpStore` field, nil until wired. **As shipped, this is
NOT constructed in `New`** (the snippet below is what the brief originally proposed):

```go
// Brief's original proposal — NOT what shipped:
// In Icarus's constructor (Task 8), next to the firestoreClient:
	s.dumps = newDumpStore(filepath.Join(dataDir, "icarus", "datadump"), httpClient)
```

`New(httpClient, projectID)` was frozen at exactly those two params by Task 8, with
Task 9's `cmd/lmm/root.go` call site already depending on that signature — adding a
third `dataDir` param would mean rewiring `cmd/lmm/root.go`/`root_test.go`, neither of
which is in this task's Files list. Coordinator-approved fix: an optional
post-construction setter, mirroring the existing `SetAPIKey` optional-setter pattern:

```go
// dumps field, added to the Icarus struct:
	dumps *DumpStore // nil until SetDataDir is called

// SetDataDir wires the base-table dump store's cache directory once the
// service's data directory is known. This is a post-construction setter
// rather than a New parameter because Task 8 froze New(httpClient, projectID)
// at exactly those two params — Task 9's call site already depends on that
// signature — so the data dir arrives the same way API keys do: an optional
// setter the registration pipeline calls when present (cmd/lmm/root.go's
// registerSource, mirroring its existing SetAPIKey wiring).
func (s *Icarus) SetDataDir(dataDir string) {
	s.dumps = newDumpStore(filepath.Join(dataDir, "icarus", "datadump"), s.firestore.httpClient)
}
```

`cmd/lmm/root.go`'s `registerSource` calls `SetDataDir` via the same optional-setter
type assertion `SetAPIKey` already uses (`dataDir` threaded through
`registerSources`/`registerCustomSources`). A source constructed but never wired
through `registerSource` (or a bare test double) has a nil `dumps`, so `Compile`
fails loudly with the error text above rather than panicking — pinned by
`TestIcarus_Compile_WithoutDataDir_FailsLoudly` and
`TestIcarus_SetDataDir_ConstructsDumpStore` in `icarus_test.go`, plus
`TestRegisterSource_WiresDataDir` in `cmd/lmm/root_test.go`.

- [ ] **Step 4: Write the failing service-level test**

Add `internal/core/service_icarus_compile_test.go` in the `core_test` external test package, matching `service_api_source_test.go`'s existing convention (`core.NewService`, `svc.RegisterSource`, `svc.AddGame`, `svc.DownloadMod`, `svc.GetGameCache` — all exported, real entry points confirmed in that file):

```go
package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/require"
)

// fakeCompilerSource is a minimal ModSource that also implements
// source.Compiler, standing in for internal/source/icarus.Icarus (Tasks
// 8/13) without pulling that package into internal/core's tests — this test
// only needs to prove Service invokes Compile when DeployMode is
// DeployCompile, which Task 12 already tests in isolation.
type fakeCompilerSource struct {
	downloadURL  string
	compileCalls int
}

func (s *fakeCompilerSource) ID() string      { return "fake-compiler" }
func (s *fakeCompilerSource) Name() string    { return "Fake Compiler Source" }
func (s *fakeCompilerSource) AuthURL() string { return "" }
func (s *fakeCompilerSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.downloadURL, nil
}
func (s *fakeCompilerSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, source.ErrNotSupported
}

// Compile implements source.Compiler by copying the downloaded source file
// through unchanged — this test only asserts Service invoked it with the
// right arguments and used its output, not that it performs real PAK
// compilation (Task 12 covers that).
func (s *fakeCompilerSource) Compile(ctx context.Context, basePakPath, baseDataPath, sourceFilePath, outputPath string) error {
	s.compileCalls++
	data, err := os.ReadFile(sourceFilePath)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}

var (
	_ source.ModSource = (*fakeCompilerSource)(nil)
	_ source.Compiler  = (*fakeCompilerSource)(nil)
)

func TestDownloadMod_DeployCompile_InvokesCompiler(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-exmodz-bytes"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	require.NoError(t, os.WriteFile(basePak, []byte("fake-base-pak"), 0o644))

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fakeCompilerSource{downloadURL: dlSrv.URL}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
	file := &domain.DownloadableFile{ID: "exmodz", FileName: "Bear_Mount.exmodz"}

	result, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.FilesExtracted)
	require.Equal(t, 1, src.compileCalls)

	gameCache := svc.GetGameCache(game)
	require.True(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "Bear_Mount_P.pak", files[0])

	data, err := os.ReadFile(gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, files[0]))
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data))
}
```

- [ ] **Step 5: Run to verify it fails**

```bash
go test ./internal/core/... -run TestDownloadMod_DeployCompile -v
```

Expected: FAIL (compiler branch doesn't exist yet in `service.go`).

- [ ] **Step 6: Wire the compile branch into `service.go`** ✅ shipped with an
      additional per-file gate — see "Fix round 1" note below.

In `internal/core/service.go`, modify the block at line 461 (inside the function containing the extract/copy logic shown earlier):

```go
	cachePath, stagePath, err := prepareStaging(gameCache, game, mod)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stagePath) //nolint:errcheck

	if game.DeployMode == domain.DeployCompile {
		compiler, ok := src.(source.Compiler)
		if !ok {
			return nil, fmt.Errorf("source %q: game %q requires DeployCompile but source does not implement Compiler", src.ID(), game.ID)
		}
		basePakPath, err := resolveBasePak(game)
		if err != nil {
			return nil, err
		}
		destPath := filepath.Join(stagePath, compiledFileName(file.FileName))
		if err := compiler.Compile(ctx, basePakPath, game.BaseDataPath, archivePath, destPath); err != nil {
			return nil, fmt.Errorf("compiling mod: %w", err)
		}
		if err := commitStagedCache(cachePath, stagePath); err != nil {
			return nil, err
		}
		return &DownloadModResult{FilesExtracted: 1, Checksum: downloadResult.Checksum}, nil
	}

	if game.DeployMode == domain.DeployCopy || !s.extractor.CanExtract(archivePath) {
		// ...existing copy-mode branch, unchanged...
```

Add the two small helpers this references (near the bottom of `service.go`, alongside its other unexported helpers):

```go
// resolveBasePak locates the currently-installed game's base pak for
// DeployCompile sources. v1 scope: Icarus only, one known pak filename
// pattern — extend this if a second DeployCompile-using game is ever added
// rather than generalizing speculatively now. The relative path below is
// Task 1's empirically-confirmed finding (docs/plans/icarus-pak-format-findings.md),
// recorded before this function was written, not an assumption made here: the
// JSON data tables live in Content/Data/data.pak, NOT in the Content/Paks
// pakchunks, which carry only cooked .uasset/.uexp assets and no JSON at all.
//
// Since rev3 this pak is no longer the source of base table *content* (that
// comes from the hosted dump — Task 12a); it is still required, because it is
// the only authority on which tables the installed game has and on which game
// week is installed. Its parent directory also locates Icarus/Config/version.json.
func resolveBasePak(game *domain.Game) (string, error) {
	candidate := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("locating base pak for %q: %w", game.ID, err)
	}
	return candidate, nil
}

// compiledFileName turns a downloaded source filename into the cached
// output's name: same base name, .pak extension, matching Icarus's "_P.pak"
// override convention.
func compiledFileName(sourceFileName string) string {
	base := strings.TrimSuffix(sourceFileName, filepath.Ext(sourceFileName))
	return base + "_P.pak"
}
```

Add `"strings"` to `service.go`'s imports if not already present (it is, per the existing `strings.EqualFold` call visible at line 450).

**Fix round 1 (post-review): gate the compile branch per-file, not per-game.** As
shipped, the `if game.DeployMode == domain.DeployCompile {` line above reads
`if game.DeployMode == domain.DeployCompile && isExmodzFile(file.FileName) {`. Review
found that a `DeployCompile` game's catalog can also serve an already-built `.pak`
(`icarus.GetModFiles` enumerates `"pak"` before `"exmodz"`, neither marked primary
when a mod has both) — routing a `.pak` through `Compile` fails loudly (`ParseExmodz`
on non-zip bytes), making plain-pak Icarus mods permanently uninstallable. Fix: a new
helper, placed beside `resolveBasePak`/`compiledFileName`:

```go
// isExmodzFile reports whether fileName is a compile-eligible archive
// (case-insensitive ".exmodz" suffix). DeployCompile games can also serve
// plain, already-built ".pak" files (icarus.GetModFiles enumerates "pak"
// before "exmodz") - those must NOT be routed through Compile, which expects
// an .exmodz diff (#136 review, Task 13 fix round 1): a prebuilt pak falls
// through to the pre-compile extract/copy logic unchanged, exactly as if
// DeployMode were not DeployCompile at all.
func isExmodzFile(fileName string) bool {
	return strings.HasSuffix(strings.ToLower(fileName), ".exmodz")
}
```

No config surface changed and no edit was needed to the fallthrough branches — a
`.pak` is not zip/7z/rar, so `!s.extractor.CanExtract` is already true regardless of
`DeployMode`, landing it in the existing copy-as-is branch exactly as on a
`DeployCopy`/`DeployExtract` game. Covered by table-driven tests in
`service_icarus_compile_test.go`
(`TestDownloadMod_DeployCompile_RoutesPerFile`,
`TestDownloadMod_DeployCompile_MixedFileMod`) — see `task-13-report.md`'s "Fix round
1" and "Plan delta" sections for full detail.

- [ ] **Step 6b: Add the `data_dump_path` game setting (rev4)**

The local dump-directory override is a per-game path, so it follows the same route
`cache_path` already takes — `games.yaml` → `GameConfig` → `domain.Game`, expanded and
round-tripped on save. Three one-line additions, no new config file and no new loader:

```go
// internal/storage/config/games.go — GameConfig, beside CachePath:
	BaseDataPath string `yaml:"data_dump_path,omitempty"`

// internal/storage/config/games.go — in the domain.Game literal, beside CachePath:
	BaseDataPath: ExpandPath(cfg.BaseDataPath),

// internal/storage/config/games.go — in the save path's GameConfig literal:
	BaseDataPath: game.BaseDataPath,
```

```go
// internal/domain/game.go — Game, beside CachePath:
	BaseDataPath string // Optional: directory holding an unpacked data.pak JSON
	// tree, used instead of fetching the hosted base-table dump (compile games only)
```

Documented YAML shape:

```yaml
games:
  icarus:
    name: Icarus
    install_path: /data/SteamLibrary/steamapps/common/Icarus
    mod_path: /data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Paks
    deploy_mode: compile
    # Optional. Point at a directory containing an unpacked data.pak JSON tree
    # (QuickBMS output, or IMM's extracted "data" folder) to compile from your
    # own extraction instead of the hosted community dump. It must match the
    # installed game version: it is byte-validated against the game's own pak
    # exactly like the hosted dump, and a mismatch is a hard error.
    data_dump_path: ~/icarus-data-dump
```

`ExpandPath` gives `~` handling for free, matching `cache_path`.

**No CLI or TUI surface is added, deliberately.** Compiling is pipeline-internal — it runs
inside `DownloadMod`, not as a user-invoked command — so this is configuration, not an
operation, exactly like `cache_path` and `deploy_mode`. Both interfaces already pick it up
by reading `games.yaml`, and neither grows a flag or a screen. This is **not** a CLI/TUI
parity gap: there is no new capability to surface in either.

Add one loader test alongside the existing games-config tests:

```go
func TestLoadGames_DataDumpPath(t *testing.T) {
	dir := t.TempDir()
	yaml := "games:\n  icarus:\n    name: Icarus\n    install_path: /games/icarus\n" +
		"    mod_path: /games/icarus/mods\n    data_dump_path: /dumps/week243\n"
	if err := os.WriteFile(filepath.Join(dir, "games.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	games, err := LoadGames(dir)
	if err != nil {
		t.Fatalf("LoadGames: %v", err)
	}
	if got := games["icarus"].BaseDataPath; got != "/dumps/week243" {
		t.Errorf("BaseDataPath = %q, want /dumps/week243", got)
	}
}
```

- [ ] **Step 7: Run tests to verify they pass**

```bash
go test ./internal/core/... -run TestDownloadModToCache_DeployCompile -v
go test ./internal/storage/config/... -run TestLoadGames_DataDumpPath -v
go test ./internal/core/... -v
```

Expected: PASS, and no regressions in the rest of `internal/core`.

- [ ] **Step 8: Update the Task 9 README example**

In `README.md`, change the Icarus `games.yaml` example's `deploy_mode: compile` (was already written that way in Task 9 — verify it matches the now-implemented `ParseDeployMode` string, `"compile"`).

As shipped, `docs/configuration.md` was also updated (its `deploy_mode` option table and "Deploy Mode" prose only listed `extract`/`copy`, so it was stale on this exact axis) — see `task-13-report.md`.

- [ ] **Step 9: Full build + vet + test sweep**

```bash
go build ./...
go vet ./...
go test ./... -v
```

Expected: all green.

- [ ] **Step 10: Commit**

```bash
git add internal/domain/game.go internal/storage/config/games.go internal/storage/config/games_test.go internal/source/source.go internal/source/icarus/icarus.go internal/core/service.go internal/core/service_icarus_compile_test.go README.md
git commit -m "feat: wire exmod compile step into cache-population pipeline (#136)"
```

---

## Post-plan manual validation (not automated — do this against your real Icarus install)

1. **Reader against the real install** (do this as soon as Task 2 is green — it is the
   acceptance gate the synthetic fixtures cannot provide):
   - `unrealpak.Open` on
     `<install>/Icarus/Content/Paks/pakchunk0-WindowsNoEditor.pak` succeeds and `Files()`
     returns exactly **9295** entries.
   - `unrealpak.Open` on `<install>/Icarus/Content/Data/data.pak` succeeds and `Files()`
     returns exactly **298** entries, all ending in `.json`.
   - `ReadFile("Factions/D_Factions.json")` on that `data.pak` returns 113 bytes of valid
     JSON beginning `{"RowStruct": "/Script/Icarus.Factions"`, and `ReadFile` on an
     Oodle-compressed table such as `Items/D_ItemsStatic.json` returns `ErrUnsupportedFormat`.

   Task 1 already verified these numbers against the real files; a mismatch means the index
   parser regressed, not that the install differs.

2. `resolveBasePak`'s path (Task 13, Step 6) is now `Icarus/Content/Data/data.pak` per the
   Task 1 spike — confirm it resolves on the target install.
   2b. **Base-table dump fetch + cross-validation** (rev3 — the acceptance gate for Task 12a):
   - `detectBuild` on the real install returns the version string in
     `Icarus/Config/version.json` (spike 3 saw `3.0.21.155335`, FeatureLevel
     `DangerousHorizons`).
   - The dump tree downloads unauthenticated from
     `https://codeload.github.com/GODOFMINECRAFT4/IcarusData/tar.gz/refs/heads/master`
     (~36 MB, a few seconds) and yields several hundred `.json` tables.
   - After `LF -> CRLF` restoration, dump tables byte-match the tables the local `data.pak`
     stores uncompressed. **Expect this to FAIL until the dump catches up** — at spike time
     the install was Week 243 and the dump HEAD was Week 236, giving 3 differing and 6
     missing stored tables. A correct implementation reports that mismatch clearly and
     refuses to compile; that is a PASS of the error path, not a bug.
   - With a genuinely matching week, `Compile` produces a `_P.pak` whose patched table
     differs from the dump's original only in the patched rows.
     2c. **Confirm the dump source is still maintained** before relying on it in a release. It is
     a single personal repo (`GODOFMINECRAFT4/IcarusData`, 0 stars, no CI) that has gone
     dormant for months before (Dec 2024 → Jul 2025). If it lags persistently, this strategy
     needs revisiting — see `docs/plans/icarus-pak-format-findings.md` Part 3.
3. **Confirm the mount point a `_P.pak` needs.** `Writer` stamps `defaultMountPoint`
   (`"../../../"`, matching Icarus's pakchunks), but the real `data.pak` uses an absolute
   cook-machine path (`C:/BA/work/.../Temp/Data/`). Which one makes the engine mount our
   override in the right place is unverified and can only be settled in-game.
4. Confirm the real Firestore project ID (Task 9, Step 1) and update `icarusFirestoreProjectID`.
5. Confirm/flip Firestore security rules to allow public reads on `mods` (design doc's stated assumption — never independently verified in this plan).
6. Run `lmm search icarus "bear"` (or the TUI equivalent) against the real catalog, install a known `.exmodz` mod end-to-end, and confirm Icarus actually loads the resulting `_P.pak` in-game.
7. Revisit the unresolved `-Compress` question from the research spike if the mod's effects don't show up in-game despite a clean compile — that was flagged as unresolved even by experienced modders and is the most likely real-world failure mode this plan's synthetic tests can't catch. Note the Task 1 spike gives this fresh weight: Icarus's own `data.pak` Oodle-compresses 258 of its 298 tables, so an all-stored override pak is _not_ what the engine normally sees.
