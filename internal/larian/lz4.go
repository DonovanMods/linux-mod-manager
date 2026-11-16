package larian

import "fmt"

// decodeLZ4Block decodes one LZ4 block (the raw block format: no frame
// header, no checksums) from src into dst, and returns how many bytes it
// wrote. It never writes past len(dst): a block that would is refused, as
// is any length, literal run or back-reference that leaves src or reaches
// before the start of dst.
//
// The format is LZ4's published block format: a sequence is a token byte
// (literal length in the high nibble, match length minus four in the low
// one, each extended by bytes while they are 255), the literals, then - for
// every sequence but the last - a two-byte little-endian offset and the
// match, which may overlap its own output.
func decodeLZ4Block(src, dst []byte) (int, error) {
	si, di := 0, 0
	for si < len(src) {
		token := src[si]
		si++
		lit := int(token >> 4)
		if lit == 15 {
			n, next, err := lz4Length(src, si)
			if err != nil {
				return 0, err
			}
			lit += n
			si = next
		}
		if lit > len(src)-si {
			return 0, fmt.Errorf("lz4: %d literal bytes run past the input", lit)
		}
		if lit > len(dst)-di {
			return 0, fmt.Errorf("lz4: the block decodes to more than %d bytes", len(dst))
		}
		copy(dst[di:], src[si:si+lit])
		si += lit
		di += lit
		if si == len(src) {
			break // the last sequence has no match
		}
		if len(src)-si < 2 {
			return 0, fmt.Errorf("lz4: a match offset runs past the input")
		}
		offset := int(src[si]) | int(src[si+1])<<8
		si += 2
		if offset == 0 || offset > di {
			return 0, fmt.Errorf("lz4: match offset %d is outside the %d bytes decoded", offset, di)
		}
		match := int(token & 0x0F)
		if match == 15 {
			n, next, err := lz4Length(src, si)
			if err != nil {
				return 0, err
			}
			match += n
			si = next
		}
		match += 4
		if match > len(dst)-di {
			return 0, fmt.Errorf("lz4: the block decodes to more than %d bytes", len(dst))
		}
		// Byte by byte: a match may overlap the bytes it is copying.
		from := di - offset
		for i := 0; i < match; i++ {
			dst[di+i] = dst[from+i]
		}
		di += match
	}
	return di, nil
}

// lz4Length reads a length extension at src[i:]: bytes added together
// while they are 255, ending at the first that is not. It returns the sum
// and the index after it. The sum is capped well above any length a valid
// block within this package's limits can hold, so it cannot overflow.
func lz4Length(src []byte, i int) (int, int, error) {
	n := 0
	for {
		if i >= len(src) {
			return 0, 0, fmt.Errorf("lz4: a length runs past the input")
		}
		b := src[i]
		i++
		n += int(b)
		if n > 1<<30 {
			return 0, 0, fmt.Errorf("lz4: a length is larger than any block lmm reads")
		}
		if b != 255 {
			return n, i, nil
		}
	}
}
