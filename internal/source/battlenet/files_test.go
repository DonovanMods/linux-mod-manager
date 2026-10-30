package battlenet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Wine writes a character outside ASCII as \xHHHH UTF-16 code units, so a
// character outside the Basic Multilingual Plane is a surrogate pair of
// them, which decodes to the one character - and a lone surrogate, which
// names none, to U+FFFD.
func TestRegQuoted_DecodesUTF16Escapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`caf\x00e9"`, "café"},
		{`Game \xd83c\xdfae"`, "Game \U0001F3AE"},
		{`a\xd83cb"`, "a\uFFFDb"},
		{`a\xdfaeb"`, "a\uFFFDb"},
		{`a\xd83c\x0041"`, "a\uFFFDA"},
		{`C:\\Games\\WoW"`, `C:\Games\WoW`},
	} {
		got, rest, ok := regQuoted(tc.in)
		require.True(t, ok, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
		assert.Empty(t, rest, tc.in)
	}
}
