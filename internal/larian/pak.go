package larian

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Refusals. Each wraps the detail.
var (
	// ErrNotPak: the file is not an LSPK package.
	ErrNotPak = errors.New("not a Larian .pak package")
	// ErrUnsupportedVersion: an LSPK package of a version Baldur's Gate 3
	// does not use (Divinity: Original Sin's 7-13).
	ErrUnsupportedVersion = errors.New("not a Baldur's Gate 3 package version")
	// ErrCorrupt: a size or offset in the package is out of bounds, or a
	// member does not decode to its stated size.
	ErrCorrupt = errors.New("corrupt .pak package")
	// ErrZstd: the member is zstd-compressed, which lmm does not decode
	// (see the package comment for why).
	ErrZstd = errors.New("zstd-compressed member")
	// ErrSolid: the package is solid (one LZ4 frame for every file), which
	// lmm does not decode.
	ErrSolid = errors.New("solid package")
	// ErrMultiPart: the member is in another part of a split package
	// (name_1.pak), which a mod never is.
	ErrMultiPart = errors.New("member in another package part")
	// ErrTooLarge: the member, or the file list, is larger than the cap.
	ErrTooLarge = errors.New("larger than lmm reads")
)

// Compression methods: an entry's flags' low nibble [LSL Enums/
// Compression.cs].
const (
	methodNone = 0
	methodZlib = 1
	methodLZ4  = 2
	methodZstd = 3
)

// flagSolid is the package flag for one LZ4 frame holding every file [LSL
// PackageFormat.cs PackageFlags].
const flagSolid = 0x04

// Layout sizes [LSL PackageFormat.cs; NMA].
const (
	headerV15Size = 34 // after the signature
	headerV16Size = 36
	nameSize      = 256
	entryV15Size  = nameSize + 40
	entryV18Size  = nameSize + 16
)

// maxFileListBytes caps a decoded file list: 64 MiB is ~240,000 entries.
// The largest mod in the survey lists 2,446 files.
const maxFileListBytes = 64 << 20

// Pak is an opened package: its header and file list. It holds the reader
// it was opened from, for ReadFile.
type Pak struct {
	// Version is the package version (15, 16 or 18).
	Version uint32
	// Flags are the package flags.
	Flags byte
	// Files are the members, in the file list's order.
	Files []PakFile

	r    io.ReaderAt
	size int64
}

// PakFile is one member of a package.
type PakFile struct {
	// Name is the member's path inside the package, '/'-separated as
	// LSLib writes it.
	Name string
	// Offset is where its stored bytes start in the package file.
	Offset uint64
	// SizeOnDisk is how many bytes are stored.
	SizeOnDisk uint64
	// Size is its uncompressed size; 0 for a stored (uncompressed) member.
	Size uint64
	// Part is the package part that holds it: 0 is this file.
	Part uint32
	// Flags are its compression flags.
	Flags uint32
}

// OpenPak reads the header and the file list of the package r, size bytes
// long. It reads nothing else.
func OpenPak(r io.ReaderAt, size int64) (*Pak, error) {
	if size < 8 {
		return nil, fmt.Errorf("%w: %d bytes", ErrNotPak, size)
	}
	head := make([]byte, 4+headerV16Size)
	n, err := r.ReadAt(head, 0)
	if n < 8 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("%w: %v", ErrNotPak, err)
	}
	head = head[:n]
	if string(head[:4]) != "LSPK" {
		return nil, fmt.Errorf("%w: no LSPK signature", ErrNotPak)
	}
	p := &Pak{Version: binary.LittleEndian.Uint32(head[4:]), r: r, size: size}
	var entrySize int
	switch p.Version {
	case 15:
		entrySize = entryV15Size
		if len(head) < 4+headerV15Size {
			return nil, fmt.Errorf("%w: the header is cut short", ErrNotPak)
		}
	case 16, 18:
		entrySize = entryV15Size
		if p.Version == 18 {
			entrySize = entryV18Size
		}
		if len(head) < 4+headerV16Size {
			return nil, fmt.Errorf("%w: the header is cut short", ErrNotPak)
		}
	default:
		return nil, fmt.Errorf("%w: version %d", ErrUnsupportedVersion, p.Version)
	}
	listOffset := binary.LittleEndian.Uint64(head[8:])
	p.Flags = head[20]

	if listOffset > uint64(size) || uint64(size)-listOffset < 8 {
		return nil, fmt.Errorf("%w: the file list at %d is outside the %d-byte file", ErrCorrupt, listOffset, size)
	}
	var counts [8]byte
	if _, err := r.ReadAt(counts[:], int64(listOffset)); err != nil {
		return nil, fmt.Errorf("%w: reading the file list: %v", ErrCorrupt, err)
	}
	numFiles := int32(binary.LittleEndian.Uint32(counts[:]))
	compressed := int32(binary.LittleEndian.Uint32(counts[4:]))
	if numFiles < 0 || compressed < 0 {
		return nil, fmt.Errorf("%w: a negative file count or list size", ErrCorrupt)
	}
	listBytes := int64(numFiles) * int64(entrySize)
	if listBytes > maxFileListBytes {
		return nil, fmt.Errorf("%w: %d files is more than lmm reads", ErrCorrupt, numFiles)
	}
	if int64(compressed) > int64(size)-int64(listOffset)-8 {
		return nil, fmt.Errorf("%w: the file list runs past the end of the file", ErrCorrupt)
	}
	if int64(compressed) > maxFileListBytes {
		return nil, fmt.Errorf("%w: the file list is %d bytes", ErrTooLarge, compressed)
	}
	src := make([]byte, compressed)
	if _, err := r.ReadAt(src, int64(listOffset)+8); err != nil {
		return nil, fmt.Errorf("%w: reading the file list: %v", ErrCorrupt, err)
	}
	list := make([]byte, listBytes)
	got, err := decodeLZ4Block(src, list)
	if err != nil {
		return nil, fmt.Errorf("%w: the file list: %v", ErrCorrupt, err)
	}
	if int64(got) != listBytes {
		return nil, fmt.Errorf("%w: the file list decodes to %d bytes, not %d", ErrCorrupt, got, listBytes)
	}
	p.Files = make([]PakFile, numFiles)
	for i := range p.Files {
		e := list[i*entrySize : (i+1)*entrySize]
		f := &p.Files[i]
		name := e[:nameSize]
		if end := bytes.IndexByte(name, 0); end >= 0 {
			name = name[:end]
		}
		f.Name = strings.ToValidUTF8(string(name), "�")
		rest := e[nameSize:]
		if p.Version == 18 {
			f.Offset = uint64(binary.LittleEndian.Uint32(rest)) | uint64(binary.LittleEndian.Uint16(rest[4:]))<<32
			f.Part = uint32(rest[6])
			f.Flags = uint32(rest[7])
			f.SizeOnDisk = uint64(binary.LittleEndian.Uint32(rest[8:]))
			f.Size = uint64(binary.LittleEndian.Uint32(rest[12:]))
		} else {
			f.Offset = binary.LittleEndian.Uint64(rest)
			f.SizeOnDisk = binary.LittleEndian.Uint64(rest[8:])
			f.Size = binary.LittleEndian.Uint64(rest[16:])
			f.Part = binary.LittleEndian.Uint32(rest[24:])
			f.Flags = binary.LittleEndian.Uint32(rest[28:])
		}
	}
	return p, nil
}

// ReadFile reads and decodes f, refusing one whose stored or decoded size
// is over limit.
func (p *Pak) ReadFile(f PakFile, limit int64) ([]byte, error) {
	if p.Flags&flagSolid != 0 {
		return nil, fmt.Errorf("%s: %w", f.Name, ErrSolid)
	}
	if f.Part != 0 {
		return nil, fmt.Errorf("%s: %w (part %d)", f.Name, ErrMultiPart, f.Part)
	}
	if f.Offset > uint64(p.size) || f.SizeOnDisk > uint64(p.size)-f.Offset {
		return nil, fmt.Errorf("%w: %s lies outside the file", ErrCorrupt, f.Name)
	}
	method := f.Flags & 0x0F
	want := f.Size
	if method == methodNone {
		want = f.SizeOnDisk
	}
	if limit < 0 || f.SizeOnDisk > uint64(limit) || want > uint64(limit) {
		return nil, fmt.Errorf("%s: %w (%d bytes)", f.Name, ErrTooLarge, max(f.SizeOnDisk, want))
	}
	stored := make([]byte, f.SizeOnDisk)
	if _, err := p.r.ReadAt(stored, int64(f.Offset)); err != nil && !(errors.Is(err, io.EOF) && len(stored) == 0) {
		return nil, fmt.Errorf("%w: reading %s: %v", ErrCorrupt, f.Name, err)
	}
	switch method {
	case methodNone:
		return stored, nil
	case methodLZ4:
		out := make([]byte, want)
		n, err := decodeLZ4Block(stored, out)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, f.Name, err)
		}
		if uint64(n) != want {
			return nil, fmt.Errorf("%w: %s decodes to %d bytes, not %d", ErrCorrupt, f.Name, n, want)
		}
		return out, nil
	case methodZlib:
		zr, err := zlib.NewReader(bytes.NewReader(stored))
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, f.Name, err)
		}
		defer func() { _ = zr.Close() }()
		out, err := io.ReadAll(io.LimitReader(zr, int64(want)+1))
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, f.Name, err)
		}
		if uint64(len(out)) != want {
			return nil, fmt.Errorf("%w: %s decodes to %d bytes or more, not %d", ErrCorrupt, f.Name, len(out), want)
		}
		return out, nil
	case methodZstd:
		return nil, fmt.Errorf("%s: %w", f.Name, ErrZstd)
	default:
		return nil, fmt.Errorf("%w: %s has unknown compression %d", ErrCorrupt, f.Name, method)
	}
}
