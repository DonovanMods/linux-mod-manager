package main

import "io"

// indentWriter prefixes every non-empty line written through it with
// indent.
//
// It exists for `lmm init` (#384): the wizard nests everything it prints
// two spaces under its step heading, but the flows it DELEGATES to - `lmm
// auth login`'s instruction block and key prompt - print at column zero,
// because on their own that is right. Wrapping their writer keeps one
// rendering of that text and lets the caller place it, instead of teaching
// each of them about a layout only the wizard has.
//
// Two details it has to get right. A write that does not end in a newline
// leaves the next one on the SAME line ("Validating... " and its result),
// so the prefix is emitted lazily, at the first byte of a line rather than
// after each newline. And a blank line gets no prefix, so an indented block
// does not leave trailing whitespace behind.
type indentWriter struct {
	w      io.Writer
	indent string
	// atLineStart is true when the next byte written begins a line and so
	// needs the prefix.
	atLineStart bool
}

// newIndentWriter wraps w so every line it receives is prefixed with
// indent. An empty indent returns w itself, so the un-indented caller pays
// nothing and renders byte for byte as it did before.
func newIndentWriter(w io.Writer, indent string) io.Writer {
	if indent == "" {
		return w
	}
	return &indentWriter{w: w, indent: indent, atLineStart: true}
}

func (iw *indentWriter) Write(p []byte) (int, error) {
	for i, b := range p {
		if b != '\n' && iw.atLineStart {
			if _, err := io.WriteString(iw.w, iw.indent); err != nil {
				return i, err
			}
			iw.atLineStart = false
		}
		if _, err := iw.w.Write(p[i : i+1]); err != nil {
			return i, err
		}
		if b == '\n' {
			iw.atLineStart = true
		}
	}
	return len(p), nil
}
