# Icarus Zlib Pivot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compile Icarus mods against base data tables read directly from the installed game's own `data.pak` — always week-correct, fully offline — by teaching `internal/unrealpak` to decompress Zlib entries, and delete the base-table dump subsystem that only ever existed to route around a blocker that was never there.

**Architecture:** `unrealpak.Reader` gains two things: the pak's own footer `CompressionMethods` table (previously parsed and discarded), and a Zlib read path in `ReadFile` that reassembles an entry from its per-block deflate streams. `icarus.Compile` then reads base tables straight from the base pak it already opens, which removes its only reason to consult a dump — so the whole dump subsystem (`datadump.go`, the hosted fetch, `validateDump`, `data_dump_path` config plumbing, and the `SetDataDir` wiring that existed to construct the store) is deleted rather than maintained. Oodle and any other unknown method stay a loud `ErrUnsupportedFormat`.

**Tech Stack:** Go 1.25.6 (this repo's version), standard library only — `compress/zlib` is stdlib, so this adds no dependency. No new third-party packages; `go.mod` is untouched.

## Why this supersedes #174

The Task-1 spike for #174 ([`icarus-quickbms-spike-findings.md`](icarus-quickbms-spike-findings.md)) established two facts by direct measurement:

1. **`Content/Data/data.pak` contains no Oodle.** Its footer declares `CompressionMethods = ["Zlib"]`, so its 258 compressed tables are Zlib. All 298 tables (40 stored + 258 Zlib) were reconstructed byte-for-byte with stdlib primitives alone — 40,908,881 bytes of decompressed table data, `Items/D_ItemsStatic.json` at 7,304,687 bytes.
2. **The earlier "258 Oodle" reading was a mislabel**, not a measurement error: the #136 rev3 sweep resolved this pak's compression-method _indices_ against `pakchunk0`'s table (`["Oodle","Zlib"]`, index 1 = Oodle) instead of `data.pak`'s own (`["Zlib"]`, index 1 = Zlib).

That single mislabel is the root of the hosted-dump strategy, the local-override hybrid, and the QuickBMS fallback. With it corrected, all three become unnecessary. [`2026-08-01-icarus-quickbms-fallback.md`](2026-08-01-icarus-quickbms-fallback.md) is obsolete and kept only for history — do not implement it.

Oodle is real in Icarus, but only in the `Content/Paks/pakchunk0*` **asset** chunks, which hold zero `.json` and which the compile path never reads.

## Global Constraints

- **Standard library only.** `compress/zlib` is stdlib; `go.mod` gains nothing. This holds the same line as the rest of the repo (`~/.claude/GO.md`).
- **Fail loud, no silent fallbacks** (repo precedent #95). An unknown or unsupported compression method, a hash mismatch, a block table that doesn't fit its payload, or a base table missing from the installed pak are each a hard error naming what went wrong. Nothing degrades quietly.
- **Method indices are resolved against the pak's own footer table, never assumed.** This is the specific mistake #175 exists to correct; a reader that hardcodes "index 1 = Oodle" is wrong for `data.pak` and a reader that hardcodes "index 1 = Zlib" is wrong for `pakchunk0`.
- **Story branch `dyoung522/175-zlib-pivot`, targeting `epic/icarus-136`** (`--base epic/icarus-136`). Reference **#175** in every commit and in the PR. No version bump in this story — the epic carries one at release.
- **No CLI or TUI surface changes.** The only user-visible change is the _removal_ of the `data_dump_path` game setting from the docs; no flag, command, or screen is added or altered. CLI/TUI parity holds trivially — shared core path.
- **Compiling becomes fully offline.** The "compile requires network access" constraint from the #136 epic no longer applies and is removed with the dump subsystem.

## Verification status of this plan's code

Every Go change in Tasks 1–4 was extracted into a scratch copy of the epic branch at `49f1784`, compiled, and run before this plan was finalized:

```text
gofmt -l ./cmd ./internal   -> clean
go build ./...              -> OK
go vet ./...                -> clean
go test ./...               -> 19/19 packages ok, 0 failures  (with the dump tests deleted)
grep -rn 'SetDataDir|DumpStore|data_dump_path|BaseDataPath' --include='*.go' . -> 0 hits
```

Against the real install:

```text
data.pak   : 298 entries, ReadFile succeeded on 298/298, all valid JSON, sizes match the index
             Items/D_ItemsStatic.json = 7,304,687 bytes
pakchunk0  : 5,155 entries now readable (stored + Zlib), 4,138 Oodle refused with
             `unsupported pak feature: compression method "Oodle" (index 1)`, 0 unexpected errors
```

---

## Task 1: `unrealpak` — parse the footer's CompressionMethods table

**Files:**

- Modify: `internal/unrealpak/pak.go`
- Modify: `internal/unrealpak/reader.go`
- Create: `internal/unrealpak/zlib_test.go`

**Interfaces:**

- Consumes: the existing `readFooter`/`footer` shape.
- Produces: `const maxCompressionMethods`, `const compressionMethodNameSize`, `const compressionMethodsOffset`, `const zlibMethodName`, `const maxUncompressedEntrySize`, `footer.methods [maxCompressionMethods]string`, `Reader.methods`, `func (r *Reader) methodName(method int32) string`. Task 2 depends on all of these.

- [ ] **Step 1: Write the failing test**

Create `internal/unrealpak/zlib_test.go` with just the fixture builder and the method-resolution test; Task 2 appends the rest. The Writer only ever emits stored entries, so a compressed fixture has to be hand-assembled.

```go
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
```

- [ ] **Step 2: Run to verify it fails (RED)**

```bash
cd /home/dyoung/Projects/orca/workspaces/linux-mod-manager/icarus-136
go test ./internal/unrealpak/... -run TestReadFile_MethodIndex -v
```

Expected: FAIL — `compressedHeaderSize`, `compressionMethodsOffset`, `compressionMethodNameSize` undefined.

- [ ] **Step 3: Add the constants to `pak.go`**

Insert directly above `// FileEntry describes one file inside a pak`:

```go
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
```

- [ ] **Step 4: Parse the table in `reader.go`**

Add the field to `footer`:

```go
type footer struct {
	version        int32
	indexOffset    int64
	indexSize      int64
	indexHash      [20]byte
	encryptedIndex bool
	methods        [maxCompressionMethods]string
}
```

Replace `readFooter`'s closing comment and `return` (the block currently beginning `// The trailing CompressionMethods name table is intentionally left`) with:

```go
	// The trailing CompressionMethods table names each compression method this
	// pak uses; entries reference them by 1-based index. It MUST be read from
	// this pak's own footer rather than assumed: Icarus's data.pak declares
	// ["Zlib"] (so index 1 means Zlib) while its pakchunks declare
	// ["Oodle","Zlib"] (so index 1 means Oodle). Assuming one pak's table
	// applies to another is exactly the mislabel that sent #136 chasing an
	// Oodle blocker that data.pak never had.
	for i := range ft.methods {
		slot := buf[compressionMethodsOffset+i*compressionMethodNameSize : compressionMethodsOffset+(i+1)*compressionMethodNameSize]
		ft.methods[i] = string(bytes.TrimRight(slot, "\x00"))
	}
	return ft, nil
}
```

Carry the table onto the `Reader`:

```go
// Reader provides read access to an unencrypted UE4-range pak. Stored entries
// and Zlib-compressed entries are readable; any other compression method is a
// loud ErrUnsupportedFormat.
type Reader struct {
	f        *os.File
	entries  []readerEntry
	fileSize int64                         // total size of the underlying file, for validateAllocSize
	methods  [maxCompressionMethods]string // this pak's own CompressionMethods table
}
```

and in `Open`'s final return:

```go
	return &Reader{f: f, entries: entries, fileSize: fileSize, methods: ft.methods}, nil
```

Add the resolver (Task 2 places it beside `ReadFile`; it can live anywhere in the file):

```go
// methodName resolves a 1-based CompressionMethodIndex against this pak's own
// footer table. An index with no corresponding name yields "", which no
// supported method matches, so it falls through to the unsupported-format
// error rather than being silently treated as stored.
func (r *Reader) methodName(method int32) string {
	if method < 1 || int(method) > len(r.methods) {
		return ""
	}
	return r.methods[method-1]
}
```

- [ ] **Step 5: Run — still RED, for the right reason**

```bash
go test ./internal/unrealpak/... -run TestReadFile_MethodIndex -v
```

Expected: still FAIL, now on `compressedHeaderSize` undefined — Task 2 supplies the read path. Confirm the constants and table parsing compile:

```bash
go build ./internal/unrealpak/
```

Expected: OK.

- [ ] **Step 6: Commit**

```bash
git add internal/unrealpak/pak.go internal/unrealpak/reader.go internal/unrealpak/zlib_test.go
git commit -m "feat: parse the pak footer's CompressionMethods table (#175)"
```

---

## Task 2: `unrealpak` — decompress Zlib entries in `ReadFile`

**Files:**

- Modify: `internal/unrealpak/reader.go`
- Modify: `internal/unrealpak/zlib_test.go`

**Interfaces:**

- Consumes: everything Task 1 produced.
- Produces: `readerEntry.size`, `readerEntry.blocks`, `func compressedHeaderSize(blocks int) int64`, `func (r *Reader) readStored(...)`, `func (r *Reader) readZlib(...)`, and a `ReadFile` that dispatches on the resolved method name. `ReadFile`'s exported signature is unchanged.

- [ ] **Step 1: Write the failing tests**

Append to `internal/unrealpak/zlib_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify they fail (RED)**

```bash
go test ./internal/unrealpak/... -run TestReadFile_ -v
```

Expected: FAIL — `compressedHeaderSize` undefined.

- [ ] **Step 3: Capture on-disk size and block count in `decodeEntry`**

Extend `readerEntry`:

```go
type readerEntry struct {
	FileEntry
	offset int64 // absolute offset of the entry's on-disk header
	method int32 // CompressionMethodIndex; 0 = stored, else a 1-based index
	// into the pak footer's CompressionMethods table.
	size int64 // on-disk size: compressed for a compressed entry, and equal
	// to FileEntry.Size (the uncompressed size) for a stored one.
	blocks int // compression block count; 0 for a stored entry.
}
```

In `decodeEntry`, replace the discarded Size read:

```go
	offset := read(flags&(1<<31) != 0)
	uncompressed := read(flags&(1<<30) != 0)
	size := uncompressed // a stored entry does not serialize Size; it equals UncompressedSize
	if method != 0 {
		size = read(flags&(1<<29) != 0)
	}
```

and its return:

```go
	return readerEntry{
		FileEntry: FileEntry{Size: uncompressed},
		offset:    offset,
		method:    method,
		size:      size,
		blocks:    blockCount,
	}, nil
```

- [ ] **Step 4: Replace `ReadFile` with a dispatching version plus the two read paths**

Add `"compress/zlib"` to `reader.go`'s imports. Replace the whole existing `ReadFile` function with:

```go
// ReadFile returns the bytes of the entry at mount-relative path.
//
// On-disk entry data is preceded by a full FPakEntry header — 53 bytes for a
// stored entry, plus a block table for a compressed one — and the index's
// offset points at that header, not the payload. The header is re-read and
// cross-checked rather than trusted: its method and sizes must agree with the
// index, and its Hash must match the on-disk payload's SHA1. Real paks satisfy
// all of this (verified across a whole install), so a disagreement means
// corruption or a layout this package misread.
func (r *Reader) ReadFile(path string) ([]byte, error) {
	for _, e := range r.entries {
		if e.Path != path {
			continue
		}
		if e.method == 0 {
			return r.readStored(path, e)
		}
		name := r.methodName(e.method)
		if strings.EqualFold(name, zlibMethodName) {
			return r.readZlib(path, e)
		}
		// Oodle and anything else this package cannot decode stay a hard
		// error. Refusing here rather than at index-parse time keeps Files()
		// able to enumerate a pak whose entries we cannot all read.
		return nil, fmt.Errorf("unrealpak: %s: %w: compression method %q (index %d)",
			path, ErrUnsupportedFormat, name, e.method)
	}
	return nil, fmt.Errorf("unrealpak: %s: %w", path, os.ErrNotExist)
}

// methodName resolves a 1-based CompressionMethodIndex against this pak's own
// footer table. An index with no corresponding name yields "", which no
// supported method matches, so it falls through to the unsupported-format
// error rather than being silently treated as stored.
func (r *Reader) methodName(method int32) string {
	if method < 1 || int(method) > len(r.methods) {
		return ""
	}
	return r.methods[method-1]
}

// readStored reads an uncompressed entry: a 53-byte header then the payload.
func (r *Reader) readStored(path string, e readerEntry) ([]byte, error) {
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
	n, err := validateAllocSize(e.Size, r.fileSize)
	if err != nil {
		return nil, fmt.Errorf("unrealpak: %s: %w", path, err)
	}
	buf := make([]byte, n)
	if _, err := r.f.ReadAt(buf, e.offset+storedHeaderSize); err != nil {
		return nil, fmt.Errorf("unrealpak: reading %s: %w", path, err)
	}
	if sum := sha1.Sum(buf); !bytes.Equal(sum[:], hdr[28:48]) { //nolint:gosec
		return nil, fmt.Errorf("unrealpak: %s: content hash mismatch", path)
	}
	return buf, nil
}

// compressedHeaderSize is the on-disk size of a compressed entry's FPakEntry
// header: the 53-byte stored shape plus a BlockCount(4) and a 16-byte
// (CompressedStart, CompressedEnd) pair per block, inserted between Hash and
// Flags.
func compressedHeaderSize(blocks int) int64 {
	return storedHeaderSize + 4 + 16*int64(blocks)
}

// readZlib reads and reassembles a Zlib-compressed entry.
//
// The entry's payload is split into independently-deflated blocks. The
// authoritative block table lives in the entry's own on-disk header as
// (CompressedStart, CompressedEnd) pairs measured from the entry offset — the
// index's optional block-size list is omitted for a lone unencrypted block, so
// it cannot be relied on. The blocks tile the payload region contiguously and
// their lengths sum to Size; the header Hash covers those on-disk (compressed)
// bytes, not the decompressed result.
//
// This procedure was validated by reconstructing all 298 tables of the real
// Icarus data.pak — 40 stored plus 258 Zlib — byte-for-byte.
// See docs/plans/icarus-quickbms-spike-findings.md.
func (r *Reader) readZlib(path string, e readerEntry) ([]byte, error) {
	if e.blocks <= 0 {
		return nil, fmt.Errorf("unrealpak: %s: %w: compressed entry declares %d compression blocks",
			path, ErrUnsupportedFormat, e.blocks)
	}
	hdrSize := compressedHeaderSize(e.blocks)
	hn, err := validateAllocSize(hdrSize, r.fileSize)
	if err != nil {
		return nil, fmt.Errorf("unrealpak: %s: entry header: %w", path, err)
	}
	hdr := make([]byte, hn)
	if _, err := r.f.ReadAt(hdr, e.offset); err != nil {
		return nil, fmt.Errorf("unrealpak: %s: reading entry header: %w", path, err)
	}
	if m := int32(binary.LittleEndian.Uint32(hdr[24:28])); m != e.method {
		return nil, fmt.Errorf("unrealpak: %s: entry header method %d disagrees with index method %d",
			path, m, e.method)
	}
	if size := int64(binary.LittleEndian.Uint64(hdr[8:16])); size != e.size {
		return nil, fmt.Errorf("unrealpak: %s: entry header size %d disagrees with index size %d",
			path, size, e.size)
	}
	if usize := int64(binary.LittleEndian.Uint64(hdr[16:24])); usize != e.Size {
		return nil, fmt.Errorf("unrealpak: %s: entry header uncompressed size %d disagrees with index size %d",
			path, usize, e.Size)
	}
	if nb := int64(int32(binary.LittleEndian.Uint32(hdr[48:52]))); nb != int64(e.blocks) {
		return nil, fmt.Errorf("unrealpak: %s: entry header block count %d disagrees with index count %d",
			path, nb, e.blocks)
	}

	pn, err := validateAllocSize(e.size, r.fileSize)
	if err != nil {
		return nil, fmt.Errorf("unrealpak: %s: %w", path, err)
	}
	payload := make([]byte, pn)
	if _, err := r.f.ReadAt(payload, e.offset+hdrSize); err != nil {
		return nil, fmt.Errorf("unrealpak: reading %s: %w", path, err)
	}
	if sum := sha1.Sum(payload); !bytes.Equal(sum[:], hdr[28:48]) { //nolint:gosec
		return nil, fmt.Errorf("unrealpak: %s: content hash mismatch", path)
	}

	if e.Size < 0 || e.Size > maxUncompressedEntrySize {
		return nil, fmt.Errorf("unrealpak: %s: %w: uncompressed size %d exceeds the %d-byte cap",
			path, ErrUnsupportedFormat, e.Size, int64(maxUncompressedEntrySize))
	}
	out := make([]byte, 0, e.Size)
	for i := 0; i < e.blocks; i++ {
		start := int64(binary.LittleEndian.Uint64(hdr[52+i*16 : 60+i*16]))
		end := int64(binary.LittleEndian.Uint64(hdr[60+i*16 : 68+i*16]))
		// Block bounds are relative to the entry offset and must land inside
		// the payload region that follows the header.
		if start < hdrSize || end < start || end > hdrSize+e.size {
			return nil, fmt.Errorf("unrealpak: %s: block %d spans [%d,%d), outside the entry's payload",
				path, i, start, end)
		}
		zr, err := zlib.NewReader(bytes.NewReader(payload[start-hdrSize : end-hdrSize]))
		if err != nil {
			return nil, fmt.Errorf("unrealpak: %s: block %d: %w", path, i, err)
		}
		// Read at most one byte more than the declared size still allows, so a
		// lying UncompressedSize cannot drive an unbounded read.
		remaining := e.Size - int64(len(out))
		chunk, err := io.ReadAll(io.LimitReader(zr, remaining+1))
		zr.Close() //nolint:errcheck // read-only decompressor
		if err != nil {
			return nil, fmt.Errorf("unrealpak: %s: decompressing block %d: %w", path, i, err)
		}
		if int64(len(chunk)) > remaining {
			return nil, fmt.Errorf("unrealpak: %s: decompressed output exceeds the declared uncompressed size %d",
				path, e.Size)
		}
		out = append(out, chunk...)
	}
	if int64(len(out)) != e.Size {
		return nil, fmt.Errorf("unrealpak: %s: decompressed %d bytes, header declares %d",
			path, len(out), e.Size)
	}
	return out, nil
}
```

Delete the now-stale trailing comment on `readerEntry.method` that said non-zero entries "cannot be read".

- [ ] **Step 5: Run tests to verify they pass (GREEN)**

```bash
gofmt -l ./internal/unrealpak
go test ./internal/unrealpak/... -v
```

Expected: all five `TestReadFile_*` tests pass, plus every pre-existing test in the package.

- [ ] **Step 6: Sanity-check against the real install (manual, not committed)**

The synthetic fixture proves internal consistency; the real pak proves the format reading. Write a throwaway `main` under the repo (so `internal/` is importable), run it, then delete it:

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

func main() {
	p, err := unrealpak.Open("/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak")
	if err != nil {
		panic(err)
	}
	defer p.Close() //nolint:errcheck
	ok, bad, total := 0, 0, 0
	for _, f := range p.Files() {
		b, err := p.ReadFile(f.Path)
		if err != nil {
			bad++
			continue
		}
		var v any
		if json.Unmarshal(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}), &v) != nil || int64(len(b)) != f.Size {
			bad++
			continue
		}
		ok++
		total += len(b)
	}
	fmt.Printf("read+valid-JSON+size-match=%d failed=%d totalBytes=%d\n", ok, bad, total)
	big, _ := p.ReadFile("Items/D_ItemsStatic.json")
	fmt.Printf("Items/D_ItemsStatic.json = %d bytes\n", len(big))
}
```

Expected, exactly:

```text
read+valid-JSON+size-match=298 failed=0 totalBytes=40936840
Items/D_ItemsStatic.json = 7304687 bytes
```

- [ ] **Step 7: Commit**

```bash
git add internal/unrealpak/reader.go internal/unrealpak/zlib_test.go
git commit -m "feat: decompress Zlib pak entries with the standard library (#175)"
```

---

## Task 3: `icarus.Compile` reads base tables from the installed pak; delete the dump subsystem

This is one atomic change: `Compile` losing its dump dependency is what makes the dump subsystem dead, and the subsystem cannot be half-removed and still compile. Everything here lands in a single green commit.

**Files:**

- Modify: `internal/source/icarus/compile.go`
- Modify: `internal/source/icarus/compile_test.go`
- Modify: `internal/source/icarus/icarus.go`
- Modify: `internal/source/icarus/icarus_test.go`
- Modify: `internal/source/source.go`
- Modify: `internal/core/service.go`
- Modify: `internal/core/service_icarus_compile_test.go`
- Create: `internal/source/icarus/helpers_test.go`
- **Delete:** `internal/source/icarus/datadump.go`
- **Delete:** `internal/source/icarus/datadump_test.go`

**Interfaces:**

- Consumes: `unrealpak.Reader.ReadFile` (Task 2).
- Produces: `func Compile(basePakPath, exmodzPath, outputPakPath string) (err error)` (was `Compile(ctx, dumps, basePakPath, localDumpDir, exmodzPath, outputPakPath)`), `source.Compiler.Compile(ctx context.Context, basePakPath, sourceFilePath, outputPath string) error` (drops `baseDataPath`), and `func writeTestBasePak(t *testing.T, files map[string][]byte) string` relocated to `helpers_test.go`.
- Removes: `Dump`, `DumpStore`, `newDumpStore`, `DumpForBuild`, `loadLocalDump`, `fetchTree`, `validateDump`, `toCRLF`, `summarize`, `Build`, `detectBuild`, `Icarus.dumps`, `Icarus.SetDataDir`.

- [ ] **Step 1: Update the tests first (RED)**

`writeTestBasePak` currently lives in `datadump_test.go` but is used six times by `compile_test.go`, so it must move before that file is deleted. Create `internal/source/icarus/helpers_test.go`:

```go
package icarus

import (
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// writeTestBasePak builds a stored, unencrypted version-11 pak holding one
// entry per (mount-relative path, content) pair, via the Task 4 Writer. It is
// the shared fixture builder for this package's compile tests.
func writeTestBasePak(t *testing.T, files map[string][]byte) string {
	t.Helper()
	pakPath := filepath.Join(t.TempDir(), "data.pak")
	w, err := unrealpak.Create(pakPath)
	if err != nil {
		t.Fatalf("creating test base pak: %v", err)
	}
	for rel, data := range files {
		if err := w.AddFile(rel, data); err != nil {
			t.Fatalf("AddFile(%q): %v", rel, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing test base pak: %v", err)
	}
	return pakPath
}
```

In `compile_test.go`: delete the `testDumpStore` helper entirely, drop the `"context"`, `"net/http"` and `"net/http/httptest"` imports, remove every `dumps := testDumpStore(...)` line, and rewrite each call as `Compile(basePak, exmodzPath, outputPath)`.

Then replace `TestCompile_DumpWeekMismatch_FailsBeforeWriting` — a week mismatch is no longer expressible, since there is only one source of base tables — with two tests that pin what actually matters now:

```go
// The base table Compile patches must come from the installed pak itself —
// that is the whole point of the #175 pivot — so a row's output has to reflect
// the pak's own bytes, not any other source.
func TestCompile_PatchesTheBasePaksOwnTable(t *testing.T) {
	basePak := writeTestBasePak(t, map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200,"OnlyInPak":true}}`),
	})
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	if err := Compile(basePak, exmodzPath, outputPath); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening compiled output: %v", err)
	}
	defer r.Close() //nolint:errcheck
	got, err := r.ReadFile("AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// The patched field changed...
	if !bytes.Contains(got, []byte(`"BaseMovementSpeed":235`)) {
		t.Errorf("patched table = %s, want BaseMovementSpeed 235", got)
	}
	// ...and a field only the base pak carried survived, proving the base
	// content was read from the pak rather than synthesized.
	if !bytes.Contains(got, []byte(`"OnlyInPak":true`)) {
		t.Errorf("patched table = %s, want the base pak's OnlyInPak field preserved", got)
	}
}

// A CurrentFile with no matching entry in the base pak fails before any output
// pak is written.
func TestCompile_UnknownBaseTable_LeavesNoOutputFile(t *testing.T) {
	basePak := writeTestBasePak(t, map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{}}`),
	})
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_NotInPak.json","File_Items":[{"Name":"X","V":1}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	if err := Compile(basePak, exmodzPath, outputPath); err == nil {
		t.Fatal("expected an error for a CurrentFile absent from the base pak, got nil")
	}
	if _, statErr := os.Stat(outputPath); statErr == nil {
		t.Error("no output pak should exist after a failed compile")
	}
}
```

In `icarus_test.go`: delete `TestIcarus_Compile_WithoutDataDir_FailsLoudly` and `TestIcarus_SetDataDir_ConstructsDumpStore` (both assert on a store that no longer exists), and drop the now-unused `"strings"` import.

In `internal/core/service_icarus_compile_test.go`, update the fake:

```go
func (s *fakeCompilerSource) Compile(ctx context.Context, basePakPath, sourceFilePath, outputPath string) error {
```

```bash
go vet ./internal/source/icarus/... ./internal/core/...
```

Expected: FAIL — `Compile` still has its old signature.

- [ ] **Step 2: Rewrite `Compile`**

In `compile.go`, drop the `"context"` import and replace the doc comment, signature, and the dump lookup:

```go
// Compile reads exmodzPath's .EXMOD diff, applies it to the game's base data
// tables, bundles in any pre-built assets the .EXMODZ carries, and writes the
// result as a new pak at outputPakPath ready to deploy as-is.
//
// Base tables are read directly out of basePakPath — the installed game's own
// Content/Data/data.pak — so they are always week-correct by construction and
// the whole operation is offline. That pak stores 40 tables uncompressed and
// compresses the other 258 with Zlib, all of which internal/unrealpak reads
// with the standard library (#175). basePakPath is also what resolves a bare,
// hyphen-flattened CurrentFile to a real mount path.
//
// There is no ctx parameter: every step is local file I/O over a ~2 MB pak,
// with no network call and no long-running loop to cancel. The
// source.Compiler interface still takes one, for implementations that need it.
func Compile(basePakPath, exmodzPath, outputPakPath string) (err error) {
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
	defer base.Close() //nolint:errcheck

	out, err := unrealpak.Create(outputPakPath)
```

(The `dump, err := dumps.DumpForBuild(...)` block and its comment are deleted outright; `unrealpak.Create` now follows the `base.Close()` defer directly.)

Inside the row loop, replace the dump lookup with a read from the pak:

```go
		baseData, err := base.ReadFile(mountPath)
		if err != nil {
			return fmt.Errorf("icarus: reading base data table %s: %w", mountPath, err)
		}
```

- [ ] **Step 3: Simplify the source and the interface**

`internal/source/source.go` — drop `baseDataPath`:

```go
// basePakPath is resolved by the caller from game.InstallPath; sourceFilePath
// is the just-downloaded file; outputPath is where the compiled result must be
// written.
type Compiler interface {
	Compile(ctx context.Context, basePakPath, sourceFilePath, outputPath string) error
}
```

`internal/source/icarus/icarus.go` — drop the `dumps` field, delete `SetDataDir` entirely, and simplify the method:

```go
type Icarus struct {
	firestore *firestoreClient
}
```

```go
// Compile implements source.Compiler by delegating to the package-level
// Compile function. ctx is unused: compiling is pure local file I/O against
// the installed game's own pak (#175), with nothing to cancel.
func (s *Icarus) Compile(_ context.Context, basePakPath, sourceFilePath, outputPath string) error {
	return Compile(basePakPath, sourceFilePath, outputPath)
}
```

`internal/core/service.go` — the call site:

```go
		if err := compiler.Compile(ctx, basePakPath, archivePath, destPath); err != nil {
			return nil, fmt.Errorf("compiling mod: %w", err)
		}
```

- [ ] **Step 4: Delete the dump subsystem**

```bash
rm internal/source/icarus/datadump.go internal/source/icarus/datadump_test.go
```

That removes, in one stroke: the hosted-tree fetch (`fetchTree`, `defaultDumpTreeURL`, `maxDumpBytes`, `maxTarEntrySize`), `loadLocalDump`, `validateDump`, `toCRLF`, `summarize`, `Dump`/`DumpStore`/`newDumpStore`/`DumpForBuild`, and `Build`/`detectBuild`.

- [ ] **Step 5: Run tests to verify they pass (GREEN)**

```bash
gofmt -l ./cmd ./internal
go build ./...
go vet ./...
go test ./internal/source/icarus/... ./internal/core/... ./internal/source/... -v 2>&1 | tail -20
```

Expected: build, vet and gofmt clean; all tests pass.

- [ ] **Step 6: Commit**

```bash
git add -A internal/source/icarus internal/source/source.go internal/core/service.go internal/core/service_icarus_compile_test.go
git commit -m "feat: compile Icarus mods from the installed pak, drop the dump subsystem (#175)"
```

---

## Task 4: Remove the `data_dump_path` config plumbing and `SetDataDir` wiring

With Task 3 landed, `domain.Game.BaseDataPath` has no reader and no source implements `SetDataDir`, so both are dead weight.

**Files:**

- Modify: `internal/domain/game.go`
- Modify: `internal/storage/config/games.go`
- **Delete:** `internal/storage/config/games_test.go`
- Modify: `cmd/lmm/root.go`
- Modify: `cmd/lmm/root_test.go`

**Interfaces:**

- Removes: `domain.Game.BaseDataPath`, `config.GameConfig.BaseDataPath` (`yaml:"data_dump_path"`), the `SetDataDir` duck-typed setter call, and the `dataDir` parameter from `registerSources`/`registerSource`/`registerCustomSources`.

- [ ] **Step 1: Remove the field from the domain and config**

`internal/domain/game.go` — delete:

```go
	// BaseDataPath is optional: a directory holding an unpacked data.pak JSON
	// tree, used instead of fetching the hosted base-table dump (compile games only)
	BaseDataPath string
```

`internal/storage/config/games.go` — delete the struct field, the load mapping, and the save mapping:

```go
	BaseDataPath string            `yaml:"data_dump_path,omitempty"`   // from GameConfig
			BaseDataPath:       ExpandPath(cfg.BaseDataPath),       // from loadGamesLocked
			BaseDataPath: game.BaseDataPath,                        // from saveGamesLocked
```

Removing the longest field name changes struct-tag alignment, so re-run `gofmt -w internal/storage/config/games.go`.

`internal/storage/config/games_test.go` contains only `TestLoadGames_DataDumpPath`, which tested exactly this key — delete the file:

```bash
rm internal/storage/config/games_test.go
```

- [ ] **Step 2: Remove the `SetDataDir` wiring**

In `cmd/lmm/root.go`, delete the setter call:

```go
	if setter, ok := src.(interface{ SetDataDir(string) }); ok {
		setter.SetDataDir(dataDir)
	}
```

and drop the now-unused parameter from all three functions and their call sites:

```go
func registerSources(svc *core.Service, cfgDir string) {
	for _, factory := range builtinSourceFactories {
		registerSource(svc, factory())
	}

	registerCustomSources(svc, cfgDir)
}
```

```go
func registerSource(svc *core.Service, src source.ModSource) {
```

```go
func registerCustomSources(svc *core.Service, cfgDir string) {
```

with `registerSource(svc, src)` inside `registerCustomSources`, and `registerSources(svc, cfg.ConfigDir)` at the `initService` call site.

Two doc comments assert the removed behavior and must go with it. In `registerSources`, cut the trailing sentence so it reads:

```go
// registerSources registers all available mod sources with the service
// through one ordered pipeline: built-ins first (so the collision rule's
// "first wins" preserves their identity against a same-id custom
// definition), then user-defined sources from <configDir>/sources/.
```

and in `registerSource`, end the pipeline description at the API key:

```go
// source accepts one → RegisterSource.
```

The package-level `dataDir` variable (the `--data` flag) is unrelated and stays.

In `cmd/lmm/root_test.go`, delete the `recordingDataDirSource` type and `TestRegisterSource_WiresDataDir`, then fix the arity of the remaining calls: `registerSource(svc, mock)`, `registerSources(svc, t.TempDir())`, `registerSources(svc, cfgDir)`, `registerCustomSources(svc, cfgDir)`.

- [ ] **Step 3: Run the full suite (GREEN)**

```bash
gofmt -l ./cmd ./internal
go build ./... && go vet ./...
go test ./... 2>&1 | grep -E 'FAIL|^ok'
grep -rn 'SetDataDir\|DumpStore\|data_dump_path\|BaseDataPath' --include='*.go' .
```

Expected: gofmt/build/vet clean, **19 packages ok and 0 FAIL**, and the final grep returns **no hits** — the subsystem is gone from Go code entirely.

- [ ] **Step 4: Commit**

```bash
git add internal/domain/game.go internal/storage/config/games.go cmd/lmm/root.go cmd/lmm/root_test.go
git rm internal/storage/config/games_test.go
git commit -m "refactor: drop the data_dump_path setting and SetDataDir wiring (#175)"
```

---

## Task 5: Product docs — README, configuration.md, CHANGELOG

**Files:**

- Modify: `README.md`
- Modify: `docs/configuration.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: `docs/configuration.md`**

Delete the `data_dump_path` row from the per-game settings table:

```markdown
| `data_dump_path` | string | no | Compile-mode only: local unpacked data.pak JSON tree, used instead of the hosted base-table dump |
```

Removing the longest cell narrows the column, so re-pad the remaining rows so the table stays aligned.

Replace the `compile` deploy-mode bullet (currently describing `data_dump_path` and the hosted dump) with:

```markdown
- **`compile`**: The downloaded file is compiled into a new artifact before caching (currently Icarus only: an `.exmodz` diff is applied to the game's base data tables to produce a deployable `_P.pak`). Only sources that implement compiling support this mode. The base data tables are read directly from the installed game's own `data.pak`, so a compile always matches the installed game version and needs no network access.
```

- [ ] **Step 2: `README.md`**

In the `icarus` games.yaml example, delete the two commented `data_dump_path` lines so the block reads:

```yaml
icarus:
  name: "Icarus"
  install_path: "/path/to/Steam/steamapps/common/Icarus"
  mod_path: "/path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods"
  deploy_mode: compile
  sources:
    icarus: "icarus"
```

- [ ] **Step 3: `CHANGELOG.md`**

The `[Unreleased]` Icarus entry currently advertises `data_dump_path` and the hosted dump, neither of which will ship. Replace that entry's final sentence — everything from "An optional per-game `data_dump_path`…" to the end — with:

```markdown
Base data tables are read directly from the installed game's own `data.pak`, so a compile always matches the installed game version and works entirely offline; `internal/unrealpak` reads both the stored and the Zlib-compressed entries that pak contains, using only the standard library (#136, #175)
```

- [ ] **Step 4: Verify**

```bash
trunk check --filter=markdownlint README.md docs/configuration.md CHANGELOG.md 2>&1 | tail -20
grep -rn 'data_dump_path' README.md docs/ CHANGELOG.md
```

Expected: no new markdownlint findings, and the grep returns nothing outside `docs/plans/` (which is gitignored history).

- [ ] **Step 5: Commit**

```bash
git add README.md docs/configuration.md CHANGELOG.md
git commit -m "docs: drop data_dump_path, document compiling from the installed pak (#175)"
```

---

## Task 6: Correct the #136 findings and plan docs

These are gitignored in-flight documents (`.gitignore:65 docs/plans/*`), so there is nothing to commit — but they are the record future work reads, and both currently assert an Oodle blocker that does not exist. **Correct them inline with a dated note; do not silently rewrite history.** The wrong conclusion is instructive, and erasing it would hide a real methodological lesson.

**Files:**

- Modify: `docs/plans/icarus-pak-format-findings.md`
- Modify: `docs/plans/2026-07-29-icarus-exmod-pak-compilation.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Correct the findings doc**

`icarus-pak-format-findings.md` asserts the blocker in three places (§ "⚠ BLOCKING RISK", the Part 2 verdict item 2, and a Part 3 line claiming `data.pak` "contains **no Zlib entries** (only 40 stored + 258 Oodle)"). Prepend a correction banner immediately under the "⚠ BLOCKING RISK: 258 of `data.pak`'s 298 JSON files are Oodle-compressed" heading:

```markdown
> **CORRECTION (2026-08-01, #175): this section is WRONG — those 258 tables are Zlib, not Oodle.**
> The counts here are right; the method _name_ is not. This section resolved `data.pak`'s
> compression-method indices against `pakchunk0`'s method table (`["Oodle","Zlib"]`, where
> index 1 = Oodle) instead of reading `data.pak`'s own footer table, which is `["Zlib"]` —
> so index 1 means **Zlib** in this pak. All 258 decompress with stdlib `compress/zlib`;
> all 298 tables were reconstructed byte-for-byte. See
> [`icarus-quickbms-spike-findings.md`](icarus-quickbms-spike-findings.md) §6b and plan
> [`2026-08-01-icarus-zlib-pivot.md`](2026-08-01-icarus-zlib-pivot.md).
>
> Everything below is left as originally written. The lesson is worth keeping: a per-pak
> fact was asserted from another pak's metadata, and that single mislabel drove the hosted
> dump strategy, the local-override hybrid, and the QuickBMS fallback — all now obsolete.
```

Add a one-line correction to the Part 2 verdict's item 2 and to the Part 3 "no Zlib entries" line, each pointing at the same banner, e.g.:

```markdown
> **CORRECTED 2026-08-01 (#175):** `data.pak` is Zlib, not Oodle — see the correction banner in Part 3.
```

- [ ] **Step 2: Correct the #136 plan doc's prose**

`2026-07-29-icarus-exmod-pak-compilation.md` asserts the blocker in its Global Constraints (the "compile path requires network access" bullet, which cites Oodle), in Task 12's `> **Base tables come from a hosted per-week dump…** ` note, in Task 12a's rationale, and in Task 13's surrounding prose. Add a single banner immediately below the plan's `# ` title rather than editing each site:

```markdown
> **SUPERSEDED IN PART (2026-08-01, #175).** Everywhere this plan states that `data.pak`'s
> tables are Oodle-compressed and therefore unreadable — the Global Constraints' network
> bullet, Task 12's base-table note, Task 12a (the dump fetcher) and Task 13's `data_dump_path`
> wiring — the premise is false: those tables are **Zlib**, which the standard library reads.
> Tasks 1–11 (the pak format work, the Firestore source, `.EXMOD`/`.EXMODZ` handling) are
> unaffected and shipped as written. The dump subsystem those later tasks built has been
> removed by [`2026-08-01-icarus-zlib-pivot.md`](2026-08-01-icarus-zlib-pivot.md); read that
> for the current design. This document is kept unedited below as the record of how the epic
> was actually built.
```

- [ ] **Step 3: Verify the cross-references resolve**

```bash
grep -n 'CORRECTION\|CORRECTED\|SUPERSEDED IN PART' docs/plans/icarus-pak-format-findings.md docs/plans/2026-07-29-icarus-exmod-pak-compilation.md
ls docs/plans/icarus-quickbms-spike-findings.md docs/plans/2026-08-01-icarus-zlib-pivot.md
```

Expected: four correction markers, and both referenced files exist.

- [ ] **Step 4: No commit**

`docs/plans/*` is gitignored. Leave the edits in the working tree and record completion in the task tracker.

---

## Task 7: Manual validation gate — the first real end-to-end compile

This is the acceptance gate the whole epic has been blocked on, and #175 is what unblocks it. **Not automated, not CI**: it needs the real game install and a real mod. Run it on the reference machine after Tasks 1–5 are green.

**Files:** none — this task produces a `_P.pak` on disk and a recorded result.

**Interfaces:** none.

- [ ] **Step 1: Obtain a real `.EXMODZ`**

`Bear_Mount.EXMODZ` is the mod the epic's fixtures were modelled on. Fetch it from the catalog the Icarus source already reads, or from the ecosystem mods repo:

```bash
mkdir -p /tmp/icarus-e2e && cd /tmp/icarus-e2e
curl -sL -o Bear_Mount.EXMODZ \
  "https://github.com/Jimk72/Icarus_Mods/raw/main/Bear_Mount.EXMODZ"
ls -la Bear_Mount.EXMODZ && file Bear_Mount.EXMODZ
```

Expected: a multi-megabyte Zip archive (~2.7 MB at the time of writing). If the URL has moved, `lmm search icarus bear` and the catalog's download URL is the supported route — the point is a genuine, unmodified mod file, not a fixture.

- [ ] **Step 2: Compile it against the real install**

Write a throwaway `main` inside the repo (so `internal/` is importable), run it, and delete it afterwards:

```go
package main

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
)

func main() {
	const (
		basePak = "/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak"
		exmodz  = "/tmp/icarus-e2e/Bear_Mount.EXMODZ"
		out     = "/tmp/icarus-e2e/Bear_Mount_P.pak"
	)
	if err := icarus.Compile(basePak, exmodz, out); err != nil {
		fmt.Println("COMPILE FAILED:", err)
		return
	}
	fmt.Println("COMPILE OK ->", out)
}
```

```bash
go run ./cmd/e2e-compile   # or wherever the throwaway main was placed
ls -la /tmp/icarus-e2e/Bear_Mount_P.pak
```

**Expected outcome:** `COMPILE OK`, and a `Bear_Mount_P.pak` of non-trivial size on disk. This is the first time this pipeline has produced a real artifact — every previous attempt died at the base-table step.

If it fails, the error names the step. The two most likely genuine failures, neither of which is a Zlib problem:

- `no matching file in base pak (expected mount path …)` — the `.EXMOD`'s `CurrentFile` does not resolve. Record the exact `CurrentFile` and the mount paths present; that is a `matchMountPath` finding, not a pivot regression.
- A `.EXMOD` schema surprise (a row shape `ApplyRowPatch` does not handle). Record the offending row verbatim.

- [ ] **Step 3: Inspect the produced pak**

```go
package main

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

func main() {
	r, err := unrealpak.Open("/tmp/icarus-e2e/Bear_Mount_P.pak")
	if err != nil {
		panic(err)
	}
	defer r.Close() //nolint:errcheck
	for _, f := range r.Files() {
		fmt.Printf("%10d  %s\n", f.Size, f.Path)
	}
}
```

**Expected outcome:** the output pak enumerates cleanly and lists (a) every data table the mod patched, at a size close to the base table's, and (b) every asset the `.EXMODZ` bundled. Record the full listing.

- [ ] **Step 4: Confirm the patch actually applied**

For one patched table, diff the compiled pak's copy against the installed pak's copy — they must differ **only** in the fields the `.EXMOD` targets:

```bash
# read the same table from both paks via a throwaway main, write to /tmp, then:
diff <(python3 -m json.tool /tmp/icarus-e2e/base_table.json) \
     <(python3 -m json.tool /tmp/icarus-e2e/patched_table.json) | head -40
```

**Expected outcome:** a small, targeted diff on the modded rows only (for Bear_Mount, mount/creature stats), with every other row byte-identical.

- [ ] **Step 5: Deploy and load in-game (the last unverified link)**

Copy the `_P.pak` into the game's mod path and launch Icarus.

**Expected outcome:** the game loads and the mod's effects are visible. If the game loads but the mod has no effect, the most likely cause is the **mount point** — `unrealpak.Writer` stamps `defaultMountPoint` (`"../../../"`), while the real `data.pak` uses an absolute cook path (`C:/BA/work/.../Temp/Data/`). That question has been open since the #136 spike and this is the moment it can finally be settled; record which mount point works and file a follow-up if it needs changing.

This step is the only one whose outcome is genuinely unknown — Steps 1–4 are expected to pass. Record what happens either way.

- [ ] **Step 6: Record the result**

Write the outcome into the epic's tracker: whether a `_P.pak` was produced, its size and entry count, the patched-table diff, and the in-game result. Nothing to commit.

---

## Post-plan verification

1. Whole suite from a clean cache, to catch anything the incremental runs cached:

   ```bash
   go clean -testcache && go test ./...
   ```

   Expected: 19 packages ok, 0 failures.

2. No trace of the removed subsystem outside gitignored history:

   ```bash
   grep -rn 'SetDataDir\|DumpStore\|DumpForBuild\|validateDump\|data_dump_path\|BaseDataPath' \
     --include='*.go' --include='*.md' --include='*.yaml' . | grep -v '^./docs/plans/'
   ```

   Expected: no hits.

3. The reader still refuses what it cannot read, on the real install: `ReadFile` on any
   `pakchunk0` Oodle entry returns `ErrUnsupportedFormat` naming `"Oodle"`, while its stored
   and Zlib entries read successfully (5,155 readable / 4,138 refused / 0 unexpected errors at
   the time of writing).
