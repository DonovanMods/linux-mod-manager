# Icarus PAK Footer/Index Format — Empirical Findings (#136 Task 1)

Scratch findings doc from an empirical, read-only spike against the user's real Icarus Steam
install. No product code was written. Not shipped as product docs — kept for Task 2's
reference while implementing `internal/unrealpak`.

## Files inspected

Icarus's Steam install lives on a secondary library, not under `~/.steam` or
`~/.local/share/Steam` as the task brief assumed — found instead under `/data/SteamLibrary`.

Base pakchunk (primary subject of this spike):

```text
/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Paks/pakchunk0-WindowsNoEditor.pak
size: 1,421,136,117 bytes
```

Icarus ships one base pakchunk (`pakchunk0-WindowsNoEditor.pak`) plus 32 numbered split
pakchunks (`pakchunk0_s1` .. `pakchunk0_s32`), all in the same directory, ranging from
~47 MB to ~1.9 GB. A second pakchunk was spot-checked for consistency (see Cross-check
section below):

```text
/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Paks/pakchunk0_s10-WindowsNoEditor.pak
size: 1,645,159,864 bytes
```

## Result summary

| Field             | Value                                                                                          |
| ----------------- | ---------------------------------------------------------------------------------------------- |
| `Version`         | **11** (`PakFile_Version_Fnv64BugFix`)                                                         |
| Footer size       | **221 bytes** — matches the plan's upper-bound assumption                                      |
| `bEncryptedIndex` | **0 (false)** — confirmed, but the brief's naive parse script reads the wrong byte (see below) |
| SHA1 cross-check  | **Match**                                                                                      |

The plan's core assumptions (221-byte footer, `bEncryptedIndex == false`) are **confirmed**.
However, the brief's Step 3 field-offset script does not correctly locate `bEncryptedIndex`
for this pak version — see "Deviation from the brief's naive layout" below. This is a
significant finding for Task 2's implementation and must be reflected in
`internal/unrealpak`'s field-offset constants.

## Step 2: Locating the magic

```bash
PAK=/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Paks/pakchunk0-WindowsNoEditor.pak
tail -c 256 "$PAK" | xxd | tail -20
```

```
00000000: 0100 0000 1700 0000 5669 7375 616c 4465  ........VisualDe
00000010: 6275 6767 6572 2e75 706c 7567 696e 0058  bugger.uplugin.X
00000020: 8002 0000 0000 0000 0000 0000 0000 0000  ................
00000030: 0000 0000 e112 6f5a 0b00 0000 f6f3 a954  ......oZ.......T
00000040: 0000 0000 d680 0200 0000 0000 5d2f 07c9  ............]/..
00000050: 209f 8a50 f17e 15a3 f656 2e07 d85b 9ab0   ..P.~...V...[..
00000060: 4f6f 646c 6500 0000 0000 0000 0000 0000  Oodle...........
00000070: 0000 0000 0000 0000 0000 0000 0000 0000  ................
00000080: 5a6c 6962 0000 0000 0000 0000 0000 0000  Zlib............
00000090: 0000 0000 0000 0000 0000 0000 0000 0000  ................
...(zero-padded to end of file)...
```

Magic (`E1 12 6F 5A`, i.e. `0x5A6F12E1` little-endian) found at offset 52 within the
last-256-byte tail window (absolute file offset 1,421,135,913). No larger tail window was
needed — 256 bytes was sufficient.

## Step 3: Parsing the footer — brief's naive script vs. actual UE4 layout

Running the brief's Step 3 script verbatim against the found magic offset:

```
version: 11
index_offset: 1420424182 index_size: 164054
index_hash (hex): 5d2f07c9209f8a50f17e15a3f6562e07d85b9ab0
footer start offset: 1421135913 -> footer size: 204
bEncryptedIndex byte: 79
```

`version`, `index_offset`, `index_size`, and `index_hash` all parse correctly (confirmed by
the SHA1 cross-check below). But `bEncryptedIndex byte: 79` (0x4F, ASCII `'O'`) is **not a
valid bool byte** — it's actually the first character of the string `"Oodle"` from the
compression-methods block. The brief's script assumes `bEncryptedIndex` immediately follows
`IndexHash`, which is the pre-UE4.25 layout and does not hold for version 11.

### Actual `FPakInfo` layout for version 11 (`PakFile_Version_Fnv64BugFix`)

Per UE4/5 source (`IPlatformFilePak.h`, `FPakInfo::Serialize`), two version gates change the
footer shape between the pre-UE4.22 layout the plan assumed and version 11:

- `PakFile_Version_EncryptionKeyGuid` (7): adds a 16-byte `EncryptionKeyGuid` **before**
  `Magic`.
- `PakFile_Version_IndexEncryption` (4): adds the 1-byte `bEncryptedIndex` **before** `Magic`
  (immediately after `EncryptionKeyGuid` when both apply).
- `PakFile_Version_FNameBasedCompressionMethod` (8): adds a `CompressionMethods` array
  (5 slots × 32 bytes, fixed-width null-terminated ASCII names) **after** `IndexHash`.

So the true footer layout for version 11 is:

```
EncryptionKeyGuid (16 bytes)
bEncryptedIndex   (1 byte)
Magic             (4 bytes)
Version           (4 bytes, int32)
IndexOffset       (8 bytes, int64)
IndexSize         (8 bytes, int64)
IndexHash         (20 bytes)
CompressionMethods (5 × 32 = 160 bytes)
---
Total: 16+1+4+4+8+8+20+160 = 221 bytes
```

Re-parsing with this corrected layout (offsets relative to the magic offset `off`):

```
magic absolute offset: 1421135913
EncryptionKeyGuid bytes (16, hex): 00000000000000000000000000000000
bEncryptedIndex byte (correct offset, off-1): 0
version: 11
index_offset: 1420424182 index_size: 164054
index_hash (hex): 5d2f07c9209f8a50f17e15a3f6562e07d85b9ab0
true footer start (guid_off = off-17): 1421135896
true footer size (EOF - guid_off): 221
compression method slot 0: 'Oodle'
compression method slot 1: 'Zlib'
compression method slot 2: ''
compression method slot 3: ''
compression method slot 4: ''
file size: 1421136117
```

`EncryptionKeyGuid` is all-zero (no per-pak encryption key) and `bEncryptedIndex == 0`
(false) — the plan's assumption is confirmed, just at a different byte offset than the
brief's naive script computes. Footer size is **221 bytes**, matching the upper bound of
the plan's `footerSizes` assumption exactly (the 61-byte alternative does not apply here).

Compression methods declared: `Oodle` and `Zlib` (3 of 5 slots unused/zero-padded).

## Step 4: SHA1 cross-check

```bash
python3 -c "
import hashlib
data = open('$PAK', 'rb').read()
index_offset = 1420424182
index_size = 164054
index_bytes = data[index_offset:index_offset+index_size]
print(hashlib.sha1(index_bytes).hexdigest())
"
```

```
sha1 of index bytes:        5d2f07c9209f8a50f17e15a3f6562e07d85b9ab0
expected (footer index_hash): 5d2f07c9209f8a50f17e15a3f6562e07d85b9ab0
```

**Match.** This is strong confirmation that `Version`, `IndexOffset`, `IndexSize`, and
`IndexHash` parsing (using the magic-relative offsets, unaffected by the
`bEncryptedIndex` discrepancy above) is correct.

## Cross-check against a second pakchunk

Spot-checked `pakchunk0_s10-WindowsNoEditor.pak` (1,645,159,864 bytes) using the corrected
layout, to confirm the format is consistent across Icarus's pakchunks and not an artifact
of the base chunk specifically:

```
version: 11  footer size: 221
bEncryptedIndex: 0
index_hash:    5bab16b081e26f151a7e827edf0092654825cf2c
sha1 computed: 5bab16b081e26f151a7e827edf0092654825cf2c
match: True
```

Same version, same footer size, same `bEncryptedIndex == false`, and SHA1 match. High
confidence the format is uniform across all of Icarus's pakchunks.

## Implications for Task 2 (`internal/unrealpak`)

1. **Footer size 221 bytes is confirmed** — proceed with that constant for version-11-class
   paks (no need to special-case the 61-byte alternative for Icarus).
2. **`bEncryptedIndex == false` is confirmed** — but Task 2's reader must place
   `bEncryptedIndex` (and `EncryptionKeyGuid`) **before** `Magic`, not after `IndexHash`, and
   must account for the trailing `CompressionMethods` array (5 × 32 bytes) for version ≥ 8.
   A reader that reuses the brief's naive Step 3 offsets verbatim will silently read garbage
   for `bEncryptedIndex` (it happened to land inside a compression-method name string here)
   and would misreport the footer size as 204 instead of 221.
3. Recommend implementing the footer parser as: read the last N bytes, locate `Magic`
   by scanning backward from EOF (not by assuming a fixed footer size up front, since the
   size varies with version/feature gates), then compute `version`/`index_offset`/
   `index_size`/`index_hash` relative to the magic offset, and separately compute
   `bEncryptedIndex`/`EncryptionKeyGuid` relative to the magic offset going _backward_
   (`magic_offset - 1` and `magic_offset - 17` respectively) rather than forward from the
   hash.
4. No indication of index encryption or per-pak encryption keys in either sampled pakchunk —
   `internal/unrealpak` does not need an encrypted-index code path for Icarus's shipped data
   (mod-authoring use case), though it may still be worth a defensive error if
   `bEncryptedIndex != 0` is ever encountered.

## Overall verdict (footer spike)

**DONE_WITH_CONCERNS** (format substantially confirmed, but the brief's naive parsing script
needs correcting): `Version == 11`, footer size `== 221` bytes (plan's upper-bound
assumption holds), `bEncryptedIndex == false` (confirmed, at a corrected offset), and the
SHA1 cross-check matches exactly on two independent pakchunks. Task 2 should implement the
corrected offset layout described above rather than the brief's Step 3 script verbatim.

---

# Part 2 — Empirical index decode (#136 plan rev2 spike)

The footer spike above stopped at the footer. This second read-only spike decodes the
**index** itself, because the plan's Task 2 assumed a _classic_ flat index
(`MountPoint`, `NumEntries`, then N inline `FPakEntry` records). **That assumption is
false for version 11.** Version 11 uses the UE 4.25+ three-part index: a primary index
holding _bit-packed_ entry records plus offsets to two secondary indexes (a path-hash
index and a full directory index), each SHA1-gated.

Everything below was decoded and verified byte-for-byte with `python3` against the real
install. **Every structural claim in Part 2 was verified across all 34 paks on disk**
(the 33 `Content/Paks/pakchunk0*` chunks plus `Content/Data/data.pak`) — 173,078 entries
total — not just a sample.

## The pak that actually matters: `Content/Data/data.pak`

The Task 1 brief looked only in `Content/Paks/`. Icarus keeps its **moddable JSON data
tables in a separate pak** that the earlier spike never examined:

```text
/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak
size: 2,458,743 bytes   version 11   221-byte footer   298 entries — ALL .json
MountPoint: 'C:/BA/work/92bbbfa44df12262/Temp/Data/'   PathHashSeed: 0xfceb4085
```

This is the `.EXMOD` base pak — the one Task 12's `resolveCurrentFile` must open. The
`Content/Paks/pakchunk0*` chunks contain **zero** `.json` files (verified by enumerating
all 33 chunks: 172,780 entries, extensions are `uexp`/`uasset`/`ubulk`/`res`/`umap`/`png`/
`ini`/…). The plan's `resolveBasePak` must point at `Icarus/Content/Data/data.pak`, not a
pakchunk.

### ⚠ BLOCKING RISK: 258 of `data.pak`'s 298 JSON files are Oodle-compressed

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

|                                        | files | uncompressed bytes |
| -------------------------------------- | ----- | ------------------ |
| `CompressionMethodIndex 0` (stored)    | 40    | 27,959             |
| `CompressionMethodIndex 1` (**Oodle**) | 258   | 40,908,881         |

The 40 stored files are all tiny stubs (113–~1 KB: `D_Factions.json`, `D_LevelSequences.json`,
…). **Every data table a mod would realistically patch is Oodle-compressed** —
`Items/D_ItemsStatic.json` (7.3 MB), `Crafting/D_ProcessorRecipes.json` (3.9 MB),
`Talents/D_Talents.json` (2.6 MB), `Traits/D_Itemable.json` (2.2 MB), `AI/D_AISetup.json`
(1.3 MB), `Quests/D_Quests.json` (1.2 MB).

Oodle is a proprietary codec with no Go stdlib implementation, so the plan's
"uncompressed-only" **read** path cannot read the files Task 12 needs to patch. This does
not affect the **write** path (our own paks can store everything uncompressed). It is
recorded here and in the plan as an unresolved blocker for Task 12 — it needs a product
decision, not a format fix.

## Primary index layout (version ≥ 10, `PakFile_Version_PathHashIndex`)

Located at the footer's `IndexOffset`/`IndexSize`; SHA1 gated by the footer's `IndexHash`.

```text
FString MountPoint                 // "../../../" (pakchunk0) / "C:/BA/.../Temp/Data/" (data.pak)
int32   NumEntries
uint64  PathHashSeed
int32   bHasPathHashIndex          // 1
  int64  PathHashIndexOffset       // absolute file offset
  int64  PathHashIndexSize
  [20]   PathHashIndexHash         // SHA1 of that region
int32   bHasFullDirectoryIndex     // 1
  int64  FullDirectoryIndexOffset
  int64  FullDirectoryIndexSize
  [20]   FullDirectoryIndexHash    // SHA1 of that region
int32   EncodedPakEntriesSize
uint8[] EncodedPakEntries          // bit-packed records, see below
int32   NumNonEncodedFiles         // 0 in all 34 paks; would be followed by full FPakEntry records
```

Decoded from the real `pakchunk0`:

```text
MountPoint: '../../../'
NumEntries: 9295
PathHashSeed: 0x000000009c4dd25a
bHasPathHashIndex: 1 off=1420588236 size=201687 sha1=52ce88320eae17b776eea8d6e1344d28a19728ac
bHasFullDirectoryIndex: 1 off=1420789923 size=345973 sha1=9a2878db6f62cf566f591c681fa58ba55973ca1e
EncodedPakEntriesSize: 163940 (blob begins 110 bytes into the primary index)
NumNonEncodedFiles: 0
bytes remaining after NumNonEncodedFiles: 0   <- primary index consumed exactly
```

`FString` is the standard UE encoding: `int32 Len`; `Len > 0` → `Len` ANSI bytes
_including_ a trailing NUL; `Len < 0` → `-Len` UTF-16LE code units including NUL.
All 34 paks use the ANSI form exclusively.

### Region tiling

The four trailing regions tile the file exactly, with no gaps and no padding:

```text
primary    [1420424182, 1420588236)  size=164054   gap_from_prev=-
path-hash  [1420588236, 1420789923)  size=201687   gap_from_prev=0
full-dir   [1420789923, 1421135896)  size=345973   gap_from_prev=0
footer     [1421135896, 1421136117)  size=221      gap_from_prev=0
EOF=1421136117  end_of_last_region=1421136117  gap=0
```

### SHA1 verification (step 5)

All three hashes verify on `pakchunk0`, and on all 34 paks:

```text
sha1(primary index)  = 5d2f07c9209f8a50f17e15a3f6562e07d85b9ab0  == footer IndexHash               MATCH
sha1(path-hash idx)  = 52ce88320eae17b776eea8d6e1344d28a19728ac  == primary PathHashIndexHash      MATCH
sha1(full-dir idx)   = 9a2878db6f62cf566f591c681fa58ba55973ca1e  == primary FullDirectoryIndexHash MATCH
```

## Encoded `FPakEntry` bit-packed format

Each record starts with a `uint32` flags word:

| bits  | meaning                                                                                     |
| ----- | ------------------------------------------------------------------------------------------- |
| 31    | `Offset` is 32-bit (else 64-bit)                                                            |
| 30    | `UncompressedSize` is 32-bit (else 64-bit)                                                  |
| 29    | `Size` is 32-bit (else 64-bit)                                                              |
| 28–23 | `CompressionMethodIndex` (6 bits; index into the footer's `CompressionMethods`, 0 = stored) |
| 22    | encrypted                                                                                   |
| 21–6  | compression block count (16 bits)                                                           |
| 5–0   | `CompressionBlockSize >> 11`, or `0x3f` = escape (explicit `uint32` follows)                |

Then, in this exact order:

```text
if (flags & 0x3f) == 0x3f : uint32 CompressionBlockSize   // NOTE: precedes Offset
Offset            : uint32 if bit31 else uint64
UncompressedSize  : uint32 if bit30 else uint64
Size              : uint32 if bit29 else uint64           // ONLY when CompressionMethodIndex != 0
                                                          // (when 0, Size == UncompressedSize, not serialized)
if blockCount > 0 && (blockCount > 1 || encrypted):
    blockCount x uint32                                   // per-block compressed length
```

The `CompressionBlockSize`-before-`Offset` ordering is the detail that broke the first
decode attempt; it was recovered by using the directory index's entry locations as ground
truth for record boundaries, then fitting fields to the observed record widths.

### Verification (step 2)

Sequential decode of `pakchunk0`'s blob consumes **163,940 / 163,940 bytes exactly** and
yields **exactly 9295 records == `NumEntries`**:

```text
compression-method-index histogram: {0: 4089, 1: 4138, 2: 1068}   (0=stored, 1=Oodle, 2=Zlib)
encrypted entries: 0
32-bit-safe bits: off32=9295 usz32=9295 sz32=9295   (all entries use the 32-bit forms)
entries with offset+size outside the data region [0,1420424182): 0
multi-block entries whose per-block sizes don't sum to Size: 0
entries carrying an explicit block-size list: 839
record-length histogram: {12: 4089, 16: 18, 20: 4349, 28: 615, 32: 78, 36: 40, 40: 23,
                          44: 28, 48: 12, 52: 5, 56: 5, 60: 3, 64: 9, 76: 2, 88: 2,
                          96: 2, 108: 9, 128: 2, 152: 3, 220: 1}
```

**Not all entries are uncompressed** — the task brief's expectation is falsified. Across
all 34 paks: `{0: 44744, 1: 127266, 2: 1068}` — i.e. **74% Oodle**, 25% stored, <1% Zlib.

**The stored (`CompressionMethodIndex == 0`) shape is exactly 12 bytes** — flags word
`0xE0000000`, `uint32 Offset`, `uint32 UncompressedSize` — for all 4089 such entries in
`pakchunk0`. That is precisely the record the Task 4 writer must emit:

```text
000000e0 00000000 ab020000    -> flags=0xE0000000, Offset=0, Size=UncompressedSize=683
```

Sanity-check of the compressed shape (block sizes sum to `Size`, verified on every
multi-block entry in all 34 paks):

```text
flags=0xe08000ff -> cmi=1, blocks=3, blkraw=0x3f
  CompressionBlockSize=1048576  Offset=1048576  UncompressedSize=2793472  Size=2584907
  blocks=[996695, 990845, 597367]   sum=2584907 == Size   MATCH
```

## Full directory index

```text
int32 DirCount
repeat DirCount:
    FString DirName          // trailing '/', NO leading '/', except the root dir which is exactly "/"
    int32   FileCount
    repeat FileCount:
        FString FileName     // leaf name only
        int32   PakEntryLocation
```

`PakEntryLocation >= 0` is a byte offset into `EncodedPakEntries`. (Negative values would
index the non-encoded `Files` array; **zero negatives observed across all 173,078 entries**,
so the reader should treat them as a hard unsupported-format error.)

### Verification (step 3)

```text
pakchunk0: directories=631  path->location mappings=9295 == NumEntries   MATCH
           consumed 345973/345973 bytes exactly
           every location resolves to a decoded record start: True   negatives: 0
sample dirs: ['Engine/Content/', 'Engine/', '/', 'Engine/Content/EngineResources/']
```

Three sample paths (`pakchunk0` carries no `.json`; those live in `data.pak`):

```text
'Engine/Config/Base.ini'                 -> loc 105252
'Engine/Config/BaseCompat.ini'           -> loc 105264
'Engine/Config/BaseDeviceProfiles.ini'   -> loc 105284
```

…and from the pak that does have JSON:

```text
data.pak: 'Factions/D_Factions.json', 'Quests/Modifiers/D_QuestWeatherModifiers.json',
          'Development/D_LevelSequences.json'   (298 entries, all .json)
```

The full mount-relative path is `DirName + FileName`, which yields a **leading `/` only for
root-directory files** (`"/" + "DataTableMetadata.json"`). Strip that leading `/` to get the
canonical mount-relative path.

## Path hash index

The path-hash _region_ holds two structures back to back:

```text
int32 Count
repeat Count: uint64 PathHash ; int32 PakEntryLocation
<pruned directory index>     // same wire format as the full directory index
```

### Verification (step 4)

```text
pakchunk0: hash entries=9295 == NumEntries   MATCH
           map ends at byte 111544 of 201687
           trailing 90143 bytes parse as a second (pruned) directory index:
             dirs=401 files=3696, consuming 201687/201687 exactly
           pruned entries are a strict subset of the full index: True
```

**33 of the 34 paks ship an EMPTY pruned directory index** (`DirCount == 0`, i.e. a bare
`int32 0`); only `pakchunk0` populates it. Emitting an empty pruned index is therefore
demonstrably a shape the engine loads.

### The hash recipe — determined by making the numbers match

```text
h := 0xCBF29CE484222325 + PathHashSeed        (uint64 wrapping ADD, not XOR)
for each byte b of UTF16LE(lowercase(mount-relative path, leading '/' stripped)):
        h ^= b
        h *= 0x00000100000001B3               (uint64 wrapping multiply)
```

That is standard **FNV-1a 64** with the offset basis _added_ to the seed, hashing the
UTF-16LE bytes of the lowercased path with **no NUL terminator**. A brute-force sweep over
{basis, seed, basis^seed, basis+seed} × {UTF-16LE, UTF-16LE+NUL, UTF-8, UTF-8+NUL} ×
{lower, upper, as-is} × {as-is, strip-leading-`/`, add-leading-`/`, backslashes} left
`basis+seed / UTF-16LE / lower / strip-leading-'/'` as the only recipe that matches.
`basis+seed` and `basis^seed` are distinguishable here (the seeds are ≤ 32 bits and the
add carries into bit 32), and only the ADD form matches.

Verified not on 3 paths but on **every path in every pak** — each computed hash is present
in the map _and_ maps to the same `PakEntryLocation` the directory index gives:

```text
pakchunk0   9295/9295     data.pak   298/298     ... all 34 paks: 173,078/173,078   MATCH
```

Worked examples (`pakchunk0`, seed `0x9c4dd25a`):

```text
'Engine/Config/Base.ini'                -> 0xaef1d2bae819faf6 -> loc 105252
'Engine/Config/BaseCompat.ini'          -> 0x53ea9a367ce73eda -> loc 105264
'Engine/Config/BaseDeviceProfiles.ini'  -> 0x6d1b238f9ca74f86 -> loc 105284
```

**The leading-`/` strip is load-bearing.** An initial sweep that concatenated
`DirName + FileName` verbatim passed on 29 of 33 chunks but failed on 5 (e.g.
`pakchunk0_s9`: only 111 of 3624 paths matched) — precisely the chunks with many
root-directory files, whose paths came out as `/M_DEP_Crate_Sinotai_D.uasset`. Stripping
the leading `/` took every pak to 100%.

Note: no non-ASCII path exists in any of the 34 paks, so ASCII-only vs. full-Unicode case
folding is not distinguishable here. Our writer controls its own paths, so lowercasing
ASCII `A-Z` matches UE's `FChar::ToLower` for everything we will emit.

## Per-entry local header (step 6)

Each file's data region begins with a **full, non-encoded `FPakEntry`** re-serialized
inline, immediately followed by the payload:

```text
int64  Offset                 // ALWAYS 0 in the local copy — not the absolute offset
int64  Size                   // on-disk (post-compression) size
int64  UncompressedSize
int32  CompressionMethodIndex
[20]   Hash                   // SHA1 of the ON-DISK payload bytes
if CompressionMethodIndex != 0:
    int32 BlockCount
    repeat BlockCount: int64 CompressedStart ; int64 CompressedEnd   // relative to entry start
uint8  Flags                  // 0 = not encrypted, not deleted
uint32 CompressionBlockSize
```

**Stored entries: exactly 53 bytes** (8+8+8+4+20+1+4). The plan's assumed 49 is wrong — it
omits the trailing `uint32 CompressionBlockSize`. Compressed entries: `53 + 4 + 16*BlockCount`.

Note `Flags` and `CompressionBlockSize` come **after** the block list, not before it.

Cross-check on a real JSON entry from `data.pak`, reached through the full production read
path (path → FNV-1a hash → path-hash index → encoded record → local header → payload):

```text
path 'Factions/D_Factions.json'
encoded entry: flags=0xe0000000 offset=816821 size=113 usize=113 cmi=0 rec_len=12
local header (53 B): Offset=0 Size=113 UncompressedSize=113 CMI=0 Flags=0x00 CompressionBlockSize=0
local Hash    = 6b899b02ef58ae54ac64a2f7acf929530c995b29
sha1(payload) = 6b899b02ef58ae54ac64a2f7acf929530c995b29   MATCH
local Size/UncompressedSize agree with the index entry: True
local Offset field is ZERO (not the absolute offset): True
payload parses as JSON: True
payload head: {\r\n    "RowStruct": "/Script/Icarus.Factions",\r\n    "Defaults": {},\r\n ...
```

Raw 53-byte header of a stored entry, for byte-level reference:

```text
00000000 00000000  Offset            = 0
10000000 00000000  Size              = 16
10000000 00000000  UncompressedSize  = 16
00000000           CompressionMethodIndex = 0
a897c5aa2519d4fb9b31c4555aa3a62b297d9e55   Hash (SHA1 of payload)
00                 Flags             = 0
00000000           CompressionBlockSize = 0
```

`Hash` covers the **on-disk** bytes. Proven by fully decompressing a Zlib entry
(`CompressionMethodIndex 2`) — the only compressed codec the stdlib can read:

```text
path 'Engine/Plugins/Runtime/HairStrands/Config/BaseHairStrands.ini'
cmi=2 blocks=1 size=286 usize=1424 blocksize=1424   local header = 73 bytes (53+4+16)
local blocks (start,end) = [(73, 359)]      <- first block starts exactly at the header end
zlib-decompressed 1424 bytes == UncompressedSize   MATCH
sha1(compressed payload) == local Hash             MATCH
decompressed head: b'[Startup]\r\nfx.UseShaderStages=1\r\n\r\n[CoreRedirects]\r\n...'
```

## Data-region packing and alignment

Entries are packed **contiguously** — `offset(n+1) == offset(n) + headerSize(n) + size(n)` —
for 8385 of `pakchunk0`'s 9294 adjacent pairs, and the last entry ends at exactly
`IndexOffset` (1420424182), so the data region is flush against the index.

The remaining **909 pairs have a padding gap**: every post-gap entry begins at a **1 MiB-aligned**
offset (the cooker's compression-block alignment; 413,219,809 bytes of padding total). A
reader must therefore always seek to each entry's recorded `Offset` and never assume
contiguity. Our writer packs contiguously with no alignment, which is valid — 8385 real
adjacent pairs demonstrate zero padding is accepted.

## Cross-check across every pak on disk (step 7)

Rather than spot-checking `pakchunk0_s10`, the full decode was run against **all 34 paks**.
Every one passes every invariant: version 11, 221-byte footer, `bEncryptedIndex == 0`, all
three SHA1 gates, encoded blob consumed exactly, `NumNonEncodedFiles == 0`, zero trailing
bytes in the primary index, decoded-record count == full-dir-index count == path-hash count
== `NumEntries`, zero out-of-range offsets, all block sizes summing to `Size`, and 100%
path-hash agreement.

```text
chunk                                    ver entries     dec     fdi     phi sha1x3    hash  blob  oob  blk  pruned
pakchunk0-WindowsNoEditor.pak             11    9295    9295    9295    9295   True    9295  True    0    0    3696 OK
pakchunk0_s10-WindowsNoEditor.pak         11    3078    3078    3078    3078   True    3078  True    0    0       0 OK
pakchunk0_s20-WindowsNoEditor.pak         11   42361   42361   42361   42361   True   42361  True    0    0       0 OK
...  (all 33 chunks)  ...
data.pak                                  11     298     298     298     298   True     298  True    0    0       0 OK

ALL PAKS PASS: True
```

`pakchunk0_s10` specifically: `mount='../../../Icarus/Content/ASS/'`, 3078 entries,
seed `0x659d16d2`, empty pruned index, and a verified 53-byte local header +
payload-SHA1 match on `DPS/SK_DPS_SML_DropShip_02_TOP_Skeleton.uexp` (558 bytes).

## Part 2 verdict

**CONFIRMED.** The version-11 index format is fully decoded and every structure the Task 2
reader and Task 4 writer need is specified above at byte level, verified against 173,078
real entries in 34 paks. Two findings change the plan's scope materially:

1. The classic flat index the plan assumed **does not exist** in version 11 — the reader
   needs the primary index + encoded-entry decoder + full directory index, and the writer
   must emit all three index structures plus the path-hash index (recipe above).
2. **Oodle.** The real `.EXMOD` base pak is `Content/Data/data.pak`, and 258 of its 298
   JSON tables — including every table worth patching — are Oodle-compressed. The
   uncompressed-only **read** path cannot reach them. Unresolved; needs a product decision.
   → **Resolved in Part 3** by a user decision: base tables come from hosted community
   dumps instead of local Oodle decompression.

   > **CORRECTED 2026-08-01 (#175):** `data.pak` is Zlib, not Oodle — see the correction banner in Part 3.

---

# Part 3 — Hosted base-table dumps (#136 plan rev3 spike)

Spike 3 grounds the user decision that resolves Part 2's Oodle blocker: the compile
pipeline takes base data tables from the community's hosted per-week JSON dumps rather
than decompressing the local `data.pak`. Everything below was fetched over the network and
byte-checked against the local install on 2026-07-31.

## ⚠ Two findings that qualify the decision

Read these before relying on this strategy.

1. **IMM does not do this.** The decision was framed as "IMM's approach", but Icarus Mod
   Manager's own README describes local extraction: _"it should unpak the data folder from
   the game"_, via an "Update data folder" button, and _"you will need to add the oodle
   compression plugin to your unrealPak folder"_. IMM solves Oodle by shipping the Oodle
   plugin alongside UnrealPak, not by downloading dumps. Hosted dumps are a real and
   separate thing (documented below), but they are not IMM's mechanism.
   Source: <https://raw.githubusercontent.com/Jimk72/Icarus_Software/main/README.md>
2. **The freshest dump is 7 weeks behind this install.** The installed game is **Week 243**;
   the best-maintained dump repo's HEAD is **Week 236** (2026-06-12). So _right now_ there
   is no dump matching this machine's game build, and the pipeline's fail-loud path would
   trigger. The maintainer has also gone dormant before (no commits Dec 2024 → Jul 2025).
   This is a live availability risk, not a theoretical one.

## 1. Where the dumps live, what they are, who maintains them

**Primary source — `GODOFMINECRAFT4/IcarusData`** (GitHub, branch `master`).

|            |                                                                                                                                                                                                          |
| ---------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Repo       | <https://github.com/GODOFMINECRAFT4/IcarusData>                                                                                                                                                          |
| README     | "Icarus Data.pak Unpack — This Repo Will be Updated Each Update To Show What Files Were Edited"                                                                                                          |
| Maintainer | GODOFMINECRAFT4 (single personal repo, 0 stars, created 2025-07-18)                                                                                                                                      |
| Format     | The unpacked `data.pak` tree as **loose JSON files committed at repo root** — `AI/`, `Accolades/`, `Items/`, … (282 root-level `.json`). Not a zip, not a release asset.                                 |
| Versioning | **Git commits only.** One commit per game week; the week appears _only in the commit message_ (`"Week 236 data.pack Unpacked Using New Semi Automated Workflow"`). **No tags, no releases, one branch.** |
| Tooling    | QuickBMS (`quickbms.exe`, `unreal_pak.bms`, `reimport*.bat` are committed alongside). QuickBMS has Oodle support including on Linux.                                                                     |
| Coverage   | Weeks 149 → 236 in history, with a dormancy gap (Week 160 Dec 2024 → Week 189 Jul 2025, commit message _"IM BACK BITCHES"_).                                                                             |

A stale `data/` subdirectory (284 JSON) also sits in the repo; the authoritative current
tree is the **root-level** one. Do not read `data/`.

### Verified URL patterns

All fetched successfully, no authentication, no rate-limit issues:

```text
# Whole tree at HEAD (tar.gz) — 36,391,684 bytes, ~3.7 s
https://codeload.github.com/GODOFMINECRAFT4/IcarusData/tar.gz/refs/heads/master

# Whole tree at a specific week (by commit SHA) — verified on Week 231 (ef2b5e11)
https://codeload.github.com/GODOFMINECRAFT4/IcarusData/tar.gz/<sha>      # 36,372,614 bytes
https://github.com/GODOFMINECRAFT4/IcarusData/archive/<sha>.zip          # HTTP 200

# One table (raw blob) — D_ItemsStatic.json = 7,040,520 bytes in 0.285 s
https://raw.githubusercontent.com/GODOFMINECRAFT4/IcarusData/<ref>/Items/D_ItemsStatic.json

# Week index (commit list; week parsed from commit message)
https://api.github.com/repos/GODOFMINECRAFT4/IcarusData/commits?per_page=100
```

### Secondary sources (all staler — fallbacks or cross-checks only)

| Source                                               | State                                                                                                                                                                                |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `MatthiasKunnen/icarus-pedia` → `gamedata/data_pak/` | Week 218 (2026-02-09). Valuable for one reason: its commit messages carry an **explicit week↔version mapping**, e.g. `"Upgrade to week 218 REV. 2.3.29.148374-SHIPPING-GREATHUNTS"`. |
| `Jimk72/Icarus_Software` → `data.zip`                | 265 tables, 24.6 MB uncompressed. Uploaded **once**, 2024-04-26, never updated. Not per-week.                                                                                        |
| `NateEkat/Icarus-DataExport`                         | "The data.pak from the steam game Icarus", last pushed 2025-09-22.                                                                                                                   |
| `Jimk72/Icarus_Software` → `DUMP_Week_140.zip`       | **Not data tables** — a C++ SDK header dump (3,477 `*_classes.h`/`*_struct.h`). Irrelevant here despite the promising name.                                                          |

## 2. Week scheme and local build detection

### The dump side

Week numbers exist only in commit messages. Parsing `Week (\d+)` from
`/repos/GODOFMINECRAFT4/IcarusData/commits` yields the week→SHA index; the SHA then
addresses that week's tree. Mod `compatibility` strings in the Firestore catalog use a
different, shorter form (e.g. `"w57"`), so any mapping between mod compatibility and dump
week is a string-normalization problem, not a lookup.

### The local side — there is NO week number in the install

Searched the whole install: `Icarus/Config/` holds only `SettingsSchema.json`,
`TestRails.json` and `version.json`, and none names a week. The two authoritative local
facts are:

```jsonc
// <install>/Icarus/Config/version.json   — the canonical build identity
{
  "Name": "Icarus",
  "Version": {
    "Major": 3,
    "Minor": 0,
    "Patch": 21,
    "Changelist": 155335,
    "BuildType": "Shipping",
    "FeatureLevel": "DangerousHorizons",
  },
  "Data": { "Changelist": 155151 }, // <- the data.pak's own changelist
}
```

```text
# <library>/steamapps/appmanifest_1149460.acf
"buildid"      "24487768"
"LastUpdated"  "1785533918"     -> 2026-07-31 21:38:38 UTC
```

### Bridging build → week

The Steam News API supplies the missing link, unauthenticated:

```text
https://api.steampowered.com/ISteamNews/GetNewsForApp/v2/?appid=1149460&count=12
```

```text
2026-07-31  Hotfix Version 3.0.21.155335-rel-DangerousHorizons     <- matches version.json exactly
2026-07-30  Icarus Week 243 Update | Livewire Revamp
2026-07-24  Icarus Week 242 Update | Workshop Flashlight
...
2026-06-12  Icarus Week 236 Update | Ubis Husbandry & Phenotypes    <- dump repo HEAD
```

`Major.Minor.Patch.Changelist` from `version.json` reproduces the hotfix title verbatim
(`3.0.21.155335`), and the nearest preceding "Icarus Week N Update" gives the week.
**Installed build = Week 243.**

**Recommended recipe — do not depend on news parsing.** Steam titles are prose and will
drift. The robust detection is _content-based_, and it is available offline:

> Read `version.json` for identity/reporting, then **prove** a candidate dump matches the
> install by byte-comparing the tables that `data.pak` stores **uncompressed**. Those 40
> tables are readable with `internal/unrealpak` alone — no Oodle — so the check costs
> nothing and is exact. If they all match, the dump is the right week; if any differs, it
> is not.

That check is what detected the 7-week drift below.

## 3. Cross-validation: dump vs. local `data.pak`

Compared the dump at HEAD (Week 236) against the installed Week 243 `data.pak`, reading
stored entries through the Part 2 reader logic.

### Normalization: line endings, and nothing else

The shipped pak stores **CRLF**; the git blobs are **LF** (committed with autocrlf; the
repo has no `.gitattributes`, and `raw.githubusercontent.com` also serves LF). The
transform is exact and reversible:

```text
AI/D_AIEvents.json:  pak = 910 bytes (26 CRLF) | dump = 884 bytes (26 LF)
pak == dump.replace(b"\n", b"\r\n")   ->  True     # byte-for-byte
pak.replace(b"\r\n", b"\n") == dump   ->  True
```

So a fetcher must restore `LF -> CRLF` to reproduce shipped bytes. No other normalization
exists — no re-indentation, no key reordering, no encoding change.

### Results (after LF→CRLF restoration)

| Check                                               | Result                                                                                                                                                |
| --------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Stored (uncompressed) tables, byte-compare          | **37 / 40 byte-identical**                                                                                                                            |
| Stored tables genuinely differing                   | 3 — `Audio/D_MusicTrackStateGroups.json`, `Audio/MusicConditions/D_MusicLocationConditions.json`, `Audio/MusicConditions/D_MusicQuestConditions.json` |
| Oodle tables, dump size vs index `UncompressedSize` | 167 / 252 exact; 85 differ (all smaller in the dump)                                                                                                  |
| Tables in `data.pak` with no dump counterpart       | 6 — `Settlement/D_SettlementNPCClothing/Items/Skills/Traits`, `Settlement/D_SettlementRaids`, `Tools/D_NPCWeapon` (added after Week 236)              |

**Verdict: the dumps are faithful, and this dump is the wrong week.** The 37 exact matches
prove fidelity — an unpack that round-trips 37 files byte-perfectly is not lossy. The 3
content differences, 85 size differences and 6 missing tables are real game changes between
Week 236 and Week 243, exactly what a 7-week gap predicts.

Note `data.pak` contains **no Zlib entries** (only 40 stored + 258 Oodle), so the planned
"compare 1 Zlib table" check was not applicable here; the 252-table `UncompressedSize`
comparison substitutes for it and covers far more ground. (Zlib entries do exist in
`pakchunk0`, and Part 2 already proved stdlib zlib round-trips them.)

> **CORRECTED 2026-08-01 (#175):** `data.pak` is Zlib, not Oodle — see the correction banner in Part 3.

For reference, an earlier comparison against `Jimk72/Icarus_Software/data.zip` gave
29/37 byte-identical with no line-ending difference, plus 34 missing tables — consistent
with that snapshot being an older week from a different unpack toolchain.

## 4. Availability and robustness facts (for error handling)

| Fact                    | Measured                                                                                                                                    |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| Authentication          | **None.** Plain public HTTPS GETs.                                                                                                          |
| Full-tree download      | 36.4 MB tar.gz, **~3.7 s**                                                                                                                  |
| Single-table download   | `D_ItemsStatic.json` 7,040,520 B in **0.285 s**                                                                                             |
| Repo size               | ~40 MB                                                                                                                                      |
| Historical weeks        | **Retrievable by commit SHA** — verified fetching Week 231 (`ef2b5e11`). Git history is permanent; there are no releases or tags to expire. |
| Rate limits             | GitHub anonymous API limits apply to the commits listing (60/hr/IP); `raw`/`codeload` downloads are not API-limited. Cache the week index.  |
| Freshness risk          | **HIGH.** HEAD is Week 236 vs installed Week 243. Prior dormancy: Dec 2024 → Jul 2025. Single maintainer, 0 stars, no CI.                   |
| Single point of failure | One personal repo. `icarus-pedia` (Week 218) is the only comparable fallback and is even staler.                                            |

## Local dump-directory override (rev4)

Because the hosted dump can lag the installed game — it was 7 weeks behind at spike time —
the pipeline also accepts a user-supplied directory holding an unpacked `data.pak` JSON
tree (QuickBMS output, IMM's extracted `data` folder, or anything with the same layout),
configured per game as `data_dump_path` in `games.yaml`. When set it replaces the network
fetch entirely, which additionally makes offline compiles possible. Crucially it does **not**
relax the correctness gate: the same byte-comparison against the 40 tables `data.pak` stores
uncompressed runs on local directories exactly as it does on fetched ones, so a local
extraction from the wrong week is rejected with the same error, additionally naming the
configured path. Verified on real data both ways — the Week 236 tree pointed at as a local
directory against this Week 243 install is refused identically to the hosted fetch, while a
directory built from the install's own 40 stored tables validates clean. One wrinkle worth
recording: a local extraction may already hold CRLF (QuickBMS writes what the pak stored)
whereas git blobs are LF, so the CRLF restoration is written to be idempotent and both
sources converge on the shipped byte shape.

## Part 3 verdict

**VERIFIED, WITH A MATERIAL CAVEAT.** Per-week hosted dumps exist, are addressable by
commit SHA, need no authentication, download in seconds, and are **byte-faithful modulo a
deterministic LF→CRLF transform** (37/40 stored tables byte-identical). The Oodle blocker
from Part 2 is genuinely resolved by this route.

The caveat is availability, not fidelity: the freshest dump is Week 236 while this install
is Week 243, so the pipeline must treat "no dump for the installed build" as a normal,
well-handled, fail-loud outcome rather than an edge case — it is the current state on this
machine. And the "IMM does this" premise is incorrect; IMM extracts locally with an Oodle
plugin.
