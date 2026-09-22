package main

import (
	"bytes"
	"io"
	"strings"

	"github.com/rivo/uniseg"
)

// displayWidthTableWriter is the narrow tabwriter-shaped surface the CLI
// tables need. It buffers tab-delimited rows until Flush so padding can use
// terminal display width rather than UTF-8 byte length.
//
// The command tables use text/tabwriter's default layout: each non-final cell
// is followed by two spaces, and a final empty cell (from a trailing tab)
// preserves that padding. Keeping that shape means ordinary ASCII output stays
// byte-for-byte unchanged while Unicode cells start later columns correctly.
type displayWidthTableWriter struct {
	out io.Writer
	buf bytes.Buffer
}

func newDisplayWidthTableWriter(out io.Writer) *displayWidthTableWriter {
	return &displayWidthTableWriter{out: out}
}

func (w *displayWidthTableWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}

func (w *displayWidthTableWriter) Flush() error {
	text := w.buf.String()
	if text == "" {
		return nil
	}

	trailingNewline := strings.HasSuffix(text, "\n")
	if trailingNewline {
		text = strings.TrimSuffix(text, "\n")
	}
	rows := strings.Split(text, "\n")
	cellsByRow := make([][]string, len(rows))
	widths := make([]int, 0)
	for i, row := range rows {
		cells := strings.Split(row, "\t")
		cellsByRow[i] = cells
		for col, cell := range cells[:len(cells)-1] {
			if col >= len(widths) {
				widths = append(widths, 0)
			}
			widths[col] = max(widths[col], uniseg.StringWidth(cell))
		}
	}

	var rendered strings.Builder
	for rowIndex, cells := range cellsByRow {
		for col, cell := range cells {
			rendered.WriteString(cell)
			if col == len(cells)-1 {
				continue
			}
			padding := widths[col] - uniseg.StringWidth(cell) + 2
			rendered.WriteString(strings.Repeat(" ", padding))
		}
		if rowIndex < len(cellsByRow)-1 || trailingNewline {
			rendered.WriteByte('\n')
		}
	}

	output := rendered.String()
	n, err := io.WriteString(w.out, output)
	if err != nil {
		return err
	}
	if n != len(output) {
		return io.ErrShortWrite
	}
	w.buf.Reset()
	return nil
}
