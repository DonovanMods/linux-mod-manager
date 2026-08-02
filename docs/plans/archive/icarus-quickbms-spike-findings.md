# Icarus QuickBMS Auto-Extraction — Task 1 Spike Findings (#174)

Empirical, read-only spike run 2026-08-01 on the reference machine. No product code was
written. No `sudo`, no system packages installed, no game files modified.

## Verdict, up front

**The design's premise is FALSIFIED — twice over, and the second falsification is good news.**

1. **Linux QuickBMS cannot decompress Icarus's Oodle data.** QuickBMS 0.12.0's Oodle support
   is the open-source `powzix/kraken` reimplementation, and on real Icarus Oodle blocks it
   **crashes the process** (SIGSEGV / assertion abort) on **19 of 20** sampled blocks. That is
   the literal stop-gate this task was written to test, and it fails.

2. **It does not matter, because `Content/Data/data.pak` contains no Oodle at all.** Its
   footer declares `CompressionMethods = ["Zlib"]` — so its 258 compressed tables are
   **Zlib**, not Oodle. All 258 decompress with stdlib zlib into valid JSON. The Oodle
   blocker that motivated both the hosted-dump strategy (#136 r3/r4) and this entire feature
   **never applied to the pak the compile pipeline actually reads.**

**Recommendation: do not implement #174 as designed.** Replace it with ~15 lines of stdlib
`compress/zlib` support in `internal/unrealpak`, which makes the installed `data.pak` fully
readable, week-correct by construction, and removes the need for hosted dumps, QuickBMS,
`auto_extract`, `quickbms_path`, and the four-leg source chain. See "What to do instead".

---

## 1. Package availability and recommended permanent install route

```text
AUR: quickbms 0.12.0-2   votes=10  last modified 2024-01-11  out-of-date=no
     "Files extractor and reimporter, archives and file formats parser..."
     URL: http://aluigi.altervista.org/quickbms.htm
command -v quickbms -> not on PATH (nothing was previously installed)
```

**Recommended permanent route (if QuickBMS is ever wanted for other reasons): the AUR package.**
Reasoning: upstream's 0.12.0 source (dated Aug 2022) **does not compile** on this machine's
toolchain (GCC 16.1.1) without patching. These are structural C23/OpenSSL-3 breakages, not
warning noise, spread across the tool's own sources and its vendored libraries:

```text
crc.c:126               error: too many arguments to function 'add_func'; expected 0, have 5
included/compresslayla.c error: too many arguments to function 'CompressLAYLA_func'; expected 0, have 7
included/microvision.c   error: too many arguments to function 'microvision_decompress_func'; expected 0, have 9
compression/camoto.c:34  error: 'bool' cannot be defined via 'typedef'
compression/de_compress.c error: too many arguments to function 'core_bytes'; expected 0, have 2
perform.c:1544          error: 'RSA_SSLV23_PADDING' undeclared   (removed in OpenSSL 3)
libs/TurboRLE, libs/libkirk: implicit declarations of abort/memcpy (hard errors since GCC 14)
```

C23 made `f()` mean "takes no arguments", which invalidates this codebase's K&R-style
declarations; `bool` became a keyword; and OpenSSL 3 removed `RSA_SSLV23_PADDING`. Passing
`-Wno-implicit-function-declaration` clears only the warning-class errors, and the C++ sources
reject those flags, so the build still fails. Patching this is a packager's job — which is
exactly the value of the AUR route. The no-root fallback is upstream's **prebuilt Linux
binary** (see §2), which works as-is.

This is recorded for completeness only — nothing in the recommended plan (below) needs
QuickBMS.

## 2. What was built/obtained, and the version banner

Upstream's `papers/quickbms.zip` is the **Windows binary distribution** (`quickbms.exe`,
`quickbms_4gb_files.exe`, docs) — it contains no source and no Makefile, so the brief's
`make`-in-that-tarball step could not apply. The two correct artifacts are:

```bash
# Prebuilt Linux binaries (what this spike used):
curl -sL -o quickbms_linux.zip https://aluigi.altervista.org/papers/quickbms_linux.zip
unzip -o -q quickbms_linux.zip && chmod +x quickbms quickbms_4gb_files
# Full source (does not build on GCC 16 unpatched):
curl -sL -o quickbms-src-0.12.0.zip https://aluigi.altervista.org/papers/quickbms-src-0.12.0.zip
```

```text
binary: ~/.local/src/quickbms/linux/quickbms
file:   ELF 32-bit LSB executable, Intel i386, statically linked, for GNU/Linux 2.6.24, stripped
        (runs fine on x86_64 — the kernel has ia32 emulation and the binary is static)

banner: QuickBMS generic files extractor and reimporter 0.12.0
        by Luigi Auriemma
        (Aug 24 2022 - 10:26:51)
```

The binary was left at `~/.local/src/quickbms/linux/quickbms` and deliberately **not** copied
onto `PATH`, since the recommendation is to not use it.

## 3. The `.bms` script and its license

**The ecosystem script the brief pointed at does not exist.** Both "scripts" committed to
`GODOFMINECRAFT4/IcarusData` are failed downloads the maintainer committed by accident:

| Repo file               | Size    | Actual content                    |
| ----------------------- | ------- | --------------------------------- |
| `unreal_pak.bms`        | 14 B    | the literal text `404: Not Found` |
| `unreal_pak_script.bms` | 2,579 B | aluigi's 404 HTML error page      |
| `quickbms_scripts.zip`  | 9 B     | stub                              |

The real canonical script is **`unreal_tournament_4.bms`**:

```text
URL:    https://aluigi.altervista.org/bms/unreal_tournament_4.bms
size:   10,336 bytes (347 lines)
sha256: fbe5c57c9b787a7b84d808a12042764e0a35615c056c5c72b40af4786a439344
header: "# Unreal Engine 4 - Unreal Tournament 4 (*WindowsNoEditor.pak) (script 0.4.25)"
        "# script for QuickBMS http://quickbms.aluigi.org"
```

### License verdict: **MURKY**

Searched the script for `licen|copyright|gpl|public domain|free|redistribut|permission`:
**no matches — the script carries no license statement of any kind.** The QuickBMS _manual_
states the **tool** is GPL-2.0 ("The tool is open source under the GPL 2.0 license... You can
distribute the original quickbms.exe file as you desire but reusing its source code and/or
modifying it may require the same or compatible open source license"), and the source tarball
ships `gpl-2.0.txt` — but that grant is scoped to the tool's own source, not to the `.bms`
script files published on the website. The QuickBMS site page carries no redistribution grant
for the scripts either.

Per the brief's own rubric, "no grant, **or** terms that restrict redistribution" → **MURKY**,
which would have flipped Tasks 2–3 from `go:embed` to download-on-demand + cache. Moot under
the recommendation below, but recorded so the decision is not re-litigated.

### The script also does not support this pak

Line 108 of `unreal_tournament_4.bms` reads:

```text
# mobile version 10 is not supported
```

and the index parser that follows assumes the **classic flat index** (`get FILES long` then
per-entry records). Icarus's paks are **version 11**, which uses the three-part index
(primary + path-hash + full-directory) with _bit-packed encoded_ entries — the layout pinned
byte-for-byte in `icarus-pak-format-findings.md` Part 2. So the script cannot enumerate this
pak at all, independent of any compression question.

## 4. Extraction attempt: invocation, exit code, output, runtime

```bash
~/.local/src/quickbms/linux/quickbms -o \
  ~/.local/src/quickbms/unreal_tournament_4.bms \
  /data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak \
  ~/.local/src/quickbms/extracted
```

```text
exit=3     real 0m0.027s

  offset   filesize   filename
--------------------------------------
Error: incomplete input file 0: .../Content/Data/data.pak
       Can't read 6 bytes from offset 00258477.
  coverage file 0     0%   22382      2458743    . offset 00258477
Last script line before the error or that produced the error:
  159 get CHUNK_OFFSET longlong TOC_FILE
```

Zero files extracted. This is the version-11 index incompatibility from §3, not a compression
failure — the script died while parsing the index.

## 5. Output layout

**Not determinable: no extraction ever succeeded.** `$OUT` remained empty (0 `.json` files),
so there is no `DataTableMetadata.json` location and no mount-path-prefix question to answer.
Any future extractor work must re-establish this.

## 6. Gate results

The brief's gate program compares QuickBMS output against `unrealpak` ground truth. With no
extraction output, that comparison is vacuous. The two substantive questions were answered
directly instead.

### 6a. Can QuickBMS decompress Icarus's Oodle on Linux? **No.**

Probed with a minimal script against real single-block Oodle entries from
`pakchunk0-WindowsNoEditor.pak` (offsets/sizes derived from the verified v11 index decoder):

```bms
comtype oodle
clog "out.bin" <absolute_block_offset> <zsize> <usize>
```

```text
20 real Icarus Oodle blocks:  success=1   fail/crash=19
```

Failures are **hard crashes, not clean errors**:

```text
quickbms: libs/powzix/kraken.cpp:235: void BitReader_RefillBackwards(BitReader*):
          Assertion `bits->bitpos <= 24' failed.          -> exit 134 (SIGABRT, core dumped)
Segmentation fault (core dumped)                          -> exit 139
```

`quickbms_4gb_files` fails identically (exit 139 on a block the standard binary also fails).
The one success produced exactly its expected 4,444 bytes, so the invocation form is correct —
the decoder itself is simply not compatible with the Oodle version Icarus ships. QuickBMS's
Oodle is `powzix/kraken`, a clean-room reimplementation that lags current `oo2core` releases.

A tool that segfaults on 95% of real inputs cannot be the basis of an auto-run fallback.

### 6b. Does `data.pak` need Oodle at all? **No — it has none.**

Read from `data.pak`'s own footer:

```text
version=11  indexOffset=2436670  indexSize=8007
CompressionMethods = ['Zlib', '', '', '', '']      <- slot 0 is Zlib; there is NO Oodle slot
mount='C:/BA/work/92bbbfa44df12262/Temp/Data/'  entries=298
compression-method-index histogram: STORED=40, Zlib=258
```

**This corrects an error in `icarus-pak-format-findings.md` Part 3.** That document recorded
data.pak as "258 Oodle-compressed" because the rev3 sweep resolved method indices against
`pakchunk0`'s method table (`['Oodle','Zlib']`, where CMI 1 = Oodle) and never read
`data.pak`'s own table (`['Zlib']`, where **CMI 1 = Zlib**). The raw histogram `{1: 258, 0: 40}`
was right; the label attached to index 1 was wrong.

Method tables across every pak in the install:

```text
data.pak                        ver 11    298 entries  ['Zlib']           -> STORED=40,   Zlib=258
pakchunk0-WindowsNoEditor.pak   ver 11   9295 entries  ['Oodle','Zlib']   -> STORED=4089, Oodle=4138, Zlib=1068
pakchunk0_s1 … s32 (32 chunks)  ver 11              …  ['Oodle']          -> STORED=…,    Oodle=…
paks that actually contain Oodle-compressed entries: 33 of 34 — all of them asset chunks
```

Oodle is real in Icarus, but exclusively in the `Content/Paks/pakchunk0*` **asset** chunks
(cooked `.uasset`/`.uexp`/`.ubulk`), which contain **zero `.json`** and which the compile
pipeline never reads for base tables.

### 6c. Full reconstruction of `data.pak` with stdlib-only primitives

Decoded every entry and decompressed using only what Go's standard library provides
(`compress/zlib` + `crypto/sha1`):

```text
STORED tables:  40 ok  (SHA1-verified against the entry header, and valid JSON), 0 bad
ZLIB tables:   258 ok  (decompressed, size-checked against UncompressedSize, valid JSON), 0 bad
TOTAL:         298/298
total decompressed bytes: 40,908,881

Items/D_ItemsStatic.json : 7,304,687 bytes
Talents/D_Talents.json   : 2,626,506 bytes
Factions/D_Factions.json :       113 bytes
```

Those figures match `icarus-pak-format-findings.md` Part 3's independently-derived numbers
exactly (40,908,881 total; D_ItemsStatic 7,304,687), which cross-validates both the decoder
and the earlier findings' raw measurements.

Current behavior, confirmed against the shipped reader:

```text
unrealpak.Open(data.pak).Files()                     -> 298 entries
ReadFile("Items/D_ItemsStatic.json")                 -> unsupported pak feature: compressed entry (method 1)
ReadFile("Factions/D_Factions.json")                 -> 113 bytes, err=<nil>
```

So the single blocking line is `ReadFile`'s refusal of `method != 0`.

## 7. What to do instead of #174

Teach `internal/unrealpak` to decompress Zlib entries, keyed on the footer's
`CompressionMethods` name (already parsed but currently discarded):

- Resolve `CompressionMethodIndex` → method **name** via the footer table; accept `"Zlib"`
  (case-insensitive), keep refusing everything else — `"Oodle"` stays a loud
  `ErrUnsupportedFormat`, which is correct and honest.
- For a Zlib entry, read the local header's block list (`BlockCount`, then
  `CompressedStart`/`CompressedEnd` pairs relative to the entry offset — layout already
  documented in Part 2), `zlib.NewReader` each block, concatenate, and verify the total
  against `UncompressedSize`. The existing per-entry SHA1 covers the on-disk (compressed)
  bytes, so it still applies unchanged.
- Reuse the existing size caps; nothing else in the reader changes.

Consequences, all favorable:

- `data.pak` becomes fully readable from the installed game — **week-correct by construction**,
  no network, no external binary, no cache, no validation gate needed against a third party.
- The hosted-dump chain (#136 r3/r4), `data_dump_path`, `validateDump`, `auto_extract`,
  `quickbms_path`, and the whole four-leg source chain become unnecessary for base tables.
  The stale-dump problem that motivated this feature disappears.
- The "compile requires network access" Global Constraint can be dropped — compiling becomes
  fully offline.
- `#174` as scoped (embed a `.bms`, detect/invoke an external binary, announce, cache) should
  be closed as obsolete rather than implemented.

Worth confirming before closing #174: whether any _asset_ (non-JSON) use case ever needs the
Oodle chunks. Nothing in the `.EXMOD`/`.EXMODZ` compile path does — mods ship their own
pre-built assets in the `.EXMODZ` — so this looks like a clean removal.

---

## 8. Answers to `SPIKE-CONFIRM:` markers

The plan (`2026-08-01-icarus-quickbms-fallback.md`) carries 5 in-code markers. All five are
resolved below. **Read the caveat first: the recommendation is to delete these code paths
rather than fill them in.** The values are recorded so the revision pass is mechanical either
way.

| #   | Marker (plan location)                                                                                                                  | Resolution                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| --- | --------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | **Binary name / path** — Task 2, `quickbmsBinaryName`                                                                                   | `quickbms` is correct as a PATH name, but **there is no working install route on this machine**: upstream 0.12.0 source does not build on GCC 16 unpatched, and the working artifact is upstream's prebuilt **32-bit** static ELF (`quickbms_linux.zip`), left at `~/.local/src/quickbms/linux/quickbms`. AUR `quickbms 0.12.0-2` is the recommended packaged route. `quickbms_4gb_files` behaves identically on Oodle (also crashes), so the base-binary default was the right choice. **Moot under the recommendation.**                                                                                                                                                                                                                            |
| 2   | **Script filename + license verdict → embed-vs-download flip** — Task 2, `embeddedScriptName` + the `go:embed` directive; Task 3 Step 3 | Filename is **`unreal_tournament_4.bms`**, NOT `unreal_pak.bms` — the IcarusData repo's copies are committed 404 pages (14 B and 2,579 B). Canonical URL `https://aluigi.altervista.org/bms/unreal_tournament_4.bms`, sha256 `fbe5c57c9b787a7b84d808a12042764e0a35615c056c5c72b40af4786a439344`, 10,336 bytes. **License verdict: MURKY** — the script carries no license statement; QuickBMS's GPL-2.0 covers the tool's source, not the published scripts. Per the rubric this **flips Tasks 2–3 to download-on-demand + cache** with URL and checksum pinned. Additionally the script **does not support version-11 paks** ("mobile version 10 is not supported"), so it could not be used even with a license. **Moot under the recommendation.** |
| 3   | **Observed runtime → timeout recommendation** — Task 3, `quickbmsTimeout`                                                               | No successful extraction, so no representative runtime exists. The failing run aborted in **0.027 s**; single-block Oodle probes crashed in well under a second. If any future external-tool invocation is added, the `quickbmsWaitDelay` mechanism found necessary during plan authoring is **still required and now doubly justified** — this tool dies by SIGSEGV/SIGABRT, so a `CommandContext` + `WaitDelay` bound is the difference between a clean error and a hung compile. The 10-minute `quickbmsTimeout` was never exercised. **Moot under the recommendation.**                                                                                                                                                                           |
| 4   | **Output layout** — Task 3, `rootMarkerTable` / `findTreeRoot`                                                                          | **Undetermined — no extraction succeeded**, output directory stayed empty. The `DataTableMetadata.json`-sentinel approach remains the right design _if_ an extractor is ever added (it is layout-agnostic and needs no prior knowledge), but it is unverified against real tool output. **Moot under the recommendation.**                                                                                                                                                                                                                                                                                                                                                                                                                            |
| 5   | **Flag order** — Task 3, `runQuickBMS` argument order                                                                                   | **Confirmed correct**: `quickbms [-o] <script.bms> <input archive> <output folder>` is the accepted form — the tool parsed all three arguments and reached script execution every time (it failed later, inside the script or the decoder). `-o` (overwrite without prompting) is **required** for non-interactive use; without it QuickBMS prompts on existing files and would hang an automated run. The plan's assumed order was right; the missing `-o` was not. **Moot under the recommendation.**                                                                                                                                                                                                                                               |

### Downstream corrections this spike forces (not `SPIKE-CONFIRM:` markers)

- `icarus-pak-format-findings.md` **Part 3** must be corrected: `data.pak` is **Zlib**, not
  Oodle. Its "⚠ BLOCKING RISK: 258 of data.pak's 298 JSON files are Oodle-compressed" section
  and the Part 2 verdict's Oodle conclusion are both wrong on the method name (the counts are
  right).
- `2026-07-29-icarus-exmod-pak-compilation.md` Task 12's blocker note and the entire rev3/rev4
  hosted-dump rationale rest on the same mistaken premise and need revisiting.

## 9. Reproduction

Probe scripts used are in `/tmp/qbms-spike/` (`probe.py` — v11 index decoder + entry finder;
`zlibtest.py` — decompress all 258; `xcheck.py` — method tables across all 34 paks;
`final.py` — full 298-table reconstruction; `find_oodle.py`/`more_oodle.py` — Oodle block
selection). QuickBMS artifacts are under `~/.local/src/quickbms/`. Nothing was installed
system-wide; no game file was modified.
