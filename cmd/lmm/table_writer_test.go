package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/require"
)

// TestDisplayWidthTableWriter_UnicodeColumnsAlign reproduces #489 with rows
// whose cells use one-byte, Cyrillic, and wide CJK text. The first two cells
// in every row must occupy the same terminal width, regardless of their UTF-8
// byte lengths.
func TestDisplayWidthTableWriter_UnicodeColumnsAlign(t *testing.T) {
	var out bytes.Buffer
	w := newDisplayWidthTableWriter(&out)
	_, err := w.Write([]byte("ID\tNAME\tKIND\n--\t----\t----\nascii\tAlpha\tascii\ncyrillic\tЖанна\tCyrillic\ncjk\t表意文字\tCJK\n"))
	require.NoError(t, err)
	require.NoError(t, w.Flush())

	const want = "ID        NAME      KIND\n" +
		"--        ----      ----\n" +
		"ascii     Alpha     ascii\n" +
		"cyrillic  Жанна     Cyrillic\n" +
		"cjk       表意文字  CJK\n"
	require.Equal(t, want, out.String())

	secondCell := []string{"NAME", "----", "Alpha", "Жанна", "表意文字"}
	for i, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		start := strings.Index(line, secondCell[i])
		require.GreaterOrEqual(t, start, 0)
		require.Equal(t, 10, uniseg.StringWidth(line[:start]), "second column start in %q", line)
	}
}

// TestDisplayWidthTableWriter_PreservesASCIITableFormat makes the migration
// from text/tabwriter explicit: ordinary ASCII tables retain their existing
// two-space padding byte-for-byte.
func TestDisplayWidthTableWriter_PreservesASCIITableFormat(t *testing.T) {
	var out bytes.Buffer
	w := newDisplayWidthTableWriter(&out)
	_, err := w.Write([]byte("ID\tNAME\tENABLED\n--\t----\t-------\nmodA\tSome Mod\tyes\nmodB-longer-id\tAnother\tno\n"))
	require.NoError(t, err)
	require.NoError(t, w.Flush())

	const want = "ID              NAME      ENABLED\n" +
		"--              ----      -------\n" +
		"modA            Some Mod  yes\n" +
		"modB-longer-id  Another   no\n"
	require.Equal(t, want, out.String())
}
