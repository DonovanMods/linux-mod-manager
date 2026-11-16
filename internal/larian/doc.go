// Package larian reads the Larian Studios file formats lmm needs to enable
// Baldur's Gate 3's .pak mods (#646): the LSPK package (.pak) - only its
// file list and the meta.lsx inside it, never the other files - and the
// LSX XML of meta.lsx and of the game's mod list, modsettings.lsx, which it
// also rewrites.
//
// It is a leaf: the standard library only, no filesystem, no lmm imports.
// Core opens the files (inside the game's Wine prefix, with its own
// no-follow rules) and hands this package an io.ReaderAt or the bytes.
//
// # Hostile input
//
// A .pak comes from a mod archive, and modsettings.lsx from a directory any
// program can write: both are untrusted. Every size and offset is checked
// against the file and against a cap before anything is allocated or read
// (maxFileListBytes, the caller's limit for a member), the LZ4 decoder
// never writes past the room it is given, and the XML reader caps nodes
// and nesting. A refusal is an error, never a panic; FuzzOpenPak and
// FuzzParseModSettings hold that.
//
// # Sources (read for facts; no code was copied)
//
//	[LSL]  github.com/Norbyte/lslib at 6f5f698 (MIT): LSLib/LS/
//	       PackageFormat.cs (headers, file entries), PackageReader.cs
//	       (signature, versions, the LZ4 file list), Compression.cs and
//	       Enums/Compression.cs (per-file flags), Enums/PackageVersion.cs.
//	[NMA]  github.com/Nexus-Mods/NexusMods.App at 48284f3 (GPL-3.0):
//	       docs/developers/games/0003-BaldursGate3.md (header and entry
//	       tables, meta.lsx and modsettings.lsx examples).
//	[VXG]  github.com/Nexus-Mods/vortex-games at 18bbee9 (GPL-3.0):
//	       game-baldursgate3/common.ts (modsettings.lsx defaults by patch),
//	       loadOrder.ts (what Vortex writes), util.ts (meta.lsx fields and
//	       their fallbacks), cache.ts (a pak without meta.lsx is never
//	       listed).
//	[BMM]  github.com/LaughingLeader/BG3ModManager at fc35358 (MIT):
//	       src/GUI/Resources/IgnoredMods.json (the game's own modules).
//
// # The LSPK layout (BG3: versions 15, 16 and 18)
//
// Little-endian throughout [LSL PackageFormat.cs; NMA]. The file starts
// with the signature "LSPK" and the version (u32). Then:
//
//	v15 (early access)           file-list offset u64, file-list size u32,
//	                             flags u8, priority u8, md5 [16]byte
//	v16 (EA patch 4), v18        the same, then the number of parts u16
//
// At the file-list offset: the number of files (i32), the compressed size
// (i32), and that many bytes of one LZ4 *block* [LSL PackageReader.cs
// ReadCompressedFileList, which calls Decompress with MethodLZ4 and
// chunked=false]. It decodes to the entries, each a 256-byte NUL-padded
// name and then:
//
//	v15, v16 (296 bytes)  offset u64, size on disk u64, uncompressed size
//	                      u64, archive part u32, flags u32, crc u32, u32
//	v18 (272 bytes)       offset low u32, offset high u16, archive part u8,
//	                      flags u8, size on disk u32, uncompressed size u32
//
// An entry's flags hold its compression in the low nibble - 0 none, 1
// zlib, 2 LZ4 (a block, not a frame: [LSL Compression.cs] decodes it with
// LZ4Codec.Decode), 3 zstd - and its level in the high one, which does not
// matter to a reader [LSL Enums/Compression.cs]. A package whose header
// flags have 0x04 (Solid) stores every file in one LZ4 frame instead.
//
// What mods use, measured 2026-11-16 on 56 .pak files from the most
// downloaded and most endorsed BG3 mods on Nexus Mods (fetched for the
// survey only, never committed): 55 were v18 and 1 v15; 46 used LZ4 for
// their files, 10 stored some uncompressed, 3 used zlib; none used zstd
// and none was solid. LSLib's divine CLI cannot write zstd at all (its -c
// takes zlib, zlibfast, lz4, lz4hc or none), and only its GUI offers it.
// So zstd and solid packages are refused with ErrZstd and ErrSolid - the
// caller warns and leaves that pak out - rather than linking a decoder lmm
// has not needed.
package larian
