// bsdiff.go is lmm's BSDIFF40 applier (#610 C5; design
// docs/plans/2026-11-10-collections-design.md §6): the binary patch format
// a NexusMods collection's patches/ tree holds (VERIFIED: every one of the
// 42 patches in the 72-recipe survey starts "BSDIFF40"). Classic bsdiff, as
// Colin Percival's bspatch reads it - written from the format, on the
// standard library's compress/bzip2, with no new dependency.
//
// A patch is a stranger's file, read as hostile:
//
//   - the header's three lengths are checked before anything is read: none
//     negative, the two block lengths within the patch, and the output size
//     within the caller's cap - so the output buffer, the one allocation
//     that grows with the patch, is bounded before it is made;
//   - each control triple is checked before it is applied: no negative
//     length, no copy or insert past the declared output size, and every
//     value within maxBSDIFFMagnitude, so the old-file cursor can never
//     overflow;
//   - a triple that makes no progress counts against a small budget, and
//     the triples together against a quarter of the output's size (bsdiff
//     writes one per match, and a match is longer than that), so a control
//     block bzip2 inflates to millions of triples ends in an error, not a
//     spin;
//   - a block is read only as far as the output needs, so a compressed
//     block that would inflate past it is never inflated past it, and the
//     output grows as bytes arrive rather than being allocated at the size
//     the header claims;
//   - the result must be exactly the declared size: a short block, or a
//     control block that stops early, is an error.
//
// bsdiff carries no checksum of its output. Fidelity is the caller's: the
// target's CRC32 must match the recipe's baseline before a patch is
// applied at all (applyFilePatches).
package core

import (
	"bytes"
	"compress/bzip2"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

// bsdiffMagic opens every BSDIFF40 patch.
const bsdiffMagic = "BSDIFF40"

// bsdiffHeaderLen is the patch header: the magic, then the control
// block's and the diff block's compressed lengths and the output's size.
const bsdiffHeaderLen = 32

// maxBSDIFFMagnitude bounds every value a control triple holds. The cursor
// into the old file moves by these, and the bound keeps it far from
// int64's edge whatever a patch says: 2^48 bytes is far past any file lmm
// patches.
const maxBSDIFFMagnitude = 1 << 48

// maxBSDIFFIdleTriples is how many control triples that neither copy nor
// insert a byte a patch may hold. bsdiff writes none in practice; the
// budget only stops a hostile control block that would spin forever. It is
// also the floor of the cap on all triples (bsdiffTripleCap).
const maxBSDIFFIdleTriples = 1024

// bsdiffChunk is how much of a block is read at a time.
const bsdiffChunk = 64 << 10

// bsdiffTripleCap is how many control triples a patch making newSize bytes
// may hold: a quarter of its size, plus the idle budget.
func bsdiffTripleCap(newSize int64) int64 { return newSize/4 + maxBSDIFFIdleTriples }

// bsdiffAppend appends n bytes read from r to dst, growing it a chunk at a
// time, so a block that ends early never cost the memory it claimed.
func bsdiffAppend(dst []byte, r io.Reader, n int64) ([]byte, error) {
	for n > 0 {
		k := int(min(n, bsdiffChunk))
		at := len(dst)
		dst = slices.Grow(dst, k)[:at+k]
		if _, err := io.ReadFull(r, dst[at:]); err != nil {
			return dst[:at], err
		}
		n -= int64(k)
	}
	return dst, nil
}

// ErrBadPatch is a patch lmm refuses to apply: not BSDIFF40, malformed,
// or describing an output over the cap.
var ErrBadPatch = errors.New("invalid binary patch")

// isBSDIFF40 reports whether data starts like a BSDIFF40 patch.
func isBSDIFF40(data []byte) bool {
	return len(data) >= len(bsdiffMagic) && string(data[:len(bsdiffMagic)]) == bsdiffMagic
}

// bsdiffOfftin decodes bsdiff's 8-byte sign-magnitude little-endian
// integer.
func bsdiffOfftin(b []byte) int64 {
	v := int64(binary.LittleEndian.Uint64(b) &^ (1 << 63))
	if b[7]&0x80 != 0 {
		return -v
	}
	return v
}

// applyBSDIFF40 applies patch to old and returns the new file, refusing a
// patch whose output would be over maxOut bytes (see the file comment for
// everything else it refuses). It never writes to old.
func applyBSDIFF40(old, patch []byte, maxOut int64) ([]byte, error) {
	if len(patch) < bsdiffHeaderLen || !isBSDIFF40(patch) {
		return nil, fmt.Errorf("%w: not a BSDIFF40 patch", ErrBadPatch)
	}
	ctrlLen := bsdiffOfftin(patch[8:16])
	diffLen := bsdiffOfftin(patch[16:24])
	newSize := bsdiffOfftin(patch[24:32])
	rest := int64(len(patch) - bsdiffHeaderLen)
	switch {
	case ctrlLen < 0 || diffLen < 0 || newSize < 0:
		return nil, fmt.Errorf("%w: its header holds a negative length", ErrBadPatch)
	case ctrlLen > rest || diffLen > rest-ctrlLen:
		return nil, fmt.Errorf("%w: its header names blocks longer than the patch", ErrBadPatch)
	case newSize > maxOut:
		return nil, fmt.Errorf("%w: it makes a %d-byte file, over the %d-byte limit", ErrBadPatch, newSize, maxOut)
	}
	body := patch[bsdiffHeaderLen:]
	ctrl := bzip2.NewReader(bytes.NewReader(body[:ctrlLen]))
	diff := bzip2.NewReader(bytes.NewReader(body[ctrlLen : ctrlLen+diffLen]))
	extra := bzip2.NewReader(bytes.NewReader(body[ctrlLen+diffLen:]))

	out := make([]byte, 0, min(newSize, bsdiffChunk))
	oldSize := int64(len(old))
	var newPos, oldPos int64
	var triple [24]byte
	idle := 0
	tripleCap := bsdiffTripleCap(newSize)
	for triples := int64(1); newPos < newSize; triples++ {
		if triples > tripleCap {
			return nil, fmt.Errorf("%w: its control block holds more triples than a file of its size needs", ErrBadPatch)
		}
		if _, err := io.ReadFull(ctrl, triple[:]); err != nil {
			return nil, fmt.Errorf("%w: its control block ends before the file does: %v", ErrBadPatch, err)
		}
		add, insert, seek := bsdiffOfftin(triple[0:8]), bsdiffOfftin(triple[8:16]), bsdiffOfftin(triple[16:24])
		switch {
		case add < 0 || insert < 0:
			return nil, fmt.Errorf("%w: a control triple holds a negative length", ErrBadPatch)
		case add > newSize-newPos:
			return nil, fmt.Errorf("%w: a control triple copies past the end of the file", ErrBadPatch)
		case insert > newSize-newPos-add:
			return nil, fmt.Errorf("%w: a control triple inserts past the end of the file", ErrBadPatch)
		case seek > maxBSDIFFMagnitude || seek < -maxBSDIFFMagnitude:
			return nil, fmt.Errorf("%w: a control triple seeks out of range", ErrBadPatch)
		}
		if add == 0 && insert == 0 {
			if idle++; idle > maxBSDIFFIdleTriples {
				return nil, fmt.Errorf("%w: its control block makes no progress", ErrBadPatch)
			}
		}

		// The diff block: bytes added to the old file's, where it has them.
		var err error
		if out, err = bsdiffAppend(out, diff, add); err != nil {
			return nil, fmt.Errorf("%w: its diff block ends early: %v", ErrBadPatch, err)
		}
		chunk := out[newPos : newPos+add]
		for i := range chunk {
			if at := oldPos + int64(i); at >= 0 && at < oldSize {
				chunk[i] += old[at]
			}
		}
		newPos += add
		oldPos += add

		// The extra block: bytes inserted as they are.
		if out, err = bsdiffAppend(out, extra, insert); err != nil {
			return nil, fmt.Errorf("%w: its extra block ends early: %v", ErrBadPatch, err)
		}
		newPos += insert
		oldPos += seek
		if oldPos > maxBSDIFFMagnitude || oldPos < -maxBSDIFFMagnitude {
			return nil, fmt.Errorf("%w: its control block seeks out of range", ErrBadPatch)
		}
	}
	if out == nil {
		out = []byte{}
	}
	return out, nil
}
